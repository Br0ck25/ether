package jellyfin

import (
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

// deviceProfile is the part of an app's DeviceProfile that decides how a
// title is played: what it can play directly, how it wants transcodes
// segmented, and which limits (height, HDR) it declares.
type deviceProfile struct {
	MaxStreamingBitrate int `json:"MaxStreamingBitrate"`
	MaxStaticBitrate    int `json:"MaxStaticBitrate"`

	DirectPlayProfiles []struct {
		Container  string `json:"Container"`
		AudioCodec string `json:"AudioCodec"`
		VideoCodec string `json:"VideoCodec"`
		Type       string `json:"Type"`
	} `json:"DirectPlayProfiles"`
	TranscodingProfiles []struct {
		Container string `json:"Container"`
		Type      string `json:"Type"`
		Protocol  string `json:"Protocol"`
	} `json:"TranscodingProfiles"`
	CodecProfiles []struct {
		Type       string `json:"Type"`
		Codec      string `json:"Codec"`
		Conditions []struct {
			Condition string `json:"Condition"`
			Property  string `json:"Property"`
			Value     string `json:"Value"`
		} `json:"Conditions"`
	} `json:"CodecProfiles"`
	SubtitleProfiles []struct {
		Format string `json:"Format"`
		Method string `json:"Method"`
	} `json:"SubtitleProfiles"`
}

// Defaults for an app that sends no profile (a plain player following a
// stream URL): the broad set nearly every current player decodes.
var defaultCaps = core.ClientCaps{
	MaxHeight:   2160,
	VideoCodecs: []string{"h264", "hevc", "av1", "vp9"},
	AudioCodecs: []string{"aac", "ac3", "eac3", "mp3", "opus", "flac", "vorbis"},
	Containers:  []string{"mkv", "mp4", "mpegts", "webm"},
	HDR10:       true,
}

func containerName(c string) []string {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "matroska", "mkv":
		return []string{"mkv"}
	case "ts", "mpegts":
		return []string{"mpegts"}
	case "m4v", "mov", "mp4":
		return []string{"mp4"}
	case "":
		return nil
	}
	return []string{strings.ToLower(strings.TrimSpace(c))}
}

func csv(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func appendUnique(dst []string, vs ...string) []string {
	for _, v := range vs {
		found := false
		for _, d := range dst {
			if d == v {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, v)
		}
	}
	return dst
}

// capsFor describes the client for the resolver and transcoder. A profile
// narrows the defaults to what the app declared.
func capsFor(sess *session, prof *deviceProfile, maxKbps int) core.ClientCaps {
	c := defaultCaps
	c.VideoCodecs = append([]string(nil), defaultCaps.VideoCodecs...)
	c.AudioCodecs = append([]string(nil), defaultCaps.AudioCodecs...)
	c.Containers = append([]string(nil), defaultCaps.Containers...)
	c.Product, c.Platform = sess.product(), "Jellyfin"
	c.Device, c.DeviceName, c.ClientID, c.Version = sess.Device, sess.Device, sess.DeviceID, sess.Version
	c.Remote = sess.Remote
	if prof != nil {
		applyProfile(&c, prof)
	}
	if prof != nil && prof.MaxStreamingBitrate > 0 && (maxKbps == 0 || prof.MaxStreamingBitrate/1000 < maxKbps) {
		maxKbps = prof.MaxStreamingBitrate / 1000
	}
	c.MaxBitrateKbps = maxKbps
	return c
}

func applyProfile(c *core.ClientCaps, p *deviceProfile) {
	var containers, video, audio []string
	allVideo, allAudio := false, false
	for _, dp := range p.DirectPlayProfiles {
		if !strings.EqualFold(dp.Type, "Video") {
			continue
		}
		for _, ct := range csv(dp.Container) {
			containers = appendUnique(containers, containerName(ct)...)
		}
		if dp.VideoCodec == "" {
			allVideo = true
		}
		if dp.AudioCodec == "" {
			allAudio = true
		}
		video = appendUnique(video, csv(dp.VideoCodec)...)
		audio = appendUnique(audio, csv(dp.AudioCodec)...)
	}
	if len(containers) > 0 {
		c.Containers = containers
	}
	if len(video) > 0 && !allVideo {
		c.VideoCodecs = video
	}
	if len(audio) > 0 && !allAudio {
		c.AudioCodecs = audio
	}
	for _, tp := range p.TranscodingProfiles {
		if !strings.EqualFold(tp.Type, "Video") || !strings.EqualFold(tp.Protocol, "hls") {
			continue
		}
		switch strings.ToLower(tp.Container) {
		case "ts", "mpegts":
			c.HLSContainers = appendUnique(c.HLSContainers, transcode.ContainerMPEGTS)
		case "mp4", "fmp4", "m4s":
			c.HLSContainers = appendUnique(c.HLSContainers, transcode.ContainerFMP4)
		}
	}
	// Declared limits. Without a VideoRangeType condition an app is taken
	// to decode every range of the codecs it lists.
	c.HDR10, c.DolbyVision = containsStr(c.VideoCodecs, "hevc"), false
	for _, cp := range p.CodecProfiles {
		if !strings.EqualFold(cp.Type, "Video") {
			continue
		}
		for _, cond := range cp.Conditions {
			switch strings.ToLower(cond.Property) {
			case "height":
				if n, err := strconv.Atoi(cond.Value); err == nil && n > 0 &&
					(strings.EqualFold(cond.Condition, "LessThanEqual") || strings.EqualFold(cond.Condition, "Equals")) &&
					(c.MaxHeight == 0 || n < c.MaxHeight || c.MaxHeight == defaultCaps.MaxHeight) {
					c.MaxHeight = n
				}
			case "videorangetype":
				v := strings.ToLower(cond.Value)
				if strings.EqualFold(cond.Condition, "EqualsAny") || strings.EqualFold(cond.Condition, "Equals") {
					c.HDR10 = strings.Contains(v, "hdr10")
					c.DolbyVision = strings.Contains(v, "dovi")
				}
			}
		}
	}
	for _, sp := range p.SubtitleProfiles {
		if m := strings.ToLower(sp.Method); m == "external" || m == "embed" || m == "hls" {
			c.SubtitleCodecs = appendUnique(c.SubtitleCodecs, strings.ToLower(sp.Format))
		}
	}
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
