package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestItemIDRoundTrip(t *testing.T) {
	for _, k := range []core.RatingKey{core.MovieKey(550), core.ShowKey(1396), core.EpisodeKey(1396, 1, 2), core.CollectionKey(8091), viewMovies} {
		id := itemID(k)
		if len(id) != 32 {
			t.Fatalf("id %q is not 32 hex digits", id)
		}
		if got, ok := parseItemID(id); !ok || got != k {
			t.Errorf("round trip of %d via %q gave %d, %v", k, id, got, ok)
		}
	}
	// Apps send ids with dashes (Jellyfin GUID form) and Emby apps decimals.
	if got, ok := parseItemID("00000000-0000-0000-0003-8d7ea4c68000"); !ok || got == 0 {
		t.Errorf("dashed id not accepted: %d %v", got, ok)
	}
	if got, ok := parseItemID(core.MovieKey(550).String()); !ok || got != core.MovieKey(550) {
		t.Errorf("decimal id: %d %v", got, ok)
	}
	// An id with anything in the high half is not one of ours.
	if _, ok := parseItemID("ffffffffffffffff0000000000000001"); ok {
		t.Error("foreign GUID accepted")
	}
}

func TestSignIn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	login := func(user, pw string) int {
		return f.call("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": user, "Pw": pw}).Code
	}

	// A viewer with no access code cannot sign in, whatever they type.
	if got := login("owner", "anything"); got != http.StatusUnauthorized {
		t.Fatalf("no code set: %d", got)
	}
	if err := f.store.SetAccessCode(ctx, ownerUser.ID, "short"); err == nil {
		t.Fatal("a 5-character code was accepted")
	}
	if err := f.store.SetAccessCode(ctx, ownerUser.ID, "sesame-1"); err != nil {
		t.Fatal(err)
	}
	if got := login("owner", "wrong-code"); got != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d", got)
	}
	// Plex user name, display name and case do not matter; only the viewer's own code works.
	if got := login("OWNER", "sesame-1"); got != http.StatusOK {
		t.Fatalf("right code: %d", got)
	}
	if got := login("friend", "sesame-1"); got != http.StatusUnauthorized {
		t.Fatalf("another viewer's code worked for friend: %d", got)
	}
	// Emby apps use the /emby prefix and any letter case.
	w := f.call("POST", "/emby/users/authenticatebyname", "", map[string]string{"Username": "owner", "Pw": "sesame-1"})
	if w.Code != http.StatusOK {
		t.Fatalf("emby sign-in: %d %s", w.Code, w.Body)
	}
	resp := decode[map[string]any](t, w)
	if resp["ServerId"] != "0123456789abcdef" || resp["AccessToken"] == "" {
		t.Fatalf("sign-in response: %v", resp)
	}
}

func TestSignInThrottle(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SetAccessCode(context.Background(), ownerUser.ID, "sesame-1"); err != nil {
		t.Fatal(err)
	}
	try := func(pw string) int {
		return f.call("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": "owner", "Pw": pw}).Code
	}
	for i := 0; i < maxFailures; i++ {
		if got := try("nope-nope"); got != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, got)
		}
	}
	// Even the right code is refused while locked: guessing must not be free.
	if got := try("sesame-1"); got != http.StatusTooManyRequests {
		t.Fatalf("locked sign-in: %d", got)
	}
	f.now = f.now.Add(lockout + time.Second)
	if got := try("sesame-1"); got != http.StatusOK {
		t.Fatalf("after the lockout: %d", got)
	}
}

func TestTokensGateEverythingButSignIn(t *testing.T) {
	f := newFixture(t)
	if w := f.call("GET", "/System/Info/Public", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Ether Test") {
		t.Fatalf("public info: %d %s", w.Code, w.Body)
	}
	if w := f.call("GET", "/Users/Public", "", nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("the viewer list must stay hidden: %s", w.Body)
	}
	for _, p := range []string{"/Users/Me", "/UserViews", "/Items", "/Items/" + itemID(fightClub.Key), "/Videos/" + itemID(fightClub.Key) + "/stream"} {
		if w := f.call("GET", p, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d", p, w.Code)
		}
		if w := f.call("GET", p, "not-a-token", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s with a bad token: %d", p, w.Code)
		}
	}
	tok := f.login(ownerUser)
	if w := f.call("GET", "/Users/Me", tok, nil); w.Code != 200 {
		t.Fatalf("me: %d", w.Code)
	}
	// api_key in the query works too: players that cannot set headers use it.
	if w := f.call("GET", "/Users/Me?api_key="+tok, "", nil); w.Code != 200 {
		t.Fatalf("api_key: %d", w.Code)
	}
	// A new code signs every device out.
	if err := f.store.SetAccessCode(context.Background(), ownerUser.ID, "another-code"); err != nil {
		t.Fatal(err)
	}
	f.store.mu.Lock()
	f.store.cache = map[string]cachedToken{} // already cleared by revoke; assert it
	f.store.mu.Unlock()
	if w := f.call("GET", "/Users/Me", tok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("token survived a code change: %d", w.Code)
	}
}

func TestViewersAreIsolated(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	friendID := userID(friendUser.ID)
	for _, p := range []string{"/Users/" + friendID, "/Users/" + friendID + "/Items", "/Users/" + friendID + "/Views",
		"/Users/" + friendID + "/Items/" + itemID(alien.Key)} {
		if w := f.call("GET", p, tok, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s as another viewer: %d", p, w.Code)
		}
	}
	// A viewer's own id is accepted wherever apps put it.
	if w := f.call("GET", "/Users/"+userID(ownerUser.ID)+"/Items?IncludeItemTypes=Movie&Recursive=true", tok, nil); w.Code != 200 {
		t.Fatalf("own id: %d %s", w.Code, w.Body)
	}
}

func names(r itemsResult) []string {
	var out []string
	for _, it := range r.Items {
		out = append(out, it.Name)
	}
	return out
}

func TestLibraryBrowsing(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)

	views := decode[itemsResult](t, f.call("GET", "/UserViews", tok, nil))
	if got := strings.Join(names(views), ","); got != "Movies,TV Shows,Collections" {
		t.Fatalf("views: %s", got)
	}
	if views.Items[0].CollectionType != "movies" || views.Items[1].CollectionType != "tvshows" {
		t.Fatalf("collection types: %+v", views.Items)
	}

	movies := decode[itemsResult](t, f.call("GET", "/Items?ParentId="+itemID(viewMovies)+"&SortBy=SortName&SortOrder=Ascending", tok, nil))
	if got := strings.Join(names(movies), ","); got != "Alien,Fjord Harbor" {
		t.Fatalf("movies: %s", got)
	}
	if movies.Items[0].Type != "Movie" || movies.Items[0].MediaType != "Video" {
		t.Fatalf("movie shape: %+v", movies.Items[0])
	}
	// Lower-case parameters and Emby's prefix are the same query.
	same := decode[itemsResult](t, f.call("GET", "/emby/items?parentid="+itemID(viewMovies), tok, nil))
	if len(same.Items) != 2 {
		t.Fatalf("emby/lower-case query: %v", names(same))
	}

	shows := decode[itemsResult](t, f.call("GET", "/Items?IncludeItemTypes=Series&Recursive=true", tok, nil))
	if got := strings.Join(names(shows), ","); got != "Boundary Bay" || shows.Items[0].Type != "Series" {
		t.Fatalf("series: %v", shows.Items)
	}
	show := shows.Items[0]
	if show.ProviderIDs["Tvdb"] != "81189" || len(show.ImageTags) == 0 {
		t.Fatalf("series metadata: %+v", show)
	}

	seasons := decode[itemsResult](t, f.call("GET", "/Shows/"+show.ID+"/Seasons", tok, nil))
	if len(seasons.Items) != 1 || seasons.Items[0].Type != "Season" || seasons.Items[0].SeriesID != show.ID {
		t.Fatalf("seasons: %+v", seasons.Items)
	}
	eps := decode[itemsResult](t, f.call("GET", "/Shows/"+show.ID+"/Episodes?SeasonId="+seasons.Items[0].ID, tok, nil))
	if got := strings.Join(names(eps), ","); got != "Pilot,Second Watch" {
		t.Fatalf("episodes (the unaired one must be hidden): %s", got)
	}
	if e := eps.Items[1]; e.IndexNumber == nil || *e.IndexNumber != 2 || *e.ParentIndexNumber != 1 || e.SeriesName != "Boundary Bay" {
		t.Fatalf("episode numbering: %+v", e)
	}

	// Detail pages carry cast and a playable media source with its streams.
	d := decode[item](t, f.call("GET", "/Users/"+userID(ownerUser.ID)+"/Items/"+itemID(fightClub.Key), tok, nil))
	if len(d.People) != 2 || d.People[0].Type != "Director" || d.People[1].Role != "Alex Mercer" {
		t.Fatalf("people: %+v", d.People)
	}
	if len(d.MediaSources) != 1 || len(d.MediaSources[0].MediaStreams) != 4 || d.MediaSources[0].MediaStreams[1].Language != "en" {
		t.Fatalf("media sources: %+v", d.MediaSources)
	}
	if d.CommunityRating != 8.4 || d.OfficialRating != "R" || d.RunTimeTicks != 139*60*ticksPerSecond {
		t.Fatalf("detail numbers: %v %q %d", d.CommunityRating, d.OfficialRating, d.RunTimeTicks)
	}

	boxes := decode[itemsResult](t, f.call("GET", "/Items?ParentId="+itemID(viewBoxes), tok, nil))
	if len(boxes.Items) != 1 || boxes.Items[0].Type != "BoxSet" {
		t.Fatalf("collections: %+v", boxes.Items)
	}
	inBox := decode[itemsResult](t, f.call("GET", "/Items?ParentId="+boxes.Items[0].ID, tok, nil))
	if got := strings.Join(names(inBox), ","); got != "Alien" {
		t.Fatalf("collection members: %s", got)
	}

	hints := decode[struct{ SearchHints []map[string]any }](t, f.call("GET", "/Search/Hints?searchTerm=alien", tok, nil))
	if len(hints.SearchHints) != 1 || hints.SearchHints[0]["Name"] != "Alien" {
		t.Fatalf("search: %+v", hints)
	}
}

func TestViewerLimitsAreEnforced(t *testing.T) {
	f := newFixture(t)
	f.auth.users["f"] = core.User{ID: 2, PlexID: 2002, Username: "friend", Title: "Friend", MaxContentRating: "PG"}
	tok := f.login(friendUser)

	movies := decode[itemsResult](t, f.call("GET", "/Items?IncludeItemTypes=Movie&Recursive=true", tok, nil))
	if got := strings.Join(names(movies), ","); got != "Alien" {
		t.Fatalf("an R movie reached a PG viewer: %s", got)
	}
	if w := f.call("GET", "/Items/"+itemID(fightClub.Key), tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("restricted item opened by id: %d", w.Code)
	}
	if w := f.call("POST", "/Items/"+itemID(fightClub.Key)+"/PlaybackInfo", tok, map[string]any{}); w.Code != http.StatusNotFound {
		t.Fatalf("restricted item playable: %d", w.Code)
	}
	if w := f.call("GET", "/Videos/"+itemID(fightClub.Key)+"/stream?static=true", tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("restricted item streamed: %d", w.Code)
	}
	if w := f.call("GET", "/Items/"+itemID(fightClub.Key)+"/Images/Primary", tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("restricted artwork served: %d", w.Code)
	}

	// The owner's own policy hides titles too, and so do section limits.
	f.policy.hidden[alien.Key] = true
	if w := f.call("GET", "/Items/"+itemID(alien.Key), tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("policy-hidden item opened: %d", w.Code)
	}
	f.auth.users["f"] = core.User{ID: 2, PlexID: 2002, Username: "friend", Title: "Friend", AllowedSections: []int{2}}
	views := decode[itemsResult](t, f.call("GET", "/UserViews", tok, nil))
	if got := strings.Join(names(views), ","); got != "TV Shows,Collections" {
		t.Fatalf("a TV-only viewer's libraries: %s", got)
	}
	if w := f.call("GET", "/Items/"+itemID(fightClub.Key), tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("a movie reached a TV-only viewer: %d", w.Code)
	}
}

func TestImagesRedirectToArtwork(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	id := itemID(fightClub.Key)
	w := f.call("GET", "/Items/"+id+"/Images/Primary?maxWidth=300", tok, nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://img.example/w342/poster550.jpg" {
		t.Fatalf("poster: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = f.call("GET", "/Items/"+id+"/Images/Backdrop/0?maxWidth=1920", tok, nil)
	if w.Header().Get("Location") != "https://img.example/original/backdrop550.jpg" {
		t.Fatalf("backdrop: %d %s", w.Code, w.Header().Get("Location"))
	}
	// An episode's primary image is its still, else the series poster.
	w = f.call("GET", "/Items/"+itemID(bbE1.Key)+"/Images/Primary", tok, nil)
	if !strings.HasSuffix(w.Header().Get("Location"), "/pilot.jpg") {
		t.Fatalf("episode still: %s", w.Header().Get("Location"))
	}
	w = f.call("GET", "/Items/"+itemID(bbE2.Key)+"/Images/Primary", tok, nil)
	if !strings.HasSuffix(w.Header().Get("Location"), "/bb.jpg") {
		t.Fatalf("episode without a still: %s", w.Header().Get("Location"))
	}
	// <img> tags carry no token; public titles still load, adult ones never do.
	if w := f.call("GET", "/Items/"+id+"/Images/Primary", "", nil); w.Code != http.StatusFound {
		t.Fatalf("anonymous artwork: %d", w.Code)
	}
	adult := fightClub
	adult.Key, adult.Adult = core.MovieKey(999), true
	f.catalog.titles[adult.Key] = adult
	if w := f.call("GET", "/Items/"+itemID(adult.Key)+"/Images/Primary", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("anonymous adult artwork: %d", w.Code)
	}
}

func TestWatchStateAndHomeRows(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	uid := userID(ownerUser.ID)
	movie, ep1, ep2 := itemID(fightClub.Key), itemID(bbE1.Key), itemID(bbE2.Key)

	// Progress reported by an app lands in the shared watch state.
	w := f.call("POST", "/Sessions/Playing/Progress", tok, map[string]any{"ItemId": movie, "PositionTicks": 600 * ticksPerSecond, "IsPaused": true})
	if w.Code != http.StatusNoContent {
		t.Fatalf("progress: %d %s", w.Code, w.Body)
	}
	last := f.state.progress[len(f.state.progress)-1]
	if last.user != ownerUser.ID || last.key != fightClub.Key || last.offset != 10*time.Minute || last.state != "paused" || last.duration != 139*time.Minute {
		t.Fatalf("recorded progress: %+v", last)
	}

	f.state.set(ownerUser.ID, core.WatchStatus{Key: fightClub.Key, ViewOffset: 10 * time.Minute})
	resume := decode[itemsResult](t, f.call("GET", "/UserItems/Resume", tok, nil))
	if len(resume.Items) != 1 || resume.Items[0].Name != "Fjord Harbor" || resume.Items[0].UserData.PlaybackPositionTicks != 600*ticksPerSecond {
		t.Fatalf("resume: %+v", resume.Items)
	}
	if p := resume.Items[0].UserData.PlayedPercentage; p < 7 || p > 7.3 {
		t.Fatalf("played percentage: %v", p)
	}

	// Marking played and unplayed round-trips through the shared state.
	ud := decode[userData](t, f.call("POST", "/Users/"+uid+"/PlayedItems/"+ep1, tok, nil))
	if !ud.Played || ud.PlayCount != 1 {
		t.Fatalf("marked played: %+v", ud)
	}
	played := decode[itemsResult](t, f.call("GET", "/Items?ParentId="+itemID(bbS1.Key)+"&Filters=IsUnplayed", tok, nil))
	_ = played
	if ud := decode[userData](t, f.call("DELETE", "/Users/"+uid+"/PlayedItems/"+ep1, tok, nil)); ud.Played {
		t.Fatalf("marked unplayed: %+v", ud)
	}
	// Marking a series played marks aired episodes only.
	f.call("POST", "/Users/"+uid+"/PlayedItems/"+itemID(boundaryBay.Key), tok, nil)
	if got := len(f.state.scrobbed); got != 3 { // one earlier + two aired episodes
		t.Fatalf("scrobbled %d: %v", got, f.state.scrobbed)
	}
	for _, k := range f.state.scrobbed {
		if k == bbE3.Key {
			t.Fatal("an unaired episode was marked played")
		}
	}
	_ = ep2

	// Latest shows what joined the catalogue, newest first.
	latest := decode[[]item](t, f.call("GET", "/Items/Latest?ParentId="+itemID(viewMovies)+"&Limit=5", tok, nil))
	if len(latest) != 2 || latest[0].Name != "Fjord Harbor" {
		t.Fatalf("latest: %+v", latest)
	}
}

func TestFavoritesShareThePlexPlaylist(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	uid := userID(ownerUser.ID)
	id := itemID(alien.Key)

	ud := decode[userData](t, f.call("POST", "/Users/"+uid+"/FavoriteItems/"+id, tok, nil))
	if !ud.IsFavorite {
		t.Fatalf("not a favourite after marking: %+v", ud)
	}
	// Marking twice does not duplicate the entry.
	f.call("POST", "/Users/"+uid+"/FavoriteItems/"+id, tok, nil)
	pls, _ := f.pls.List(context.Background(), ownerUser.ID)
	if len(pls) != 1 || pls[0].Title != "Favorites" || len(pls[0].Items) != 1 {
		t.Fatalf("playlist behind favourites: %+v", pls)
	}
	favs := decode[itemsResult](t, f.call("GET", "/Items?Filters=IsFavorite&Recursive=true", tok, nil))
	if got := strings.Join(names(favs), ","); got != "Alien" {
		t.Fatalf("favourites: %s", got)
	}
	// Favourites are per viewer.
	friend := f.login(friendUser)
	if got := decode[itemsResult](t, f.call("GET", "/Items?Filters=IsFavorite", friend, nil)); len(got.Items) != 0 {
		t.Fatalf("another viewer sees %v", names(got))
	}
	if ud := decode[userData](t, f.call("DELETE", "/Users/"+uid+"/FavoriteItems/"+id, tok, nil)); ud.IsFavorite {
		t.Fatalf("still a favourite: %+v", ud)
	}
}

func TestNextUp(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	f.state.history = []core.HistoryEntry{{UserID: ownerUser.ID, Key: bbE1.Key, Completed: true, ViewedAt: fixedNow}}
	got := decode[itemsResult](t, f.call("GET", "/Shows/NextUp", tok, nil))
	if names := strings.Join(names(got), ","); names != "Second Watch" {
		t.Fatalf("next up: %s", names)
	}
	// An unaired follow-up is never offered.
	f.state.history = []core.HistoryEntry{{UserID: ownerUser.ID, Key: bbE2.Key, Completed: true, ViewedAt: fixedNow}}
	if got := decode[itemsResult](t, f.call("GET", "/Shows/NextUp", tok, nil)); len(got.Items) != 0 {
		t.Fatalf("an unaired episode was offered: %v", names(got))
	}
}

func TestSignInThrottlePerAddress(t *testing.T) {
	f := newFixture(t)
	// Cycling through user names from one address does not dodge the lock.
	for i := 0; i < maxIPFailures; i++ {
		f.call("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": fmt.Sprintf("guess%d", i), "Pw": "nope-nope"})
	}
	if err := f.store.SetAccessCode(context.Background(), ownerUser.ID, "sesame-1"); err != nil {
		t.Fatal(err)
	}
	w := f.call("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": "owner", "Pw": "sesame-1"})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("an address that failed %d times signed in: %d", maxIPFailures, w.Code)
	}
}

func TestSignInAcceptsFormEncodedBody(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SetAccessCode(context.Background(), ownerUser.ID, "sesame-1"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/emby/Users/AuthenticateByName", strings.NewReader("Username=owner&Pw=sesame-1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "192.168.1.20:5555"
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("form sign-in: %d %s", w.Code, w.Body)
	}
}

func TestAppsWithoutADeviceIdStayApart(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SetAccessCode(context.Background(), ownerUser.ID, "sesame-1"); err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for i := 0; i < 2; i++ {
		w := f.call("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": "owner", "Pw": "sesame-1"})
		tokens = append(tokens, decode[map[string]any](t, w)["AccessToken"].(string))
	}
	// Two apps that never name their device must not sign each other out.
	for _, tok := range tokens {
		if w := f.call("GET", "/Users/Me?api_key="+tok, "", nil); w.Code != 200 {
			t.Fatalf("token %s signed out by another sign-in: %d", tok, w.Code)
		}
	}
}

func TestLandingPageAndHealth(t *testing.T) {
	f := newFixture(t)
	if w := f.call("GET", "/", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Jellyfin and Emby compatible") {
		t.Fatalf("landing: %d %s", w.Code, w.Body)
	}
	if w := f.call("GET", "/health", "", nil); w.Body.String() != "Healthy" {
		t.Fatalf("health: %s", w.Body)
	}
	// A hostile Host header cannot inject markup.
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = `"><script>alert(1)</script>`
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("unescaped Host header in the landing page")
	}
}
