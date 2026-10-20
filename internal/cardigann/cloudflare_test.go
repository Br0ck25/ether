package cardigann

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

const challengePage = `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>
<script>window._cf_chl_opt={cvId:'3'};</script></body></html>`

// challengeSite answers only clients holding the clearance cookie and the
// solver's user agent, like a site behind a browser challenge.
func challengeSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("cf_clearance")
		if err != nil || c.Value != "solved" || r.UserAgent() != "SolverBrowser/1.0" {
			w.Header().Set("Server", "cloudflare")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, challengePage)
			return
		}
		fmt.Fprint(w, `<ul><li><span>Film 2020 1080p</span><a href="magnet:?xt=urn:btih:abababababababababababababababababababab">m</a></li></ul>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCloudflareWithoutFlareSolverr(t *testing.T) {
	site := challengeSite(t)
	r := testRunner(t, loadDef(t, "cf.yml"), site.URL, runnerSetup{})
	_, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Film"}, nil, nil, nil)
	if !errors.Is(err, ErrCloudflare) || !strings.Contains(err.Error(), "FlareSolverr") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloudflareSolvedThroughFlareSolverr(t *testing.T) {
	site := challengeSite(t)
	var mu sync.Mutex
	var calls []map[string]any
	flare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		calls = append(calls, in)
		mu.Unlock()
		host := strings.TrimPrefix(site.URL, "http://")
		host = host[:strings.IndexByte(host, ':')]
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "message": "Challenge solved!",
			"solution": map[string]any{
				"url": in["url"], "status": 200, "userAgent": "SolverBrowser/1.0", "response": "<html></html>",
				"cookies": []map[string]any{{"name": "cf_clearance", "value": "solved", "domain": "." + host, "path": "/"}},
			},
		})
	}))
	defer flare.Close()
	r := testRunner(t, loadDef(t, "cf.yml"), site.URL, runnerSetup{flare: flare.URL})
	rels, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Film"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].InfoHash != strings.Repeat("ab", 20) {
		t.Fatalf("rels = %+v", rels)
	}
	if len(calls) != 1 || calls[0]["cmd"] != "request.get" || !strings.HasPrefix(calls[0]["url"].(string), site.URL+"/search") {
		t.Fatalf("flaresolverr calls = %v", calls)
	}
	// The clearance cookie and user agent are reused: no second solve.
	if _, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Film 2"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("solved again: %d calls", len(calls))
	}
}

func TestFlareSolverrFailureIsReported(t *testing.T) {
	site := challengeSite(t)
	flare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "Error solving the challenge. Timeout after 60.0 seconds."})
	}))
	defer flare.Close()
	r := testRunner(t, loadDef(t, "cf.yml"), site.URL, runnerSetup{flare: flare.URL + "/v1"})
	_, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Film"}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Timeout after 60.0 seconds") {
		t.Fatalf("err = %v", err)
	}
}

func TestIsChallenge(t *testing.T) {
	cf := http.Header{"Server": {"cloudflare"}}
	for _, tc := range []struct {
		r    response
		want bool
	}{
		{response{status: 503, header: cf, body: []byte(challengePage)}, true},
		{response{status: 403, header: cf, body: []byte("<title>Attention Required! | Cloudflare</title>")}, true},
		{response{status: 403, header: http.Header{}, body: []byte("<script src='/cdn-cgi/challenge-platform/x'></script>")}, true},
		{response{status: 200, header: cf, body: []byte(challengePage)}, false},
		{response{status: 403, header: cf, body: []byte("Forbidden")}, false},
		{response{status: 503, header: http.Header{}, body: []byte("<title>Just a moment</title>")}, false},
	} {
		if got := isChallenge(&tc.r); got != tc.want {
			t.Errorf("isChallenge(%d %q) = %v", tc.r.status, tc.r.body, got)
		}
	}
}
