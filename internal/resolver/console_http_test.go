package resolver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

type fakeCatalog struct {
	titles map[core.RatingKey]*core.Title
}

func (f fakeCatalog) Get(_ context.Context, key core.RatingKey) (*core.Title, error) {
	if t, ok := f.titles[key]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, core.ErrNotFound
}

func (f fakeCatalog) Children(_ context.Context, key core.RatingKey) ([]core.Title, error) {
	switch key.Kind() {
	case core.KindShow:
		return []core.Title{{Key: core.SeasonKey(key.TMDB(), 1), Type: core.TypeSeason, Title: "Season 1", Season: 1}}, nil
	case core.KindSeason:
		return []core.Title{
			{Key: core.EpisodeKey(key.TMDB(), 1, 1), Type: core.TypeEpisode, Title: "Pilot"},
			{Key: core.EpisodeKey(key.TMDB(), 1, 2), Type: core.TypeEpisode, Title: "Second"},
		}, nil
	}
	return nil, core.ErrNotFound
}

type consoleFixture struct {
	svc *Service
	srv *httptest.Server

	mu          sync.Mutex
	invalidated []core.RatingKey
}

func newConsoleFixture(t *testing.T) *consoleFixture {
	t.Helper()
	f := &consoleFixture{}
	idx := &fakeIndexer{releases: movieReleases()}
	f.svc = New(idx, fakeHealth{unhealthy: map[string]bool{"sd720": true}}, openTestDB(t), testCfg(), Options{})
	cat := fakeCatalog{titles: map[core.RatingKey]*core.Title{matrixTitle().Key: matrixTitle()}}
	h := f.svc.ConsoleHandler(ConsoleDeps{
		Catalog: cat,
		Invalidate: func(k core.RatingKey) {
			f.mu.Lock()
			f.invalidated = append(f.invalidated, k)
			f.mu.Unlock()
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	f.srv = httptest.NewServer(h)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *consoleFixture) do(t *testing.T, method, path, body string, out any) int {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	return res.StatusCode
}

func (f *consoleFixture) takeInvalidated() []core.RatingKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.invalidated
	f.invalidated = nil
	return out
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestConsoleHTTPSearchPinAndUnpin(t *testing.T) {
	f := newConsoleFixture(t)
	key := matrixTitle().Key.String()

	var search ConsoleSearch
	if code := f.do(t, "GET", "/search?key="+key+"&profile=1080p", "", &search); code != 200 {
		t.Fatalf("search status %d", code)
	}
	if search.Winner != "hd" || len(search.Releases) != 3 || search.TitleName != "The Signal (1999)" {
		t.Fatalf("search %+v", search)
	}

	var e apiError
	for _, tc := range []struct{ path, code string }{
		{"/search?key=abc", "invalid"},
		{"/search?key=" + core.ShowKey(1).String(), "invalid"},
		{"/search?key=" + core.MovieKey(42).String(), "not_found"},
		{"/search?key=" + key + "&profile=nope", "invalid"},
	} {
		e = apiError{}
		if code := f.do(t, "GET", tc.path, "", &e); code < 400 || e.Error.Code != tc.code {
			t.Errorf("%s: status %d code %q", tc.path, code, e.Error.Code)
		}
	}

	var pin Pin
	if code := f.do(t, "POST", "/pins", `{"key":"`+key+`","releaseId":"hd","note":"keeper"}`, &pin); code != 200 {
		t.Fatalf("pin status %d", code)
	}
	if pin.Release.ID != "hd" || pin.TitleName != "The Signal (1999)" || pin.Note != "keeper" {
		t.Fatalf("pin %+v", pin)
	}
	if got := f.takeInvalidated(); len(got) != 1 || got[0] != matrixTitle().Key {
		t.Fatalf("pin invalidated %v", got)
	}
	e = apiError{}
	if code := f.do(t, "POST", "/pins", `{"key":"`+key+`","releaseId":"nope"}`, &e); code != 404 || e.Error.Code != "unknown_release" {
		t.Fatalf("unknown release status %d %+v", code, e)
	}
	if code := f.do(t, "POST", "/pins", `{"key":"`+key+`","releaseId":"hd","nzbUrl":"http://evil"}`, nil); code != 400 {
		t.Fatalf("unknown field status %d", code)
	}
	var pins struct{ Pins []Pin }
	if f.do(t, "GET", "/pins", "", &pins); len(pins.Pins) != 1 || pins.Pins[0].TitleKey != matrixTitle().Key {
		t.Fatalf("pins %+v", pins)
	}
	if code := f.do(t, "DELETE", "/pins/"+key, "", nil); code != 204 {
		t.Fatalf("unpin status %d", code)
	}
	if code := f.do(t, "DELETE", "/pins/"+key, "", nil); code != 404 {
		t.Fatalf("second unpin status %d", code)
	}
	if got := f.takeInvalidated(); len(got) != 1 {
		t.Fatalf("unpin invalidated %v", got)
	}
}

func TestConsoleHTTPBlocklistHealthForgetHistory(t *testing.T) {
	f := newConsoleFixture(t)
	key := matrixTitle().Key.String()
	if code := f.do(t, "GET", "/search?key="+key, "", nil); code != 200 {
		t.Fatalf("search status %d", code)
	}

	var entry BlockEntry
	if code := f.do(t, "POST", "/blocklist", `{"releaseId":"hd","key":"`+key+`","reason":"bad subs"}`, &entry); code != 200 {
		t.Fatalf("block status %d", code)
	}
	if entry.Source != BlockOwner || !entry.Manual || entry.Reason != "bad subs" || entry.TitleName != "The Signal (1999)" {
		t.Fatalf("entry %+v", entry)
	}
	if got := f.takeInvalidated(); len(got) != 1 || got[0] != matrixTitle().Key {
		t.Fatalf("block invalidated %v", got)
	}
	var e apiError
	if code := f.do(t, "POST", "/pins", `{"key":"`+key+`","releaseId":"hd"}`, &e); code != 409 || e.Error.Code != "blocked" {
		t.Fatalf("pin blocked status %d %+v", code, e)
	}

	var health HealthResult
	if code := f.do(t, "POST", "/health", `{"key":"`+key+`","releaseId":"sd720"}`, &health); code != 200 {
		t.Fatalf("health status %d", code)
	}
	if health.Healthy || !health.Blocked {
		t.Fatalf("health %+v", health)
	}
	if got := f.takeInvalidated(); len(got) != 1 {
		t.Fatalf("failed check invalidated %v", got)
	}
	if code := f.do(t, "POST", "/health", `{"releaseId":""}`, nil); code != 400 {
		t.Fatalf("empty health status %d", code)
	}

	var list struct {
		Items []BlockEntry
		Total int
	}
	if f.do(t, "GET", "/blocklist?source=auto", "", &list); list.Total != 1 || list.Items[0].ReleaseID != "sd720" {
		t.Fatalf("auto list %+v", list)
	}
	if code := f.do(t, "GET", "/blocklist?source=weird", "", nil); code != 400 {
		t.Fatalf("bad source status %d", code)
	}
	if f.do(t, "GET", "/blocklist?q=web-dl&key="+key, "", &list); list.Total != 1 || list.Items[0].ReleaseID != "hd" {
		t.Fatalf("query list %+v", list)
	}
	if code := f.do(t, "DELETE", "/blocklist/hd", "", nil); code != 204 {
		t.Fatalf("unblock status %d", code)
	}
	if code := f.do(t, "DELETE", "/blocklist/hd", "", nil); code != 404 {
		t.Fatalf("second unblock status %d", code)
	}
	if got := f.takeInvalidated(); len(got) != 1 || got[0] != matrixTitle().Key {
		t.Fatalf("unblock invalidated %v", got)
	}

	if code := f.do(t, "POST", "/forget", `{"key":"`+key+`"}`, nil); code != 200 {
		t.Fatalf("forget status %d", code)
	}
	if got := f.takeInvalidated(); len(got) != 1 {
		t.Fatalf("forget invalidated %v", got)
	}

	if _, err := f.svc.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()}); err != nil {
		t.Fatal(err)
	}
	var hist struct {
		Items []HistoryRecord
		Total int
	}
	if f.do(t, "GET", "/history?key="+key, "", &hist); hist.Total != 1 || hist.Items[0].ReleaseID != "hd" {
		t.Fatalf("history %+v", hist)
	}
	var full HistoryRecord
	if code := f.do(t, "GET", "/history/"+itoa(hist.Items[0].ID), "", &full); code != 200 || full.Trace == nil || len(full.Trace.Candidates) == 0 {
		t.Fatalf("history entry %d %+v", code, full)
	}
	if code := f.do(t, "GET", "/history/0", "", nil); code != 400 {
		t.Fatalf("bad id status %d", code)
	}
	if code := f.do(t, "GET", "/history/12345", "", nil); code != 404 {
		t.Fatalf("missing id status %d", code)
	}
}

func TestConsoleHTTPChildrenAndProfiles(t *testing.T) {
	f := newConsoleFixture(t)
	var kids struct{ Items []childTitle }
	if code := f.do(t, "GET", "/titles/"+core.SeasonKey(5, 1).String()+"/children", "", &kids); code != 200 {
		t.Fatalf("children status %d", code)
	}
	if len(kids.Items) != 2 || kids.Items[1].Episode != 2 || kids.Items[1].Season != 1 || kids.Items[0].Key != core.EpisodeKey(5, 1, 1).String() {
		t.Fatalf("episodes %+v", kids.Items)
	}
	if code := f.do(t, "GET", "/titles/"+matrixTitle().Key.String()+"/children", "", nil); code != 400 {
		t.Fatalf("movie children status %d", code)
	}
	var profiles struct {
		Default  string
		Profiles []string
		Special  []string
	}
	if f.do(t, "GET", "/profiles", "", &profiles); profiles.Default != "1080p" || len(profiles.Profiles) != 3 || len(profiles.Special) != 2 {
		t.Fatalf("profiles %+v", profiles)
	}
	if code := f.do(t, "GET", "/nope", "", nil); code != 404 {
		t.Fatalf("unknown path status %d", code)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
