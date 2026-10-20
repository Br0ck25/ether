package transcode

import (
	"context"
	"strings"
	"testing"
)

const liveProbeJSON = `{"format":{"format_name":"hls"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1280,"height":720},{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"sample_rate":"48000"}]}`

func TestLiveProbeDoesNotRetryForUnknownDuration(t *testing.T) {
	fr := newFakeRunner()
	fr.probeJSON = []string{liveProbeJSON}
	m := &Manager{runner: fr, ffprobe: "ffprobe"}
	info, err := m.ProbeLive(context.Background(), "https://fixture.invalid/live")
	if err != nil || info.Duration != 0 || info.Video() == nil || len(fr.probeCalls) != 1 {
		t.Fatalf("complete live stream retried: info=%+v err=%v calls=%d", info, err, len(fr.probeCalls))
	}
	fileRunner := newFakeRunner()
	fileRunner.probeJSON = []string{liveProbeJSON}
	if _, err := probe(context.Background(), fileRunner, "ffprobe", "https://fixture.invalid/file"); err != nil || len(fileRunner.probeCalls) != 2 {
		t.Fatal("ordinary file probing stopped requiring a duration")
	}
}

func TestLiveProbeStillRetriesMissingCodecParameters(t *testing.T) {
	fr := newFakeRunner()
	fr.probeJSON = []string{strings.Replace(liveProbeJSON, `"channels":2`, `"channels":0`, 1), liveProbeJSON}
	info, err := probeMode(context.Background(), fr, "ffprobe", "https://fixture.invalid/live", true)
	if err != nil || info.Streams[1].Channels != 2 || len(fr.probeCalls) != 2 {
		t.Fatalf("incomplete audio was accepted: info=%+v err=%v calls=%d", info, err, len(fr.probeCalls))
	}
}
