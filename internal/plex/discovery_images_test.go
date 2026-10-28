package plex

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/overlays"
)

type discoveryImageSource struct{ calls int }

func (s *discoveryImageSource) Get(_ context.Context, path string, _ int) ([]byte, string, error) {
	s.calls++
	return []byte(path), "image/jpeg", nil
}

func TestPhotoTranscodeDiscoveryArtwork(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	ctx := context.Background()
	images := &discoveryImageSource{}
	ov := overlays.New(discoveryDB(t), overlays.Options{Images: images})
	if err := ov.SaveSettings(ctx, overlays.Settings{Enabled: true, Localize: true}); err != nil {
		t.Fatal(err)
	}
	f.srv.d.Discovery = &DiscoveryFeatures{Overlays: ov, Localize: func(_ context.Context, title *core.Title, lang string) (*core.Title, error) {
		localized := *title
		localized.PosterPath = "/" + lang + "-poster.jpg"
		localized.BackdropPath = "/" + lang + "-backdrop.jpg"
		localized.LogoPath = "/" + lang + "-logo.jpg"
		return &localized, nil
	}}
	pr := &Request{User: &ownerUser, Language: "fr"}
	md := f.srv.metadata(ctx, pr, &fightClub, renderOpts{})
	if !strings.Contains(md.Thumb, "/discoveryPoster/") {
		t.Fatalf("custom poster not advertised: %q", md.Thumb)
	}
	for _, kind := range []string{"poster", "backdrop", "logo"} {
		t.Run(kind, func(t *testing.T) {
			path := md.Thumb + "&kind=" + kind
			direct := f.request(t, http.MethodGet, path, nil)
			want := "/fr-" + kind + ".jpg"
			if direct.Code != http.StatusOK || direct.Body.String() != want {
				t.Fatalf("direct artwork: %d %q", direct.Code, direct.Body.String())
			}
			for _, prefix := range []string{"", "http://127.0.0.1:32400"} {
				target := "/photo/:/transcode?width=300&height=450&url=" + url.QueryEscape(prefix+path)
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					r := f.request(t, method, target, nil)
					if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/jpeg" {
						t.Fatalf("%s wrapped artwork: %d %q", method, r.Code, r.Body.String())
					}
					if method == http.MethodGet && r.Body.String() != want || method == http.MethodHead && r.Body.Len() != 0 {
						t.Fatalf("%s wrapped body: %q", method, r.Body.String())
					}
					assertPassiveArtwork(t, r)
				}
				r := f.request(t, http.MethodGet, target, map[string]string{"If-None-Match": direct.Header().Get("ETag")})
				if r.Code != http.StatusNotModified || r.Body.Len() != 0 {
					t.Fatalf("conditional artwork: %d %q", r.Code, r.Body.String())
				}
			}
		})
	}
	// The nested URL cannot supply credentials or bypass viewer restrictions.
	f.srv.d.Auth.(*fakeAuth).users["kid"] = core.User{ID: 30, MaxContentRating: "PG"}
	target := "/photo/:/transcode?url=" + url.QueryEscape(md.Thumb+"&X-Plex-Token="+ownerToken)
	for token, want := range map[string]int{"kid": http.StatusNotFound, "invalid": http.StatusUnauthorized} {
		before := images.calls
		r := f.request(t, http.MethodGet, target, map[string]string{"X-Plex-Token": token})
		if r.Code != want || images.calls != before {
			t.Fatalf("viewer %q: status=%d image fetches=%d", token, r.Code, images.calls-before)
		}
	}
	r := f.request(t, http.MethodGet, "/photo/:/transcode?url="+url.QueryEscape("https://untrusted.invalid"+md.Thumb), nil)
	if r.Code != http.StatusForbidden {
		t.Fatalf("foreign host: %d", r.Code)
	}
}
