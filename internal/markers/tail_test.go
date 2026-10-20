package markers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// Open-ended TheIntroDB credits ("to the end") submitted against a
// slightly different cut must still reach the end of the played release:
// without an aligner (or when alignment is unconfident) the unshifted end
// would lie past a shorter release and the credits marker was dropped.
func TestOpenEndedCreditsFollowPlayedTail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		played  time.Duration
		aligner core.TimelineAligner
	}{
		{"shorter, no aligner", 1199 * time.Second, nil},
		{"shorter by more than the tolerance, no aligner", 1190 * time.Second, nil},
		{"shorter, unconfident aligner", 1190 * time.Second, &offsetAligner{ok: false}},
		{"longer, no aligner", 1212 * time.Second, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.OpenMemory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"group_duration_ms":1200000,"intro":[{"start_ms":30000,"end_ms":90000}],"credits":[{"start_ms":1100000,"end_ms":null}]}`)
			}))
			defer srv.Close()
			s := New(Options{DB: db, Enabled: true, BaseURL: srv.URL, Aligner: tc.aligner})
			m, err := s.Markers(ctx, core.MovieKey(603), core.MediaInfo{Duration: tc.played})
			if err != nil {
				t.Fatal(err)
			}
			var credits *Marker
			for i := range m {
				if m[i].Type == "credits" {
					credits = &m[i]
				}
			}
			if credits == nil {
				t.Fatalf("credits marker dropped: %+v", m)
			}
			if credits.StartMS != 1100000 || credits.EndMS != tc.played.Milliseconds() || !credits.Final {
				t.Fatalf("credits = %+v, want 1100000..%d final", *credits, tc.played.Milliseconds())
			}
		})
	}
}

// The last chapter routinely ends a few milliseconds past the probed
// container duration; its credits (and a structural post-credits scene)
// must not be discarded for it.
func TestChapterEndingJustPastDuration(t *testing.T) {
	dur := 100*time.Minute + 12*time.Millisecond
	info := core.MediaInfo{Duration: dur, Chapters: []core.Chapter{
		{Title: "Chapter 1", Start: 0, End: 90 * time.Minute},
		{Title: "End Credits", Start: 90 * time.Minute, End: dur + 40*time.Millisecond},
	}}
	m := FromChapters(info)
	if len(m) != 1 || m[0].Type != "credits" || m[0].EndMS != dur.Milliseconds() || !m[0].Final {
		t.Fatalf("credits chapter = %+v, want one final credits marker ending at the duration", m)
	}
	info.Chapters = []core.Chapter{
		{Title: "Chapter 1", Start: 0, End: 90 * time.Minute},
		{Title: "Credits", Start: 90 * time.Minute, End: 97 * time.Minute},
		{Title: "Chapter 9", Start: 97 * time.Minute, End: dur + 40*time.Millisecond},
	}
	if st := StingersFromChapters(info); len(st) != 1 || st[0].End != dur {
		t.Fatalf("tail stinger = %+v", st)
	}
	// A chapter far past the duration is still bad data.
	info.Chapters = []core.Chapter{{Title: "Credits", Start: 90 * time.Minute, End: dur + time.Minute}}
	if m := FromChapters(info); len(m) != 0 {
		t.Fatalf("overlong chapter accepted: %+v", m)
	}
}
