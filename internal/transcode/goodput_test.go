package transcode

import (
	"testing"
	"time"
)

func TestGoodputEstimatorIgnoresTinyWritesAndDropsImmediately(t *testing.T) {
	var g goodputEstimator
	g.add(1024, time.Second)
	if g.Kbps() != 0 || g.Samples() != 0 {
		t.Fatal("tiny write should not influence estimator")
	}
	g.add(1_000_000, time.Second)
	if got := g.Kbps(); got != 8000 {
		t.Fatalf("initial goodput = %d kbps, want 8000", got)
	}
	g.add(500_000, time.Second)
	if got := g.Kbps(); got != 4000 {
		t.Fatalf("goodput = %d kbps, want immediate 4000", got)
	}
}
