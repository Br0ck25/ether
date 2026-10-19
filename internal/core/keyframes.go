package core

import "time"

// KeyframePoint ties a byte offset in a media file to the presentation time
// of the sync sample (keyframe) or cue point that starts there, as read from
// the container index (Matroska Cues, MP4 stss with stco/co64) or a packet
// scan. MediaInfo.Keyframes holds them sorted by Offset and Time.
type KeyframePoint struct {
	Offset int64         `json:"offset"`
	Time   time.Duration `json:"time"`
}
