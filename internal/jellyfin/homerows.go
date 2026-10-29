package jellyfin

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// The Jellyfin web client's Home has fixed sections and no place for
// arbitrary rows. Ether adds its own Home rows (collections, trending,
// "because you watched", ...) with a small script that the web client's
// index.html loads, drawing them under the standard sections.

//go:embed homerows.js
var homeScript []byte

func (s *Server) handleHomeScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(homeScript)
}

type homeRow struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Items []item `json:"Items"`
}

const (
	maxHomeRows     = 12
	homeRowItemsMax = 24
)

// nativeRow reports rows the Jellyfin client already shows itself.
func nativeRow(h core.Hub) bool {
	id := strings.ToLower(h.ID + " " + h.Context)
	for _, k := range []string{"continue", "inprogress", "upnext", "recentlyadded", "livetv", "ondeck"} {
		if strings.Contains(id, k) {
			return true
		}
	}
	return false
}

// handleHomeRows returns the viewer's Ether Home rows.
func (s *Server) handleHomeRows(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	rows := []homeRow{}
	if s.d.Recommender == nil {
		writeJSON(w, http.StatusOK, rows)
		return
	}
	var (
		hubs []core.Hub
		err  error
	)
	if cr, ok := s.d.Recommender.(core.ContextualRecommender); ok {
		hubs, err = cr.HomeHubsFor(ctx, sess.User, core.NewHubContext(s.now().Local(), "Jellyfin", "", ""), homeRowItemsMax)
	} else {
		hubs, err = s.d.Recommender.HomeHubs(ctx, sess.User, homeRowItemsMax)
	}
	if err != nil {
		s.log.Warn("jellyfin: home rows unavailable", "err", err)
		writeJSON(w, http.StatusOK, rows)
		return
	}
	for _, h := range hubs {
		if nativeRow(h) {
			continue
		}
		ts := s.visible(ctx, sess.User, h.Items)
		if len(ts) > homeRowItemsMax {
			ts = ts[:homeRowItemsMax]
		}
		if len(ts) == 0 {
			continue
		}
		rows = append(rows, homeRow{ID: h.ID, Name: h.Title, Items: s.items(ctx, sess, ts, false)})
		if len(rows) == maxHomeRows {
			break
		}
	}
	writeJSON(w, http.StatusOK, rows)
}
