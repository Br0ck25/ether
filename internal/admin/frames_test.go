package admin_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
)

func TestDashboardFramePolicy(t *testing.T) {
	store := newStore(t, func(c *config.Config) { seededConfig(c); c.SetupComplete = true })
	h := admin.New(admin.Deps{Config: store, IsOwnerToken: storedOwner(store)})
	check := func(wantCSP, wantXFO string) {
		t.Helper()
		r := req(http.MethodGet, "/ether/", "")
		r.Header.Set("Origin", "https://untrusted.example")
		r.Header.Set("Referer", "https://untrusted.example/")
		rec := serve(h, r)
		if rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != wantCSP || rec.Header().Get("X-Frame-Options") != wantXFO {
			t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
		}
	}
	check("frame-ancestors 'none'", "DENY")
	if err := store.Update(func(c *config.Config) error {
		c.Server.WebFrameOrigins = []string{"http://dashboard.example:8090/", "https://dashboard.example"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check("frame-ancestors http://dashboard.example:8090 https://dashboard.example", "")
	r := req(http.MethodGet, "/ether/api/config", "")
	if rec := serve(h, r); rec.Code != http.StatusUnauthorized {
		t.Fatalf("framing bypassed auth: %d", rec.Code)
	}
	r = req(http.MethodGet, "/ether/api/config", "")
	r.Header.Set("X-Plex-Token", ownerToken)
	if rec := serve(h, r); rec.Code != http.StatusOK {
		t.Fatalf("owner access failed: %d", rec.Code)
	}
	for _, bad := range []string{"*", "https://*.example.com", "javascript:alert(1)", "https://user:pass@example.com", "https://example.com/path", "https://example.com?q=x", "https://example.com#", "https://example.com; frame-ancestors *", "https://example.com\r\nX-Evil: yes", "https://example.com:99999", "https://example.com:", "https://example.com?", "https://example.com/%2f"} {
		t.Run(bad, func(t *testing.T) {
			if err := store.Update(func(c *config.Config) error { c.Server.WebFrameOrigins = []string{bad}; return nil }); err != nil {
				t.Fatal(err)
			}
			check("frame-ancestors 'none'", "DENY")
			found := false
			for _, e := range admin.ValidateConfig(store.Get()) {
				if e.Field == "server.webFrameOrigins" {
					found = true
				}
			}
			if !found {
				t.Fatal("invalid origin accepted by config API")
			}
		})
	}
	if err := store.Update(func(c *config.Config) error {
		c.Server.WebFrameOrigins = strings.Fields(strings.Repeat("https://example.com ", 17))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check("frame-ancestors 'none'", "DENY")
}
