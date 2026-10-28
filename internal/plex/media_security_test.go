package plex

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/extras"
)

func assertPassiveMedia(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	directives := map[string]string{}
	for _, raw := range strings.Split(rec.Header().Get("Content-Security-Policy"), ";") {
		key, val, _ := strings.Cut(strings.TrimSpace(raw), " ")
		directives[key] = val
	}
	if val, ok := directives["sandbox"]; !ok || val != "" {
		t.Fatalf("media is not sandboxed: %v", directives)
	}
	for _, key := range []string{"script-src", "object-src", "base-uri"} {
		if directives[key] != "'none'" {
			t.Fatalf("media allows %s: %v", key, directives)
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("media can be MIME-sniffed")
	}
}

func TestPlexDataAndMediaCannotExecuteOnOwnerOrigin(t *testing.T) {
	// All Plex data paths pass commonHeaders, including direct media,
	// subtitles, live resource proxies, error responses, and new routes.
	// The header policy must not change native clients' bytes or ranges.
	for _, ct := range []string{"text/html", "image/svg+xml", "text/vtt", "application/json", "video/mp4", "video/mp2t", "application/vnd.apple.mpegurl"} {
		t.Run(ct, func(t *testing.T) {
			body := "<script>localStorage.getItem('ether-admin-token')</script>"
			s := &Server{}
			h := s.commonHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", ct)
				w.Header().Set("Content-Range", "bytes 0-4/10")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte(body))
			}))
			r := httptest.NewRequest(http.MethodGet, "/resource", nil)
			r.Header.Set("Origin", "https://app.plex.tv")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusPartialContent || rec.Body.String() != body || rec.Header().Get("Content-Type") != ct || rec.Header().Get("Content-Range") != "bytes 0-4/10" || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.plex.tv" {
				t.Fatalf("media response changed: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
			}
			assertPassiveMedia(t, rec)
		})
	}
}

func TestExtraPartActiveContentIsSandboxed(t *testing.T) {
	for _, ct := range []string{"text/html", "image/svg+xml"} {
		t.Run(ct, func(t *testing.T) {
			f, ex, _ := extrasFixture(t)
			body := hostileArtworkSVG
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", ct)
				_, _ = w.Write([]byte(body))
			}))
			defer upstream.Close()
			ex.resolved = extras.Resolved{URL: upstream.URL + "/trailer.mp4", Container: "mp4"}
			rec := f.request(t, http.MethodGet, "/library/parts/"+fightClubTrailer.Key.String()+"/1/file.mp4?X-Plex-Token="+friendToken, map[string]string{"X-Plex-Token": ""})
			if rec.Code != http.StatusOK || rec.Body.String() != body || rec.Header().Get("Content-Type") != ct {
				t.Fatalf("extra upstream body not reached: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
			}
			assertPassiveMedia(t, rec)
		})
	}
}
