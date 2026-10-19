package livetv

import "github.com/braedonsaunders/ether/internal/core"

// The live relay exposes MPEG-TS segments. Constrain its decision to codecs
// that transport supports, instead of putting HEVC/fMP4 or Matroska in .ts URLs.
func (s *Service) startIPTVRelay(ss *session, info core.MediaInfo) error {
	caps := ss.caps
	caps.VideoCodecs = []string{"h264"}
	caps.HDR10, caps.DolbyVision = false, false
	caps.HLSContainers = nil
	caps.AudioCodecs = nil
	for _, codec := range ss.caps.AudioCodecs {
		switch codec {
		case "aac", "ac3", "eac3", "mp3":
			caps.AudioCodecs = append(caps.AudioCodecs, codec)
		}
	}
	if len(caps.AudioCodecs) == 0 {
		caps.AudioCodecs = []string{"aac"}
	}
	dec := s.o.Transcoder.Decide(info, caps, core.DecideOptions{DirectStream: !ss.info.Channel.Transcode, AudioStream: -1, SubtitleStream: -1})
	dec.Container = "mpegts"
	relay, err := s.o.Transcoder.StartHLS(s.ctx, core.HLSRequest{SessionID: "livetv-" + ss.info.ID, InputURL: ss.info.Channel.StreamURL, Info: info, Decision: dec, SegmentDuration: segmentDuration})
	if err != nil {
		return err
	}
	ss.relay = relay
	output := liveRelayInfo(info, dec)
	ss.relayInfo = &output
	return nil
}

// Tune/MDE describe what the viewer receives. Keep the original probe separately
// in ss.info for any subsequent source-based decision (including DASH).
func liveRelayInfo(source core.MediaInfo, dec core.Decision) core.MediaInfo {
	out := core.MediaInfo{Container: dec.Container, BitrateKbps: dec.BitrateKbps}
	if v := source.Video(); v != nil {
		video := *v
		video.Index = 0
		if !dec.VideoCopy {
			video.Codec = dec.VideoCodec
			video.CodecString, video.Profile, video.Level = "", "", 0
			video.DolbyVision = false
			video.DolbyVisionProfile, video.DolbyVisionLevel, video.DolbyVisionCompatibilityID = 0, 0, 0
			video.BitrateKbps = 0
			if dec.MaxHeight > 0 && video.Height > dec.MaxHeight {
				video.Width = video.Width * dec.MaxHeight / video.Height
				video.Width -= video.Width % 2
				video.Height = dec.MaxHeight
			}
			if video.Codec == "h264" || dec.ToneMap {
				video.BitDepth = 8
				video.ColorTransfer = "bt709"
			}
		}
		out.Streams = append(out.Streams, video)
	}
	for _, stream := range source.Streams {
		if stream.Type != "audio" || stream.Index != dec.AudioStream {
			continue
		}
		stream.Index = len(out.Streams)
		stream.Default = true
		if !dec.AudioCopy {
			stream.Codec = dec.AudioCodec
			stream.CodecString, stream.Profile, stream.ChannelLayout = "", "", ""
			stream.Level, stream.BitrateKbps = 0, 0
			if stream.Channels <= 0 {
				stream.Channels = 2
			}
			limit := 2
			if stream.Codec == "ac3" || stream.Codec == "eac3" {
				limit = 6
			}
			stream.Channels = min(stream.Channels, limit)
		}
		out.Streams = append(out.Streams, stream)
		break
	}
	return out
}
