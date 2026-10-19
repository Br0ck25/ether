package transcode

import (
	"github.com/braedonsaunders/ether/internal/core"
	"path/filepath"
	"strconv"
	"time"
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

// The segment muxer closes each independent Matroska file before publishing
// it in its M3U8 list. Each file contains codec headers, without an MP4 map.
func matroskaHLSMuxArgs(s runSpec, segDur time.Duration) []string {
	return []string{
		"-avoid_negative_ts", "disabled", "-max_muxing_queue_size", "4096",
		"-f", "segment", "-segment_format", "matroska",
		"-segment_time", secs(segDur), "-segment_start_number", strconv.Itoa(s.StartSeg),
		"-segment_list_type", "m3u8", "-segment_list_size", "0",
		"-segment_list", filepath.Join(s.Dir, s.Playlist), filepath.Join(s.Dir, "%05d.mkv"),
	}
}
