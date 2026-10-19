package extras

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

var testNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

type fakeVideos struct{ vs []tmdb.Video }

func (f fakeVideos) MovieVideos(context.Context, int64, string) ([]tmdb.Video, error) {
	return f.vs, nil
}
func (f fakeVideos) TVVideos(context.Context, int64, string) ([]tmdb.Video, error) { return f.vs, nil }
func (f fakeVideos) SeasonVideos(context.Context, int64, int, string) ([]tmdb.Video, error) {
	return f.vs, nil
}
func (f fakeVideos) EpisodeVideos(context.Context, int64, int, int, string) ([]tmdb.Video, error) {
	return f.vs, nil
}

type fakeLocal struct{ files []LocalFile }

func (f fakeLocal) LocalFiles(context.Context) ([]LocalFile, error) { return f.files, nil }

func lookOK(string) (string, error) { return "/bin/true", nil }

func movie() *core.Title {
	return &core.Title{Key: core.MovieKey(603), Type: core.TypeMovie, Title: "The Matrix", IDs: core.IDs{TMDB: 603}}
}

func enabledCfg(cmd string) func() config.Extras {
	return func() config.Extras {
		return config.Extras{Enabled: true, ResolverCommand: cmd, LocalTrailers: true}
	}
}

func TestListOrderingFilteringCap(t *testing.T) {
	vs := []tmdb.Video{
		{Site: "YouTube", Key: "clip0000001", Name: "A Clip", Type: "Clip", Official: true, Size: 1080, ISO639: "en"},
		{Site: "YouTube", Key: "trlUnoff001", Name: "Unofficial Trailer", Type: "Trailer", Size: 720, ISO639: "en"},
		{Site: "YouTube", Key: "trlOffDE001", Name: "Deutscher Trailer", Type: "Trailer", Official: true, Size: 720, ISO639: "de"},
		{Site: "YouTube", Key: "trlOffEN001", Name: "Official Trailer", Type: "Trailer", Official: true, Size: 1080, ISO639: "en"},
		{Site: "YouTube", Key: "trlOffEN001", Name: "Official Trailer dup", Type: "Trailer", Official: true, Size: 480, ISO639: "en"},
		{Site: "YouTube", Key: "bad key!", Name: "Bad", Type: "Trailer", Official: true},
		{Site: "Dailymotion", Key: "x123", Name: "Other site", Type: "Trailer", Official: true},
		{Site: "Vimeo", Key: "123456789", Name: "Featurette", Type: "Featurette", Size: 1080, ISO639: "en"},
	}
	s := New(Options{Videos: fakeVideos{vs}, Config: enabledCfg("yt-dlp"), LookPath: lookOK, Now: func() time.Time { return testNow }})
	got, err := s.List(context.Background(), movie(), "de-DE")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("extras %d: %+v", len(got), got)
	}
	// Official trailer in the viewer language first, then official EN,
	// then the unofficial trailer, then clip, then featurette.
	want := []string{"trlOffDE001", "trlOffEN001", "trlUnoff001", "clip0000001", "123456789"}
	for i, w := range want {
		if got[i].SiteKey != w {
			t.Errorf("order[%d] = %q want %q (all %+v)", i, got[i].SiteKey, w, got)
		}
		if got[i].Key.Kind() != core.KindExtra || got[i].ParentKey != core.MovieKey(603) {
			t.Errorf("keys %+v", got[i])
		}
		if _, ok := s.Lookup(got[i].Key); !ok {
			t.Errorf("lookup %v", got[i].Key)
		}
	}
	if got[0].Subtype != "trailer" || got[0].ExtraType != 1 || got[4].Subtype != "featurette" || got[4].ExtraType != 6 {
		t.Errorf("subtypes %+v %+v", got[0], got[4])
	}
	// Deterministic keys across listings.
	again, _ := s.List(context.Background(), movie(), "de-DE")
	if again[0].Key != got[0].Key {
		t.Errorf("unstable key %v vs %v", again[0].Key, got[0].Key)
	}
}

func TestListHiddenWithoutSource(t *testing.T) {
	vs := fakeVideos{[]tmdb.Video{{Site: "YouTube", Key: "trlOffEN001", Type: "Trailer", Official: true}}}
	// Enabled but no command configured and no local lister.
	s := New(Options{Videos: vs, Config: func() config.Extras {
		return config.Extras{Enabled: true, LocalTrailers: true}
	}, LookPath: lookOK})
	if s.Enabled() {
		t.Fatal("should not be enabled without a source")
	}
	got, err := s.List(context.Background(), movie(), "en")
	if err != nil || len(got) != 0 {
		t.Fatalf("unplayable extras advertised: %v %v", got, err)
	}
	if s.DisabledReason() == "" {
		t.Fatal("expected a disabled reason")
	}
	// Disabled entirely.
	s2 := New(Options{Videos: vs, Config: func() config.Extras { return config.Extras{} }, LookPath: lookOK})
	got, _ = s2.List(context.Background(), movie(), "en")
	if len(got) != 0 || !strings.Contains(s2.DisabledReason(), "disabled") {
		t.Fatalf("disabled: %v %q", got, s2.DisabledReason())
	}
	// Command configured but not on disk.
	s3 := New(Options{Videos: vs, Config: enabledCfg("nope"), LookPath: func(string) (string, error) {
		return "", os.ErrNotExist
	}})
	got, _ = s3.List(context.Background(), movie(), "en")
	if len(got) != 0 {
		t.Fatalf("missing command still advertised: %v", got)
	}
}

func TestListingCachesStayBounded(t *testing.T) {
	s := New(Options{
		Videos: fakeVideos{[]tmdb.Video{{Site: "YouTube", Key: "trlOffEN001", Type: "Trailer", Official: true}}},
		Config: enabledCfg("yt-dlp"), LookPath: lookOK, Now: func() time.Time { return testNow },
	})
	for id := int64(1); id <= maxListEntries+1; id++ {
		title := &core.Title{Key: core.MovieKey(id), Type: core.TypeMovie, IDs: core.IDs{TMDB: id}}
		if _, err := s.List(context.Background(), title, "en"); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.lists) > maxListEntries || len(s.registry) > maxRegistryEntries {
		t.Fatalf("unbounded list/registry caches: lists=%d registry=%d", len(s.lists), len(s.registry))
	}
}

func TestLocalTrailers(t *testing.T) {
	dir := t.TempDir()
	feature := filepath.Join(dir, "The Matrix (1999).mkv")
	trailer := filepath.Join(dir, "The Matrix (1999)-trailer.mp4")
	sub := filepath.Join(dir, "Trailers", "teaser.mp4")
	for _, p := range []string{feature, trailer, sub} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []LocalFile{
		{Path: feature, Key: core.MovieKey(603), Container: "mkv"},
		{Path: trailer, Key: core.MovieKey(603), Container: "mp4", Duration: 2 * time.Minute},
		{Path: sub, Container: "mp4"},
		{Path: filepath.Join(dir, "Other-trailer.mp4"), Key: core.MovieKey(999)},
	}
	s := New(Options{Local: fakeLocal{files}, Config: enabledCfg(""), LookPath: lookOK,
		Now: func() time.Time { return testNow }})
	if !s.Enabled() {
		t.Fatal("local source should enable extras")
	}
	got, err := s.List(context.Background(), movie(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("local extras %d %+v", len(got), got)
	}
	r, err := s.Resolve(context.Background(), got[0])
	if err != nil || r.LocalPath == "" || r.Input() != r.LocalPath {
		t.Fatalf("resolve local: %v %+v", err, r)
	}
}
