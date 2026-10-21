package usenet

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// BenchmarkBodyPipelineRTT exercises Fetcher.Body, priority admission, pooling,
// and CRC-checked yEnc decoding. The synthetic transport schedules each ordered
// reply at command arrival+RTT: it models propagation latency, not a server that
// sleeps serially for every command (which no pipeline can overcome). It is not
// a bandwidth/provider benchmark. Burst size32, one socket, 64KiB/article.
func BenchmarkBodyPipelineRTT(b *testing.B) {
	const burst = 32
	payload := strings.Repeat("native article payload", (64<<10)/22)
	reply := pipelineReply(b, payload)
	for _, rtt := range []time.Duration{0, 10 * time.Millisecond} {
		for _, depth := range []int{1, 4} {
			b.Run(fmt.Sprintf("rtt=%s/depth=%d", rtt, depth), func(b *testing.B) {
				f := New([]config.UsenetProvider{{ID: "synthetic", Host: "synthetic", Enabled: true, Connections: 1}}, Options{
					MinIdle: -1, KeepAlive: -1, BodyPipelineDepth: depth,
					Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
					DialContext: timedPipelineDial(b, rtt, reply),
				})
				defer f.Close()
				b.ReportAllocs()
				b.SetBytes(int64(burst * len(payload)))
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					// Hold the only socket until the burst is queued. Both depths see the
					// exact same work and warmed connection; no artificial batching delay.
					p := f.pools[0]
					c, err := p.get(context.Background(), core.PriorityLive)
					if err != nil {
						b.Fatal(err)
					}
					errs := make(chan error, burst)
					for i := 0; i < burst; i++ {
						go func(i int) {
							_, err := f.Body(context.Background(), fmt.Sprintf("article%d@test", i), nil, core.PriorityLive)
							errs <- err
						}(i)
					}
					deadline := time.Now().Add(time.Second)
					for {
						p.bodyMu.Lock()
						queued := len(p.bodyQueue[core.PriorityLive])
						p.bodyMu.Unlock()
						if queued == burst-1 {
							break
						}
						if time.Now().After(deadline) {
							b.Fatal("burst failed to queue")
						}
						time.Sleep(time.Microsecond)
					}
					p.put(c, core.PriorityLive)
					for i := 0; i < burst; i++ {
						if err := <-errs; err != nil {
							b.Fatal(err)
						}
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(f.Stats()[0].Dials), "sockets")
			})
		}
	}
}

func timedPipelineDial(tb testing.TB, rtt time.Duration, reply string) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		done := make(chan struct{})
		tb.Cleanup(func() { client.Close(); server.Close(); <-done })
		go func() {
			defer close(done)
			defer server.Close()
			if _, err := io.WriteString(server, "200 synthetic\r\n"); err != nil {
				return
			}
			type command struct {
				line  string
				ready time.Time
			}
			commands := make(chan command, 16)
			var reader sync.WaitGroup
			reader.Add(1)
			go func() {
				defer reader.Done()
				defer close(commands)
				r := bufio.NewReader(server)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					commands <- command{line, time.Now().Add(rtt)}
				}
			}()
			// Unblock the reader before waiting for it, including a half-written
			// pipeline when the benchmark aborts.
			defer func() {
				server.Close()
				for range commands {
				}
				reader.Wait()
			}()
			for cmd := range commands {
				if delay := time.Until(cmd.ready); delay > 0 {
					time.Sleep(delay)
				}
				response := reply
				if strings.HasPrefix(cmd.line, "QUIT") {
					response = "205 bye\r\n"
				}
				if _, err := io.WriteString(server, response); err != nil {
					return
				}
			}
		}()
		return client, nil
	}
}
