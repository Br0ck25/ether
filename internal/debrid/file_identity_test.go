package debrid

import (
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
)

func TestChooseFileRejectsSameNumberedReboot(t *testing.T) {
	target := &core.ReleaseTarget{Season: 2, Episode: 2, ShowYear: 2005, EpisodeTitle: "Stone and Sky"}
	files := []File{{ID: "wrong-year", Path: "Space.Pilot.2023.S02E02.Light.1080p.mkv", Size: 2000}, {ID: "wrong-title", Path: "Space.Pilot.S02E02.Light.1080p.mkv", Size: 1500}, {ID: "right", Path: "Space.Pilot.2005.S02E02.Stone.and.Sky.1080p.mkv", Size: 1000}}
	got, err := ChooseFile(files, target)
	if err != nil || got.ID != "right" {
		t.Fatalf("wrong episode file: %+v %v", got, err)
	}
	if _, err := ChooseFile(files[:2], target); err == nil || errors.Is(err, core.ErrReleaseRejected) {
		t.Fatalf("identity mismatch accepted or permanently poisoned other series: %v", err)
	}
}
