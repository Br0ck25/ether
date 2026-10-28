package trackprefs

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Handler serves /{userID} and /{userID}/shows/{showID} (DELETE).
// Mount behind the admin owner's authentication and CSRF middleware.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		user, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || user < 0 {
			http.Error(w, "invalid user", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(parts) == 2 && parts[1] == "shows" && r.Method == http.MethodGet {
			memory, err := s.Memory(r.Context(), user)
			if err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			json.NewEncoder(w).Encode(memory)
			return
		}
		if len(parts) == 3 && parts[1] == "shows" && r.Method == http.MethodDelete {
			show, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil || show <= 0 {
				http.Error(w, "invalid show", 400)
				return
			}
			if err = s.Forget(r.Context(), user, show); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if len(parts) != 1 {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			rule, err := s.GetRules(r.Context(), user)
			if err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			json.NewEncoder(w).Encode(rule)
		case http.MethodPut:
			var rule Rules
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&rule); err != nil {
				http.Error(w, "invalid rules", 400)
				return
			}
			if err := rule.Validate(); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := s.PutRules(r.Context(), user, rule); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			json.NewEncoder(w).Encode(rule)
		case http.MethodDelete:
			if err := s.DeleteRules(r.Context(), user); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			w.WriteHeader(204)
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			w.WriteHeader(405)
		}
	})
}
