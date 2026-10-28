package personalize

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestDiscoveryHeadAcrossPagesAndRenderedMetadata(t *testing.T) {
	for _, section := range []string{"home", "movies", "shows"} {
		t.Run(section, func(t *testing.T) {
			rows := []core.HomeRow{row("theme", KindEra, 24), row("cw", KindContinue, 0), row("trend", KindTrending, 24), row("crowd", KindCrowd, 24), row("new", KindNewReleases, 24), row("top", KindTop10, 10)}
			for _, rendered := range []bool{false, true} {
				for _, populated := range []bool{false, true} {
					input := append([]core.HomeRow(nil), rows...)
					if populated {
						input[1] = row("cw", KindContinue, 1)
					}
					if rendered {
						for i := range input {
							input[i].Hub.ItemCount = len(input[i].Items)
							input[i].Items = nil
						}
					}
					got := order(arrangeLayout(input, core.HomeLayout{}, 1, hubContext(testNow, core.DeviceWeb), section, 8))
					want := []string{"trend", "top", "new", "crowd"}
					if populated {
						want = []string{"cw", "trend", "top", "new", "crowd"}
					}
					if !reflect.DeepEqual(got[:len(want)], want) {
						t.Fatalf("rendered=%v populated=%v: %v", rendered, populated, got)
					}
				}
			}
			// An explicit viewer anchor and an off policy win over new defaults.
			d := core.HomeLayout{Viewers: []core.ViewerLayout{{UserID: 1, Pages: []core.LayoutPage{{Section: section, Rows: []core.RowPolicy{{Row: KindEra, Status: "always", Slot: 1}, {Row: KindNewReleases, Status: "off"}}}}}}}
			got := order(arrangeLayout(rows, d, 1, hubContext(testNow, core.DeviceWeb), section, 8))
			if got[0] != "theme" || got[1] != "trend" || got[2] != "top" {
				t.Fatal(got)
			}
			for _, id := range got {
				if id == "new" {
					t.Fatal("disabled new releases returned")
				}
			}
		})
	}
}

func TestDiscoverySurvivesCandidateCapAndMixesHome(t *testing.T) {
	// Each row has more than a full screen of both types. Movies deliberately
	// outrank every show: mixing must happen before the display window is cut.
	makeRow := func(kind, id string, base int) *candRow {
		var its []candItem
		for i := 0; i < 80; i++ {
			typ := core.TypeMovie
			key := core.MovieKey(int64(base + i))
			if i >= 40 {
				typ = core.TypeShow
				key = core.ShowKey(int64(base + i))
			}
			its = append(its, candItem{t: core.Title{Key: key, Type: typ, Title: fmt.Sprint(key), Year: 2025}, base: 1 - float64(i)/100, fit: .5})
		}
		r := withTyped(testRow(kind, id, its[:40]), its)
		if kind == KindTop10 {
			r.maxItems = 10
		}
		return r
	}
	var candidates []*candRow
	for i := 0; i < 8; i++ {
		candidates = append(candidates, makeRow(KindShowcase, fmt.Sprintf("showcase.%d", i), 1000+i*100))
	}
	candidates = append(candidates, makeRow(KindTop10, "top10", 3000), makeRow(KindNewReleases, "newreleases", 4000), makeRow(KindTrending, "trending", 5000), makeRow(KindCrowd, "crowd", 6000))
	for _, section := range []core.MediaType{"", core.TypeMovie, core.TypeShow} {
		a := &assembly{model: emptyModel(candidates...), user: core.User{ID: 1}, hc: hubContext(testNow, core.DeviceWeb), section: section, limit: 24, maxRows: 4, pageLen: 4, minRow: 12, day: dayOf(testNow, time.UTC)}
		a.model.taste.Confidence = 1
		rows := a.run()
		got := arrangeLayout(rows, core.HomeLayout{}, 1, a.hc, sectionName(section), 4)
		want := []string{KindTrending, KindTop10, KindNewReleases, KindCrowd}
		if len(got) != 4 {
			t.Fatalf("%s: %v", section, kindsOf(got))
		}
		for i, r := range got {
			if r.Kind != want[i] || r.Hidden {
				t.Fatalf("%s: %v", section, kindsOf(got))
			}
			n := 24
			if r.Kind == KindTop10 {
				n = 10
			}
			if len(r.Items) != n {
				t.Fatalf("%s %s has %d", section, r.Kind, len(r.Items))
			}
			types := map[core.MediaType]bool{}
			for _, it := range r.Items {
				types[it.Type] = true
				if section != "" && it.Type != section {
					t.Fatal("wrong section type")
				}
			}
			if section == "" && i < 3 && len(types) != 2 {
				t.Fatalf("%s not mixed: %v", r.Kind, types)
			}
		}
	}
}

func TestFinalizeRenderedRowsUsesCounts(t *testing.T) {
	h := newHarness(t, nil)
	hubs := []core.Hub{{ID: "movie.era.1990", ItemCount: 24}, {ID: "movie.newreleases", ItemCount: 24}, {ID: "movie.trending", ItemCount: 24}, {ID: "movie.top10", ItemCount: 10}}
	got, err := h.e.FinalizeHome(context.Background(), &core.User{ID: 1}, hubContext(testNow, core.DeviceWeb), "movies", hubs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hubIDsOf(got), []string{"movie.trending", "movie.top10", "movie.newreleases", "movie.era.1990"}) {
		t.Fatal(hubIDsOf(got))
	}
}

func TestDiscoveryIsNotRemovedByHomeDeduplication(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var keys []core.RatingKey
	for i := 0; i < 24; i++ {
		keys = append(keys, movieKey(0, i))
	}
	its := items(cat, keys...)
	for _, kind := range []string{KindNewReleases, KindTrending} {
		c := testRow(kind, "discovery", its)
		a := &assembly{section: core.TypeMovie, limit: 24, minRow: 12, lead: &homeLead{keys: map[core.RatingKey]bool{}}}
		for _, it := range its {
			a.lead.keys[it.t.Key] = true
		}
		got, _, demoted := a.holdBackHomeLead(c, its)
		if len(got) != 24 || demoted {
			t.Fatalf("%s lost to Home: %d, demoted=%v", kind, len(got), demoted)
		}
	}
}
