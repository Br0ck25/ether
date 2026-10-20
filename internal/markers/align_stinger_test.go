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

// offsetAligner pretends the played release runs shift ahead of the
// reference cut (a longer studio logo).
type offsetAligner struct {
	shift time.Duration
	ok    bool
	calls int
}

func (a *offsetAligner) AlignTime(_ context.Context, req core.AlignRequest, at time.Duration) (time.Duration, bool) {
	a.calls++
	if req.FromDuration == 0 || req.ToDuration == 0 {
		return 0, false
	}
	return at + a.shift, a.ok
}

func theIntroDBWithRef(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("details") != "true" {
			t.Error("details=true not requested")
		}
		// Markers were submitted against a 1200s cut; we play a 1212s cut.
		io.WriteString(w, `{"group_duration_ms":1200000,`+
			`"intro":[{"start_ms":30000,"end_ms":90000}],`+
			`"credits":[{"start_ms":1100000,"end_ms":null}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheIntroDBMarkersShiftedToPlayedRelease(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := theIntroDBWithRef(t)
	aligner := &offsetAligner{shift: 12 * time.Second, ok: true}
	s := New(Options{DB: db, Enabled: true, BaseURL: srv.URL, Aligner: aligner})
	info := core.MediaInfo{Duration: 1212 * time.Second}
	m, err := s.Markers(ctx, core.MovieKey(603), info)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("markers %+v", m)
	}
	intro, credits := m[0], m[1]
	if intro.Type != "intro" || intro.StartMS != 42000 || intro.EndMS != 102000 {
		t.Fatalf("intro not shifted: %+v", intro)
	}
	if credits.Type != "credits" || credits.StartMS != 1112000 || credits.EndMS != 1212000 || !credits.Final {
		t.Fatalf("credits not shifted to tail: %+v", credits)
	}
	if credits.Source != "theintrodb+aligned" {
		t.Fatalf("source %q", credits.Source)
	}
}

func TestTheIntroDBMarkersKeptWhenAlignmentUnconfident(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := theIntroDBWithRef(t)
	s := New(Options{DB: db, Enabled: true, BaseURL: srv.URL, Aligner: &offsetAligner{ok: false}})
	info := core.MediaInfo{Duration: 1212 * time.Second}
	m, err := s.Markers(ctx, core.MovieKey(603), info)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m[0].StartMS != 30000 || m[1].StartMS != 1100000 {
		t.Fatalf("unconfident alignment changed markers: %+v", m)
	}
}

func TestStingersFromChapters(t *testing.T) {
	dur := 100 * time.Minute
	info := core.MediaInfo{Duration: dur, Chapters: []core.Chapter{
		{Title: "Scene 1", Start: 0, End: 90 * time.Minute},
		{Title: "Credits", Start: 90 * time.Minute, End: 95 * time.Minute},
		{Title: "Post-Credits Scene", Start: 95 * time.Minute, End: 97 * time.Minute},
		{Title: "Credits", Start: 97 * time.Minute, End: dur},
	}}
	st := StingersFromChapters(info)
	if len(st) != 1 || st[0].Start != 95*time.Minute || st[0].End != 97*time.Minute {
		t.Fatalf("named stinger: %+v", st)
	}
	// Structural: short unnamed chapter after a long credits chapter.
	info.Chapters[2].Title = "Chapter 12"
	st = StingersFromChapters(info)
	if len(st) != 1 || st[0].Start != 95*time.Minute {
		t.Fatalf("structural stinger: %+v", st)
	}
	// The same short chapter mid-film is not a stinger.
	info.Chapters = []core.Chapter{
		{Title: "Credits", Start: 10 * time.Minute, End: 12 * time.Minute},
		{Title: "Chapter 3", Start: 12 * time.Minute, End: 14 * time.Minute},
	}
	if st := StingersFromChapters(info); len(st) != 0 {
		t.Fatalf("mid-film chapter called stinger: %+v", st)
	}
}

func TestStingersFromDialogueAndApply(t *testing.T) {
	dur := 100 * time.Minute
	creditsStart := 90 * time.Minute
	cues := []DialogueCue{
		{Start: 89 * time.Minute, End: 89*time.Minute + 2*time.Second},                 // last film line
		{Start: 90*time.Minute + 5*time.Second, End: 90*time.Minute + 8*time.Second},   // credits song lyric
		{Start: 90*time.Minute + 12*time.Second, End: 90*time.Minute + 15*time.Second}, // lyric, no long gap
		// Silence, then a post-credits scene at 96:00.
		{Start: 96 * time.Minute, End: 96*time.Minute + 3*time.Second},
		{Start: 96*time.Minute + 5*time.Second, End: 96*time.Minute + 8*time.Second},
		{Start: 96*time.Minute + 10*time.Second, End: 96*time.Minute + 14*time.Second},
	}
	st := StingersFromDialogue(cues, creditsStart, dur)
	if len(st) != 1 || st[0].Start != 96*time.Minute || st[0].End != 96*time.Minute+14*time.Second {
		t.Fatalf("dialogue stinger: %+v", st)
	}
	out := ApplyStingers([]Marker{
		{Type: "credits", StartMS: creditsStart.Milliseconds(), EndMS: dur.Milliseconds(), Final: true, Source: "theintrodb"},
	}, st, dur)
	if len(out) != 2 {
		t.Fatalf("split credits: %+v", out)
	}
	if out[0].EndMS != (96*time.Minute).Milliseconds() || out[0].Final {
		t.Fatalf("first credits piece: %+v", out[0])
	}
	if out[1].StartMS != (96*time.Minute+14*time.Second).Milliseconds() || out[1].EndMS != dur.Milliseconds() || !out[1].Final {
		t.Fatalf("final credits piece: %+v", out[1])
	}
	// Continuous lyrics alone never become a stinger.
	if st := StingersFromDialogue(cues[:3], creditsStart, dur); len(st) != 0 {
		t.Fatalf("lyrics called stinger: %+v", st)
	}
}

func TestMarkersServiceCutsDialogueStinger(t *testing.T) {
	dur := 100 * time.Minute
	s := New(Options{Dialogue: func(_ context.Context, _ core.RatingKey, _ core.MediaInfo) ([]DialogueCue, bool) {
		return []DialogueCue{
			{Start: 96 * time.Minute, End: 96*time.Minute + 3*time.Second},
			{Start: 96*time.Minute + 5*time.Second, End: 96*time.Minute + 9*time.Second},
		}, true
	}})
	info := core.MediaInfo{Duration: dur, Chapters: []core.Chapter{
		{Title: "Credits", Start: 90 * time.Minute, End: dur},
	}}
	m, err := s.Markers(context.Background(), core.MovieKey(603), info)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m[0].Type != "credits" || m[1].Type != "credits" {
		t.Fatalf("markers %+v", m)
	}
	if m[0].EndMS != (96*time.Minute).Milliseconds() || m[0].Final || !m[1].Final {
		t.Fatalf("stinger split wrong: %+v", m)
	}
}
