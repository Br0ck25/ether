package admin_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/core"
)

// fakeEngine is a scripted Personalization.
type fakeEngine struct {
	mu        sync.Mutex
	hc        core.HubContext
	section   string
	limit     int
	overrides []core.TasteOverride
	refreshed []int64
	rules     []core.LayoutRule
	fail      error
}

type viewerFieldErr struct{ field, msg string }

func (e viewerFieldErr) Error() string              { return e.field + ": " + e.msg }
func (e viewerFieldErr) InvalidFields() [][2]string { return [][2]string{{e.field, e.msg}} }

var poster = core.Title{Key: core.MovieKey(603), Type: core.TypeMovie, Title: "The Matrix", Year: 1999, PosterPath: "/m.jpg",
	BackdropPath: "/b.jpg", Runtime: 136 * time.Minute, Genres: []string{"Science Fiction"}, ContentRating: "R", Rating: 8.2}

func (f *fakeEngine) ViewerStats(_ context.Context, ids []int64) (map[int64]core.ViewerStats, error) {
	out := map[int64]core.ViewerStats{}
	for _, id := range ids {
		out[id] = core.ViewerStats{UserID: id, Plays30d: int(id) * 2, HoursWatched30d: 1.5, LastActive: time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)}
	}
	return out, nil
}
func (f *fakeEngine) RecentActivity(context.Context, int64, int) ([]core.ViewerActivity, error) {
	return []core.ViewerActivity{{Title: poster, At: time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC), Completed: true, Progress: 1}}, nil
}
func (f *fakeEngine) Taste(_ context.Context, u *core.User) (*core.TasteProfile, error) {
	return &core.TasteProfile{UserID: u.ID, Confidence: 0.7, Genres: []core.TasteWeight{{Name: "Science Fiction", Weight: 1}},
		People: []core.TastePerson{}, Keywords: []core.TasteWeight{}, Decades: []core.TasteDecade{}, Languages: []core.TasteLanguage{},
		Habits: core.TasteHabits{PeakHours: []int{21}}, Overrides: f.overrides}, nil
}
func (f *fakeEngine) SetTasteOverrides(ctx context.Context, u *core.User, o []core.TasteOverride) (*core.TasteProfile, error) {
	for _, x := range o {
		if x.Weight > 1 {
			return nil, viewerFieldErr{"overrides[0].weight", "must be between -1 and 1"}
		}
	}
	f.mu.Lock()
	f.overrides = o
	f.mu.Unlock()
	return f.Taste(ctx, u)
}
func (f *fakeEngine) PreviewHome(_ context.Context, _ *core.User, hc core.HubContext, section string, limit int) (*core.HomePreview, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	f.mu.Lock()
	f.hc, f.section, f.limit = hc, section, limit
	f.mu.Unlock()
	return &core.HomePreview{GeneratedAt: time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC), Context: hc, Rows: []core.HomeRow{
		{HubID: "home.top10", Title: "Top 10 for You", Kind: "top-10", Reason: "why", Score: 0.17, Source: core.RowSourceEngine,
			Items: []core.Title{poster}, ItemReasons: map[core.RatingKey]string{poster.Key: "Matches your taste for Science Fiction"}},
		{HubID: "home.trending", Title: "Trending", Kind: "trending", Reason: "hidden by you", Hidden: true, Source: core.RowSourceEngine, Items: []core.Title{}},
	}}, nil
}
func (f *fakeEngine) Refresh(_ context.Context, u *core.User) error {
	f.mu.Lock()
	f.refreshed = append(f.refreshed, u.ID)
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) Layout(context.Context) ([]core.LayoutRule, error) { return f.rules, nil }
func (f *fakeEngine) SetLayout(_ context.Context, rules []core.LayoutRule) ([]core.LayoutRule, error) {
	for i, r := range rules {
		for _, rr := range r.Rows {
			if rr.Action == "explode" {
				return nil, viewerFieldErr{"rules[" + string(rune('0'+i)) + "].rows[0].action", "must be pin, hide, boost or bury"}
			}
		}
		if rules[i].ID == "" {
			rules[i].ID = "r1"
		}
	}
	f.rules = rules
	return rules, nil
}
func (f *fakeEngine) AvailableRows(context.Context) ([]core.RowDescriptor, error) {
	return []core.RowDescriptor{{HubID: "top-10", Title: "Top 10 for You", Kind: "top-10", Description: "d"}}, nil
}
func (f *fakeEngine) RowInsights(context.Context, int) ([]core.RowInsight, error) {
	return []core.RowInsight{{HubID: "top-10", Title: "Top 10 for You", Impressions7d: 20, Plays7d: 5, PlayRate: 0.25}}, nil
}
func (f *fakeEngine) Activity(context.Context, int) ([]core.ActivityDay, error) {
	return []core.ActivityDay{{Date: "2026-10-18", Plays: 3, Hours: 2.5, UniqueViewers: 2}}, nil
}

func personalizationHandler(t *testing.T, eng admin.Personalization) http.Handler {
	t.Helper()
	store := newStore(t, nil)
	us := &fakeUsers{users: []core.User{{ID: 1, PlexID: 11, Username: "alice", Title: "Alice", IsOwner: true}, {ID: 2, PlexID: 22, Username: "bob"}}}
	return admin.New(admin.Deps{Config: store, Users: us, Personalization: eng})
}

func TestViewersEndpoints(t *testing.T) {
	eng := &fakeEngine{}
	h := personalizationHandler(t, eng)

	rec := serve(h, req(http.MethodGet, "/ether/api/viewers", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("viewers: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Viewers []map[string]any `json:"viewers"`
	}
	decode(t, rec, &list)
	if len(list.Viewers) != 2 || list.Viewers[0]["username"] != "alice" || list.Viewers[1]["title"] != "bob" ||
		list.Viewers[1]["plays30d"].(float64) != 4 || list.Viewers[0]["lastActive"] != "2026-10-18T20:00:00Z" || list.Viewers[0]["plexId"].(float64) != 11 {
		t.Fatalf("viewers = %+v", list.Viewers)
	}

	rec = serve(h, req(http.MethodGet, "/ether/api/viewers/1", ""))
	var one map[string]any
	decode(t, rec, &one)
	recent := one["recent"].([]any)
	card := recent[0].(map[string]any)["title"].(map[string]any)
	if one["id"].(float64) != 1 || card["key"] != "1000000000000603" || card["poster"] != "/ether/api/image?path=%2Fm.jpg&w=342" ||
		card["runtimeMinutes"].(float64) != 136 || recent[0].(map[string]any)["completed"] != true {
		t.Fatalf("viewer = %+v", one)
	}
	if rec := serve(h, req(http.MethodGet, "/ether/api/viewers/99", "")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown viewer: %d", rec.Code)
	}
	if rec := serve(h, req(http.MethodGet, "/ether/api/viewers/x", "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}

	rec = serve(h, req(http.MethodGet, "/ether/api/viewers/1/taste", ""))
	var taste core.TasteProfile
	decode(t, rec, &taste)
	if taste.UserID != 1 || taste.Genres[0].Name != "Science Fiction" {
		t.Fatalf("taste = %+v", taste)
	}

	rec = serve(h, req(http.MethodPut, "/ether/api/viewers/2/taste/overrides", `{"overrides":[{"kind":"genre","value":"Horror","weight":-1}]}`))
	if rec.Code != http.StatusOK || len(eng.overrides) != 1 || eng.overrides[0].Value != "Horror" {
		t.Fatalf("overrides: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodPut, "/ether/api/viewers/2/taste/overrides", `{"overrides":[{"kind":"genre","value":"Horror","weight":5}]}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"field":"overrides[0].weight"`) {
		t.Fatalf("invalid overrides: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodPut, "/ether/api/viewers/2/taste/overrides", `{"overides":[]}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", rec.Code)
	}
	// CSRF: mutations need the header.
	r := req(http.MethodPost, "/ether/api/viewers/1/refresh", "")
	r.Header.Del("X-Ether-Request")
	if rec := serve(h, r); rec.Code != http.StatusForbidden {
		t.Fatalf("refresh without CSRF header: %d", rec.Code)
	}
	if rec := serve(h, req(http.MethodPost, "/ether/api/viewers/1/refresh", "")); rec.Code != http.StatusNoContent || len(eng.refreshed) != 1 || eng.refreshed[0] != 1 {
		t.Fatalf("refresh: %d %v", rec.Code, eng.refreshed)
	}
}

func TestViewerHomeEndpoint(t *testing.T) {
	eng := &fakeEngine{}
	h := personalizationHandler(t, eng)
	rec := serve(h, req(http.MethodGet, "/ether/api/viewers/1/home?device=mobile&at=2026-10-17T20:30:00Z&section=movies&count=20", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("home: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Rows []struct {
			HubID       string            `json:"hubId"`
			Kind        string            `json:"kind"`
			Hidden      bool              `json:"hidden"`
			Source      string            `json:"source"`
			Score       float64           `json:"score"`
			Items       []map[string]any  `json:"items"`
			ItemReasons map[string]string `json:"itemReasons"`
		} `json:"rows"`
		GeneratedAt string         `json:"generatedAt"`
		Context     map[string]any `json:"context"`
	}
	decode(t, rec, &body)
	if eng.hc.Device != "mobile" || !eng.hc.Now.Equal(time.Date(2026, 10, 17, 20, 30, 0, 0, time.UTC)) || eng.section != "movies" || eng.limit != 20 {
		t.Fatalf("engine got %+v section %q limit %d", eng.hc, eng.section, eng.limit)
	}
	if len(body.Rows) != 2 || body.Rows[0].HubID != "home.top10" || body.Rows[0].Items[0]["title"] != "The Matrix" ||
		body.Rows[0].ItemReasons["1000000000000603"] == "" || !body.Rows[1].Hidden || body.Rows[1].Items == nil {
		t.Fatalf("rows = %+v", body.Rows)
	}
	if body.Context["device"] != "mobile" || body.GeneratedAt != "2026-10-19T01:00:00Z" {
		t.Fatalf("context = %+v generatedAt %s", body.Context, body.GeneratedAt)
	}
	for _, bad := range []string{"device=fridge", "at=yesterday", "count=0"} {
		if rec := serve(h, req(http.MethodGet, "/ether/api/viewers/1/home?"+bad, "")); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
	// Defaults: TV, now.
	serve(h, req(http.MethodGet, "/ether/api/viewers/1/home", ""))
	if eng.hc.Device != core.DeviceTV || time.Since(eng.hc.Now) > time.Minute || eng.limit != 12 {
		t.Fatalf("defaults = %+v %d", eng.hc, eng.limit)
	}
	eng.fail = viewerFieldErr{"section", "must be home, movies, shows or a library section id"}
	if rec := serve(h, req(http.MethodGet, "/ether/api/viewers/1/home?section=kitchen", "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("engine validation: %d", rec.Code)
	}
}

func TestLayoutAndInsightsEndpoints(t *testing.T) {
	eng := &fakeEngine{}
	h := personalizationHandler(t, eng)
	rec := serve(h, req(http.MethodGet, "/ether/api/layout", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rules":[]`) || !strings.Contains(rec.Body.String(), `"availableRows":[{"hubId":"top-10"`) {
		t.Fatalf("layout: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodPut, "/ether/api/layout", `{"rules":[{"scope":{"userIds":[2]},"section":"home","rows":[{"hubId":"short-on-time","action":"pin","position":1,"when":{"days":[1,2,3,4],"fromHour":18}}],"maxRows":8}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("put layout: %d %s", rec.Code, rec.Body)
	}
	if len(eng.rules) != 1 || eng.rules[0].Scope.All || eng.rules[0].Scope.UserIDs[0] != 2 || *eng.rules[0].Rows[0].Position != 1 || *eng.rules[0].Rows[0].When.FromHour != 18 {
		t.Fatalf("rules = %+v", eng.rules)
	}
	if !strings.Contains(rec.Body.String(), `"scope":{"userIds":[2]}`) || !strings.Contains(rec.Body.String(), `"id":"r1"`) {
		t.Fatalf("put layout response: %s", rec.Body)
	}
	rec = serve(h, req(http.MethodPut, "/ether/api/layout", `{"rules":[{"scope":"all","rows":[{"hubId":"x","action":"explode"}]}]}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "rows[0].action") {
		t.Fatalf("invalid layout: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodPut, "/ether/api/layout", `{"rules":[{"scope":"everyone","rows":[]}]}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d", rec.Code)
	}

	rec = serve(h, req(http.MethodGet, "/ether/api/insights/rows", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"impressions7d":20`) || !strings.Contains(rec.Body.String(), `"playRate":0.25`) {
		t.Fatalf("insights rows: %d %s", rec.Code, rec.Body)
	}
	rec = serve(h, req(http.MethodGet, "/ether/api/insights/activity", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"days":[{"date":"2026-10-18","plays":3,"hours":2.5,"uniqueViewers":2}]`) {
		t.Fatalf("insights activity: %d %s", rec.Code, rec.Body)
	}
}

func TestPersonalizationNotWired(t *testing.T) {
	store := newStore(t, nil)
	h := admin.New(admin.Deps{Config: store})
	for _, p := range []string{"/ether/api/viewers", "/ether/api/viewers/1/home", "/ether/api/layout", "/ether/api/insights/rows", "/ether/api/insights/activity"} {
		if rec := serve(h, req(http.MethodGet, p, "")); rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
}
