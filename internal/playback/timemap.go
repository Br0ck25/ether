package playback

import (
	"math"
	"sort"
	"time"
)

// IndexPoint ties a byte offset in a media file to a presentation timestamp,
// e.g. a Matroska cue point or an MP4 sync sample. Points let TimeMap follow
// variable bitrate content instead of assuming a constant bitrate.
type IndexPoint struct {
	Offset int64
	Time   time.Duration
}

// TimeMap converts between byte offsets and timestamps inside one media file.
//
// Without Points it assumes a constant bitrate: offset/Size == time/Duration.
// With Points it interpolates linearly between the surrounding points, with
// (0, 0) and (Size, Duration) as implicit end anchors. See docs/failover.md.
type TimeMap struct {
	Size     int64
	Duration time.Duration
	// Points are optional index entries sorted by Offset (and Time).
	Points []IndexPoint
}

// Valid reports whether the map has enough information to convert.
func (m TimeMap) Valid() bool { return m.Size > 0 && m.Duration > 0 }

// anchors returns the interpolation points including the implicit ends,
// dropping entries that are out of range or not monotonic.
func (m TimeMap) anchors() []IndexPoint {
	pts := make([]IndexPoint, 0, len(m.Points)+2)
	pts = append(pts, IndexPoint{0, 0})
	for _, p := range m.Points {
		last := pts[len(pts)-1]
		if p.Offset <= last.Offset || p.Time < last.Time || p.Offset >= m.Size || p.Time >= m.Duration {
			continue
		}
		pts = append(pts, p)
	}
	return append(pts, IndexPoint{m.Size, m.Duration})
}

// TimeAt maps a byte offset to a timestamp, clamped to [0, Duration].
func (m TimeMap) TimeAt(off int64) time.Duration {
	if !m.Valid() || off <= 0 {
		return 0
	}
	if off >= m.Size {
		return m.Duration
	}
	pts := m.anchors()
	i := sort.Search(len(pts), func(i int) bool { return pts[i].Offset > off }) // first point after off
	a, b := pts[i-1], pts[i]
	frac := float64(off-a.Offset) / float64(b.Offset-a.Offset)
	return a.Time + time.Duration(math.Round(frac*float64(b.Time-a.Time)))
}

// OffsetAt maps a timestamp to a byte offset, clamped to [0, Size].
func (m TimeMap) OffsetAt(t time.Duration) int64 {
	if !m.Valid() || t <= 0 {
		return 0
	}
	if t >= m.Duration {
		return m.Size
	}
	pts := m.anchors()
	i := sort.Search(len(pts), func(i int) bool { return pts[i].Time > t })
	a, b := pts[i-1], pts[i]
	if b.Time == a.Time {
		return a.Offset
	}
	frac := float64(t-a.Time) / float64(b.Time-a.Time)
	return a.Offset + int64(math.Round(frac*float64(b.Offset-a.Offset)))
}

// palRatio is the runtime ratio between a film-rate (23.976 fps) release and
// a PAL-speedup (25 fps) release of the same cut.
const palRatio = 25.0 / (24000.0 / 1001.0)

// AlignTime maps a timestamp in a release of duration from to the matching
// timestamp in a release of duration to.
//
// Releases of the same cut share timestamps, so the time is kept as is. The
// one systematic difference that can be detected from durations alone is
// PAL speedup (25 fps vs 23.976 fps, a 4.3% runtime difference): then the
// time is scaled by the ratio. The result is clamped to [0, to] when to is
// known. Different cuts or intros cannot be detected this way; see
// docs/failover.md.
func AlignTime(at, from, to time.Duration) time.Duration {
	if at < 0 {
		at = 0
	}
	if from > 0 && to > 0 {
		r := float64(to) / float64(from)
		if near(r, palRatio) || near(r, 1/palRatio) {
			at = time.Duration(math.Round(float64(at) * r))
		}
	}
	if to > 0 && at > to {
		at = to
	}
	return at
}

func near(a, b float64) bool { return math.Abs(a-b)/b < 0.004 }

// MapOffset maps a byte offset in one release to the byte offset showing the
// same moment in another release: offset -> time (from), align, time ->
// offset (to).
func MapOffset(off int64, from, to TimeMap) int64 {
	return to.OffsetAt(AlignTime(from.TimeAt(off), from.Duration, to.Duration))
}

// tsPacket is the MPEG transport stream packet size.
const tsPacket = 188

// alignPacket rounds an offset down to a transport stream packet boundary so
// a demuxer splicing onto a new source resynchronises immediately.
func alignPacket(off int64) int64 { return off - off%tsPacket }
