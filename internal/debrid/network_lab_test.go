package debrid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/stream"
	"github.com/braedonsaunders/ether/internal/usenet"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

// This lab exercises production range fetching over real TCP sockets. Provider
// acquisition is deterministic and offline; these are not live-account results.
// Measurements intentionally have no throughput pass/fail threshold.
type networkLabMetric struct {
	Scenario             string  `json:"scenario"`
	Protocol             string  `json:"protocol"`
	Bytes                int64   `json:"bytes"`
	FirstByteMS          float64 `json:"first_byte_ms"`
	SeekMS               float64 `json:"seek_ms,omitempty"`
	CompleteMS           float64 `json:"complete_ms"`
	SHA256               string  `json:"sha256"`
	Requests             int64   `json:"requests,omitempty"`
	SharedBytesPerSecond int64   `json:"shared_bytes_per_second,omitempty"`
}

func networkLabLog(t *testing.T, m networkLabMetric) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("NETWORK_LAB %s", b)
}

func networkLabRead(ctx context.Context, src readAtContexter, size int64) (string, time.Duration, error) {
	hash := sha256.New()
	buf := make([]byte, 256<<10)
	started := time.Now()
	var first time.Duration
	if n, err := src.ReadAtContext(ctx, buf[:1], 0); err != nil || n != 1 {
		return "", time.Since(started), fmt.Errorf("first byte: n=%d: %v", n, err)
	}
	first = time.Since(started)
	for off := int64(0); off < size; {
		want := int(min(int64(len(buf)), size-off))
		n, err := src.ReadAtContext(ctx, buf[:want], off)
		if err != nil && !(errors.Is(err, io.EOF) && n == want) {
			return "", first, err
		}
		if n != want {
			return "", first, fmt.Errorf("short read at %d: %d/%d", off, n, want)
		}
		hash.Write(buf[:n])
		off += int64(n)
	}
	return hex.EncodeToString(hash.Sum(nil)), first, nil
}

// First establish playback, then seek into a previously unseen part of a 16 MiB
// file. A fault affects that new run. Verify the entire file afterward, including
// the interrupted block, rather than accepting successful status codes alone.
func TestNetworkLabRangeRecovery(t *testing.T) {
	for _, fault := range []string{"truncate", "body_stall", "header_timeout", "http_503"} {
		t.Run(fault, func(t *testing.T) {
			const size = 16<<20 + 731
			const seek = 8 << 20
			r := newEngineRig(t, size, -1)
			cfg := r.svc.Config()
			cfg.ReadAheadChunks = -1
			r.svc.UpdateConfig(cfg)
			r.eng.stall = 80 * time.Millisecond
			tr := NewStreamingTransport()
			tr.Proxy = nil
			tr.ResponseHeaderTimeout = 80 * time.Millisecond
			r.eng.hc = &http.Client{Transport: tr}
			t.Cleanup(tr.CloseIdleConnections)
			var injected atomic.Bool
			var calls, active, forwarded atomic.Int64
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				active.Add(1)
				defer active.Add(-1)
				var a, b int64
				fmt.Sscanf(req.Header.Get("Range"), "bytes=%d-%d", &a, &b)
				if a == seek && injected.CompareAndSwap(false, true) {
					switch fault {
					case "http_503":
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					case "header_timeout":
						<-req.Context().Done()
						return
					case "truncate", "body_stall":
						w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, size))
						w.Header().Set("Content-Length", fmt.Sprint(b-a+1))
						w.WriteHeader(http.StatusPartialContent)
						w.Write(r.media.data[a : a+testBlock+123])
						w.(http.Flusher).Flush()
						if fault == "body_stall" {
							<-req.Context().Done()
						}
						return
					}
				}
				forwarded.Add(1)
				r.media.serve(w, req)
			}))
			defer cdn.Close()
			p, err := DecodePayload(r.pl)
			if err != nil {
				t.Fatal(err)
			}
			p.URL = cdn.URL + "/m/realdebrid-1"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := time.Now()
			src, err := r.eng.Open(ctx, r.rel.ID, p.Encode())
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			rc := src.(readAtContexter)
			head := make([]byte, 1)
			if n, err := rc.ReadAtContext(ctx, head, 0); err != nil || n != len(head) {
				t.Fatalf("head: %d %v", n, err)
			}
			first := time.Since(started)
			sought := time.Now()
			buf := make([]byte, 4*testBlock)
			if n, err := rc.ReadAtContext(ctx, buf, seek); err != nil || n != len(buf) {
				t.Fatalf("faulted seek: %d %v", n, err)
			}
			if sha256.Sum256(buf) != sha256.Sum256(r.media.data[seek:seek+len(buf)]) {
				t.Fatal("seek content mismatch")
			}
			seekTime := time.Since(sought)
			got, _, err := networkLabRead(ctx, rc, size)
			if err != nil {
				t.Fatal(err)
			}
			want := sha256.Sum256(r.media.data)
			if got != hex.EncodeToString(want[:]) {
				t.Fatal("complete download SHA-256 mismatch")
			}
			if !injected.Load() {
				t.Fatal("fault was never exercised")
			}
			src.Close()
			waitNoInflight(t, r.media)
			deadline := time.Now().Add(time.Second)
			for active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if active.Load() != 0 {
				t.Fatal("fault handler leaked")
			}
			networkLabLog(t, networkLabMetric{Scenario: fault, Protocol: "debrid_http_range", Bytes: size, FirstByteMS: float64(first) / float64(time.Millisecond), SeekMS: float64(seekTime) / float64(time.Millisecond), CompleteMS: float64(time.Since(started)) / float64(time.Millisecond), SHA256: got, Requests: calls.Load() + r.media.requests.Load() - forwarded.Load()})
		})
	}
}

// An HTTP 429 is intentionally not retried immediately. Exercise the real
// account/CDN pause, verify repeated reads make no new request, then advance the
// policy clock and prove the same source downloads the whole file correctly.
func TestNetworkLabRateLimitRecovery(t *testing.T) {
	const size = 16<<20 + 731
	r := newEngineRig(t, size, -1)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1
	r.svc.UpdateConfig(cfg)
	clock := &testClock{t: time.Now()}
	r.eng.now = clock.now
	var calls atomic.Int64
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		r.media.serve(w, req)
	}))
	defer cdn.Close()
	p, err := DecodePayload(r.pl)
	if err != nil {
		t.Fatal(err)
	}
	p.URL = cdn.URL + "/m/realdebrid-1"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	src, err := r.eng.Open(ctx, r.rel.ID, p.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	rc := src.(readAtContexter)
	for i := 0; i < 3; i++ {
		_, err = rc.ReadAtContext(ctx, make([]byte, 4096), 0)
		if !errors.Is(err, core.ErrSourceUnavailable) || !errors.Is(err, ErrRateLimited) || errors.Is(err, core.ErrArticleMissing) {
			t.Fatalf("rate limit classification: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("pause made %d upstream requests", calls.Load())
	}
	clock.add(61 * time.Second)
	started := time.Now()
	got, first, err := networkLabRead(ctx, rc, size)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(r.media.data)
	if got != hex.EncodeToString(want[:]) {
		t.Fatal("post-pause SHA-256 mismatch")
	}
	src.Close()
	waitNoInflight(t, r.media)
	networkLabLog(t, networkLabMetric{Scenario: "http_429_policy_clock_advanced_61s", Protocol: "debrid_http_range", Bytes: size, FirstByteMS: float64(first) / float64(time.Millisecond), CompleteMS: float64(time.Since(started)) / float64(time.Millisecond), SHA256: got, Requests: calls.Load()})
}

// networkLabPacer shares one serialized receive budget across HTTP and NNTP
// connections, including protocol overhead. It models a receiver-side aggregate
// bandwidth cap, not TCP loss/congestion or a physical Internet link.
type networkLabPacer struct {
	mu   sync.Mutex
	next time.Time
	rate int64
}

func (p *networkLabPacer) wait(n int, done <-chan struct{}) error {
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	p.next = p.next.Add(time.Duration(n) * time.Second / time.Duration(p.rate))
	until := p.next
	p.mu.Unlock()
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-done:
		return net.ErrClosed
	}
}

type networkLabConn struct {
	net.Conn
	pacer *networkLabPacer
	done  chan struct{}
	once  sync.Once
}

func (c *networkLabConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b[:min(len(b), 32<<10)])
	if n > 0 {
		if waitErr := c.pacer.wait(n, c.done); waitErr != nil {
			return 0, waitErr
		}
	}
	return n, err
}
func (c *networkLabConn) Close() error { c.once.Do(func() { close(c.done) }); return c.Conn.Close() }
func (p *networkLabPacer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return &networkLabConn{Conn: c, pacer: p, done: make(chan struct{})}, nil
}

// Opt in because this intentionally consumes a shared 8 MiB/s network budget
// for two full 16 MiB downloads while a third native file download runs. Invoke:
// ETHER_NETWORK_LAB=1 go test ./internal/debrid -run TestNetworkLabMixed -v -count=1
func TestNetworkLabMixed(t *testing.T) {
	if os.Getenv("ETHER_NETWORK_LAB") != "1" {
		t.Skip("set ETHER_NETWORK_LAB=1 for concurrent download lab")
	}
	const size = 16<<20 + 731
	const rate = 8 << 20
	r := newEngineRig(t, size, -1)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1
	r.svc.UpdateConfig(cfg)
	pacer := &networkLabPacer{rate: rate}
	tr := NewStreamingTransport()
	tr.Proxy = nil
	tr.DialContext = pacer.dial
	r.eng.hc = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)
	// Bounded, deterministic origin-side scheduling delay; this is not an RTT
	// emulator. Bandwidth is shared by every connection through the pacer above.
	r.media.latency = 12 * time.Millisecond
	srv := nntptest.Start(t, nntptest.Options{})
	nzb, err := srv.PostFiles(map[string][]byte{"movie.mkv": r.media.data}, 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := usenet.New([]config.UsenetProvider{srv.Provider("network-lab", 4)}, usenet.Options{DialContext: pacer.dial, MinIdle: -1, CommandTimeout: 10 * time.Second, IdleReadTimeout: 2 * time.Second})
	defer fetcher.Close()
	eng := stream.New(fetcher, nil, stream.Options{ReadAheadMin: 256 << 10, ReadAheadMax: 1 << 20, HotBufferBytes: 32 << 20}, r.svc.log)
	defer eng.Close()
	path := filepath.Join(t.TempDir(), "native.mkv")
	if err = os.WriteFile(path, r.media.data, 0600); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(r.media.data)
	wantHex := hex.EncodeToString(want[:])
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	type result struct {
		metric networkLabMetric
		err    error
	}
	results := make(chan result, 3)
	for _, protocol := range []string{"debrid_http_range", "usenet_nntp_yenc", "native_file_warm_os_cache"} {
		go func() {
			<-start
			began := time.Now()
			m := networkLabMetric{Scenario: "concurrent_shared_receive_budget", Protocol: protocol, Bytes: size, SharedBytesPerSecond: rate}
			var sum string
			var first time.Duration
			var err error
			if protocol == "native_file_warm_os_cache" {
				m.SharedBytesPerSecond = 0 // native disk I/O does not use the network budget
				var f *os.File
				f, err = os.Open(path)
				if err == nil {
					h := sha256.New()
					buf := make([]byte, 256<<10)
					var n int
					n, err = f.Read(buf[:1])
					first = time.Since(began)
					if err == nil {
						h.Write(buf[:n])
						_, err = io.Copy(h, f)
					}
					sum = hex.EncodeToString(h.Sum(nil))
					f.Close()
				}
			} else {
				var src core.MediaSource
				if protocol == "debrid_http_range" {
					src, err = r.eng.Open(ctx, r.rel.ID, r.pl)
				} else {
					src, err = eng.Open(ctx, "network-lab", nzb)
				}
				if err == nil {
					openTime := time.Since(began)
					sum, first, err = networkLabRead(ctx, src.(readAtContexter), size)
					first += openTime
					src.Close()
				}
			}
			m.FirstByteMS = float64(first) / float64(time.Millisecond)
			m.CompleteMS = float64(time.Since(began)) / float64(time.Millisecond)
			m.SHA256 = sum
			if err == nil && sum != wantHex {
				err = fmt.Errorf("%s SHA-256 mismatch", protocol)
			}
			results <- result{m, err}
		}()
	}
	close(start)
	for i := 0; i < 3; i++ {
		res := <-results
		if res.err != nil {
			t.Error(res.metric.Protocol, res.err)
		}
		networkLabLog(t, res.metric)
	}
	eng.Close()
	fetcher.Close()
	r.eng.Close()
	tr.CloseIdleConnections()
	waitNoInflight(t, r.media)
}
