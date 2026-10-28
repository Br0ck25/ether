package jellyfin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// codeAlphabet leaves out look-alikes (0/O, 1/I/L) so a code read off a
// screen is typed correctly the first time.
const (
	codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	generatedLen = 10
)

// GenerateCode returns a random access code, about 49 bits.
func GenerateCode() string {
	b := make([]byte, generatedLen)
	_, _ = rand.Read(b)
	out := make([]byte, generatedLen)
	for i := range b {
		out[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(out)
}

// Devices counts the signed-in devices of every viewer.
func (s *Store) Devices(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, COUNT(*) FROM jellyfin_tokens GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Store) usersWithCode(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM jellyfin_credentials`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// AdminHandler is the owner's side of sign-in: which viewers have an access
// code, setting and clearing codes, and signing devices out. Mount it behind
// the owner-authenticated admin API; it does no authentication itself.
//
//	GET    viewers                  who can sign in
//	PUT    viewers/{id}/code        set a code ({"code":"…"}, or empty to generate); returns it once
//	DELETE viewers/{id}/code        remove the code and sign the viewer out
//	POST   viewers/{id}/signout     sign the viewer out of every device
func (s *Server) AdminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		ctx := r.Context()
		switch {
		case len(parts) == 1 && parts[0] == "viewers" && r.Method == http.MethodGet:
			s.adminViewers(w, r)
			return
		case len(parts) >= 3 && parts[0] == "viewers":
			id, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || id <= 0 || s.activeUser(ctx, id) == nil {
				http.Error(w, "no such viewer", http.StatusNotFound)
				return
			}
			switch {
			case parts[2] == "code" && r.Method == http.MethodPut:
				var body struct {
					Code string `json:"code"`
				}
				_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)
				code := strings.TrimSpace(body.Code)
				if code == "" {
					code = GenerateCode()
				}
				if err := s.d.Store.SetAccessCode(ctx, id, code); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				writeJSON(w, http.StatusOK, map[string]string{"code": code})
				return
			case parts[2] == "code" && r.Method == http.MethodDelete:
				if err := s.d.Store.ClearAccessCode(ctx, id); err != nil {
					s.writeErr(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			case parts[2] == "signout" && r.Method == http.MethodPost:
				if err := s.d.Store.RevokeUser(ctx, id); err != nil {
					s.writeErr(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)
	})
}

func (s *Server) adminViewers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := s.d.Auth.Users(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	codes, err := s.d.Store.usersWithCode(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	devices, err := s.d.Store.Devices(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	type row struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Title    string `json:"title"`
		IsOwner  bool   `json:"isOwner"`
		HasCode  bool   `json:"hasCode"`
		Devices  int    `json:"devices"`
	}
	out := make([]row, 0, len(users))
	for _, u := range users {
		out = append(out, row{ID: u.ID, Username: u.Username, Title: u.Title, IsOwner: u.IsOwner, HasCode: codes[u.ID], Devices: devices[u.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewers": out})
}
