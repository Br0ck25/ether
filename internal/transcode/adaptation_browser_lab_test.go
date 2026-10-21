package transcode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// Explicitly opt in with a local input, hls.min.js path and URL metadata path.
// This fixture never contacts a provider or production service. The listener
// is ephemeral loopback, the encoder count is one, and lifetime is <=3min.
// Root owns the browser/CDP impairment and final playback observations.
func TestAdaptiveBrowserLab(t *testing.T) {
	input := os.Getenv("ETHER_ADAPTIVE_LAB_INPUT")
	if input == "" {
		t.Skip("opt-in local browser fixture")
	}
	script := os.Getenv("ETHER_ADAPTIVE_LAB_HLS_JS")
	ready := os.Getenv("ETHER_ADAPTIVE_LAB_READY")
	if script == "" || ready == "" {
		t.Fatal("set ETHER_ADAPTIVE_LAB_HLS_JS and ETHER_ADAPTIVE_LAB_READY")
	}
	for _, p := range []string{input, script} {
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			t.Fatalf("invalid local fixture: %s", p)
		}
	}
	e := newRealEnv(t, WithoutHWDetection(), WithBuffering(24*time.Second, 12*time.Second, 8*time.Second))
	e.m.UpdateConfig(config.Transcode{ABRLadder: true, VBVReservoir: true, MaxTranscodes: 1})
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, input) }))
	defer source.Close()
	info, err := e.m.Probe(t.Context(), source.URL+"/input.mkv")
	if err != nil {
		t.Fatal(err)
	}
	container := ContainerMPEGTS
	if os.Getenv("ETHER_ADAPTIVE_LAB_FMP4") == "1" {
		container = ContainerFMP4
	}
	hs, err := e.m.StartHLS(t.Context(), core.HLSRequest{SessionID: "adaptive-browser", InputURL: source.URL + "/input.mkv", Info: *info,
		Decision: core.Decision{Kind: core.DecisionTranscode, VideoCodec: "h264", AudioCodec: "aac", AudioStream: 1, AudioCopy: true, BitrateKbps: 4000, MaxHeight: 720, Container: container}, SegmentDuration: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer hs.Close()
	s := hs.(*Session)
	type trace struct {
		Path   string
		Millis int64
		Status int
		Bytes  int64
	}
	var mu sync.Mutex
	traces := make([]trace, 0, 512)
	events := make([]json.RawMessage, 0, 512)
	mux := http.NewServeMux()
	mux.HandleFunc("/hls.min.js", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, script) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, adaptiveBrowserHTML)
	})
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = fmt.Fprintln(w, "#EXTM3U")
		for _, v := range s.Rungs() {
			_, _ = fmt.Fprintf(w, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=%q\n/%s/index.m3u8\n", v.Bandwidth, v.AverageBandwidth, v.Width, v.Height, v.Codecs, v.ID)
		}
	})
	mux.HandleFunc("/{rung}/{name}", func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("rung"), r.PathValue("name")
		if name == "index.m3u8" {
			s.ServeRungPlaylist(w, r, id)
			return
		}
		n := InitSegment
		if name != initSegmentName(container) {
			var parseErr error
			n, parseErr = strconv.Atoi(strings.TrimSuffix(name, "."+segmentExt(container)))
			if parseErr != nil {
				http.NotFound(w, r)
				return
			}
		}
		s.ServeRungSegment(w, r, id, n)
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var event json.RawMessage
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&event) != nil {
			http.Error(w, "invalid event", 400)
			return
		}
		mu.Lock()
		if len(events) < 1000 {
			events = append(events, event)
		}
		mu.Unlock()
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		state := map[string]any{"runSeq": s.runSeq, "wantTrack": s.wantTrack, "deliveredSegment": s.deliveredSeg, "deliveredCount": s.deliveredCount, "goodputKbps": s.GoodputKbps(), "container": s.container}
		s.mu.Unlock()
		mu.Lock()
		state["requests"] = append([]trace(nil), traces...)
		state["events"] = append([]json.RawMessage(nil), events...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	})
	stopped := make(chan struct{})
	var stopOnce sync.Once
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			stopOnce.Do(func() { close(stopped) })
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		cw := &countingWriter{ResponseWriter: w}
		mux.ServeHTTP(cw, r)
		mu.Lock()
		if len(traces) < 1000 {
			traces = append(traces, trace{r.URL.Path, time.Since(start).Milliseconds(), cw.status, cw.bytes})
		}
		mu.Unlock()
	}))
	defer func() { _ = hs.Close(); srv.Close() }()
	metadata, _ := json.Marshal(map[string]any{"url": srv.URL, "input": filepath.Base(input), "rungs": s.Rungs(), "lifetimeSeconds": 180})
	if err := os.WriteFile(ready, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("ADAPTIVE_BROWSER_LAB %s", metadata)
	timer := time.NewTimer(180 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-stopped:
	case <-t.Context().Done():
	}
}

const adaptiveBrowserHTML = `<!doctype html><meta charset="utf-8"><title>Ether adaptive lab</title>
<video id="video" controls muted autoplay width="960"></video><pre id="status"></pre><script src="/hls.min.js"></script><script>
const video=document.querySelector('video');const records=[];
function record(kind,data={}){const b=video.buffered;const e={at:performance.now(),kind,time:video.currentTime,buffer:b.length?Math.max(0,b.end(b.length-1)-video.currentTime):0,...data};records.push(e);fetch('/events',{method:'POST',body:JSON.stringify(e)});document.querySelector('#status').textContent=JSON.stringify(e);}
const hls=new Hls({startLevel:0,maxBufferLength:12,maxMaxBufferLength:16});window.hls=hls;window.records=records;
for(const k of ['LEVEL_SWITCHING','LEVEL_SWITCHED','FRAG_CHANGED','FRAG_BUFFERED'])hls.on(Hls.Events[k],(_,d)=>record(k,{level:d.level??d.frag?.level,sn:d.frag?.sn}));
hls.on(Hls.Events.ERROR,(_,d)=>record('error',{type:d.type,details:d.details,fatal:d.fatal}));
for(const k of ['playing','waiting','stalled','ended','seeking','seeked'])video.addEventListener(k,()=>record(k));
setInterval(()=>record('sample'),1000);hls.loadSource('/master.m3u8');hls.attachMedia(video);hls.on(Hls.Events.MANIFEST_PARSED,()=>video.play().catch(e=>record('play-error',{message:e.message})));
</script>`
