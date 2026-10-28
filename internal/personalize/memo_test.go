package personalize

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestAssembledScreensAreMemoised(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	sciFiFan(h, 1)
	u := core.User{ID: 1}
	h.rebuild(u)
	hc := hubContext(testNow, core.DeviceTV)

	first, _ := h.e.HomeHubsFor(ctx, &u, hc, 12)
	gets := h.cat.callCount("get")
	again, _ := h.e.HomeHubsFor(ctx, &u, hc, 12)
	if h.cat.callCount("get") != gets {
		t.Fatal("a repeated Home load re-assembled instead of using the memo")
	}
	if len(first) != len(again) || first[2].ID != again[2].ID {
		t.Fatal("memoised Home differs")
	}
	// The memo hands out copies: a caller reordering its rows does not
	// change the next caller's.
	p1, _ := h.e.PreviewHome(ctx, &u, hc, "", 12)
	p1.Rows[0], p1.Rows[1] = p1.Rows[1], p1.Rows[0]
	p2, _ := h.e.PreviewHome(ctx, &u, hc, "", 12)
	if p2.Rows[0].HubID == p1.Rows[0].HubID {
		t.Fatal("memo shared its slice")
	}

	// Starting something new changes Continue Watching: re-assembled.
	h.start(1, movieKey(1, 3), 0.3, testNow.Add(time.Minute))
	h.e.HomeHubsFor(ctx, &u, hc, 12)
	if h.cat.callCount("get") == gets {
		t.Fatal("Continue Watching changed but the memo was reused")
	}
	// So does a layout change.
	gets = h.cat.callCount("get")
	if _, err := h.e.SetHomeLayout(ctx, core.HomeLayout{Global: []core.LayoutPage{{Section: "home", Rows: []core.RowPolicy{{Row: KindTop10, Status: "off"}}}}}); err != nil {
		t.Fatal(err)
	}
	hubs, _ := h.e.HomeHubsFor(ctx, &u, hc, 12)
	for _, hb := range hubs {
		if hb.ID == "home.top10" {
			t.Fatal("layout change ignored by the memo")
		}
	}
	if h.cat.callCount("get") == gets {
		t.Fatal("layout change did not re-assemble")
	}
}
