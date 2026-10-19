package notify

import (
	"encoding/json"
	"net/http"
	"time"
)

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]any{"error": map[string]string{"code": "notifications", "message": msg}})
}

// Handler is an owner-only API; mount with /notifications stripped.
func (s *Service) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		cs, err := s.Channels(r.Context())
		if err != nil {
			fail(w, 500, "Channels unavailable")
			return
		}
		type Delivery struct {
			ID      int64  `json:"id"`
			Channel string `json:"channel"`
			Created int64  `json:"created"`
			Status  string `json:"status"`
		}
		deliveries := []Delivery{}
		rows, err := s.db.QueryContext(r.Context(), `SELECT id,channel,created,status FROM notify_queue ORDER BY id DESC LIMIT 100`)
		if err != nil {
			fail(w, 500, "Delivery log unavailable")
			return
		}
		for rows.Next() {
			var d Delivery
			if rows.Scan(&d.ID, &d.Channel, &d.Created, &d.Status) != nil {
				rows.Close()
				fail(w, 500, "Delivery log unavailable")
				return
			}
			deliveries = append(deliveries, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			fail(w, 500, "Delivery log unavailable")
			return
		}
		reply(w, 200, map[string]any{"channels": cs, "events": Kinds, "deliveries": deliveries})
	})
	m.HandleFunc("PUT /{id}", func(w http.ResponseWriter, r *http.Request) {
		var c Channel
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&c) != nil {
			fail(w, 400, "Invalid channel JSON")
			return
		}
		c.ID = r.PathValue("id")
		if err := s.Save(r.Context(), c); err != nil {
			fail(w, 400, err.Error())
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})
	m.HandleFunc("DELETE /{id}", func(w http.ResponseWriter, r *http.Request) {
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			fail(w, 500, "Delete unavailable")
			return
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM notify_channels WHERE id=?`, r.PathValue("id")); err == nil {
			_, err = tx.ExecContext(r.Context(), `DELETE FROM notify_queue WHERE channel=?`, r.PathValue("id"))
		}
		if err != nil || tx.Commit() != nil {
			fail(w, 500, "Delete unavailable")
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})
	m.HandleFunc("POST /{id}/test", func(w http.ResponseWriter, r *http.Request) {
		cs, err := s.channels(r.Context())
		if err != nil {
			fail(w, 500, "Channels unavailable")
			return
		}
		for _, c := range cs {
			if c.ID == r.PathValue("id") {
				if !c.Enabled {
					fail(w, 400, "Enable the channel before testing")
					return
				}
				e := Event{Kind: "test", Title: "Ether notification test", Message: "Your owner notification channel is connected.", At: s.opts.Now()}
				if err := s.enqueue(r.Context(), c, e); err != nil {
					fail(w, 500, "Could not enqueue test")
					return
				}
				reply(w, http.StatusAccepted, map[string]any{"queued": true, "delivery": "The test follows the channel rate and digest settings."})
				return
			}
		}
		fail(w, 404, "Channel not found")
	})
	m.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&e) != nil {
			fail(w, 400, "Invalid event")
			return
		}
		e.At = time.Time{}
		if err := s.Emit(r.Context(), e); err != nil {
			fail(w, 400, err.Error())
			return
		}
		reply(w, 202, map[string]bool{"queued": true})
	})
	return m
}
