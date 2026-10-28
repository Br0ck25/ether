package newznab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestProwlarrDiscoversUsenetAndTorrentAndUsesBudgetedProxy(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/base/api/v1/indexer":
			if r.Header.Get("X-Api-Key") != "test-key" {
				t.Error("missing auth")
			}
			fmt.Fprint(w, `[{"id":4,"name":"News","enable":true,"protocol":"usenet","priority":2},{"id":5,"name":"Torrent","enable":true,"protocol":"torrent"},{"id":6,"name":"Disabled","enable":false,"protocol":"usenet"}]`)
		case "/base/4/api":
			if r.URL.Query().Get("apikey") != "test-key" {
				t.Error("missing proxied auth")
			}
			fmt.Fprint(w, `<caps><searching><search available="yes" supportedParams="q"/></searching></caps>`)
		default:
			t.Errorf("bad proxy URL: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cfg := config.Prowlarr{URL: srv.URL + "/base", APIKey: "test-key", DailyLimit: 1, DailyGrabs: 2, Priority: 5}
	items, err := DiscoverProwlarr(context.Background(), cfg, Options{})
	if err != nil || len(items) != 2 {
		t.Fatalf("discover %+v %v", items, err)
	}
	if items[0].IsTorrent() || !items[1].IsTorrent() || !strings.Contains(items[1].Name, "Torrent") {
		t.Fatalf("protocols %+v", items)
	}
	item := items[0]
	if item.DailyLimit != 1 || item.DailyGrabs != 2 || item.Priority != 7 || !strings.HasPrefix(item.ID, "prowlarr-") {
		t.Fatalf("budget/identity %+v", item)
	}
	c := New(item, nil, Options{})
	if _, err := c.Caps(context.Background()); err != nil {
		t.Fatal(err)
	}
	remaining, err := c.Remaining(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls %d remaining %+v", calls, remaining)
	}
	if !c.Exhausted(context.Background()) {
		t.Fatal("discovered indexer bypassed daily budget")
	}
}
