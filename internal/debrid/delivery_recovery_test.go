package debrid

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type deliveryTransport func(*http.Request) (*http.Response, error)

func (f deliveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInterruptedBodyResumesSameFileAtIncompleteBlock(t *testing.T) {
	r := newEngineRig(t, 8*testBlock, -1)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1
	r.svc.UpdateConfig(cfg)
	transport := r.eng.hc.Transport
	var calls atomic.Int32
	var retryRange string
	r.eng.hc = &http.Client{Transport: deliveryTransport(func(req *http.Request) (*http.Response, error) {
		resp, err := transport.RoundTrip(req)
		if err != nil {
			return resp, err
		}
		switch calls.Add(1) {
		case 1:
			// Two complete blocks, then a truncated third block. Headers remain valid.
			resp.Body = readCloser{Reader: io.LimitReader(resp.Body, 2*testBlock+123), Closer: resp.Body}
		case 2:
			retryRange = req.Header.Get("Range")
		}
		return resp, nil
	})}
	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	got := make([]byte, 4*testBlock)
	if n, err := src.ReadAt(got, 0); err != nil || n != len(got) || !bytes.Equal(got, r.media.data[:len(got)]) {
		t.Fatalf("read n=%d err=%v", n, err)
	}
	// The retry starts at the incomplete block and covers the rest of the run.
	if !strings.HasPrefix(retryRange, fmt.Sprintf("bytes=%d-", 2*testBlock)) {
		t.Fatalf("resumed at %q", retryRange)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d", calls.Load())
	}
}

func TestPermanentlyTruncatedBodyStopsAfterBoundedRetries(t *testing.T) {
	r := newEngineRig(t, 2*testBlock, -1)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1
	r.svc.UpdateConfig(cfg)
	transport := r.eng.hc.Transport
	var calls atomic.Int32
	r.eng.hc = &http.Client{Transport: deliveryTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		resp, err := transport.RoundTrip(req)
		if err == nil {
			resp.Body = readCloser{Reader: io.LimitReader(resp.Body, 123), Closer: resp.Body}
		}
		return resp, err
	})}
	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.ReadAt(make([]byte, testBlock), 0); !errors.Is(err, core.ErrSourceUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != maxRangeAttempts {
		t.Fatalf("unbounded body attempts=%d", calls.Load())
	}
}

func TestMediaBudgetIncludesHedgesAndHoldsUntilBodyClose(t *testing.T) {
	r := newEngineRig(t, testBlock, time.Millisecond)
	var calls atomic.Int32
	r.eng.hc = &http.Client{Transport: deliveryTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 206, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("data")))}, nil
	})}
	r.eng.hedge = r.eng.hc
	slots := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, _, err := r.eng.get(ctx, "http://media.invalid/file", 0, 3, true, slots)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	done := make(chan *http.Response, 1)
	go func() {
		resp, _, err := r.eng.get(ctx, "http://media.invalid/file", 0, 3, true, slots)
		if err != nil {
			done <- nil
			return
		}
		done <- resp
	}()
	select {
	case resp := <-done:
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("request exceeded budget before body closed")
	case <-time.After(20 * time.Millisecond):
	}
	if calls.Load() != 1 {
		t.Fatalf("requests=%d", calls.Load())
	}
	first.Body.Close()
	select {
	case resp := <-done:
		if resp == nil {
			t.Fatal("blocked request failed")
		}
		resp.Body.Close()
	case <-ctx.Done():
		t.Fatal("closing response did not release slot")
	}
	if len(slots) != 0 {
		t.Fatal("body close leaked slot")
	}
}

func TestHedgeWinnerImmediatelyCancelsStalledLoser(t *testing.T) {
	r := newEngineRig(t, testBlock, time.Millisecond)
	canceled := make(chan struct{})
	r.eng.hc = &http.Client{Transport: deliveryTransport(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		close(canceled)
		return nil, req.Context().Err()
	})}
	r.eng.hedge = &http.Client{Transport: deliveryTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 206, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("data")))}, nil
	})}
	slots := make(chan struct{}, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, _, err := r.eng.get(ctx, "http://media.invalid/file", 0, 3, true, slots)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("loser remained active after hedge won")
	}
	resp.Body.Close()
	// Cleanup receives the canceled result asynchronously.
	for len(slots) > 0 && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	if len(slots) != 0 {
		t.Fatal("hedge leaked media slot")
	}
}

func TestFullBudgetSkipsHedgeWithoutBlockingPrimary(t *testing.T) {
	r := newEngineRig(t, testBlock, time.Millisecond)
	var calls atomic.Int32
	r.eng.hc = &http.Client{Transport: deliveryTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		select {
		case <-time.After(20 * time.Millisecond):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		return &http.Response{StatusCode: 206, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("data")))}, nil
	})}
	r.eng.hedge = r.eng.hc
	slots := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, _, err := r.eng.get(ctx, "http://media.invalid/file", 0, 3, true, slots)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 1 || len(slots) != 0 {
		t.Fatalf("requests=%d held=%d", calls.Load(), len(slots))
	}
}
