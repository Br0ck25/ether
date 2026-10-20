package personalize

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// A weeknight night owl's usual stop is 00:30. Just after midnight that
// stop is twenty minutes away, not a day away; seen from the evening it is
// tomorrow's 00:30.
func TestStopBudgetNightOwlAfterMidnight(t *testing.T) {
	ts := &Taste{Habits: core.TasteHabits{PeakHours: []int{22, 21, 20}}}
	// Tuesday 00:10, a weeknight.
	now := time.Date(2026, 10, 13, 0, 10, 0, 0, time.UTC)
	hc := hubContext(now, core.DeviceTV)
	if hc.Weekend {
		t.Fatal("test setup: expected a weeknight context")
	}
	if got := ts.stopBudgetMinutes(hc, now); got != 20 {
		t.Errorf("budget at 00:10 = %v minutes, want 20 (stop at 00:30)", got)
	}
	// Monday 23:00: the 00:30 stop is ninety minutes away, so the context
	// target (under ninety) governs.
	eve := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	hcEve := hubContext(eve, core.DeviceTV)
	if got, want := ts.stopBudgetMinutes(hcEve, eve), max(ts.targetMinutes(hcEve), 15); got != want {
		t.Errorf("budget at 23:00 = %v, want the context target %v", got, want)
	}
}
