package markers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteDurationCacheAndChapters(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("tmdb_id") != "1396" || q.Get("season") != "1" || (q.Get("duration_ms") != "1200000" && q.Get("duration_ms") != "1201000") || q.Get("merge_unknown") != "false" {
			t.Error(q)
		}
		io.WriteString(w, `{"intro":[{"start_ms":null,"end_ms":60000}],"credits":[{"start_ms":1100000,"end_ms":null}],"recap":[{"start_ms":-1,"end_ms":9000}]}`)
	}))
	defer srv.Close()
	s := New(Options{DB: db, Enabled: true, BaseURL: srv.URL})
	info := core.MediaInfo{Duration: 20 * time.Minute}
	key := core.EpisodeKey(1396, 1, 1)
	m, err := s.Markers(ctx, key, info)
	if err != nil || len(m) != 2 || !m[1].Final {
		t.Fatalf("markers %+v %v", m, err)
	}
	_, err = s.Markers(ctx, key, info)
	if err != nil || calls != 1 {
		t.Fatalf("cache calls %d %v", calls, err)
	}
	info.Duration += time.Second
	_, err = s.Markers(ctx, key, info)
	if err != nil || calls != 2 {
		t.Fatal("runtime cache collision")
	}
	info.Chapters = []core.Chapter{{Title: "Intro", Start: time.Second, End: 30 * time.Second}}
	m = FromChapters(info)
	if len(m) != 1 || m[0].StartMS != 1000 {
		t.Fatal(m)
	}
}
func TestMatchRejectSilenceAndDetectLongIntro(t *testing.T) {
	a := make([]uint32, 400)
	b := make([]uint32, 500)
	for i := 0; i < 120; i++ {
		v := uint32(((i*173+51)^(i*991))&4095) + 1
		a[40+i] = v
		b[90+i] = v
	}
	x, y, n, ok := MatchIntro(a, b)
	if !ok || x != 40 || y != 90 || n != 120 {
		t.Fatalf("match %d %d %d %v", x, y, n, ok)
	}
	if _, _, _, ok := MatchIntro(make([]uint32, 400), make([]uint32, 500)); ok {
		t.Fatal("silence matched")
	}
}
func TestTailBlackSilence(t *testing.T) {
	log := "black_start:590 black_end:600\nsilence_start: 589\nsilence_end: 600"
	m, ok := CreditsFromLog(log, 10*time.Minute, 20*time.Minute)
	if !ok || m.StartMS != 1190000 || !m.Final {
		t.Fatal(m, ok)
	}
	_, ok = CreditsFromLog(log, 0, 20*time.Minute)
	if ok {
		t.Fatal("mid-program black frame called credits")
	}
	if _, err := json.Marshal(m); err != nil {
		t.Fatal(err)
	}
}

type blockingRunner struct{ started chan struct{} }

func (r blockingRunner) Run(ctx context.Context, _ string, _ []string, _ io.Writer, _ io.Writer) error {
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}
func TestDetectorQueueBoundedAndCloseCancels(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	file := filepath.Join(t.TempDir(), "cached.mkv")
	if err = os.WriteFile(file, []byte("cached media"), 0600); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	s := New(Options{DB: db, Detector: DetectOptions{FFmpeg: "fake", Runner: blockingRunner{started}}})
	info := core.MediaInfo{Duration: 20 * time.Minute}
	if err = s.ScheduleCached(ctx, core.EpisodeKey(1396, 1, 1), info, file); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	for ep := 2; ep <= 9; ep++ {
		if err = s.ScheduleCached(ctx, core.EpisodeKey(1396, 1, ep), info, file); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.ScheduleCached(ctx, core.EpisodeKey(1396, 1, 10), info, file); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("queue not bounded: %v", err)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel detection")
	}
	if err = s.ScheduleCached(ctx, core.EpisodeKey(1396, 1, 11), info, file); err == nil {
		t.Fatal("closed worker accepted job")
	}
}

func TestMergePreservesMultipleRemoteRanges(t *testing.T) {
	remote := []Marker{{Type: "intro", StartMS: 1, EndMS: 2}, {Type: "intro", StartMS: 3, EndMS: 4}, {Type: "credits", StartMS: 5, EndMS: 6}}
	if got := merge(nil, remote); len(got) != 3 {
		t.Fatalf("lost ranges: %+v", got)
	}
	chapter := []Marker{{Type: "intro", StartMS: 9, EndMS: 10}}
	got := merge(chapter, remote)
	if len(got) != 2 || got[0].StartMS != 9 || got[1].Type != "credits" {
		t.Fatalf("source priority: %+v", got)
	}
}
