package debrid

import (
	"context"
	"testing"
	"time"
)

// Time to first byte of the head of a file at Play, against fakes with
// injected latency: a provider API call (link generation) costs
// apiLatency and every CDN request costs cdnLatency before headers. These
// are fakes on loopback, not live services; they show the shape of the
// startup cost, not absolute numbers for any provider.
const (
	apiLatency = 120 * time.Millisecond
	cdnLatency = 40 * time.Millisecond
)

type ttfbScenario struct {
	name    string
	prepare func(r *engineRig) []byte // returns the payload Play opens with
}

var ttfbScenarios = []ttfbScenario{
	{"expired-link", func(r *engineRig) []byte {
		p, _ := DecodePayload(r.pl)
		p.Expires = time.Now().Add(-time.Hour)
		r.svc.db = nil // no fresher stored link: Play must call the API
		return p.Encode()
	}},
	{"fresh-link", func(r *engineRig) []byte { return r.pl }},
	{"prewarmed", func(r *engineRig) []byte {
		// What the detail-page prewarm does: health check + prefetch.
		if ok, err := r.eng.Check(context.Background(), r.pl); !ok || err != nil {
			panic(err)
		}
		if err := r.eng.Prefetch(context.Background(), r.rel.ID, r.pl, 0, 1<<20); err != nil {
			panic(err)
		}
		return r.pl
	}},
}

func headTTFB(tb testing.TB, sc ttfbScenario) time.Duration {
	r := newEngineRig(tb, 8<<20, -1)
	r.media.latency = cdnLatency
	r.provs["realdebrid-0"].delay = apiLatency
	pl := sc.prepare(r)
	start := time.Now()
	src, err := r.eng.Open(context.Background(), r.rel.ID, pl)
	if err != nil {
		tb.Fatal(err)
	}
	defer src.Close()
	buf := make([]byte, 32<<10)
	if _, err := src.ReadAt(buf, 0); err != nil {
		tb.Fatal(err)
	}
	return time.Since(start)
}

func TestHeadTTFBAtPlay(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	got := map[string]time.Duration{}
	for _, sc := range ttfbScenarios {
		got[sc.name] = headTTFB(t, sc)
		t.Logf("%-12s head TTFB at Play: %v", sc.name, got[sc.name].Round(time.Microsecond))
	}
	if got["prewarmed"] >= got["fresh-link"] || got["fresh-link"] >= got["expired-link"] {
		t.Fatalf("expected prewarmed < fresh-link < expired-link: %v", got)
	}
	if got["fresh-link"] < cdnLatency || got["expired-link"] < apiLatency+cdnLatency {
		t.Fatalf("latency not injected: %v", got)
	}
}

func BenchmarkHeadTTFB(b *testing.B) {
	for _, sc := range ttfbScenarios {
		b.Run(sc.name, func(b *testing.B) {
			var total time.Duration
			for i := 0; i < b.N; i++ {
				total += headTTFB(b, sc)
			}
			b.ReportMetric(float64(total.Microseconds())/float64(b.N)/1000, "ms/ttfb")
		})
	}
}
