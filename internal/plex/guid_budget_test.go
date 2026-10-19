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
