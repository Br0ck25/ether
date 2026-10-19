package usenet

import (
	"context"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
	"testing"
)

func TestInsightsBodyTakedownAndBackfillCounters(t *testing.T) {
	primary := nntptest.Start(t, nntptest.Options{})
	addSeg(t, primary, "gone@insights", []byte("hello"))
	primary.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "BODY" {
			return nntptest.Fault{Code: 451, Text: "taken down"}
		}
		return nntptest.Fault{}
	})
	backfill := nntptest.Start(t, nntptest.Options{})
	addSeg(t, backfill, "gone@insights", []byte("hello"))
	a, b := primary.Provider("primary", 1), backfill.Provider("block", 1)
	b.Backfill = true
	f := newFetcher(t, []config.UsenetProvider{a, b}, Options{})
	if _, err := f.Body(context.Background(), "gone@insights", nil, core.PriorityLive); err != nil {
		t.Fatal(err)
	}
	p, q := statsFor(f, "primary"), statsFor(f, "block")
	if p.BodyMisses != 1 || p.BodyTakedowns != 1 || q.BackfillSaves != 1 || q.Articles != 1 {
		t.Fatalf("primary %+v block %+v", p, q)
	}
}
