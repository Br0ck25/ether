package transcode

import (
	"path/filepath"
	"strconv"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// HLSContainer preserves explicit client targets separately from the formats
// its direct-play decoder accepts. Plex's enhanced player requests segmented
// Matroska; AVPlayer and browser HLS keep the standard TS/fMP4 path.
func HLSContainer(caps core.ClientCaps, videoCodec, audioCodec string) string {
	for _, c := range caps.HLSContainers {
		switch c {
		case ContainerMatroska:
			return ContainerMatroska
		case "mp4", ContainerFMP4:
			return ContainerFMP4
		}
	}
	return segmentContainer(videoCodec, audioCodec)
}

// Plex's enhanced player joins an initialization header and Matroska clusters.
// Repeating complete MKVs corrupts its virtual stream's Segment/SeekHead offsets.
// Use the stream segment muxer's separate header, continuous timestamps and
// cluster-only fragments; the playlist advertises the header with EXT-X-MAP.
func matroskaHLSMuxArgs(s runSpec, segDur time.Duration) []string {
	start, pattern := s.StartSeg, filepath.Join(s.Dir, "%05d.mkv")
	if s.ChunkDir != "" {
		segDur, start, pattern = s.chunkCut(), 0, filepath.Join(s.ChunkDir, "%05d.mkv")
	}
	args := []string{
		"-avoid_negative_ts", "disabled", "-max_muxing_queue_size", "4096",
		"-f", "ssegment", "-segment_format", "matroska", "-flags", "+global_header",
		"-individual_header_trailer", "0", "-reset_timestamps", "0",
		// PMS uses a positive timestamp origin for non-TS segmented HLS.
		// Preserve that origin across startup, seeks and release swaps.
		"-segment_header_filename", filepath.Join(s.Dir, s.InitName),
		"-segment_time", secs(segDur), "-segment_start_number", strconv.Itoa(start),
		"-segment_list_type", "m3u8", "-segment_list_size", "0",
		"-segment_list", filepath.Join(s.Dir, s.Playlist),
	}
	if s.jellyfinMatroskaChunks() {
		// Jellyfin's segment-muxer patch counts the first PTS twice in
		// its cut threshold. A negative first PTS also overflows its
		// saturation check, preventing every subsequent cut after a seek.
		// Keep the title clock positive before the outer muxer sees it.
		// The delta allows a cut at every keyframe even with the doubled
		// origin; collectChunks regroups those GOPs onto the actual plan.
		// Never cut within a GOP. Upstream FFmpeg uses the relative
		// clock below: it lacks Jellyfin's first-entry duration fix.
		args = append(args, "-output_ts_offset", secs(matroskaOrigin),
			"-segment_time_delta", secs(s.Start+matroskaOrigin))
	} else {
		args = append(args, "-segment_format_options", "output_ts_offset="+secs(s.Start+matroskaOrigin))
	}
	return append(args, pattern)
}

// matroskaOrigin is added to media time in segmented Matroska output.
const matroskaOrigin = 10 * time.Second

// Jellyfin also fixes the first playlist entry's duration for nonzero
// timestamps. Unpatched FFmpeg counts that entry from zero, so it must
// retain the run-relative outer clock instead.
func (s runSpec) jellyfinMatroskaChunks() bool {
	return s.ChunkDir != "" && s.Features.jellyfinSegmentClock
}
