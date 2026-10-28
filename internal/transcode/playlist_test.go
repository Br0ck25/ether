package transcode

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMediaPlaylistTS(t *testing.T) {
	name := func(n int) string { return SegmentName(n, ContainerMPEGTS) }
	got := string(mediaPlaylist(10*time.Second+500*time.Millisecond, 4*time.Second, ContainerMPEGTS, name))
	want := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-INDEPENDENT-SEGMENTS
#EXTINF:4.000000,
00000.ts
#EXTINF:4.000000,
00001.ts
#EXTINF:2.500000,
00002.ts
#EXT-X-ENDLIST
`
	if got != want {
		t.Errorf("playlist:\n%s\nwant:\n%s", got, want)
	}
}

func TestMediaPlaylistFMP4(t *testing.T) {
	name := func(n int) string {
		return "/video/:/transcode/universal/session/x/base/" + SegmentName(n, ContainerFMP4)
	}
	got := string(mediaPlaylist(2*time.Hour, 4*time.Second, ContainerFMP4, name))
	for _, want := range []string{"#EXT-X-VERSION:7", `#EXT-X-MAP:URI="init.mp4"`, "/base/00000.m4s", "/base/01799.m4s", "#EXT-X-ENDLIST"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "01800.m4s") {
		t.Error("one segment too many")
	}
	if n := strings.Count(got, "#EXTINF:4.000000,"); n != 1800 {
		t.Errorf("EXTINF count %d", n)
	}
	// A folded remainder lengthens the last segment and the target duration.
	got = string(mediaPlaylist(8*time.Second+900*time.Millisecond, 4*time.Second, ContainerMPEGTS, name))
	if !strings.Contains(got, "#EXTINF:4.900000,\n/video") || !strings.Contains(got, "#EXT-X-TARGETDURATION:5") {
		t.Errorf("folded playlist:\n%s", got)
	}
}

func TestSegmentCount(t *testing.T) {
	cases := []struct {
		span, seg time.Duration
		want      int
	}{
		{8 * time.Second, 4 * time.Second, 2},
		{8*time.Second + time.Millisecond/2, 4 * time.Second, 2},   // rounding noise
		{8*time.Second + 900*time.Millisecond, 4 * time.Second, 2}, // sliver folded into the last
		{9 * time.Second, 4 * time.Second, 3},
		{time.Second, 4 * time.Second, 1},
		{0, 4 * time.Second, 0},
	}
	for _, c := range cases {
		if got := segmentCount(c.span, c.seg); got != c.want {
			t.Errorf("segmentCount(%v,%v) = %d, want %d", c.span, c.seg, got, c.want)
		}
	}
}

func TestParseSegmentName(t *testing.T) {
	cases := map[string]int{"00000.ts": 0, "00042.ts": 42, "01234.m4s": 1234, "init.mp4": InitSegment, "base/00007.ts": 7, "123456.ts": 123456}
	for in, want := range cases {
		got, ok := ParseSegmentName(in)
		if !ok || got != want {
			t.Errorf("ParseSegmentName(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"index.m3u8", "abc.ts", "-1.ts", "00001.mp4", ""} {
		if _, ok := ParseSegmentName(bad); ok {
			t.Errorf("ParseSegmentName(%q) should fail", bad)
		}
	}
}

func TestParseFFmpegPlaylist(t *testing.T) {
	p := parseFFmpegPlaylist([]byte(`#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:50
#EXT-X-PLAYLIST-TYPE:EVENT
#EXT-X-MAP:URI="init-3.mp4"
#EXTINF:4.000000,
00050.m4s
#EXTINF:4.000000,
00051.m4s
#EXT-X-ENDLIST
`))
	if !reflect.DeepEqual(p.segments, []int{50, 51}) || !p.ended {
		t.Errorf("got %+v", p)
	}
}

func TestEventPlaylist(t *testing.T) {
	name := func(n int) string { return SegmentName(n, ContainerMPEGTS) }
	got := string(eventPlaylist([]int{0, 1, 2, 5}, 4*time.Second, ContainerMPEGTS, false, name))
	if !strings.Contains(got, "00002.ts") || strings.Contains(got, "00005.ts") || strings.Contains(got, "ENDLIST") {
		t.Errorf("event playlist:\n%s", got)
	}
}
