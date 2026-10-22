package transcode

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func matchPCM(gain, frequency float64) []byte {
	out := make([]byte, audioMatchRate*6*4)
	for i := 0; i < len(out)/4; i++ {
		t := float64(i) / audioMatchRate
		env := 0.025 + 0.17*math.Pow((1+math.Sin(t*3.7+math.Sin(t*1.3)))/2, 2)
		v := gain * env * (math.Sin(t*frequency*2*math.Pi) + 0.3*math.Sin(t*frequency*3.14*2*math.Pi))
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(float32(v)))
	}
	return out
}

func TestSourceAudioMatchingEvidence(t *testing.T) {
	a := matchPCM(1, 437)
	cases := []struct {
		name string
		b    []byte
		want float64
		ok   bool
	}{
		{"louder", matchPCM(2, 437), -6.0206, true},
		{"quieter", matchPCM(0.75, 437), 2.4988, true},
		{"unchanged", matchPCM(1, 437), 0, true},
		{"different-content-same-envelope", matchPCM(2, 673), 0, false},
		{"silence", make([]byte, len(a)), 0, false},
		{"truncated", a[:len(a)-4], 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gain, ok := matchingAudioGain(a, tc.b)
			if ok != tc.ok || ok && math.Abs(gain-tc.want) > 0.02 {
				t.Fatalf("gain=%v ok=%v", gain, ok)
			}
		})
	}
	differentMix := matchPCM(2, 437)
	for i := len(differentMix) / 2; i < len(differentMix); i += 4 {
		v := math.Float32frombits(binary.LittleEndian.Uint32(differentMix[i:]))
		binary.LittleEndian.PutUint32(differentMix[i:], math.Float32bits(v/3))
	}
	if _, ok := matchingAudioGain(a, differentMix); ok {
		t.Fatal("changing mix accepted")
	}
	// Shifted encoded audio, same passage; offset does not become a level jump.
	delayed := append(make([]byte, 137*4), matchPCM(2, 437)...)
	if g, ok := matchingAudioGain(a, delayed[:len(a)]); !ok || math.Abs(g+6.0206) > 0.15 {
		t.Fatalf("delayed gain=%v ok=%v", g, ok)
	}
	for _, v := range []float32{float32(math.NaN()), float32(math.Inf(1))} {
		bad := append([]byte(nil), a...)
		binary.LittleEndian.PutUint32(bad, math.Float32bits(v))
		if _, ok := matchingAudioGain(a, bad); ok {
			t.Fatal("invalid PCM accepted")
		}
	}
}

func audioMatchFixture(t *testing.T, runner Runner) (*Session, core.MediaInfo) {
	t.Helper()
	info := core.MediaInfo{Duration: time.Minute, Streams: []core.StreamInfo{{Index: 0, Type: "video", Codec: "h264", Width: 640, Height: 360}, {Index: 1, Type: "audio", Codec: "aac", Profile: "LC", Channels: 2, SampleRate: 48000, Language: "eng", ChannelLayout: "stereo"}}}
	s := &Session{m: &Manager{runner: runner, feat: features{encoders: map[string]bool{"aac": true}, filters: map[string]bool{"alimiter": true}}}, log: testLogger(t), container: ContainerMatroska, segDur: 4 * time.Second, total: 15}
	s.input = "http://127.0.0.1:1234/src/session/old"
	s.info = info
	s.decision = core.Decision{Container: ContainerMatroska, Kind: core.DecisionRemux, VideoCopy: true, AudioCopy: true, AudioStream: 1, SubtitleStream: -1}
	s.done = []map[int]bool{{0: true, 1: true, 2: true, 3: true}}
	return s, info
}

func TestAudioPreparationAndAbsoluteGain(t *testing.T) {
	var calls atomic.Int32
	runner := &fakeRunner{runFunc: func(ctx context.Context, c Command) error {
		calls.Add(1)
		args := strings.Join(c.Args, " ")
		if !strings.Contains(args, "background=1") || !strings.Contains(args, "-map 0:1 -vn") {
			t.Errorf("unsafe/non-selected sample: %s", args)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 1500*time.Millisecond {
			t.Error("unbounded sample")
		}
		gain := 1.0
		if strings.Contains(args, "/new?") {
			gain = 2
		}
		_, err := c.Stdout.Write(matchPCM(gain, 437))
		return err
	}}
	s, info := audioMatchFixture(t, runner)
	nextURL := "http://127.0.0.1:1234/src/session/new"
	s.prepareAudioMatch(context.Background(), nextURL, info, 16*time.Second)
	s.prepareAudioMatch(context.Background(), nextURL, info, 20*time.Second)
	if calls.Load() != 2 {
		t.Fatalf("cache calls=%d", calls.Load())
	}
	d := s.applyAudioMatchLocked(nextURL, adaptDecision(s.audioMatchBaseDecision(), s.info, info, s.container))
	if d.AudioCopy || !d.VideoCopy || s.audioMatch.gain != -6 || d.AudioCodec != "aac" {
		t.Fatalf("decision=%+v match=%+v", d, s.audioMatch)
	}
	s.decision = d
	s.input = nextURL
	if !s.audioMatchBaseDecision().AudioCopy {
		t.Fatal("copy override sticky")
	}
	// A third source at the original level must return to copy, not accumulate -6.
	third := "http://127.0.0.1:1234/src/session/third"
	s.prepareAudioMatch(context.Background(), third, info, 20*time.Second)
	d = s.applyAudioMatchLocked(third, adaptDecision(s.audioMatchBaseDecision(), s.info, info, s.container))
	if !d.AudioCopy || s.audioMatch.gain != 0 {
		t.Fatalf("gain compounded: %+v gain=%v", d, s.audioMatch.gain)
	}
}

func TestAudioPreparationSkipsUnsafeOrUnbuffered(t *testing.T) {
	for _, name := range []string{"unbuffered", "filters", "language", "channels", "atmos", "busy", "live", "ladder", "RTX", "HE-AAC", "EAC3-copy"} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{runFunc: func(context.Context, Command) error { t.Error("unexpected sampling"); return fmt.Errorf("unexpected") }}
			s, info := audioMatchFixture(t, runner)
			info.Streams = append([]core.StreamInfo(nil), info.Streams...)
			switch name {
			case "unbuffered":
				s.done[0] = map[int]bool{}
			case "filters":
				s.decision.AudioFilters = []string{"loudnorm=I=-16"}
			case "language":
				info.Streams[1].Language = "fra"
			case "channels":
				info.Streams[1].Channels = 6
			case "atmos":
				info.Streams[1].Profile = "EAC3 Atmos"
			case "busy":
				s.m.audioMatchBusy.Store(true)
			case "live":
				s.liveInput = true
			case "ladder":
				s.rungs = make([]rung, 2)
			case "RTX":
				s.rtxTimeline = true
			case "HE-AAC":
				s.info.Streams[1].Profile = "HE-AAC"
			case "EAC3-copy":
				s.info.Streams[1].Codec = "eac3"
				s.m.feat.encoders["eac3"] = true
			}
			s.prepareAudioMatch(context.Background(), "http://127.0.0.1/new", info, 16*time.Second)
		})
	}
}

func TestAudioSamplingCancellationJoins(t *testing.T) {
	var active atomic.Int32
	runner := &fakeRunner{runFunc: func(ctx context.Context, c Command) error {
		active.Add(1)
		defer active.Add(-1)
		<-ctx.Done()
		return ctx.Err()
	}}
	s, info := audioMatchFixture(t, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s.prepareAudioMatch(ctx, "http://127.0.0.1/new", info, 16*time.Second)
	if active.Load() != 0 || s.m.audioMatchBusy.Load() || len(s.audioMatch.prepared) > 0 {
		t.Fatal("sample outlived caller or cached failure")
	}
}

func TestAudioGainArgsAndBounds(t *testing.T) {
	for _, g := range []float64{-20, -6, 0.9, 2.4, 20} {
		gain := boundedAudioGain(g)
		if gain < -6 || gain > 3 || g == 0.9 && gain != 0 {
			t.Fatalf("bound %v -> %v", g, gain)
		}
		s, info := audioMatchFixture(t, nil)
		d := s.decision
		d.AudioCopy = false
		d.AudioCodec = "aac"
		args := strings.Join(buildArgs(runSpec{Input: "test.mkv", Info: info, Decision: d, AudioGainDB: gain, SegDur: 4 * time.Second}), " ")
		if !strings.Contains(args, "-c:v copy") {
			t.Fatal("audio matching reencodes video")
		}
		if gain > 0 && !strings.Contains(args, "level=false:latency=true") {
			t.Fatal("boost without compensated limiter")
		}
		if gain == 0 && strings.Contains(args, "volume=") {
			t.Fatal("gain applied in dead band")
		}
	}
}
