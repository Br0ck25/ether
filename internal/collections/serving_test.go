package collections_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/artwork"
	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// fakeState knows which titles each user has watched or started.
type fakeState struct {
	core.State
	mu      sync.Mutex
	watched map[int64]map[core.RatingKey]bool
	started map[int64]map[core.RatingKey]bool
}

func (f *fakeState) GetMany(ctx context.Context, userID int64, keys []core.RatingKey) (map[core.RatingKey]core.WatchStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[core.RatingKey]core.WatchStatus{}
	for _, k := range keys {
		if f.watched[userID][k] {
			out[k] = core.WatchStatus{Key: k, ViewCount: 1}
		} else if f.started[userID][k] {
			out[k] = core.WatchStatus{Key: k, ViewOffset: time.Minute}
		}
	}
	return out, nil
}

type fakeAuth struct{ core.Auth }

func (fakeAuth) Owner(context.Context) (*core.User, error) {
	return &core.User{ID: 1, Title: "Owner", IsOwner: true}, nil
}

// fakeScorer prefers lower TMDB ids.
type fakeScorer struct{ calls int }

func (f *fakeScorer) ScoreTitles(ctx context.Context, user *core.User, titles []core.Title) (map[core.RatingKey]float64, error) {
	f.calls++
	out := map[core.RatingKey]float64{}
	for _, t := range titles {
		out[t.Key] = -float64(t.Key.TMDB())
	}
	return out, nil
}

var (
	owner = &core.User{ID: 1, Username: "owner", IsOwner: true}
	kid   = &core.User{ID: 2, Username: "kid", MaxContentRating: "PG"}
	guest = &core.User{ID: 3, Username: "guest"}
)

func listed(t *testing.T, e *env, user *core.User, typ core.MediaType, name string) *core.Collection {
	t.Helper()
	cs, err := e.svc.List(e.ctx, user, typ)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cs {
		if cs[i].Title == name {
			return &cs[i]
		}
	}
	return nil
}

func TestPublishingToPlexRespectsTargetsAndRatings(t *testing.T) {
	e := newEnv(t)
	sp := spec("Big Hits", "movie", catalogSource(), rl("votes", "gte", 9000))
	sp.Targets.Users = core.UserTargets{IDs: []int64{1, 2}}
	created, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	c := listed(t, e, owner, core.TypeMovie, "Big Hits")
	if c == nil || c.Key.String() != created.RatingKey || c.ChildCount != created.ItemCount || c.ChildCount < 5 {
		t.Fatalf("owner list: %+v (spec %+v)", c, created)
	}
	if c.PosterPath == "" || c.BackdropPath == "" || c.UpdatedAt.IsZero() {
		t.Fatalf("collection art/stamp missing: %+v", c)
	}
	if listed(t, e, owner, core.TypeShow, "Big Hits") != nil {
		t.Fatal("movie collection in the TV section")
	}
	if listed(t, e, guest, core.TypeMovie, "Big Hits") != nil {
		t.Fatal("untargeted user sees the collection")
	}
	if _, err := e.svc.Get(e.ctx, guest, c.Key); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("guest Get: %v", err)
	}
	// The kid only sees G/PG titles.
	kc := listed(t, e, kid, core.TypeMovie, "Big Hits")
	if kc == nil || kc.ChildCount >= c.ChildCount {
		t.Fatalf("kid list: %+v vs %+v", kc, c)
	}
	page, err := e.svc.Items(e.ctx, kid, c.Key, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range page.Items {
		if it.ContentRating != "G" && it.ContentRating != "PG" {
			t.Fatalf("kid sees %s (%s)", it.Title, it.ContentRating)
		}
	}
	// Paging.
	p1, _ := e.svc.Items(e.ctx, owner, c.Key, 0, 2)
	p2, _ := e.svc.Items(e.ctx, owner, c.Key, 2, 2)
	if len(p1.Items) != 2 || len(p2.Items) != 2 || p1.Items[0].Key == p2.Items[0].Key || p1.Total != c.ChildCount {
		t.Fatalf("paging: %+v %+v", p1, p2)
	}
	// Hydrated titles for Plex.
	if len(p1.Items[0].Genres) == 0 || p1.Items[0].Summary == "" && p1.Items[0].Title == "" {
		t.Fatalf("items not hydrated: %+v", p1.Items[0])
	}
	// Disabled collections disappear from Plex.
	created.Enabled = false
	if _, err := e.svc.UpdateSpec(e.ctx, created.ID, *created); err != nil {
		t.Fatal(err)
	}
	if listed(t, e, owner, core.TypeMovie, "Big Hits") != nil {
		t.Fatal("disabled collection still listed")
	}
	// Built-ins and franchises keep working.
	if listed(t, e, owner, core.TypeMovie, "Top Rated Movies") == nil {
		t.Fatal("built-in missing")
	}
}

func TestPersonalRulesPersonalSortAndRotation(t *testing.T) {
	st := &fakeState{watched: map[int64]map[core.RatingKey]bool{1: {core.MovieKey(102): true}},
		started: map[int64]map[core.RatingKey]bool{1: {core.MovieKey(105): true}}}
	scorer := &fakeScorer{}
	e := newEnv(t, withOpts(collections.WithState(st), collections.WithAuth(fakeAuth{}), collections.WithScorer(scorer)))
	sp := spec("Unwatched classics", "movie", catalogSource(), rl("rating", "gte", 8.4), rl("watched", "is", false))
	sp.Exclude = []core.CollectionRule{rl("inProgress", "is", true)}
	sp.Sort = "personal"
	created, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	key := core.SmartKey(mustInt(t, created.ID))
	ownerItems, err := e.svc.Items(e.ctx, owner, key, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	guestItems, _ := e.svc.Items(e.ctx, guest, key, 0, 50)
	has := func(p core.Page, id int64) bool {
		for _, it := range p.Items {
			if it.Key == core.MovieKey(id) {
				return true
			}
		}
		return false
	}
	if has(ownerItems, 102) || has(ownerItems, 105) {
		t.Fatalf("owner sees watched/in-progress titles: %+v", ownerItems.Items)
	}
	if !has(guestItems, 102) || !has(guestItems, 105) {
		t.Fatalf("guest should see everything: %d items", len(guestItems.Items))
	}
	// Personal order: the fake scorer prefers low ids.
	for i := 1; i < len(guestItems.Items); i++ {
		if guestItems.Items[i-1].Key.TMDB() > guestItems.Items[i].Key.TMDB() {
			t.Fatalf("not in personal order: %v", guestItems.Items)
		}
	}
	if scorer.calls == 0 {
		t.Fatal("scorer never called")
	}
	// Previews apply personal rules as the owner and say so.
	p, err := e.svc.Preview(e.ctx, sp, 60)
	if err != nil {
		t.Fatal(err)
	}
	if contains(titlesOf(p), "The Family Business") || !strings.Contains(strings.Join(p.Warnings, " "), "Owner") {
		t.Fatalf("preview personal: %v %v", titlesOf(p), p.Warnings)
	}

	// Rotation shows a stable subset per day.
	rot := spec("Rotating", "movie", catalogSource(), rl("votes", "gte", 1000))
	rot.Rotate = &core.CollectionRotate{Size: 3, Every: "daily"}
	rc, err := e.svc.CreateSpec(e.ctx, rot)
	if err != nil {
		t.Fatal(err)
	}
	rkey := core.SmartKey(mustInt(t, rc.ID))
	a, _ := e.svc.Items(e.ctx, guest, rkey, 0, 50)
	b, _ := e.svc.Items(e.ctx, guest, rkey, 0, 50)
	if a.Total != 3 || len(a.Items) != 3 || a.Items[0].Key != b.Items[0].Key || a.Items[2].Key != b.Items[2].Key {
		t.Fatalf("rotation: %+v vs %+v", a.Items, b.Items)
	}
}

func TestHomeRowsForTargetsAndSchedule(t *testing.T) {
	e := newEnv(t)
	evening := spec("Evening Thrills", "movie", catalogSource(), rl("genre", "is", "Thriller"))
	evening.Targets.HomeRow = true
	from, to := 18, 23
	evening.Targets.Schedule = &core.CollectionSchedule{Days: []int{5, 6}, FromHour: &from, ToHour: &to, Devices: []string{"tv"}}
	if _, err := e.svc.CreateSpec(e.ctx, evening); err != nil {
		t.Fatal(err)
	}
	section := spec("Comedy Shelf", "show", catalogSource(), rl("genre", "is", "Comedy"))
	section.Targets.SectionRow = true
	section.Targets.Users = core.UserTargets{IDs: []int64{1}}
	if _, err := e.svc.CreateSpec(e.ctx, section); err != nil {
		t.Fatal(err)
	}
	friday9pm := time.Date(2026, 10, 16, 21, 0, 0, 0, time.Local)
	rows := func(u *core.User, device string, at time.Time) map[string]core.CollectionRow {
		hc := core.HubContext{Device: device, Now: at}
		got, err := e.svc.HomeRowsFor(e.ctx, u, hc)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]core.CollectionRow{}
		for _, r := range got {
			out[r.Hub.Title] = r
		}
		return out
	}
	r := rows(owner, "tv", friday9pm)
	ev, ok := r["Evening Thrills"]
	if !ok || !ev.HomeRow || ev.SectionRow || !ev.Hub.Promoted || len(ev.Hub.Items) == 0 || ev.Hub.Key.Kind() != core.KindSmart {
		t.Fatalf("evening row: %+v", ev)
	}
	if ev.Hub.ID != "collection."+ev.CollectionID || ev.Hub.Type != core.TypeMovie {
		t.Fatalf("hub identity: %+v", ev.Hub)
	}
	cs, ok := r["Comedy Shelf"]
	if !ok || cs.HomeRow || !cs.SectionRow || cs.Hub.Type != core.TypeShow {
		t.Fatalf("section row: %+v", cs)
	}
	if _, ok := rows(owner, "mobile", friday9pm)["Evening Thrills"]; ok {
		t.Fatal("device filter ignored")
	}
	if _, ok := rows(owner, "tv", friday9pm.Add(-5*time.Hour))["Evening Thrills"]; ok {
		t.Fatal("hour filter ignored")
	}
	if _, ok := rows(owner, "tv", friday9pm.AddDate(0, 0, 3))["Evening Thrills"]; ok {
		t.Fatal("day filter ignored")
	}
	if _, ok := rows(guest, "tv", friday9pm)["Comedy Shelf"]; ok {
		t.Fatal("user targeting ignored")
	}
}

func TestSchedulerRebuildsWhenDue(t *testing.T) {
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	e := newEnv(t, withOpts(collections.WithNow(clock)))
	hourly := spec("Hourly", "movie", catalogSource(), rl("votes", "gte", 1000))
	hourly.Refresh = "hourly"
	h, err := e.svc.CreateSpec(e.ctx, hourly)
	if err != nil {
		t.Fatal(err)
	}
	weekly := spec("Weekly", "movie", catalogSource())
	weekly.Refresh = "weekly"
	if _, err := e.svc.CreateSpec(e.ctx, weekly); err != nil {
		t.Fatal(err)
	}
	live := spec("Live", "movie", catalogSource())
	live.Refresh = "live"
	if _, err := e.svc.CreateSpec(e.ctx, live); err != nil {
		t.Fatal(err)
	}
	// Built-ins have never been built; everything else was built on save.
	first := e.svc.RunDue(e.ctx)
	if first == 0 {
		t.Fatal("never-built built-ins should be due")
	}
	if n := e.svc.RunDue(e.ctx); n != 0 {
		t.Fatalf("nothing should be due right after a run, built %d", n)
	}
	hkey := core.SmartKey(mustInt(t, h.ID))
	advance(61 * time.Minute)
	n := e.svc.RunDue(e.ctx) // hourly owner + hourly built-ins
	got, _ := e.svc.GetSpec(e.ctx, h.ID)
	if n == 0 || !got.LastBuiltAt.Equal(now.Truncate(time.Second)) {
		t.Fatalf("hourly not rebuilt: n=%d %+v", n, got.LastBuiltAt)
	}
	// The built-ins joined a popular title, so this rebuild changed the
	// members; the next one does not, and Plex keeps its cached artwork.
	before, err := e.svc.Get(e.ctx, owner, hkey)
	if err != nil || !before.UpdatedAt.Equal(now.Truncate(time.Second)) {
		t.Fatalf("stamp after a changed rebuild: %+v %v", before, err)
	}
	advance(61 * time.Minute)
	if n := e.svc.RunDue(e.ctx); n == 0 {
		t.Fatal("hourly not rebuilt again")
	}
	after, _ := e.svc.Get(e.ctx, owner, hkey)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("stamp moved on an unchanged rebuild: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestDefaultsAreSeededOnce(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := newEnv(t)
	svc, err := collections.New(db, e.cat, nil)
	if err != nil {
		t.Fatal(err)
	}
	specs, _ := svc.ListSpecs(ctx)
	owned := 0
	for _, s := range specs {
		if !s.BuiltIn {
			owned++
			if err := svc.DeleteSpec(ctx, s.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if owned == 0 {
		t.Fatal("no starter collections")
	}
	svc2, err := collections.New(db, e.cat, nil)
	if err != nil {
		t.Fatal(err)
	}
	specs, _ = svc2.ListSpecs(ctx)
	for _, s := range specs {
		if !s.BuiltIn {
			t.Fatalf("defaults re-seeded after the owner deleted them: %s", s.Name)
		}
	}
}

func TestGeneratedArtworkForStudioAndPlex(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 60, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 60; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), 80, uint8(y * 2), 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	var fetched int
	var mu sync.Mutex
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched++
		mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(buf.Bytes())
	}))
	defer imgSrv.Close()
	images := artwork.NewImages(artwork.ImagesOptions{ImageURL: func(path, size string) string { return imgSrv.URL + "/" + size + path }})
	renderer := artwork.NewRenderer(images, t.TempDir(), nil)
	e := newEnv(t, withOpts(collections.WithArtwork(renderer, images)))
	created, err := e.svc.CreateSpec(e.ctx, spec("Arty", "movie", catalogSource(), rl("votes", "gte", 9000)))
	if err != nil {
		t.Fatal(err)
	}
	data, ct, mod, err := e.svc.Artwork(e.ctx, created.ID, 300)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || ct != "image/jpeg" || cfg.Width != 300 || cfg.Height != 450 || mod.IsZero() {
		t.Fatalf("artwork: %s %+v %v %v", ct, cfg, err, mod)
	}
	key := core.SmartKey(mustInt(t, created.ID))
	pdata, pct, ok, err := e.svc.CollectionArtwork(e.ctx, owner, key, "thumb", 300)
	if err != nil || !ok || pct != "image/jpeg" || !bytes.Equal(pdata, data) {
		t.Fatalf("plex thumb: ok=%v err=%v", ok, err)
	}
	if _, _, ok, _ := e.svc.CollectionArtwork(e.ctx, owner, key, "art", 300); ok {
		t.Fatal("art falls back to the member backdrop")
	}
	if _, _, ok, _ := e.svc.CollectionArtwork(e.ctx, owner, core.CollectionKey(10), "thumb", 300); ok {
		t.Fatal("franchises keep TMDB art")
	}
	// Custom artwork is fetched and served as is.
	created.Artwork = core.CollectionArtwork{Mode: "custom", URL: "/custom.jpg"}
	if _, err := e.svc.UpdateSpec(e.ctx, created.ID, *created); err != nil {
		t.Fatal(err)
	}
	data2, _, _, err := e.svc.Artwork(e.ctx, created.ID, 300)
	if err != nil || !bytes.Equal(data2, buf.Bytes()) {
		t.Fatalf("custom art: %v", err)
	}
}

func TestLayoutRowAddsUnpromotedCollectionAndPreservesRestrictions(t *testing.T) {
	e := newEnv(t)
	sp := spec("Designer only", "movie", catalogSource())
	sp.Targets.HomeRow = false
	sp.Targets.Users = core.UserTargets{IDs: []int64{1}}
	saved, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	hc := core.HubContext{Device: core.DeviceTV, Now: time.Now()}
	before, err := e.svc.HomeRowsFor(e.ctx, guest, hc)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range before {
		if row.CollectionID == saved.ID {
			t.Fatal("unpromoted collection appeared without an explicit layout selection")
		}
	}
	limited := *guest
	limited.MaxContentRating = "G"
	row, err := e.svc.LayoutRow(e.ctx, &limited, hc, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || !row.HomeRow || !row.SectionRow {
		t.Fatalf("selected row = %+v", row)
	}
	for _, title := range row.Hub.Items {
		if title.ContentRating != "G" && title.ContentRating != "" {
			t.Fatalf("content restriction escaped: %+v", title)
		}
	}
	private, err := e.svc.CreateSmart(e.ctx, core.Collection{Title: "Private", OwnerUserID: owner.ID, Filter: &core.Filter{Type: core.TypeMovie}})
	if err != nil {
		t.Fatal(err)
	}
	privateID := ""
	specs, err := e.svc.ListSpecs(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range specs {
		if s.Name == private.Title {
			privateID = s.ID
		}
	}
	if privateID == "" {
		t.Fatal("private collection missing")
	}
	if _, err := e.svc.LayoutRow(e.ctx, guest, hc, privateID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("private collection leaked: %v", err)
	}

	disabled := *saved
	disabled.Enabled = false
	if _, err := e.svc.UpdateSpec(e.ctx, saved.ID, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LayoutRow(e.ctx, guest, hc, saved.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("disabled collection selection = %v", err)
	}
}
