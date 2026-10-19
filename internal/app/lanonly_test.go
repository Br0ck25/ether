package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLANOnly(t *testing.T) {
	h := lanOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for addr, want := range map[string]int{
		"127.0.0.1:5000":  http.StatusOK,
		"192.168.1.20:1":  http.StatusOK,
		"10.0.0.43:1":     http.StatusOK,
		"100.101.102.1:1": http.StatusOK, // Tailscale
		"[fd00::1]:1":     http.StatusOK,
		"203.0.113.9:1":   http.StatusForbidden,
		"[2001:db8::1]:1": http.StatusForbidden,
		"not-an-address":  http.StatusForbidden,
	} {
		r := httptest.NewRequest("GET", "/ether/", nil)
		r.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", addr, rec.Code, want)
		}
	}
}
