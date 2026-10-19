package transcode

import "github.com/braedonsaunders/ether/internal/core"

// MPEG-2 broadcast A53 captions can trigger VideoToolbox's FFmpeg SEI rewrite
// bug: encoding exits successfully but produces undecodable H.264. Disable
// that caption injection for this conversion only. Software encoders, copied
// streams and other input codecs retain their normal caption behavior.
// This path temporarily omits embedded A53 captions; video/audio stay on HW.
func videotoolboxCaptionArgs(video *core.StreamInfo, encodeArgs []string) []string {
	if video == nil || video.Codec != "mpeg2video" {
		return nil
	}
	for i := 0; i+1 < len(encodeArgs); i++ {
		if encodeArgs[i] == "-c:v" && encodeArgs[i+1] == "h264_videotoolbox" {
			return []string{"-a53cc", "0"}
		}
	}
	return nil
}
