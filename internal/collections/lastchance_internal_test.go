package collections

import (
	"testing"
	"time"
)

func TestRotationBoundaryUsesCalendarTimezoneAcrossDST(t *testing.T) {
	zone, err := time.LoadLocation("America/Toronto")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 0, 30, 0, 0, zone)
	daily := nextRotationBoundary("daily", now)
	expected := time.Date(2026, 11, 2, 0, 0, 0, 0, zone)
	if !daily.Equal(expected) || daily.Sub(now) != 24*time.Hour+30*time.Minute {
		t.Fatalf("DST boundary %s elapsed %s", daily, daily.Sub(now))
	}
	weekly := nextRotationBoundary("weekly", time.Date(2026, 10, 30, 12, 0, 0, 0, zone))
	if !weekly.Equal(expected) {
		t.Fatalf("weekly boundary %s", weekly)
	}
}
