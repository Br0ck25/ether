package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

type fakeCatalog struct{ byTitle map[string][]core.Title }

func (f fakeCatalog) Search(_ context.Context, q string, _ []core.MediaType, _ int) ([]core.Title, error) {
	return f.byTitle[strings.ToLower(q)], nil
}
func (f fakeCatalog) Get(context.Context, core.RatingKey) (*core.Title, error) {
	return nil, core.ErrNotFound
}
func (f fakeCatalog) Children(context.Context, core.RatingKey) ([]core.Title, error) {
	return nil, nil
}
func (f fakeCatalog) Grandchildren(context.Context, core.RatingKey) ([]core.Title, error) {
	return nil, nil
}
func (f fakeCatalog) Browse(context.Context, core.BrowseQuery) (core.Page, error) {
	return core.Page{}, nil
}
func (f fakeCatalog) List(context.Context, string, core.MediaType, int) ([]core.Title, error) {
	return nil, nil
}
func (f fakeCatalog) Similar(context.Context, core.RatingKey, int) ([]core.Title, error) {
	return nil, nil
}
func (f fakeCatalog) Collection(context.Context, int64) (string, []core.Title, error) {
	return "", nil, nil
}
func (f fakeCatalog) Facet(context.Context, core.MediaType, string) ([]core.FacetValue, error) {
	return nil, nil
}
func (f fakeCatalog) NextEpisode(context.Context, core.RatingKey) (*core.Title, error) {
	return nil, core.ErrNotFound
}
func (f fakeCatalog) ByExternalID(context.Context, string, string) (*core.Title, error) {
	return nil, core.ErrNotFound
}

func TestParseTitlesMalformed(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		first string
		year  int
		count int
	}{
		{"object", `{"titles":[{"title":"Arrival","year":2016}]}`, "Arrival", 2016, 1},
		{"fenced+prose", "Here you go:\n```json\n{\"titles\":[{\"name\":\"Dune\",\"year\":\"2021\"}]}\n```\nEnjoy!", "Dune", 2021, 1},
		{"bare array", `[{"title":"Severance","year":2022,"type":"show"}]`, "Severance", 2022, 1},
		{"string array", `{"titles":["Heat (1995)","Casino (1995)"]}`, "Heat", 1995, 2},
		{"lines", "1. Oldboy (2003)\n2. Parasite (2019)\n- Memories of Murder (2003)", "Oldboy", 2003, 3},
		{"empty", "", "", 0, 0},
		{"garbage json", `{"titles": [broken`, "", 0, 0},
	}
	for _, tc := range cases {
		refs := ParseTitles(tc.in)
		if len(refs) != tc.count {
			t.Errorf("%s: got %d refs %v, want %d", tc.name, len(refs), refs, tc.count)
			continue
		}
		if tc.count > 0 && (refs[0].Title != tc.first || refs[0].Year != tc.year) {
			t.Errorf("%s: first=%+v want %s %d", tc.name, refs[0], tc.first, tc.year)
		}
	}

	rows := ParseRows(`{"rows":[{"title":"Slow-burn sci-fi","titles":[{"title":"Arrival","year":2016}]},{"theme":"Heists","items":["Heat (1995)"]}]}`)
	if len(rows) != 2 || rows[0].Title != "Slow-burn sci-fi" || rows[1].Titles[0].Title != "Heat" {
		t.Fatalf("rows: %+v", rows)
	}
}

func chatServer(t *testing.T, rejectJSONMode bool, content string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if rejectJSONMode && req["response_format"] != nil {
			http.Error(w, `{"error":"response_format unsupported"}`, 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestServiceMoreLikeThisAndRows(t *testing.T) {
	var calls atomic.Int32
	srv := chatServer(t, true, `{"rows":[{"title":"Mind-benders","titles":[{"title":"Arrival","year":2016},{"title":"Nonexistent Film","year":1999}]}],"titles":[{"title":"Arrival","year":2016}]}`, &calls)
	cat := fakeCatalog{byTitle: map[string][]core.Title{
		"arrival": {{Key: core.MovieKey(329865), Type: core.TypeMovie, Title: "Arrival", Year: 2016}},
	}}
	client := NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL + "/v1", Model: "test-model"}})
	svc := New(ServiceOptions{Client: client, Catalog: cat})

	seed := &core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, Title: "Starlight", Year: 2014, Genres: []string{"Sci-Fi"}}
	titles, err := svc.MoreLikeThis(context.Background(), seed, 5)
	if err != nil {
		t.Fatal(err)
	}
	// ParseTitles flattens rows+titles; unresolvable drops, dupe collapses.
	if len(titles) != 1 || titles[0].Title != "Arrival" {
		t.Fatalf("titles: %+v", titles)
	}
	// JSON mode was rejected once, retried without -> 2 calls; second read is cached.
	before := calls.Load()
	if _, err := svc.MoreLikeThis(context.Background(), seed, 5); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatalf("expected cache hit, calls %d -> %d", before, calls.Load())
	}

	// ThemedRows: first call is non-blocking empty, background fills cache.
	user := &core.User{ID: 7}
	hubs, err := svc.ThemedRows(context.Background(), user, []core.Title{*seed}, 5)
	if err != nil || len(hubs) != 0 {
		t.Fatalf("first themed rows should omit while computing: %v %v", hubs, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hubs, _ = svc.ThemedRows(context.Background(), user, []core.Title{*seed}, 5)
		if len(hubs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(hubs) != 1 || hubs[0].Title != "Mind-benders" || len(hubs[0].Items) != 1 {
		t.Fatalf("themed hubs: %+v", hubs)
	}

	// Disabled client: nil, nil.
	client.UpdateConfig(config.LLM{Enabled: false})
	if out, err := svc.MoreLikeThis(context.Background(), seed, 5); err != nil || out != nil {
		t.Fatalf("disabled: %v %v", out, err)
	}
}

func TestThemedRowsContrastiveSyncExcludesDislikes(t *testing.T) {
	var calls atomic.Int32
	var seenPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Messages []Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) > 1 {
			seenPrompt = req.Messages[1].Content
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role":    "assistant",
				"content": `{"rows":[{"title":"Cerebral Sci-Fi","titles":[{"title":"Arrival","year":2016},{"title":"Bounced Trash","year":2020}]}]}`,
			}}},
		})
	}))
	t.Cleanup(srv.Close)
	cat := fakeCatalog{byTitle: map[string][]core.Title{
		"arrival":       {{Key: core.MovieKey(329865), Type: core.TypeMovie, Title: "Arrival", Year: 2016}},
		"bounced trash": {{Key: core.MovieKey(999999), Type: core.TypeMovie, Title: "Bounced Trash", Year: 2020}},
	}}
	client := NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL + "/v1", Model: "test-model"}})
	svc := New(ServiceOptions{Client: client, Catalog: cat})
	seed := core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, Title: "Dune", Year: 2021, Genres: []string{"Sci-Fi"}}
	disliked := core.Title{Key: core.MovieKey(999999), Type: core.TypeMovie, Title: "Bounced Trash", Year: 2020, Genres: []string{"Horror"}}
	hubs, err := svc.ThemedRowsContrastiveSync(context.Background(), &core.User{ID: 9}, []core.Title{seed}, []core.Title{disliked}, []string{"Horror"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenPrompt, "Bounced Trash") || !strings.Contains(seenPrompt, "Horror") {
		t.Fatalf("contrastive prompt missing negative evidence: %s", seenPrompt)
	}
	if len(hubs) != 1 || len(hubs[0].Items) != 1 || hubs[0].Items[0].Title != "Arrival" {
		t.Fatalf("expected disliked title excluded from resolved items, got %+v", hubs)
	}
}
