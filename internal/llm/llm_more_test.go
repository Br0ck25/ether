package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ether-media/ether/internal/config"
	"github.com/ether-media/ether/internal/core"
)

func TestParseMalformedModelOutputs(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		titles []string
	}{
		{"trailing commas", `{"titles":[{"title":"Heat","year":1995,},{"title":"Ronin","year":1998},],}`, []string{"Heat", "Ronin"}},
		{"truncated mid-object", `{"titles":[{"title":"Heat","year":1995},{"title":"Ronin","year":1998},{"title":"Coll`, []string{"Heat", "Ronin"}},
		{"think block", "<think>The user likes {heists}. Maybe [Heat]?</think>\n{\"titles\":[{\"title\":\"Thief\",\"year\":1981}]}", []string{"Thief"}},
		{"unterminated think", "{\"titles\":[{\"title\":\"Heat\"}]}<think>more", []string{"Heat"}},
		{"year as string with spaces", `[{"name":"Collateral","year":" 2004 "}]`, []string{"Collateral"}},
		{"recommendations key", `{"recommendations":[{"title":"Drive","year":2011}]}`, []string{"Drive"}},
		{"numbered plain text", "Sure! Here are some:\n1) Heat (1995)\n2) Ronin (1998)", []string{"Sure! Here are some:", "Heat", "Ronin"}},
	}
	for _, tc := range cases {
		refs := ParseTitles(StripReasoning(tc.in))
		var got []string
		for _, r := range refs {
			got = append(got, r.Title)
		}
		if strings.Join(got, "|") != strings.Join(tc.titles, "|") {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.titles)
		}
	}
	if refs := ParseTitles(`[{"title":"Collateral","year":" 2004 "}]`); refs[0].Year != 2004 {
		t.Fatalf("string year: %+v", refs)
	}

	// Truncated rows output keeps the complete rows.
	rows := ParseRows(`{"rows":[{"title":"Heists","titles":[{"title":"Heat","year":1995}]},{"title":"Neo-noir","titles":[{"title":"Drive","year":2011},{"tit`)
	if len(rows) != 2 || rows[0].Title != "Heists" || len(rows[1].Titles) != 1 || rows[1].Titles[0].Title != "Drive" {
		t.Fatalf("truncated rows: %+v", rows)
	}
	// A single row object.
	rows = ParseRows(`{"title":"Cosy mysteries","titles":["Knives Out (2019)"]}`)
	if len(rows) != 1 || rows[0].Title != "Cosy mysteries" || rows[0].Titles[0].Year != 2019 {
		t.Fatalf("single row: %+v", rows)
	}
}

func TestMatchRef(t *testing.T) {
	res := []core.Title{
		{Key: core.MovieKey(1), Title: "Dune", Year: 1984},
		{Key: core.MovieKey(2), Title: "Dune", Year: 2021},
		{Key: core.MovieKey(3), Title: "Dune: Part Two", Year: 2024},
	}
	cases := []struct {
		ref  TitleRef
		want int
	}{
		{TitleRef{Title: "Dune", Year: 2021}, 1},
		{TitleRef{Title: "Dune", Year: 2020}, 1}, // off by one
		{TitleRef{Title: "Dune", Year: 1984}, 0},
		{TitleRef{Title: "dune part two", Year: 2024}, 2}, // punctuation folded
		{TitleRef{Title: "Dune Messiah", Year: 2027}, -1}, // hallucination drops
		{TitleRef{Title: "", Year: 2021}, -1},
	}
	for _, tc := range cases {
		if got := matchRef(tc.ref, res); got != tc.want {
			t.Errorf("matchRef(%+v)=%d want %d", tc.ref, got, tc.want)
		}
	}
	if got := matchRef(TitleRef{Title: "The Office"}, []core.Title{{Title: "Office"}}); got != 0 {
		t.Errorf("article folding: %d", got)
	}
	if got := matchRef(TitleRef{Title: "Fast & Furious", Year: 2009}, []core.Title{{Title: "Fast and Furious", Year: 2009}}); got != 0 {
		t.Errorf("ampersand folding: %d", got)
	}
}

func TestClientErrorsAndHeaders(t *testing.T) {
	var gotAuth, gotModel atomic.Value
	var mode atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		gotModel.Store(req["model"])
		switch mode.Load() {
		case 0: // 422 on response_format, OK without
			if req["response_format"] != nil {
				w.WriteHeader(422)
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"content":"<think>hmm</think>{\"ok\":true}"}}]}`))
		case 1:
			http.Error(w, "boom", 500)
		case 2:
			w.Write([]byte(`{"error":{"message":"model not found"}}`))
		case 3:
			w.Write([]byte(`{"choices":[]}`))
		case 4:
			w.Write([]byte(`not json`))
		}
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL + "/", APIKey: "sk-test", Model: "m1"}})
	out, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, true)
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("422 fallback + strip: %q %v", out, err)
	}
	if gotAuth.Load() != "Bearer sk-test" || gotModel.Load() != "m1" {
		t.Fatalf("headers: %v %v", gotAuth.Load(), gotModel.Load())
	}
	for m := int32(1); m <= 4; m++ {
		mode.Store(m)
		if _, err := c.Chat(context.Background(), nil, false); err == nil {
			t.Errorf("mode %d: expected error", m)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Chat(ctx, nil, false); err == nil {
		t.Fatal("cancelled ctx should error")
	}
	c.UpdateConfig(config.LLM{Enabled: true})
	if _, err := c.Chat(context.Background(), nil, false); err == nil {
		t.Fatal("missing base URL/model should error")
	}
}

// slowServer answers after release is closed, counting calls.
func slowServer(t *testing.T, content string, release chan struct{}, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMoreLikeThisNeverOutwaitsCaller(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := slowServer(t, `{"titles":[{"title":"Arrival","year":2016}]}`, release, &calls)
	cat := fakeCatalog{byTitle: map[string][]core.Title{"arrival": {{Key: core.MovieKey(9), Type: core.TypeMovie, Title: "Arrival", Year: 2016}}}}
	svc := New(ServiceOptions{Client: NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL, Model: "m"}}), Catalog: cat})
	seed := &core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, Title: "Interstellar", Year: 2014}

	// Concurrent callers with short deadlines: one model call, nobody waits long.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			start := time.Now()
			out, _ := svc.MoreLikeThis(ctx, seed, 5)
			if time.Since(start) > time.Second || out != nil {
				t.Errorf("blocked or returned early data: %v %v", time.Since(start), out)
			}
		}()
	}
	wg.Wait()
	close(release)
	waitFor(t, func() bool {
		out, _ := svc.MoreLikeThis(context.Background(), seed, 5)
		return len(out) == 1
	})
	if n := calls.Load(); n != 1 {
		t.Fatalf("single-flight: %d model calls", n)
	}
}

func TestThemedRowsStaleServeAndBackoff(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "down", 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"content": `{"rows":[{"title":"Heists","titles":[{"title":"Heat","year":1995}]},{"title":"Heists","titles":[{"title":"Ronin","year":1998}]}]}`}}}})
	}))
	defer srv.Close()
	cat := fakeCatalog{byTitle: map[string][]core.Title{
		"heat":  {{Key: core.MovieKey(949), Type: core.TypeMovie, Title: "Heat", Year: 1995}},
		"ronin": {{Key: core.MovieKey(8195), Type: core.TypeMovie, Title: "Ronin", Year: 1998}},
	}}
	var now atomic.Int64
	now.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	svc := New(ServiceOptions{
		Client:  NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL, Model: "m"}}),
		Catalog: cat, TTL: time.Hour, MinRefresh: 10 * time.Minute, ErrorBackoff: 5 * time.Minute,
		Now: func() time.Time { return time.Unix(0, now.Load()) },
	})
	user := &core.User{ID: 3, MaxContentRating: "PG-13"}
	seedA := []core.Title{{Key: core.MovieKey(1), Title: "Thief", Year: 1981}}
	ctx := context.Background()

	hubs, err := svc.ThemedRowsSync(ctx, user, seedA, 5)
	if err != nil || len(hubs) != 2 || hubs[0].ID != "llm.heists" || hubs[1].ID != "llm.heists-2" {
		t.Fatalf("sync rows: %+v %v", hubs, err)
	}
	if hubs[0].Context != "hub.home.llm" || !hubs[0].Promoted {
		t.Fatalf("hub shape: %+v", hubs[0])
	}
	// New seeds within MinRefresh: served from cache, no call.
	seedB := append(seedA, core.Title{Key: core.MovieKey(2), Title: "Collateral"})
	before := calls.Load()
	if hubs, _ := svc.ThemedRows(ctx, user, seedB, 5); len(hubs) != 2 || calls.Load() != before {
		t.Fatalf("within MinRefresh should serve cache without a call: %d->%d", before, calls.Load())
	}
	// Past MinRefresh with changed seeds: stale served, refresh in background.
	now.Add(int64(11 * time.Minute))
	if hubs, _ := svc.ThemedRows(ctx, user, seedB, 5); len(hubs) != 2 {
		t.Fatalf("stale rows not served: %+v", hubs)
	}
	waitFor(t, func() bool { return calls.Load() == before+1 })

	// Endpoint down after TTL: stale rows still served, retries back off.
	fail.Store(true)
	now.Add(int64(2 * time.Hour))
	waitFor(t, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return len(svc.flights) == 0
	})
	before = calls.Load()
	if hubs, _ := svc.ThemedRows(ctx, user, seedB, 5); len(hubs) != 2 {
		t.Fatalf("stale rows on failure: %+v", hubs)
	}
	waitFor(t, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return len(svc.flights) == 0 && calls.Load() == before+1
	})
	for i := 0; i < 5; i++ {
		_, _ = svc.ThemedRows(ctx, user, seedB, 5)
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != before+1 {
		t.Fatalf("error backoff not applied: %d calls", calls.Load()-before)
	}
	// Another user has an independent cache entry.
	if hubs, _ := svc.ThemedRows(ctx, &core.User{ID: 4}, nil, 5); hubs != nil {
		t.Fatalf("other user: %+v", hubs)
	}
	svc.Invalidate(3)
	if hubs, _ := svc.ThemedRows(ctx, user, seedB, 5); hubs != nil {
		t.Fatalf("invalidated: %+v", hubs)
	}
}

func TestCacheBounded(t *testing.T) {
	var calls atomic.Int32
	srv := chatServer(t, false, `{"titles":[]}`, &calls)
	svc := New(ServiceOptions{Client: NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL, Model: "m"}}), Catalog: fakeCatalog{}, MaxEntries: 3})
	for i := 1; i <= 10; i++ {
		_, _ = svc.MoreLikeThis(context.Background(), &core.Title{Key: core.MovieKey(int64(i)), Title: "x"}, 3)
	}
	svc.mu.Lock()
	n := len(svc.cache)
	svc.mu.Unlock()
	if n > 3 {
		t.Fatalf("cache grew to %d", n)
	}
	if slug("  Slow-burn Sci-Fi: With a Twist!! ") != "slow-burn-sci-fi-with-a-twist" || slug("!!!") != "row" {
		t.Fatalf("slug: %q", slug("  Slow-burn Sci-Fi: With a Twist!! "))
	}
}
