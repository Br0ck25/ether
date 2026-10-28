package trackprefs

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
)

func TestPolicyAndShowMemory(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db)
	r := Default()
	r.AudioLanguages = []string{"en"}
	r.SubtitleLanguages = []string{"en"}
	r.SubtitleMode = "foreign-only"
	if err = s.PutRules(ctx, 0, r); err != nil {
		t.Fatal(err)
	}
	info := core.MediaInfo{Streams: []core.StreamInfo{{Type: "audio", Language: "jpn", Index: 3}, {Type: "audio", Language: "eng", Index: 5}, {Type: "subtitle", Language: "eng", Index: 9}}}
	key := core.EpisodeKey(1396, 1, 1)
	sel, err := s.Select(ctx, 7, key, info, "ja")
	if err != nil || sel.Audio != 1 || sel.Subtitle != -1 {
		t.Fatalf("policy: %+v %v", sel, err)
	}
	if err = s.Remember(ctx, 7, key, "audio", &info.Streams[0]); err != nil {
		t.Fatal(err)
	}
	sel, err = s.Select(ctx, 7, core.EpisodeKey(1396, 1, 2), info, "ja")
	if err != nil || sel.Audio != 0 || sel.Subtitle != 2 {
		t.Fatalf("memory: %+v %v", sel, err)
	}
	sel, _ = s.Select(ctx, 8, key, info, "")
	if sel.Audio != 1 {
		t.Fatal("memory leaked between viewers")
	}
	if err = s.Remember(ctx, 7, key, "subtitle", nil); err != nil {
		t.Fatal(err)
	}
	sel, _ = s.Select(ctx, 7, key, info, "")
	if sel.Subtitle != -1 {
		t.Fatal("subtitle off not remembered")
	}
	if err = s.Forget(ctx, 7, 1396); err != nil {
		t.Fatal(err)
	}
	sel, _ = s.Select(ctx, 7, key, info, "")
	if sel.Audio != 1 {
		t.Fatal("forget failed")
	}
}
func TestFiltersAndValidation(t *testing.T) {
	r := Default()
	r.Loudness = true
	r.DialogueBoost = true
	r.NightMode = true
	if len(r.AudioFilters(2)) != 2 || len(r.AudioFilters(6)) != 3 {
		t.Fatal("filter conditions")
	}
	r.SubtitleMode = "invented"
	if r.Validate() == nil {
		t.Fatal("invalid mode accepted")
	}
}
