package plex

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// artCollections adds generated posters to the fake collections.
type artCollections struct {
	*fakeCollections
	calls []string
}

func (a *artCollections) CollectionArtwork(_ context.Context, u *core.User, key core.RatingKey, element string, width int) ([]byte, string, bool, error) {
	a.calls = append(a.calls, element+":"+strconv.Itoa(width))
	if element != "thumb" || key.Kind() != core.KindSmart {
		return nil, "", false, nil
	}
	return []byte("GENERATED-" + u.Username), "image/jpeg", true, nil
}

func TestGeneratedCollectionArtwork(t *testing.T) {
	updated := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	f := newFixture()
	f.srv.Close()
	owned := core.Collection{Key: core.SmartKey(12), Title: "Owner Picks", Kind: "smart", Type: core.TypeMovie,
		ChildCount: 1, BackdropPath: "/ownerbd.jpg", UpdatedAt: updated}
	f.cols.cols[owned.Key] = owned
	f.cols.items[owned.Key] = []core.Title{alien}
	art := &artCollections{fakeCollections: f.cols}
	d := f.srv.d
	d.Collections = art
	f.srv = NewServer(d)
	defer f.srv.Close()

	// The collection's thumb URL carries its update stamp.
	rec := f.get(t, "/library/sections/1/collections", jsonAccept...)
	stamp := strconv.FormatInt(updated.Unix(), 10)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/library/metadata/"+owned.Key.String()+"/thumb/"+stamp) {
		t.Fatalf("collections listing: %d %s", rec.Code, rec.Body)
	}
	// Plex fetches the generated poster.
	rec = f.get(t, "/library/metadata/"+owned.Key.String()+"/thumb/"+stamp)
	if rec.Code != http.StatusOK || rec.Body.String() != "GENERATED-owner" || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb: %d %q", rec.Code, rec.Body)
	}
	// Through the photo transcoder too (how Plex Web asks), with the width.
	rec = f.get(t, "/photo/:/transcode?width=400&height=600&url="+urlEsc("/library/collections/"+owned.Key.String()+"/thumb/"+stamp))
	if rec.Code != http.StatusOK || rec.Body.String() != "GENERATED-owner" {
		t.Fatalf("photo transcode: %d %q", rec.Code, rec.Body)
	}
	if last := art.calls[len(art.calls)-1]; last != "thumb:400" {
		t.Fatalf("width not passed: %v", art.calls)
	}
	// Franchise collections keep their TMDB poster (the hook declines).
	calls := len(art.calls)
	f.get(t, "/library/collections/5000000000008091/thumb/1")
	if len(art.calls) != calls {
		t.Fatal("franchise art should not ask the collection engine")
	}
}
