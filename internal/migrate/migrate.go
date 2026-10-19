// Package migrate performs read-only PMS discovery and reviewed, idempotent local imports.
package migrate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	_ "github.com/braedonsaunders/ether/internal/auth"
	_ "github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
	_ "github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func init() {
	store.Register("migrate", 1, `CREATE TABLE migrate_plans(id TEXT PRIMARY KEY, document TEXT NOT NULL, created INTEGER NOT NULL, imported INTEGER NOT NULL DEFAULT 0); CREATE TABLE migrate_objects(source TEXT NOT NULL, viewer INTEGER NOT NULL, kind TEXT NOT NULL, external TEXT NOT NULL, local_id INTEGER NOT NULL, PRIMARY KEY(source,viewer,kind,external)); CREATE TABLE migrate_watchlist(viewer INTEGER NOT NULL,key INTEGER NOT NULL,PRIMARY KEY(viewer,key));`)
}

type Service struct {
	db      *sql.DB
	catalog core.Catalog
	client  *http.Client
}

func New(db *sql.DB, catalog core.Catalog) *Service {
	return &Service{db: db, catalog: catalog, client: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

type Viewer struct {
	LocalUserID     int64  `json:"localUserId"`
	PlexID          int64  `json:"plexId"`
	Name            string `json:"name"`
	Token           string `json:"token,omitempty"`
	SourceAccountID int64  `json:"sourceAccountId,omitempty"`
}
type Request struct {
	URL         string   `json:"url"`
	Token       string   `json:"token"`
	Viewers     []Viewer `json:"viewers"`
	HistoryOnly bool     `json:"historyOnly,omitempty"`
}
type Item struct {
	External  string         `json:"external"`
	Key       core.RatingKey `json:"key"`
	Title     string         `json:"title"`
	Viewer    int64          `json:"viewer"`
	Views     int            `json:"views"`
	Offset    int64          `json:"offsetMs"`
	Duration  int64          `json:"durationMs"`
	Rating    float64        `json:"rating"`
	ViewedAt  int64          `json:"viewedAt"`
	Watchlist bool           `json:"watchlist,omitempty"`
}
type List struct {
	External string           `json:"external"`
	Name     string           `json:"name"`
	Kind     string           `json:"kind"`
	Viewer   int64            `json:"viewer"`
	Keys     []core.RatingKey `json:"keys"`
}
type Plan struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"`
	Items    []Item   `json:"items"`
	History  []Item   `json:"history"`
	Lists    []List   `json:"lists"`
	Viewers  []Viewer `json:"viewers"`
	Warnings []string `json:"warnings"`
	Imported bool     `json:"imported"`
}
type media struct {
	Key   string `xml:"ratingKey,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
	GUID  string `xml:"guid,attr"`
	GUIDs []struct {
		ID string `xml:"id,attr"`
	} `xml:"Guid"`
	Views       int     `xml:"viewCount,attr"`
	Offset      int64   `xml:"viewOffset,attr"`
	Duration    int64   `xml:"duration,attr"`
	Rating      float64 `xml:"userRating,attr"`
	ViewedAt    int64   `xml:"lastViewedAt,attr"`
	HistoryAt   int64   `xml:"viewedAt,attr"`
	AccountID   int64   `xml:"accountID,attr"`
	Grandparent string  `xml:"grandparentRatingKey,attr"`
	Season      int     `xml:"parentIndex,attr"`
	Episode     int     `xml:"index,attr"`
}
type container struct {
	Accounts []struct {
		ID   int64  `xml:"id,attr"`
		Name string `xml:"name,attr"`
	} `xml:"Account"`
	Machine     string `xml:"machineIdentifier,attr"`
	Total       int    `xml:"totalSize,attr"`
	Size        int    `xml:"size,attr"`
	Directories []struct {
		Key       string `xml:"key,attr"`
		RatingKey string `xml:"ratingKey,attr"`
		GUIDs     []struct {
			ID string `xml:"id,attr"`
		} `xml:"Guid"`
		Type  string `xml:"type,attr"`
		Title string `xml:"title,attr"`
		GUID  string `xml:"guid,attr"`
	} `xml:"Directory"`
	Videos    []media `xml:"Video"`
	Playlists []media `xml:"Playlist"`
	Metadata  []media `xml:"Metadata"`
}

func (s *Service) get(ctx context.Context, base, token, path string) (container, error) {
	var c container
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if e != nil {
		return c, e
	}
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("Accept", "application/xml")
	// PMS accepts these pagination controls as headers. Some versions silently
	// ignore the identically named query parameters and return the whole library.
	for _, name := range []string{"X-Plex-Container-Start", "X-Plex-Container-Size"} {
		if value := req.URL.Query().Get(name); value != "" {
			req.Header.Set(name, value)
		}
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return c, errors.New("PMS connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return c, fmt.Errorf("PMS returned HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if e != nil {
		return c, errors.New("PMS response read failed")
	}
	if len(b) > 32<<20 {
		return c, errors.New("PMS response too large")
	}
	e = xml.Unmarshal(b, &c)
	return c, e
}
func (s *Service) mapMedia(ctx context.Context, base, token string, m media) (core.RatingKey, error) {
	guids := []string{m.GUID}
	for _, g := range m.GUIDs {
		guids = append(guids, g.ID)
	}
	for _, g := range guids {
		for _, prefix := range []string{"tmdb://", "com.plexapp.agents.themoviedb://", "imdb://", "com.plexapp.agents.imdb://", "tvdb://", "com.plexapp.agents.thetvdb://"} {
			if !strings.HasPrefix(g, prefix) {
				continue
			}
			id := strings.Split(strings.TrimPrefix(g, prefix), "?")[0]
			if strings.Contains(prefix, "tmdb") || strings.Contains(prefix, "themoviedb") {
				n, e := strconv.ParseInt(id, 10, 64)
				if e != nil || n <= 0 {
					continue
				}
				var k core.RatingKey
				if m.Type == "movie" {
					k = core.MovieKey(n)
				} else if m.Type == "show" {
					k = core.ShowKey(n)
				}
				if k != 0 {
					if s.catalog != nil {
						if _, e = s.catalog.Get(ctx, k); e != nil {
							continue
						}
					}
					return k, nil
				}
			} else if s.catalog != nil {
				source := "imdb"
				if strings.Contains(prefix, "tvdb") {
					source = "tvdb"
				}
				t, e := s.catalog.ByExternalID(ctx, source, id)
				if e == nil && t != nil && string(t.Type) == m.Type {
					return t.Key, nil
				}
			}
		}
	}
	if m.Type == "episode" && m.Grandparent != "" && base != "" {
		c, e := s.get(ctx, base, token, "/library/metadata/"+url.PathEscape(m.Grandparent))
		if e == nil {
			for _, d := range c.Directories {
				show := media{Type: "show", GUID: d.GUID, GUIDs: d.GUIDs}
				k, e := s.mapMedia(ctx, base, token, show)
				if e == nil {
					return core.EpisodeKey(k.TMDB(), m.Season, m.Episode), nil
				}
			}
		}
	}
	return 0, core.ErrNotFound
}
func (s *Service) pages(ctx context.Context, base, token, path string) ([]media, error) {
	out := []media{}
	for start := 0; start < 1000000; {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		c, e := s.get(ctx, base, token, path+sep+"includeGuids=1&X-Plex-Container-Start="+strconv.Itoa(start)+"&X-Plex-Container-Size=500")
		if e != nil {
			return nil, e
		}
		items := append(c.Videos, c.Metadata...)
		items = append(items, c.Playlists...)
		for _, d := range c.Directories {
			key := d.RatingKey
			if key == "" {
				key = d.Key
			}
			items = append(items, media{Key: key, Title: d.Title, Type: d.Type, GUID: d.GUID, GUIDs: d.GUIDs})
		}
		out = append(out, items...)
		if len(items) == 0 || c.Total == 0 || start+len(items) >= c.Total {
			return out, nil
		}
		start += len(items)
	}
	return nil, errors.New("PMS paging limit exceeded")
}
func (s *Service) Preview(ctx context.Context, q Request) (Plan, error) {
	p := Plan{Items: []Item{}, Lists: []List{}, Warnings: []string{}, Viewers: []Viewer{}}
	u, e := url.Parse(q.URL)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return p, errors.New("PMS URL must be an http(s) origin without credentials")
	}
	base := strings.TrimRight(u.String(), "/")
	ident, e := s.get(ctx, base, q.Token, "/identity")
	if e != nil {
		return p, e
	}
	if ident.Machine == "" {
		return p, errors.New("PMS identity missing machine identifier")
	}
	p.Source = ident.Machine
	if len(q.Viewers) == 0 {
		return p, errors.New("choose at least one local viewer")
	}
	sections, e := s.get(ctx, base, q.Token, "/library/sections")
	if e != nil {
		return p, e
	}
	for _, v := range q.Viewers {
		if v.LocalUserID <= 0 {
			return p, errors.New("viewer localUserId required")
		}
		var exists int
		if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users WHERE id=?`, v.LocalUserID).Scan(&exists); e != nil {
			return p, e
		}
		if exists == 0 {
			return p, errors.New("viewer does not exist locally; sync Plex sharing first")
		}
		token := v.Token
		if token == "" {
			if len(q.Viewers) > 1 {
				return p, errors.New("each viewer needs their own PMS access token")
			}
			token = q.Token
		}
		v.Token = ""
		accountID, accountErr := s.sourceAccount(ctx, base, token, v)
		if accountErr != nil && q.HistoryOnly {
			return p, accountErr
		}
		v.SourceAccountID = accountID
		p.Viewers = append(p.Viewers, v)
		keyMap := map[string]core.RatingKey{}
		mapper := sourceMapper{service: s, base: base, token: token, keys: keyMap, parents: map[string]core.RatingKey{}, missing: map[string]bool{}}
		for _, section := range sections.Directories {
			if section.Type != "movie" && section.Type != "show" {
				continue
			}
			typ := "1"
			if section.Type == "show" {
				typ = "4"
			}
			sectionPath := "/library/sections/" + url.PathEscape(section.Key) + "/all?type=" + typ
			if q.HistoryOnly {
				sectionPath += "&unwatched=0"
			}
			ms, e := s.pages(ctx, base, token, sectionPath)
			if e != nil {
				return p, e
			}
			for _, m := range ms {
				if q.HistoryOnly && m.Views == 0 {
					continue
				}
				k, me := mapper.resolve(ctx, m, false)
				if me != nil {
					p.Warnings = append(p.Warnings, "Unmatched: "+m.Title)
					continue
				}
				keyMap[m.Key] = k
				if m.Views > 0 || m.Offset > 0 || m.Rating > 0 {
					p.Items = append(p.Items, Item{External: m.Key, Key: k, Title: m.Title, Viewer: v.LocalUserID, Views: m.Views, Offset: m.Offset, Duration: m.Duration, Rating: m.Rating, ViewedAt: m.ViewedAt})
				}
			}
			if q.HistoryOnly {
				continue
			}
			cs, ce := s.pages(ctx, base, token, "/library/sections/"+url.PathEscape(section.Key)+"/collections")
			if ce != nil {
				p.Warnings = append(p.Warnings, "Collections unavailable for section "+section.Title)
			} else {
				for _, c := range cs {
					list, e := s.readList(ctx, base, token, c, "collection", v.LocalUserID, keyMap)
					if e != nil {
						p.Warnings = append(p.Warnings, "Collection unavailable: "+c.Title)
					} else {
						p.Lists = append(p.Lists, list)
					}
				}
			}
		}
		var history []media
		he := accountErr
		if he == nil {
			history, he = s.pages(ctx, base, token, "/status/sessions/history/all?sort=viewedAt:asc&accountID="+strconv.FormatInt(accountID, 10))
		}
		if he != nil {
			p.Warnings = append(p.Warnings, "Detailed watch history unavailable for "+v.Name+"; last-viewed summaries will be imported")
		} else {
			for _, h := range history {
				// Defend against a server ignoring the account filter.
				if h.AccountID != accountID {
					continue
				}
				k := keyMap[h.Key]
				if k == 0 {
					k, _ = mapper.resolve(ctx, h, false)
				}
				at := h.HistoryAt
				if at == 0 {
					at = h.ViewedAt
				}
				if k == 0 {
					p.Warnings = append(p.Warnings, "Unmatched history: "+h.Title)
				}
				if k != 0 && at > 0 {
					p.History = append(p.History, Item{Key: k, Viewer: v.LocalUserID, ViewedAt: at, Views: 1, Title: h.Title})
				}
			}
		}
		if q.HistoryOnly {
			continue
		}
		pls, pe := s.pages(ctx, base, token, "/playlists")
		if pe != nil {
			p.Warnings = append(p.Warnings, "Playlists unavailable for "+v.Name)
		} else {
			for _, pl := range pls {
				l, e := s.readList(ctx, base, token, pl, "playlist", v.LocalUserID, keyMap)
				if e != nil {
					p.Warnings = append(p.Warnings, "Playlist unavailable: "+pl.Title)
				} else {
					p.Lists = append(p.Lists, l)
				}
			}
		}
	}
	p.Warnings = append(p.Warnings, "PMS exposes state only for supplied viewer access tokens. Existing Ether users are mapped; sharing must be granted through plex.tv.")
	return s.savePlan(ctx, p)
}
func (s *Service) readList(ctx context.Context, base, token string, m media, kind string, viewer int64, keys map[string]core.RatingKey) (List, error) {
	path := "/library/collections/" + url.PathEscape(m.Key) + "/children"
	if kind == "playlist" {
		path = "/playlists/" + url.PathEscape(m.Key) + "/items"
	}
	items, e := s.pages(ctx, base, token, path)
	l := List{External: m.Key, Name: m.Title, Kind: kind, Viewer: viewer, Keys: []core.RatingKey{}}
	if e != nil {
		return l, e
	}
	for _, item := range items {
		k := keys[item.Key]
		if k == 0 {
			k, _ = s.mapMedia(ctx, base, token, item)
		}
		if k != 0 {
			l.Keys = append(l.Keys, k)
		}
	}
	return l, nil
}
func (s *Service) savePlan(ctx context.Context, p Plan) (Plan, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return p, e
	}
	p.ID = hex.EncodeToString(b)
	doc, e := json.Marshal(p)
	if e != nil {
		return p, e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO migrate_plans(id,document,created) VALUES(?,?,?)`, p.ID, string(doc), time.Now().Unix())
	return p, e
}
func (s *Service) Import(ctx context.Context, id string) (Plan, error) {
	var p Plan
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return p, e
	}
	defer tx.Rollback()
	var doc string
	var imported bool
	if e = tx.QueryRowContext(ctx, `SELECT document,imported FROM migrate_plans WHERE id=?`, id).Scan(&doc, &imported); e != nil {
		return p, e
	}
	if e = json.Unmarshal([]byte(doc), &p); e != nil {
		return p, e
	}
	if imported {
		p.Imported = true
		return p, nil
	}
	now := time.Now().UnixNano()
	initialUpdated := map[string]int64{}
	for _, it := range p.Items {
		if it.Key <= 0 || it.Viewer <= 0 || it.Views < 0 || it.Offset < 0 || it.Rating < 0 || it.Rating > 10 {
			return p, errors.New("invalid imported item")
		}
		if strings.HasPrefix(p.Source, "csv:") && strings.HasSuffix(p.Source, ":watchlist") {
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO migrate_watchlist(viewer,key) VALUES(?,?)`, it.Viewer, it.Key); e != nil {
				return p, e
			}
			continue
		}
		if strings.HasPrefix(p.Source, "csv:") && strings.HasSuffix(p.Source, ":ratings") {
			if _, e = tx.ExecContext(ctx, `INSERT INTO state_watch(user_id,rating_key,user_rating,updated_at) VALUES(?,?,?,?) ON CONFLICT(user_id,rating_key) DO UPDATE SET user_rating=excluded.user_rating,updated_at=excluded.updated_at`, it.Viewer, it.Key, it.Rating, now); e != nil {
				return p, e
			}
			continue
		}
		if it.Views > 0 {
			_, e = tx.ExecContext(ctx, `INSERT INTO state_history(user_id,rating_key,viewed_at,completed) SELECT ?,?,?,1 WHERE NOT EXISTS(SELECT 1 FROM state_history WHERE user_id=? AND rating_key=? AND viewed_at=?)`, it.Viewer, it.Key, it.ViewedAt*int64(time.Second), it.Viewer, it.Key, it.ViewedAt*int64(time.Second))
			if e != nil {
				return p, e
			}
		}
		ident := fmt.Sprintf("%d:%d", it.Viewer, it.Key)
		oldUpdated, seen := initialUpdated[ident]
		if !seen {
			if qe := tx.QueryRowContext(ctx, `SELECT updated_at FROM state_watch WHERE user_id=? AND rating_key=?`, it.Viewer, it.Key).Scan(&oldUpdated); qe != nil && !errors.Is(qe, sql.ErrNoRows) {
				return p, qe
			}
			initialUpdated[ident] = oldUpdated
		}
		if oldUpdated > it.ViewedAt*int64(time.Second) && oldUpdated > 0 && it.ViewedAt > 0 {
			continue
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO state_watch(user_id,rating_key,view_offset_ns,duration_ns,view_count,counted,last_viewed_at,user_rating,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(user_id,rating_key) DO UPDATE SET view_offset_ns=excluded.view_offset_ns,duration_ns=excluded.duration_ns,view_count=MAX(state_watch.view_count,excluded.view_count),last_viewed_at=MAX(state_watch.last_viewed_at,excluded.last_viewed_at),user_rating=CASE WHEN excluded.user_rating>0 THEN excluded.user_rating ELSE state_watch.user_rating END,updated_at=excluded.updated_at`, it.Viewer, it.Key, it.Offset*int64(time.Millisecond), it.Duration*int64(time.Millisecond), it.Views, it.Views > 0, it.ViewedAt*int64(time.Second), it.Rating, now)
		if e != nil {
			return p, e
		}

		if it.Watchlist {
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO migrate_watchlist(viewer,key) VALUES(?,?)`, it.Viewer, it.Key); e != nil {
				return p, e
			}
		}
	}
	for _, h := range p.History {
		if h.Key <= 0 || h.Viewer <= 0 || h.ViewedAt <= 0 {
			return p, errors.New("invalid history event")
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO state_history(user_id,rating_key,viewed_at,completed) SELECT ?,?,?,1 WHERE NOT EXISTS(SELECT 1 FROM state_history WHERE user_id=? AND rating_key=? AND viewed_at=?)`, h.Viewer, h.Key, h.ViewedAt*int64(time.Second), h.Viewer, h.Key, h.ViewedAt*int64(time.Second)); e != nil {
			return p, e
		}
	}
	for _, l := range p.Lists {
		if e = importList(ctx, tx, p.Source, l, now); e != nil {
			return p, e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE migrate_plans SET imported=1 WHERE id=?`, id); e != nil {
		return p, e
	}
	e = tx.Commit()
	p.Imported = e == nil
	return p, e
}
func importList(ctx context.Context, tx *sql.Tx, source string, l List, now int64) error {
	var id int64
	e := tx.QueryRowContext(ctx, `SELECT local_id FROM migrate_objects WHERE source=? AND viewer=? AND kind=? AND external=?`, source, l.Viewer, l.Kind, l.External).Scan(&id)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if l.Kind == "playlist" {
		if id == 0 {
			res, e := tx.ExecContext(ctx, `INSERT INTO state_playlists(user_id,title,updated_at) VALUES(?,?,?)`, l.Viewer, l.Name, now)
			if e != nil {
				return e
			}
			id, e = res.LastInsertId()
			if e != nil {
				return e
			}
		} else {
			if _, e = tx.ExecContext(ctx, `UPDATE state_playlists SET title=?,updated_at=? WHERE id=?`, l.Name, now, id); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM state_playlist_items WHERE playlist_id=?`, id); e != nil {
			return e
		}
		for i, k := range l.Keys {
			if _, e = tx.ExecContext(ctx, `INSERT INTO state_playlist_items(playlist_id,position,rating_key) VALUES(?,?,?)`, id, i, k); e != nil {
				return e
			}
		}
	} else {
		if id == 0 {
			if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0)+1 FROM collections_specs WHERE id<900000`).Scan(&id); e != nil {
				return e
			}
		}
		ks := []string{}
		for _, k := range l.Keys {
			ks = append(ks, k.String())
		}
		sp := core.CollectionSpec{Name: l.Name, MediaType: "mixed", Sources: []core.CollectionSource{{Kind: "titles", Keys: ks}}, Match: "all", Sort: "source", Refresh: "live", Enabled: true, OwnerUserID: l.Viewer, Targets: core.CollectionTargets{CollectionsTab: true, Users: core.UserTargets{IDs: []int64{l.Viewer}}}}
		raw, e := json.Marshal(sp)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO collections_specs(id,owner_user_id,spec_json,enabled,created_at,updated_at) VALUES(?,?,?,1,?,?) ON CONFLICT(id) DO UPDATE SET spec_json=excluded.spec_json,updated_at=excluded.updated_at`, id, l.Viewer, string(raw), now, now)
		if e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO migrate_objects(source,viewer,kind,external,local_id) VALUES(?,?,?,?,?) ON CONFLICT(source,viewer,kind,external) DO UPDATE SET local_id=excluded.local_id`, source, l.Viewer, l.Kind, l.External, id)
	return e
}
func fingerprint(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
