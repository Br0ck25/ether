package plex

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plexguid"
	"github.com/braedonsaunders/ether/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type detailWarmer struct {
	core.Playback
	calls int
}

func (w *detailWarmer) PreResolve(context.Context, *core.User, core.RatingKey, core.ClientCaps) bool {
	w.calls++
	return true
}

func TestBulkMetadataUsesCachedGUIDsWithoutNetwork(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"MediaContainer":{"Metadata":[{"guid":"plex://movie/5d7768265af944001f1f6976","type":"movie","Guid":[{"id":"tmdb://550"}]}]}}`)
	}))
	defer upstream.Close()
	f := newFixture()
	defer f.srv.Close()
	warmer := &detailWarmer{Playback: f.srv.d.Playback}
	f.srv.d.Playback = warmer
	f.srv.d.PlexGUID = plexguid.New(db, plexguid.Options{BaseURL: upstream.URL, Token: func() string { return "fixture" }})
	rec := f.get(t, "/library/metadata/"+fightClub.Key.String()+","+alien.Key.String())
	if rec.Code != 200 || (calls.Load() != 0 || warmer.calls != 0) {
		t.Fatalf("bulk status=%d network=%d", rec.Code, calls.Load())
	}
	rec = f.get(t, "/library/metadata/"+fightClub.Key.String())
	if rec.Code != 200 || (calls.Load() != 1 || warmer.calls != 1) {
		t.Fatalf("detail status=%d network=%d", rec.Code, calls.Load())
	}
}

func TestHubsWarmPlexGUIDsInBackgroundAndEpisodesRenderHierarchy(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const (
		fcGUID     = "plex://movie/5d7768265af944001f1f6976"
		showGUID   = "plex://show/5d9c086c46115600200aa2fe"
		seasonGUID = "plex://season/602e5e2194f08e002d001122"
		ep1GUID    = "plex://episode/602e5e2294f08e002d003344"
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/library/metadata/matches":
			q := r.URL.Query()
			switch {
			case q.Get("type") == "1" && q.Get("guid") == "tmdb://550":
				io.WriteString(w, `{"MediaContainer":{"Metadata":[{"guid":"`+fcGUID+`","type":"movie","Guid":[{"id":"tmdb://550"}]}]}}`)
				return
			case q.Get("type") == "4" && q.Get("guid") == "tmdb://1396" && q.Get("parentIndex") == "1" && q.Get("index") == "1":
				io.WriteString(w, `{"MediaContainer":{"Metadata":[{"guid":"`+ep1GUID+`","type":"episode","index":1,"parentIndex":1,"parentGuid":"`+seasonGUID+`","grandparentGuid":"`+showGUID+`","Guid":[{"id":"tmdb://1396/1/1"}]}]}}`)
				return
			}
			io.WriteString(w, `{"MediaContainer":{"Metadata":[]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	f := newFixture()
	defer f.srv.Close()
	pg := plexguid.New(db, plexguid.Options{
		BaseURL:    upstream.URL,
		Token:      func() string { return "fixture" },
		Background: true,
	})
	defer pg.Close()
	f.srv.d.PlexGUID = pg

	// First /hubs render schedules background resolution for hub items.
	rec := f.get(t, "/hubs")
	if rec.Code != 200 {
		t.Fatalf("first /hubs status=%d", rec.Code)
	}
	f.srv.waitBackground()

	// Second /hubs render serves the resolved plex://movie/... GUID on the hub tile.
	rec = f.get(t, "/hubs")
	if rec.Code != 200 || !containsSubstr(rec.Body.String(), fcGUID) {
		t.Fatalf("expected %s in /hubs after background warm, got: %s", fcGUID, rec.Body.String())
	}

	// Single episode detail open resolves episode + parent season + grandparent show GUIDs.
	rec = f.get(t, "/library/metadata/"+bbE1.Key.String())
	body := rec.Body.String()
	if rec.Code != 200 || !containsSubstr(body, ep1GUID) || !containsSubstr(body, seasonGUID) || !containsSubstr(body, showGUID) {
		t.Fatalf("expected episode/season/show plex:// GUIDs in episode detail, got: %s", body)
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
