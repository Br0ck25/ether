package watchlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// plex uses only an account token supplied by the authenticated token source.
// A server share token is not an account token and a friend has no implicit grant.
func (s *Service) plex(ctx context.Context, token string) ([]core.Title, error) {
	out := []core.Title{}
	for start := 0; start < 2000; start += 100 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.o.PlexBaseURL, "/")+"/library/sections/watchlist/all?includeGuids=1&X-Plex-Container-Start="+strconv.Itoa(start)+"&X-Plex-Container-Size=100", nil)
		if err != nil {
			return nil, errors.New("Plex watchlist URL invalid")
		}
		req.Header.Set("X-Plex-Token", token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Plex-Product", "Ether")
		resp, err := s.o.Client.Do(req)
		if err != nil {
			return nil, errors.New("Plex watchlist request failed")
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if e != nil {
			return nil, errors.New("Plex watchlist response unreadable")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Plex watchlist: HTTP %d (viewer account authorization may be required)", resp.StatusCode)
		}
		var envelope struct {
			MediaContainer struct {
				Size      int `json:"size"`
				TotalSize int `json:"totalSize"`
				Metadata  []struct {
					Type string `json:"type"`
					Guid []struct {
						ID string `json:"id"`
					} `json:"Guid"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			return nil, errors.New("Plex watchlist response invalid")
		}
		for _, m := range envelope.MediaContainer.Metadata {
			for _, g := range m.Guid {
				var t *core.Title
				var e error
				switch {
				case strings.HasPrefix(g.ID, "tmdb://"):
					id, err := strconv.ParseInt(strings.TrimPrefix(g.ID, "tmdb://"), 10, 64)
					if err != nil || id <= 0 {
						continue
					}
					key := core.MovieKey(id)
					if m.Type == "show" {
						key = core.ShowKey(id)
					} else if m.Type != "movie" {
						continue
					}
					t, e = s.o.Catalog.Get(ctx, key)
				case strings.HasPrefix(g.ID, "imdb://"):
					t, e = s.o.Catalog.ByExternalID(ctx, "imdb", strings.TrimPrefix(g.ID, "imdb://"))
				default:
					continue
				}
				if e == nil && t != nil {
					out = append(out, *t)
					break
				}
			}
		}
		n := len(envelope.MediaContainer.Metadata)
		if n < 100 || (envelope.MediaContainer.TotalSize > 0 && start+n >= envelope.MediaContainer.TotalSize) {
			return out, nil
		}
	}
	return nil, errors.New("Plex watchlist exceeds 2000-item safety limit; previous snapshot preserved")
}
