package transcode

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultSegmentDuration is the HLS segment length when a request leaves it
// unset.
const DefaultSegmentDuration = 4 * time.Second

// InitSegment is the segment number ServeSegment uses for the fMP4
// initialization segment (init.mp4).
const InitSegment = -1

// InitSegmentName is the init segment's URI in media playlists.
const InitSegmentName = "init.mp4"

// segmentCount is how many segments cover a span. A remainder shorter than
// a quarter segment is folded into the last segment: ffmpeg only cuts at
// hls_time boundaries, so it never produces such a sliver on its own.
func segmentCount(span, segDur time.Duration) int {
	if span <= 0 || segDur <= 0 {
		return 0
	}
	n := int(span / segDur)
	if rem := span % segDur; n == 0 || rem >= segDur/4 {
		n++
	}
	return n
}

// segmentLength is the playlist duration of segment i of n.
func segmentLength(i, n int, span, segDur time.Duration) time.Duration {
	if i < n-1 {
		return segDur
	}
	if rem := span - time.Duration(i)*segDur; rem > 0 {
		return rem
	}
	return segDur
}

// mediaPlaylist renders the full-length VOD playlist for a span, so clients
// can seek anywhere before ffmpeg has produced a thing.
func mediaPlaylist(span, segDur time.Duration, container string, name func(n int) string) []byte {
	var b bytes.Buffer
	n := segmentCount(span, segDur)
	version := 3
	if container == ContainerFMP4 {
		version = 7
	}
	target := int(math.Ceil(segDur.Seconds()))
	if n > 0 {
		if last := int(math.Round(segmentLength(n-1, n, span, segDur).Seconds())); last > target {
			target = last
		}
	}
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", version)
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", target)
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	if container == ContainerFMP4 {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", InitSegmentName)
	}
	for i := 0; i < n; i++ {
		d := segmentLength(i, n, span, segDur)
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", d.Seconds(), name(i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.Bytes()
}

// eventPlaylist lists the contiguous completed segments from 0, for sources
// with unknown duration. ended adds ENDLIST.
func eventPlaylist(done []int, segDur time.Duration, container string, ended bool, name func(n int) string) []byte {
	var b bytes.Buffer
	version := 3
	if container == ContainerFMP4 {
		version = 7
	}
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", version)
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(segDur.Seconds())))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	if container == ContainerFMP4 {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", InitSegmentName)
	}
	for i, n := range done {
		if n != i {
			break
		}
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", segDur.Seconds(), name(n))
	}
	if ended {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.Bytes()
}

// planPlaylist renders a Timeline as a media playlist: exact per-segment
// #EXTINF durations, the timeline's target duration, and a
// #EXT-X-DISCONTINUITY (plus a fresh #EXT-X-MAP for fMP4) wherever discont
// says the encode parameters change. With event set it renders an EVENT
// playlist of the full timeline without ENDLIST until ended, so clients
// that reload it pick up re-planned segments ahead of the playhead; with
// total==0 timelines only the first limit segments are listed.
func planPlaylist(tl *Timeline, container string, name func(n int) string, discont func(n int) bool, event, ended bool, limit int) []byte {
	var b bytes.Buffer
	version := 3
	if container == ContainerFMP4 {
		version = 7
	}
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", version)
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(tl.TargetDuration().Seconds()))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	if event {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	} else {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
		b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	}
	if container == ContainerFMP4 {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", InitSegmentName)
	}
	n := tl.Total()
	if n == 0 {
		n = limit
	}
	for i := 0; i < n; i++ {
		if i > 0 && discont != nil && discont(i) {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
			if container == ContainerFMP4 {
				fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", InitSegmentName)
			}
		}
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", tl.Dur(i).Seconds(), name(i))
	}
	if !event || ended {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.Bytes()
}

// SegmentName is the default playlist URI of segment n: "00042.ts" or
// "00042.m4s" or "00042.mkv" (relative to index.m3u8).
func SegmentName(n int, container string) string {
	return segmentFile(n, segmentExt(container))
}

// ParseSegmentName parses a segment URI produced by SegmentName (or
// InitSegmentName) back to its number; init.mp4 yields InitSegment.
func ParseSegmentName(name string) (int, bool) {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if name == InitSegmentName {
		return InitSegment, true
	}
	base, ext, ok := strings.Cut(name, ".")
	if !ok || (ext != "ts" && ext != "m4s" && ext != "mkv") || base == "" {
		return 0, false
	}
	n, err := strconv.Atoi(base)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// ffmpegPlaylist is what an ffmpeg run's own (EVENT) playlist says.
type ffmpegPlaylist struct {
	segments []int // completed segment numbers, in order
	ended    bool
}

// readFFmpegPlaylist parses ffmpeg's playlist. Only segments listed there
// are complete (ffmpeg writes them with temp_file and lists them after the
// rename).
func readFFmpegPlaylist(path string) (ffmpegPlaylist, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ffmpegPlaylist{}, err
	}
	return parseFFmpegPlaylist(b), nil
}

func parseFFmpegPlaylist(b []byte) ffmpegPlaylist {
	var p ffmpegPlaylist
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case line == "#EXT-X-ENDLIST":
			p.ended = true
		case strings.HasPrefix(line, "#"):
		default:
			if n, ok := ParseSegmentName(line); ok && n >= 0 {
				p.segments = append(p.segments, n)
			}
		}
	}
	return p
}
