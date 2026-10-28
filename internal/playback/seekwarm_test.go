package playback

import (
	"testing"
	"time"
)

func TestWarmInputFetchesTheRunSpanFromKeyframes(t *testing.T) {
	rel := mkv("warm", 200<<20, 100*time.Second)
	h := newHarness(t, []*fakeRelease{rel}, []string{"warm"})
	ps := h.prepare(owner, movieKey, "warm")
	g := h.m.lookup(ps.ID).current()
	<-g.indexReady // the background index read is not part of warming
	var points []IndexPoint
	for i := int64(1); i < 10; i++ {
		points = append(points, IndexPoint{Offset: i * (20 << 20), Time: time.Duration(i) * 10 * time.Second})
	}
	g.setIndex(points)
	src := h.engine.sources[len(h.engine.sources)-1]
	before := len(src.readSpans())

	// A run over 55s-65s starts at the 50s keyframe and needs the 70s one.
	h.m.WarmInput(h.m.urlFor(g), 55*time.Second, 65*time.Second)
	lo, hi := int64(100<<20), int64(140<<20)
	deadline := time.Now().Add(5 * time.Second)
	for {
		covered := int64(0)
		for _, sp := range src.readSpans()[before:] {
			a, b := max(sp[0], lo), min(sp[1], hi)
			if b > a {
				covered += b - a
			}
			if sp[0] < lo || sp[1] > hi {
				t.Fatalf("warm read outside the run span: %v", sp)
			}
		}
		if covered >= hi-lo {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("warmed %d of %d bytes", covered, hi-lo)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWarmInputIgnoresUnknownInputs(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("warm", 10<<20, time.Minute)}, []string{"warm"})
	h.m.WarmInput("http://127.0.0.1:1/src/nope/nope", 0, time.Minute)
	h.m.WarmInput("https://iptv.example/live.m3u8", 0, time.Minute)
	h.m.WarmInput("::bad::", 0, time.Minute)
}
