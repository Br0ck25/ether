package transcode

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// Explicit lab opt-in. Ordinary go test remains offline and never provisions
// binaries or starts an RTX encode. The executable package is owner-installed.
func TestRTXNativeLifecycle(t *testing.T) {
	nv := os.Getenv("ETHER_RTX_TEST_EXECUTABLE")
	ff := os.Getenv("ETHER_RTX_TEST_FFMPEG")
	if runtime.GOOS != "windows" || nv == "" || ff == "" {
		t.Skip("native Windows RTX lab test requires explicit executable paths")
	}
	fp := filepath.Join(filepath.Dir(ff), "ffprobe.exe")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	t.Cleanup(func() {
		if err := removeAllRetry(dir); err != nil {
			t.Errorf("lab cleanup: %v", err)
		}
	})
	input := filepath.Join(dir, "source.mp4")
	rate := "24"
	if os.Getenv("ETHER_RTX_TEST_FRACTIONAL") == "1" {
		rate = "24000/1001"
	}
	args := []string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=" + rate, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-t", "20", "-c:v", "h264_nvenc", "-g", "72", "-bf", "0", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", input}
	if out, err := exec.CommandContext(ctx, ff, args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	quality, _ := strconv.Atoi(os.Getenv("ETHER_RTX_TEST_QUALITY"))
	m, err := New(config.Transcode{FFmpegPath: ff, FFprobePath: fp, TempDir: filepath.Join(dir, "sessions"), RTXVideo: config.RTXVideo{Mode: "on", ExecutablePath: nv, Quality: quality}}, testLogger(t), WithoutHWDetection(), WithBuffering(24*time.Second, 0, 0), WithSegmentTimeout(60*time.Second), withPollInterval(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if support := m.EnhancementStatus(); !support.Available {
		t.Fatalf("detection: %+v", support)
	}
	info, err := m.Probe(ctx, srv.URL+"/source.mp4")
	if err != nil {
		t.Fatal(err)
	}
	// Fixture has exact three-second source GOPs; scan actual packets rather
	// than assuming the encoder honored that request.
	packetArgs := []string{"-v", "error", "-select_streams", "v:0", "-show_packets", "-show_entries", "packet=pts_time,flags", "-of", "json", input}
	out, err := exec.CommandContext(ctx, fp, packetArgs...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var packets struct {
		Packets []struct {
			PTS   string `json:"pts_time"`
			Flags string `json:"flags"`
		}
	}
	if err := json.Unmarshal(out, &packets); err != nil {
		t.Fatal(err)
	}
	for _, p := range packets.Packets {
		if strings.Contains(p.Flags, "K") {
			f, _ := strconv.ParseFloat(p.PTS, 64)
			info.Keyframes = append(info.Keyframes, core.KeyframePoint{Time: time.Duration(f * float64(time.Second))})
		}
	}
	_, caps, opts := rtxFixture()
	opts.AudioStream = 2
	for _, proto := range []string{core.ProtocolHLS, "hls-fmp4", core.ProtocolDASH} {
		t.Run(proto, func(t *testing.T) {
			d := m.Decide(*info, caps, opts)
			if !enhancementSelected(d) {
				t.Fatalf("selection %+v", d)
			}
			transport := proto
			if proto == "hls-fmp4" {
				transport = core.ProtocolHLS
				d.Container = ContainerFMP4
			}
			req := core.HLSRequest{SessionID: proto, InputURL: srv.URL + "/source.mp4", Info: *info, Decision: d, Protocol: transport, SegmentDuration: 4 * time.Second}
			hs, err := m.StartHLS(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			s := hs.(*Session)
			defer s.Close()
			if proto == core.ProtocolDASH {
				rec := httptest.NewRecorder()
				s.ServeManifest(rec, httptest.NewRequest("GET", "/manifest.mpd", nil))
				if rec.Code != 200 {
					t.Fatalf("MPD %d %s", rec.Code, rec.Body.String())
				}
			}
			waitNative := func(seg int) {
				t.Helper()
				deadline := time.Now().Add(60 * time.Second)
				for time.Now().Before(deadline) {
					s.mu.Lock()
					done := s.done[0][seg]
					cur := s.cur
					confirmed := cur != nil && cur.rtx && cur.rtxConfirmed.Load()
					d := s.decision
					s.mu.Unlock()
					if d.Enhancement.State == "fallback" {
						t.Fatalf("native fallback: %+v", d)
					}
					if done && confirmed {
						return
					}
					time.Sleep(30 * time.Millisecond)
				}
				t.Fatal("native segment timed out")
			}
			waitNative(0)
			s.mu.Lock()
			first := s.trackSegPath(0, 0)
			init := s.initFile
			if s.dash != nil {
				init = ""
			}
			s.mu.Unlock()
			checkNativeSegment(t, ctx, fp, dir, s.dir, first, init, 0)
			if proto == core.ProtocolHLS {
				checkNativeAudio(t, ctx, ff, first)
			}
			if rate != "24" {
				return
			}
			// 8s is not a source keyframe: this exercises seek to 6s + 48 frames
			// trimmed, and absolute mux timestamp restoration.
			if err := s.SwapInput(ctx, req.InputURL, *info, 8*time.Second); err != nil {
				t.Fatal(err)
			}
			waitNative(2)
			s.mu.Lock()
			second := s.trackSegPath(0, 2)
			init = s.initFile
			if s.dash != nil {
				init = ""
			}
			s.mu.Unlock()
			checkNativeSegment(t, ctx, fp, dir, s.dir, second, init, 8)
			if proto == core.ProtocolHLS {
				checkNativeAudio(t, ctx, ff, second)
			}
			t.Logf("native %s: genuine NGX output, 1920x1080, indexed seek/trim at 8s, exact GOP boundary", proto)
		})
	}
}
func checkNativeSegment(t *testing.T, ctx context.Context, probe, dir, sessionDir, path, init string, start float64) {
	t.Helper()
	if init != "" {
		initPath := init
		if !filepath.IsAbs(initPath) {
			initPath = filepath.Join(sessionDir, init)
		}
		b, e := os.ReadFile(initPath)
		if e != nil {
			t.Fatal(e)
		}
		p, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		path = filepath.Join(dir, "inspect-"+strconv.FormatFloat(start, 'f', 0, 64)+".mp4")
		if e := os.WriteFile(path, append(b, p...), 0600); e != nil {
			t.Fatal(e)
		}
	}
	args := []string{"-v", "error", "-select_streams", "v:0", "-show_streams", "-show_packets", "-show_entries", "stream=width,height:packet=pts_time,flags", "-of", "json", path}
	out, err := exec.CommandContext(ctx, probe, args...).Output()
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	var p struct {
		Streams []struct{ Width, Height int }
		Packets []struct {
			PTS   string `json:"pts_time"`
			Flags string `json:"flags"`
		}
	}
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Streams) != 1 || p.Streams[0].Width != 1920 || p.Streams[0].Height != 1080 || len(p.Packets) == 0 {
		t.Fatalf("bad enhanced segment: %s", out)
	}
	pts, _ := strconv.ParseFloat(p.Packets[0].PTS, 64)
	if math.Abs(pts-start) > 1.0/24+0.002 || !strings.Contains(p.Packets[0].Flags, "K") {
		t.Fatalf("boundary pts=%f want=%f flags=%s", pts, start, p.Packets[0].Flags)
	}
	last, _ := strconv.ParseFloat(p.Packets[len(p.Packets)-1].PTS, 64)
	if math.Abs(last+1.0/24-start-4) > 0.006 {
		t.Fatalf("GOP duration mismatch start=%f last=%f", pts, last)
	}
}

// Two known fixture tones establish selected-track preservation through a seek.
func checkNativeAudio(t *testing.T, ctx context.Context, ff, path string) {
	t.Helper()
	out, err := exec.CommandContext(ctx, ff, "-v", "error", "-i", path, "-map", "0:a:0", "-t", "0.5", "-ac", "1", "-ar", "8000", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	crossings := 0
	prev := int16(0)
	for i := 0; i+1 < len(out); i += 2 {
		v := int16(binary.LittleEndian.Uint16(out[i : i+2]))
		if v > 0 && prev <= 0 {
			crossings++
		}
		prev = v
	}
	if crossings < 400 || crossings > 480 {
		t.Fatalf("wrong audio track: %d positive crossings, expected 880Hz selected track", crossings)
	}
}
