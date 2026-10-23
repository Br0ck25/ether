package availability

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/resolver"
)

type suitabilityValidator struct {
	*fakeValidator
	r *resolver.Service
}

func (v suitabilityValidator) DiscoveryCandidate(t *core.Title, r core.Release) string {
	return v.r.DiscoveryCandidate(t, r)
}

func TestDiscoverySuppressesOnlyKnownUnsuitableRecentMovies(t *testing.T) {
	x := setup(t)
	r := resolver.New(nil, nil, nil, x.c, resolver.Options{})
	defer r.Close()
	x.s.d.Validator = suitabilityValidator{x.v, r}
	title := x.cat.titles[core.MovieKey(1)]
	title.Language = "en"
	put := func(id, name string) {
		t.Helper()
		if err := x.s.remember(t.Context(), x.f.sources[0], title, core.Release{ID: id, Name: name}, x.s.policy()); err != nil {
			t.Fatal(err)
		}
	}
	unknown := title
	unknown.Key = core.MovieKey(2)
	show := title
	show.Type = core.TypeShow
	show.Key = core.ShowKey(3)
	items := []core.Title{title, unknown, show}
	if got := x.s.FilterDiscovery(t.Context(), items); len(got) != 3 {
		t.Fatal("missing evidence hid titles")
	}
	put("dub", "Film.2026.1080p.WEB-DL.[Ukr.Dub]")
	put("cam", "Film.2026.1080p.CAM")
	put("pre", "Film.2026.1080p.HQ.Pre.Multi")
	got := x.s.FilterDiscovery(t.Context(), items)
	if len(got) != 2 || got[0].Key != unknown.Key || len(items) != 3 {
		t.Fatal("known unsuitable sources were promoted or input mutated", got)
	}
	// A catalogue title from years ago must not be hidden by a narrow feed sample.
	old := title
	old.Released = x.now.AddDate(-5, 0, 0)
	if got := x.s.FilterDiscovery(t.Context(), []core.Title{old}); len(got) != 1 {
		t.Fatal("old title hidden")
	}
	// A changed/disabled source cannot supply current negative evidence.
	x.f.sources[0].Generation = "two"
	if got := x.s.FilterDiscovery(t.Context(), items); len(got) != 3 {
		t.Fatal("stale source generation hid title")
	}
	x.f.sources[0].Generation = "one"
	// A later suitable release restores discovery immediately, without a search.
	put("good", "Film.2026.1080p.WEB-DL.ENG")
	if got := x.s.FilterDiscovery(t.Context(), items); len(got) != 3 {
		t.Fatal("new good release did not restore title")
	}
	x.now = x.now.Add(31 * 24 * time.Hour)
	if got := x.s.FilterDiscovery(t.Context(), items); len(got) != 3 {
		t.Fatal("stale evidence hid title")
	}
}
