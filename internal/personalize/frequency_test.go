package personalize

import (
	"context"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestFrequencyCapFromImpressions(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	sciFiFan(h, 1)
	u := core.User{ID: 1, Title: "Alice"}
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)
	p, _ := h.e.PreviewHome(ctx, &u, hc, "", 12)
	var row *core.HomeRow
	for i := range p.Rows {
		if !p.Rows[i].Hidden && p.Rows[i].Source == core.RowSourceEngine {
			row = &p.Rows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("no engine row: %v", kindsOf(p.Rows))
	}
	if _, err := h.e.SetHomeLayout(ctx, &core.HomeLayout{Global: home(core.RowPolicy{Row: row.Kind, MaxDaysPerWeek: 2})}); err != nil {
		t.Fatal(err)
	}
	// Shown on two of the last six days (the impressions the Plex front end
	// reports): today it rests.
	for _, back := range []int{2, 4} {
		at := testNow.AddDate(0, 0, -back)
		h.e.RecordImpressions(&u, hubContext(at, core.DeviceTV), []core.Hub{row.Hub})
	}
	h.e.Flush(ctx)
	h.e.invalidateStats()
	got, _ := h.e.PreviewHome(ctx, &u, hc, "", 12)
	r := rowByKind(got.Rows, row.Kind)
	if r == nil || !r.Hidden || !strings.Contains(r.HiddenReason, "at most 2 days a week, and it was shown on 2 of the last 6 days") {
		t.Fatalf("capped row = %+v", r)
	}
	hubs, _ := h.e.HomeHubsFor(ctx, &u, hc, 12)
	for _, hb := range hubs {
		if hb.ID == row.HubID {
			t.Fatal("/hubs served a row over its frequency cap")
		}
	}
	// Five days later the older showing has left the week.
	h.clock.Set(testNow.AddDate(0, 0, 3))
	h.e.invalidateStats()
	got, _ = h.e.PreviewHome(ctx, &u, hubContext(testNow.AddDate(0, 0, 3), core.DeviceTV), "", 12)
	if r := rowByKind(got.Rows, row.Kind); r != nil && r.Hidden && strings.Contains(r.HiddenReason, "Frequency cap") {
		t.Fatalf("cap outlived the week: %+v", r)
	}
}
