package usenet

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
	"github.com/braedonsaunders/ether/internal/yenc"
)

// A small article is drained after its header checks out, so the probe
// reuses the connection instead of paying a redial per health check.
func TestCheckBodyPartReusesConnectionForSmallArticle(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	data := randData(3, 4096)
	if _, err := s.AddYenc("small@x", data, yenc.EncodeOptions{Name: "f.bin", Part: 2, Total: 5, FileSize: 20_480, Begin: 4097}); err != nil {
		t.Fatal(err)
	}
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 2)}, Options{})
	want := core.ArticlePart{Part: 2, Total: 5, FileSize: 20_480, Begin: 4097, End: 8192}
	if err := f.CheckBodyPart(context.Background(), "small@x", nil, core.PriorityLive, want); err != nil {
		t.Fatal(err)
	}
	st := statsFor(f, "p")
	if st.Dials != 1 || st.LiveConnections != 1 || st.IdleConnections != 1 {
		t.Fatalf("probe did not keep the connection: %+v", st)
	}
	if err := f.CheckBodyPart(context.Background(), "small@x", nil, core.PriorityLive, want); err != nil {
		t.Fatal(err)
	}
	if st := statsFor(f, "p"); st.Dials != 1 {
		t.Fatalf("second probe redialled: %+v", st)
	}
	// The drained connection is fully usable, not desynchronised.
	if got, err := f.Body(context.Background(), "small@x", nil, core.PriorityLive); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("body after probe: %v", err)
	}
}

// A large article is abandoned by closing the connection: the health
// verdict must not wait for a ~1 MiB drain.
func TestCheckBodyPartClosesConnectionForLargeArticle(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	addSeg(t, s, "big@x", bytes.Repeat([]byte("b"), 1<<20))
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 2)}, Options{})
	if err := f.CheckBodyPart(context.Background(), "big@x", nil, core.PriorityLive, core.ArticlePart{Part: 1, Total: 1}); err != nil {
		t.Fatal(err)
	}
	st := statsFor(f, "p")
	if st.LiveConnections != 0 {
		t.Fatalf("large-article probe kept the connection: %+v", st)
	}
	if st.Bytes > 4*readBufferSize {
		t.Fatalf("probe downloaded %d bytes of a 1 MiB article", st.Bytes)
	}
}

func TestCheckBodyPartRejectsWrongArticle(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	if _, err := s.AddYenc("part@x", randData(4, 2000), yenc.EncodeOptions{Name: "f.bin", Part: 2, Total: 5, FileSize: 10_000, Begin: 2001}); err != nil {
		t.Fatal(err)
	}
	s.AddArticle("plain@x", []byte("just a text post\r\nwith several lines\r\n"))
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 2)}, Options{})
	ctx := context.Background()

	cases := []struct {
		name string
		id   string
		want core.ArticlePart
	}{
		{"wrong part", "part@x", core.ArticlePart{Part: 3, Total: 5}},
		{"wrong total", "part@x", core.ArticlePart{Part: 2, Total: 9}},
		{"wrong file size", "part@x", core.ArticlePart{Part: 2, Total: 5, FileSize: 99_999}},
		{"wrong begin", "part@x", core.ArticlePart{Part: 2, Total: 5, Begin: 1}},
		{"wrong end", "part@x", core.ArticlePart{Part: 2, Total: 5, Begin: 2001, End: 7777}},
		{"not yenc", "plain@x", core.ArticlePart{Part: 1, Total: 1}},
	}
	for _, c := range cases {
		err := f.CheckBodyPart(ctx, c.id, nil, core.PriorityLive, c.want)
		if !errors.Is(err, ErrCorrupt) || errors.Is(err, core.ErrArticleMissing) {
			t.Fatalf("%s: err = %v, want ErrCorrupt", c.name, err)
		}
	}
	// The right expectation passes, and unknown fields are not checked.
	if err := f.CheckBodyPart(ctx, "part@x", nil, core.PriorityLive, core.ArticlePart{Part: 2, Total: 5, FileSize: 10_000, Begin: 2001, End: 4000}); err != nil {
		t.Fatal(err)
	}
	if err := f.CheckBodyPart(ctx, "part@x", nil, core.PriorityLive, core.ArticlePart{Part: 2}); err != nil {
		t.Fatal(err)
	}
}

// STAT says 223 everywhere; BODY says the article was taken down. Only the
// BODY probe can see it, and a backfill provider that still serves BODY
// makes the release healthy again.
func TestCheckBodyPartCatchesTakedownBehindStat223(t *testing.T) {
	primary := nntptest.Start(t, nntptest.Options{})
	addSeg(t, primary, "gone@x", []byte("alpha"))
	primary.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "BODY" {
			return nntptest.Fault{Code: 451, Text: "0 <gone@x>"}
		}
		return nntptest.Fault{}
	})
	block := nntptest.Start(t, nntptest.Options{})
	addSeg(t, block, "gone@x", []byte("alpha"))
	bp := block.Provider("block", 1)
	bp.Backfill = true
	ctx := context.Background()

	only := newFetcher(t, []config.UsenetProvider{primary.Provider("p", 1)}, Options{})
	if ok, err := only.Stat(ctx, "gone@x"); err != nil || !ok {
		t.Fatalf("STAT fixture: %v %v", ok, err)
	}
	if err := only.CheckBodyPart(ctx, "gone@x", nil, core.PriorityLive, core.ArticlePart{Part: 1, Total: 1}); !errors.Is(err, core.ErrArticleMissing) {
		t.Fatalf("BODY takedown not detected: %v", err)
	}

	both := newFetcher(t, []config.UsenetProvider{primary.Provider("p", 1), bp}, Options{})
	if err := both.CheckBodyPart(ctx, "gone@x", nil, core.PriorityLive, core.ArticlePart{Part: 1, Total: 1}); err != nil {
		t.Fatalf("backfill BODY should save the probe: %v", err)
	}
}

// An unterminated giant first line must fail the probe instead of being
// buffered whole.
func TestCheckBodyPartBoundedLines(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	s.AddArticle("evil@x", []byte(strings.Repeat("A", 300_000)+"\n=yend size=0\n"))
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 1)}, Options{})
	started := time.Now()
	err := f.CheckBodyPart(context.Background(), "evil@x", nil, core.PriorityLive, core.ArticlePart{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("probe spent too long on a hostile article")
	}
	if st := statsFor(f, "p"); st.Bytes > 200_000 {
		t.Fatalf("probe read %d bytes before failing", st.Bytes)
	}
}

func TestStatStreamPrimaryThenBackfill(t *testing.T) {
	primary := nntptest.Start(t, nntptest.Options{})
	block := nntptest.Start(t, nntptest.Options{})
	addSeg(t, primary, "here@x", []byte("a"))
	addSeg(t, block, "blockonly@x", []byte("b"))
	bp := block.Provider("block", 2)
	bp.Backfill = true
	f := newFetcher(t, []config.UsenetProvider{primary.Provider("p", 2), bp}, Options{})

	got := map[string]bool{}
	err := f.StatStream(context.Background(), []string{"here@x", "blockonly@x", "nowhere@x"}, func(id string, present bool) bool {
		got[id] = present
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got["here@x"] || !got["blockonly@x"] || got["nowhere@x"] || len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["nowhere@x"]; !ok {
		t.Fatal("confirmed miss not reported")
	}
}

// fn returning false must cancel the slowest outstanding chunk: one STAT
// is delayed by the server, but the confirmed miss returns immediately.
func TestStatStreamEarlyStopCancelsSlowChunk(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	for _, id := range []string{"a@x", "b@x", "c@x", "d@x", "slow@x"} {
		addSeg(t, s, id, []byte("x"))
	}
	s.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "STAT" && arg == "slow@x" {
			return nntptest.Fault{Delay: 1500 * time.Millisecond}
		}
		return nntptest.Fault{}
	})
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 4)}, Options{})
	started := time.Now()
	reported := 0
	err := f.StatStream(context.Background(), []string{"a@x", "b@x", "c@x", "d@x", "slow@x", "gone@x"}, func(id string, present bool) bool {
		reported++
		return present // stop at the first confirmed miss
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("early stop took %v; waited for the slow chunk", elapsed)
	}
	if reported == 0 || reported >= 6 {
		t.Fatalf("reported %d ids, want a partial stream", reported)
	}
}

// A provider that errors cannot prove absence: its articles stay undecided
// and StatStream returns the error instead of reporting them missing.
func TestStatStreamProviderErrorIsNotMissing(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	addSeg(t, s, "here@x", []byte("a"))
	s.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "STAT" && arg == "here@x" {
			return nntptest.Fault{Code: 500, Text: "internal error"}
		}
		return nntptest.Fault{}
	})
	f := newFetcher(t, []config.UsenetProvider{s.Provider("p", 1)}, Options{})
	got := map[string]bool{}
	err := f.StatStream(context.Background(), []string{"here@x", "gone@x"}, func(id string, present bool) bool {
		got[id] = present
		return true
	})
	if err == nil {
		t.Fatal("expected the provider error")
	}
	if _, reported := got["here@x"]; reported {
		t.Fatalf("errored article reported as decided: %v", got)
	}
	if _, reported := got["gone@x"]; reported {
		t.Fatalf("article on the errored chunk reported as missing: %v", got)
	}
}

// BenchmarkCheckBody compares the drain and close strategies for the BODY
// probe. Loopback makes drains look cheaper than they are on a real
// provider link, while a close costs a background redial measured by
// BenchmarkDial-ish phase stats; the numbers justify keeping the drain
// threshold small (checkBodyDrainMax).
func BenchmarkCheckBody(b *testing.B) {
	s := nntptest.Start(b, nntptest.Options{})
	addSegBench := func(id string, n int) {
		b.Helper()
		if _, err := s.AddYenc(id, make([]byte, n), yenc.EncodeOptions{Name: "f.bin", Part: 1, Total: 1, FileSize: int64(n)}); err != nil {
			b.Fatal(err)
		}
	}
	addSegBench("small@x", 8<<10)
	addSegBench("large@x", 768<<10)
	f := New([]config.UsenetProvider{s.Provider("p", 1)}, Options{Logger: quiet, MinIdle: -1})
	b.Cleanup(func() { f.Close() })
	ctx := context.Background()

	b.Run("small_drain_reuse", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := f.CheckBody(ctx, "small@x", nil, core.PriorityLive); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("large_close_redial", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := f.CheckBody(ctx, "large@x", nil, core.PriorityLive); err != nil {
				b.Fatal(err)
			}
		}
	})
	old := checkBodyDrainMax
	checkBodyDrainMax = 1 << 30
	b.Run("large_drain_forced", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := f.CheckBody(ctx, "large@x", nil, core.PriorityLive); err != nil {
				b.Fatal(err)
			}
		}
	})
	checkBodyDrainMax = old
}

// A provider locked out by bad credentials cannot answer: like Body and
// Stat, StatStream must not let it keep every missing article undecided.
func TestStatStreamIgnoresAuthLockedProvider(t *testing.T) {
	bad := nntptest.Start(t, nntptest.Options{Username: "u", Password: "right"})
	good := nntptest.Start(t, nntptest.Options{})
	addSeg(t, good, "here@x", []byte("a"))
	bp := bad.Provider("bad", 2)
	bp.Password = "wrong"
	gp := good.Provider("good", 2)
	gp.Priority = 1
	f := newFetcher(t, []config.UsenetProvider{bp, gp}, Options{MinIdle: -1})
	for pass := 0; pass < 2; pass++ { // mid-request lockout, then already disabled
		got := map[string]bool{}
		err := f.StatStream(context.Background(), []string{"here@x", "gone@x"}, func(id string, present bool) bool {
			got[id] = present
			return true
		})
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if p, ok := got["gone@x"]; !ok || p || !got["here@x"] {
			t.Fatalf("pass %d: got %v", pass, got)
		}
	}
	if st := statsFor(f, "bad"); st.State != StateAuthFail {
		t.Fatalf("state %q", st.State)
	}
}
