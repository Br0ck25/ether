package transcode

import (
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestNativeImageSubtitlesAvoidBurn(t *testing.T) {
	info := mustParse(t, "mkv_pgs.json")
	o := allow()
	o.SubtitleStream = 2
	c := appleTV
	c.SubtitleCodecs = []string{"pgs"}
	d := Decide(info, c, o, Policy{})
	if d.BurnSubtitles || !d.VideoCopy || d.Kind != core.DecisionDirectPlay {
		t.Fatalf("native PGS %+v", d)
	}
}
func TestAudioProcessingCopiesVideo(t *testing.T) {
	info := mustParse(t, "mp4_h264_aac.json")
	o := allow()
	o.AudioFilters = []string{"loudnorm=I=-16:TP=-1.5:LRA=11"}
	d := Decide(info, appleTV, o, Policy{})
	if !d.VideoCopy || d.AudioCopy || d.Kind != core.DecisionRemux || !strings.Contains(d.Reason, "audio processing") {
		t.Fatalf("processing %+v", d)
	}
	args := buildArgs(baseSpec(info, d))
	if argValue(args, "-af") != o.AudioFilters[0] || argValue(args, "-c:v") != "copy" || argValue(args, "-c:a") == "copy" {
		t.Fatalf("bad filter args %v", args)
	}
}
func TestExplicitVideoProfileAndLevel(t *testing.T) {
	info := mustParse(t, "mp4_h264_aac.json")
	c := appleTV
	c.VideoLevels = map[string]int{"h264": 31}
	d := Decide(info, c, allow(), Policy{})
	if d.VideoCopy || !strings.Contains(d.Reason, "video level") {
		t.Fatalf("level ignored %+v", d)
	}
	c.VideoLevels = nil
	c.VideoProfiles = map[string][]string{"h264": {"baseline"}}
	d = Decide(info, c, allow(), Policy{})
	if d.VideoCopy || !strings.Contains(d.Reason, "video profile") {
		t.Fatalf("profile ignored %+v", d)
	}
}
func TestProbeChaptersRejectsNonFiniteTimes(t *testing.T) {
	m, err := ParseProbe([]byte(`{"format":{"format_name":"matroska","duration":"60"},"chapters":[{"id":1,"start_time":"0","end_time":"10.5","tags":{"title":"Intro"}},{"id":2,"start_time":"NaN","end_time":"20"},{"id":3,"start_time":"20","end_time":"+Inf"},{"id":4,"start_time":"30","end_time":"20"}]}`))
	if err != nil || len(m.Chapters) != 1 || m.Chapters[0].End != 10500*time.Millisecond || m.Chapters[0].Title != "Intro" {
		t.Fatalf("chapters %+v %v", m, err)
	}
}
func TestDevicePolicyFamilies(t *testing.T) {
	for _, family := range []string{"Apple TV", "iOS", "Android TV", "Google TV", "Roku", "Fire TV", "LG webOS", "Samsung Tizen", "Xbox", "PlayStation", "Plex Web Chrome", "Plex Web Firefox", "Plex Web Safari"} {
		p := ClientPolicyFor(core.ClientCaps{Product: family})
		if p.Family == "generic" || len(p.SubtitleCodecs) == 0 {
			t.Errorf("missing policy for %s", family)
		}
	}
}
