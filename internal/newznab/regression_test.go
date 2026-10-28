package newznab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func TestTransportErrorDoesNotLeakAPIKey(t *testing.T) {
	cfg := testConfig("https://indexer.invalid")
	cfg.APIKey = "very-secret-key"
	c := New(cfg, nil, Options{HTTPClient: &http.Client{Transport: failingTransport{}}})
	_, err := c.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, IDs: core.IDs{IMDB: "tt0133093"}})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "very-secret-key") {
		t.Fatalf("error leaks API key: %v", err)
	}
	if err := TestWithOptions(context.Background(), cfg, Options{HTTPClient: &http.Client{Transport: failingTransport{}}}); err == nil || strings.Contains(err.Error(), "very-secret-key") {
		t.Fatalf("setup test error leaks API key or is nil: %v", err)
	}
}

func TestParseJSONFeedSingleItemObject(t *testing.T) {
	body := `{"rss":{"channel":{"title":"Indexer","item":{"title":"The.Signal.1999.1080p.BluRay.x264-GRP","guid":"one","link":"https://x/nzb?id=1","attr":[{"@attributes":{"name":"size","value":"8000"}}]}}}}`
	got, err := parseFeed([]byte(body), "idx")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GUID != "one" || got[0].Size != 8000 {
		t.Fatalf("got %+v", got)
	}
}

func TestTorrentDownload429PausesIndexer(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.Protocol = "torrent"
	c := New(cfg, nil, Options{})
	rel := core.Release{Name: "x", Protocol: core.ProtocolTorrent, NZBURL: srv.URL + "/dl/1"}
	if _, err := c.ResolveTorrent(context.Background(), rel); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("err = %v", err)
	}
	if !c.Exhausted(context.Background()) {
		t.Fatal("429 from a torrent download did not pause the indexer")
	}
	rel.NZBURL = srv.URL + "/dl/2"
	if _, err := c.ResolveTorrent(context.Background(), rel); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("second err = %v", err)
	}
	if hits != 1 {
		t.Fatalf("throttled indexer contacted %d times", hits)
	}
}
