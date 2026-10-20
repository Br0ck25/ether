package personalize

import (
	"testing"
	"time"
)

func TestRankerTrainsOnlyWhenQuiet(t *testing.T) {
	busy := false
	h := newHarness(t, func(o *Options) { o.Busy = func() bool { return busy } })
	if h.e.rk == nil {
		t.Skip("ranker disabled in this harness")
	}
	h.e.rk.opts.TrainHour = 4
	at := func(hour int) time.Time { return time.Date(2026, 10, 20, hour, 30, 0, 0, time.UTC) }
	recent := at(4).Add(-12 * time.Hour)
	if !h.e.quietToTrain(at(5), recent) {
		t.Fatal("should train in the overnight window")
	}
	if h.e.quietToTrain(at(20), recent) {
		t.Fatal("should wait for the overnight window while the model is fresh")
	}
	if !h.e.quietToTrain(at(20), at(20).Add(-49*time.Hour)) || !h.e.quietToTrain(at(20), time.Time{}) {
		t.Fatal("a stale or untrained model should train at the next quiet moment")
	}
	busy = true
	if h.e.quietToTrain(at(4), time.Time{}) {
		t.Fatal("never train while someone is streaming")
	}
	busy = false
	h.e.rk.opts.TrainHour = -1
	if !h.e.quietToTrain(at(20), recent) {
		t.Fatal("TrainHour < 0 trains at any hour")
	}
}
