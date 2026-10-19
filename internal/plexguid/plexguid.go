// Package plexguid maps verified external identities through Plex's metadata
// provider. It never invents plex:// IDs or grants Plex account entitlements.
package plexguid

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func init() {
	store.Register("plexguid", 1, `CREATE TABLE plexguid_mappings(rating_key INTEGER PRIMARY KEY,guid TEXT NOT NULL,checked_at INTEGER NOT NULL);`)
}

type Options struct {
	HTTP    *http.Client
	BaseURL string
	Token   func() string
}
type Service struct {
	db *sql.DB
	o  Options
}

func New(db *sql.DB, o Options) *Service {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://metadata.provider.plex.tv"
	}
	return &Service{db, o}
}

var validGUID = regexp.MustCompile(`^plex://(movie|show)/[a-f0-9]{24}$`)

// Cached is safe for lists: no network requests or credentials are needed.
func (s *Service) Cached(ctx context.Context, k core.RatingKey) string {
	var guid string
	_ = s.db.QueryRowContext(ctx, `SELECT guid FROM plexguid_mappings WHERE rating_key=? AND checked_at>?`, int64(k), time.Now().Add(-30*24*time.Hour).Unix()).Scan(&guid)
	return guid
}
func (s *Service) Resolve(ctx context.Context, k core.RatingKey) (string, error) {
	kind := "movie"
	typ := "1"
	if k.Kind() == core.KindShow {
		kind = "show"
		typ = "2"
	} else if k.Kind() != core.KindMovie {
		return "", nil
	}
	var cached string
	var at int64
	if e := s.db.QueryRowContext(ctx, `SELECT guid,checked_at FROM plexguid_mappings WHERE rating_key=?`, int64(k)).Scan(&cached, &at); e == nil && time.Since(time.Unix(at, 0)) < 24*time.Hour {
		return cached, nil
	}
	if s.o.Token == nil || s.o.Token() == "" {
		return "", core.ErrUnauthorized
	}
	external := fmt.Sprintf("tmdb://%d", k.TMDB())
	q := url.Values{"type": {typ}, "guid": {external}, "includeGuids": {"1"}}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.o.BaseURL, "/")+"/library/metadata/matches?"+q.Encode(), nil)
	if e != nil {
		return "", fmt.Errorf("Plex metadata request invalid")
	}
	req.Header.Set("X-Plex-Token", s.o.Token())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", "Ether")
	req.Header.Set("X-Plex-Client-Identifier", "ether-metadata")
	// Never forward account credentials to a redirect target.
	client := *s.o.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return "", fmt.Errorf("Plex metadata unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Plex metadata HTTP %d", resp.StatusCode)
	}
	var body struct {
		Container struct {
			Metadata []struct {
				GUID string `json:"guid"`
				Type string `json:"type"`
				IDs  []struct {
					ID string `json:"id"`
				} `json:"Guid"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); e != nil {
		return "", fmt.Errorf("Plex metadata response invalid")
	}
	found := ""
	for _, item := range body.Container.Metadata {
		if item.Type != kind || !validGUID.MatchString(item.GUID) || !strings.HasPrefix(item.GUID, "plex://"+kind+"/") {
			continue
		}
		for _, id := range item.IDs {
			if id.ID == external {
				if found != "" && found != item.GUID {
					return "", fmt.Errorf("ambiguous Plex identity")
				}
				found = item.GUID
			}
		}
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO plexguid_mappings(rating_key,guid,checked_at) VALUES(?,?,?) ON CONFLICT(rating_key) DO UPDATE SET guid=excluded.guid,checked_at=excluded.checked_at`, int64(k), found, time.Now().Unix())
	return found, e
}
