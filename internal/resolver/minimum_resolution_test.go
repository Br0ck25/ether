package resolver

import (
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestMinimumResolutionIsAProfileGate(t *testing.T) {
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{})
	defer s.Close()
	profile := s.ProfileFor(appleTVCaps(), nil)
	profile.MinHeight = 2160
	for _, tc := range []struct {
		name      string
		height    int
		preferred bool
		want      string
	}{
		{"below minimum", 1080, false, "below profile min height"},
		{"at minimum", 2160, false, ""},
		{"unreported height", 0, false, ""},
		{"explicit version", 1080, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := withParsed(rel("candidate", "The.Signal.1999.2160p.WEB-DL.HEVC-GRP", 8000))
			r.Info.Height = tc.height
			got := s.rejectReason(r, matrixTitle(), searchRequestFor(matrixTitle()), profile, s.resolverCfg(), appleTVCaps(), nil, tc.preferred)
			if got != tc.want {
				t.Fatalf("reason=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestMinimumResolutionDoesNotHideTitleForOtherProfiles(t *testing.T) {
	cfg := testCfg()
	for i := range cfg.Quality.Profiles {
		if cfg.Quality.Profiles[i].Name == "4K" {
			cfg.Quality.Profiles[i].MinHeight = 2160
		}
	}
	s := New(&fakeIndexer{releases: []core.Release{rel("hd", "The.Signal.1999.1080p.WEB-DL.H264-GRP", 8000)}}, fakeHealth{}, nil, cfg, Options{})
	defer s.Close()
	if _, err := s.Resolve(t.Context(), core.ResolveRequest{Title: matrixTitle(), Client: appleTVCaps()}); !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("4K-only resolve: %v", err)
	}
	if known, _ := s.Available(t.Context(), matrixTitle().Key); known {
		t.Fatal("profile-specific floor marked the title unavailable for everyone")
	}
	if result, err := s.Resolve(t.Context(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()}); err != nil || result.Release.ID != "hd" {
		t.Fatalf("1080p profile result=%v err=%v", result, err)
	}
}
