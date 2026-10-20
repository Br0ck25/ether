package debrid

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

// stallingReader delivers n bytes, then blocks until its request ends.
type stallingReader struct {
	r   io.Reader
	n   int64
	ctx context.Context
}

func (s *stallingReader) Read(p []byte) (int, error) {
	if s.n <= 0 {
		<-s.ctx.Done()
		return 0, s.ctx.Err()
	}
	if int64(len(p)) > s.n {
		p = p[:s.n]
	}
	n, err := s.r.Read(p)
	s.n -= int64(n)
	return n, err
}

func TestStalledBodyIsAbandonedAndResumedAtIncompleteBlock(t *testing.T) {
	r := newEngineRig(t, 8*testBlock, -1)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1
	r.svc.UpdateConfig(cfg)
	r.eng.stall = 100 * time.Millisecond
	transport := r.eng.hc.Transport
	var calls atomic.Int32
	var retryRange atomic.Value
	r.eng.hc = &http.Client{Transport: deliveryTransport(func(req *http.Request) (*http.Response, error) {
		resp, err := transport.RoundTrip(req)
		if err != nil {
			return resp, err
		}
		switch calls.Add(1) {
		case 1:
			// Two complete blocks, part of the third, then the CDN stops
			// sending while keeping the connection open.
			resp.Body = readCloser{Reader: &stallingReader{r: resp.Body, n: 2*testBlock + 123, ctx: req.Context()}, Closer: resp.Body}
		case 2:
			retryRange.Store(req.Header.Get("Range"))
		}
		return resp, nil
	})}
	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make([]byte, 4*testBlock)
	n, err := src.(interface {
		ReadAtContext(context.Context, []byte, int64) (int, error)
	}).ReadAtContext(ctx, got, 0)
	if err != nil || n != len(got) || !bytes.Equal(got, r.media.data[:len(got)]) {
		t.Fatalf("read n=%d err=%v", n, err)
	}
	if rr, _ := retryRange.Load().(string); rr != fmt.Sprintf("bytes=%d-%d", 2*testBlock, 4*testBlock-1) {
		t.Fatalf("resumed at %q", rr)
	}
}

func TestStallBodyReportsStallAndFreesSlot(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	b := &stallBody{rc: io.NopCloser(&stallingReader{r: bytes.NewReader(make([]byte, 10)), n: 4, ctx: ctx}),
		d: 20 * time.Millisecond, stop: cancel, release: func() { <-slots }}
	buf := make([]byte, 10)
	if n, err := b.Read(buf); n != 4 || err != nil {
		t.Fatalf("first read n=%d err=%v", n, err)
	}
	time.Sleep(50 * time.Millisecond) // time between reads is not a stall
	if ctx.Err() != nil {
		t.Fatal("idle consumer counted as a stalled body")
	}
	if _, err := b.Read(buf); !errors.Is(err, errBodyStalled) {
		t.Fatalf("stalled read err=%v", err)
	}
	b.Close()
	if len(slots) != 0 {
		t.Fatal("slot not released on close")
	}
}

// budgetProvider answers Acquire only when its caller gives up.
type budgetProvider struct{ *fakeProvider }

func (p budgetProvider) Acquire(ctx context.Context, _ AcquireRequest) (*Acquired, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("fake: request timed out: %w", ctx.Err())
}

func TestCallerBudgetExpiryDoesNotPauseProvider(t *testing.T) {
	fp := newFakeProvider("realdebrid", nil, nil)
	cfg := config.Debrid{CachedOnly: true, Providers: []config.DebridProvider{{ID: "rd", Kind: "realdebrid", APIKey: "k", Enabled: true}}}
	svc := NewService(cfg, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewProvider: func(config.DebridProvider, ProviderOptions) (Provider, error) { return budgetProvider{fp}, nil }})
	for i := range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, err := svc.Acquire(ctx, torrentRelease(fmt.Sprintf("rel-%d", i)))
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("acquire %d err=%v", i, err)
		}
	}
	if st := svc.Stats()[0]; st.State != "ok" || st.Failures != 0 {
		t.Fatalf("caller budgets paused the provider: state=%s failures=%d", st.State, st.Failures)
	}
}

func TestRetryAfterIsBoundedAndNeverOverflows(t *testing.T) {
	for v, want := range map[string]time.Duration{
		"":                              0,
		"7":                             7 * time.Second,
		"99999999999":                   maxBackoff, // n*1e9 overflows int64
		"9223372036854775807":           maxBackoff,
		"86400":                         maxBackoff,
		"-3":                            0,
		"not a retry-after":             0,
		"Mon, 01 Jan 1990 00:00:00 GMT": -1, // in the past: not positive
	} {
		got := retryAfter(v)
		if want < 0 {
			if got > 0 {
				t.Errorf("retryAfter(%q)=%v, want <= 0", v, got)
			}
			continue
		}
		if got != want {
			t.Errorf("retryAfter(%q)=%v, want %v", v, got, want)
		}
	}
	if got := retryAfter(time.Now().Add(240 * time.Hour).UTC().Format(http.TimeFormat)); got != maxBackoff {
		t.Errorf("far HTTP date: %v", got)
	}
}

func TestSuccessDuringPauseKeepsReportedState(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e := &entry{cfg: config.DebridProvider{ID: "rd", Enabled: true}, sem: newLimiter(1)}
	e.observe(now, &rateLimitError{retryAfter: time.Minute})
	// A call started before the 429 finishes successfully inside the pause.
	if ev := e.observe(now.Add(time.Second), nil); ev != nil {
		t.Fatalf("success inside the pause emitted %+v", ev)
	}
	if e.blocked(now.Add(2*time.Second)) == nil || e.state != "backoff" {
		t.Fatalf("pause lost or state blanked: state=%q", e.state)
	}
	// After the pause a success ends it and announces that.
	if ev := e.observe(now.Add(2*time.Minute), nil); ev == nil || ev.State != "" || e.state != "" {
		t.Fatalf("pause end: ev=%+v state=%q", ev, e.state)
	}
}
