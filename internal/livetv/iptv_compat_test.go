package livetv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

type compatibilityTranscoder struct {
	fakeTranscoder
	info   core.MediaInfo
	probes int
}

func (f *compatibilityTranscoder) Probe(context.Context, string) (*core.MediaInfo, error) {
	f.probes++
	info := f.info
	return &info, nil
}

func (f *compatibilityTranscoder) Decide(info core.MediaInfo, caps core.ClientCaps, opts core.DecideOptions) core.Decision {
	return transcode.Decide(info, caps, opts, transcode.Policy{})
}

func TestIPTVHLSChecksDecoderCompatibilityBeforePassthrough(t *testing.T) {
	for _, tt := range []struct {
		name, video, audio string
		relay, videoCopy   bool
	}{
		{"MPEG2 and MP2 need encoding", "mpeg2video", "mp2", true, false},
		{"compatible H264 AAC stays upstream", "h264", "aac", false, true},
		{"only unsupported audio is encoded", "h264", "mp2", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\npart.ts\n")
			}))
			defer up.Close()
			tc := &compatibilityTranscoder{info: core.MediaInfo{Container: "hls", Duration: time.Hour, BitrateKbps: 5000, Streams: []core.StreamInfo{
				{Index: 3, Type: "video", Codec: tt.video, CodecString: "source-video", Width: 1280, Height: 720, FrameRate: 59.94},
				{Index: 5, Type: "audio", Codec: tt.audio, CodecString: "source-audio", Channels: 2, Default: true, SampleRate: 44100},
				{Index: 7, Type: "audio", Codec: "aac", Channels: 6},
			}}}
			s := testService(t, Options{HTTPClient: up.Client(), Transcoder: tc})
			channel, err := s.SaveChannel(context.Background(), Channel{Name: "Fixture", Number: "1", Kind: "iptv", Enabled: true, StreamURL: up.URL})
			if err != nil {
				t.Fatal(err)
			}
			u := &core.User{ID: 1}
			info, err := s.Tune(context.Background(), u, channel.ID, core.ClientCaps{ClientID: "apple-tv", Platform: "tvOS", VideoCodecs: []string{"h264", "hevc"}, AudioCodecs: []string{"aac", "ac3", "eac3"}, HLSContainers: []string{"mkv"}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.probes != 1 {
				t.Fatalf("probes = %d", tc.probes)
			}
			if !tt.relay {
				if len(tc.requests) != 0 || info.MediaInfo.Video().Codec != tt.video {
					t.Fatal("compatible source unnecessarily converted")
				}
				return
			}
			if len(tc.requests) != 1 {
				t.Fatalf("relay requests = %d", len(tc.requests))
			}
			r := tc.requests[0]
			if r.Info.Duration != 0 || r.Info.Video().Codec != tt.video || r.Decision.VideoCopy != tt.videoCopy || r.Decision.AudioCopy || r.Decision.AudioStream != 5 || r.Decision.Container != "mpegts" || r.Decision.VideoCodec != "h264" || r.Decision.AudioCodec != "aac" {
				t.Fatalf("wrong conversion plan: %+v", r.Decision)
			}
			for _, snapshot := range []SessionInfo{info, mustLiveSession(t, s, info.ID, u)} {
				media := snapshot.MediaInfo
				if media.Duration != 0 || media.Container != "mpegts" || len(media.Streams) != 2 || media.Streams[0].Index != 0 || media.Streams[1].Index != 1 || media.Streams[0].Codec != "h264" || media.Streams[1].Codec != "aac" || media.Streams[1].Channels != 2 || media.Streams[1].CodecString != "" {
					t.Fatalf("tune describes source rather than output: %+v", media)
				}
				if media.Streams[1].SampleRate != 48000 {
					t.Fatalf("audio analysis retained source sample rate: %d", media.Streams[1].SampleRate)
				}
			}
			if tc.info.Duration != time.Hour || tc.info.Streams[0].Index != 3 || tc.info.Streams[1].Codec != tt.audio {
				t.Fatal("source analysis mutated")
			}
			rec := httptest.NewRecorder()
			s.ServePlaylist(rec, httptest.NewRequest("GET", "/", nil), info.ID, u)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/relay/0.ts") {
				t.Fatalf("relay playlist: %d %s", rec.Code, rec.Body.String())
			}
			if err := s.Stop(context.Background(), info.ID, u); err != nil {
				t.Fatal(err)
			}
			if !tc.hls[0].closed {
				t.Fatal("live encoder survived stop")
			}
		})
	}
}

func mustLiveSession(t *testing.T, s *Service, id string, u *core.User) SessionInfo {
	t.Helper()
	info, err := s.Session(context.Background(), id, u)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

type startingLiveRelay struct {
	core.HLSSession
	ready bool
	fail  bool
	waits int
}

func (f *startingLiveRelay) ServePlaylist(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
	if f.ready {
		fmt.Fprint(w, "#EXTINF:4,\n00000.ts\n")
	}
}
func (f *startingLiveRelay) ServeSegment(w http.ResponseWriter, r *http.Request, n int) {
	f.waits++
	if f.fail {
		http.Error(w, "encoder failed", http.StatusBadGateway)
		return
	}
	f.ready = true
	fmt.Fprint(w, "segment bytes")
}

func TestRelayFirstPlaylistWaitsForMediaAndPropagatesFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		h := &startingLiveRelay{fail: fail}
		ss := &session{info: SessionInfo{ID: "live-fixture"}, relay: h}
		s := &Service{}
		rec := httptest.NewRecorder()
		s.serveRelayPlaylist(rec, httptest.NewRequest("GET", "/", nil), ss)
		if h.waits != 1 {
			t.Fatalf("initial segment waits = %d", h.waits)
		}
		if fail {
			if rec.Code != 502 {
				t.Fatalf("encoder failure returned %d", rec.Code)
			}
			continue
		}
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/relay/0.ts") || strings.Contains(rec.Body.String(), "segment bytes") {
			t.Fatalf("first playlist unusable: %d %s", rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		s.serveRelayPlaylist(rec, httptest.NewRequest("GET", "/", nil), ss)
		if h.waits != 1 {
			t.Fatal("ready playlist reread a complete segment")
		}
	}
}
