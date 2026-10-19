package transcode

import (
	"slices"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// ClientPolicy records conservative product quirks, not device certification.
// Headers/profile declarations remain authoritative; model/display/audio chain
// capabilities must not be inferred from a platform name alone.
type ClientPolicy struct {
	Family              string   `json:"family"`
	DolbyVisionProfiles []int    `json:"dolbyVisionProfiles"`
	SubtitleCodecs      []string `json:"subtitleCodecs"`
	Notes               string   `json:"notes"`
}

func ClientPolicyFor(c core.ClientCaps) ClientPolicy {
	p := strings.ToLower(c.Product + " " + c.Platform + " " + c.Device)
	policy := ClientPolicy{Family: "generic", SubtitleCodecs: []string{"srt", "subrip", "webvtt", "vtt", "ass", "ssa"}, Notes: "Text is served separately; image subtitles require explicit client support."}
	switch {
	case strings.Contains(p, "apple tv"), strings.Contains(p, "tvos"):
		policy.Family = "Apple TV"
		policy.DolbyVisionProfiles = []int{5, 8}
		policy.Notes = "Dolby Vision profile 7 uses a compatible HDR base layer; TrueHD requires an audio transcode unless declared supported."
	case strings.Contains(p, "ios"), strings.Contains(p, "ipados"):
		policy.Family = "iOS"
		policy.DolbyVisionProfiles = []int{5, 8}
	case strings.Contains(p, "plex web"), strings.Contains(p, "safari"), strings.Contains(p, "chrome"), strings.Contains(p, "firefox"), strings.Contains(p, "edge"):
		policy.Family = "Plex Web"
		policy.Notes = "Browser codec declarations govern playback; text subtitles remain sidecars."
	case strings.Contains(p, "shield"):
		policy.Family = "Android TV (Shield)"
		policy.DolbyVisionProfiles = []int{5, 8}
		policy.SubtitleCodecs = append(policy.SubtitleCodecs, "pgs", "hdmv_pgs_subtitle", "vobsub", "dvd_subtitle")
	case strings.Contains(p, "android"), strings.Contains(p, "google tv"):
		policy.Family = "Android / Google TV"
	case strings.Contains(p, "fire tv"):
		policy.Family = "Fire TV"
	case strings.Contains(p, "roku"):
		policy.Family = "Roku"
	case strings.Contains(p, "webos"), strings.Contains(p, "lg"):
		policy.Family = "LG webOS"
	case strings.Contains(p, "tizen"), strings.Contains(p, "samsung"):
		policy.Family = "Samsung Tizen"
	case strings.Contains(p, "xbox"), strings.Contains(p, "playstation"):
		policy.Family = "Console"
	case strings.Contains(p, "htpc"), strings.Contains(p, "kodi"), strings.Contains(p, "infuse"):
		policy.Family = "HTPC"
		policy.DolbyVisionProfiles = []int{5, 7, 8}
		policy.SubtitleCodecs = append(policy.SubtitleCodecs, "pgs", "hdmv_pgs_subtitle", "vobsub", "dvd_subtitle")
	}
	if len(c.DVProfiles) > 0 {
		policy.DolbyVisionProfiles = slices.Clone(c.DVProfiles)
	}
	if c.SubtitleCodecs != nil {
		policy.SubtitleCodecs = slices.Clone(c.SubtitleCodecs)
	}
	return policy
}

// SupportsDVProfile requires both the generic DV flag and a compatible profile.
// With no model evidence an unspecified profile is accepted only if the client
// explicitly advertised DV; known unsupported profiles use their base layer.
func SupportsDVProfile(c core.ClientCaps, profile int) bool {
	if !c.DolbyVision {
		return false
	}
	if profile == 0 {
		return true
	}
	p := ClientPolicyFor(c)
	return slices.Contains(p.DolbyVisionProfiles, profile)
}

func supportsSubtitle(c core.ClientCaps, codec string) bool {
	return slices.Contains(ClientPolicyFor(c).SubtitleCodecs, codec)
}

// ClientPolicies is the owner-facing supplemental quirk matrix. It describes
// defaults only; a real request's profile and hardware declarations refine it.
func ClientPolicies() []ClientPolicy {
	products := []string{"Apple TV", "iOS", "Android TV", "Google TV", "NVIDIA Shield", "Roku", "Fire TV", "LG webOS", "Samsung Tizen", "Plex Web Chrome", "Plex Web Firefox", "Plex Web Safari", "Plex Web Edge", "Xbox", "PlayStation", "HTPC"}
	out := make([]ClientPolicy, 0, len(products))
	for _, product := range products {
		p := ClientPolicyFor(core.ClientCaps{Product: product})
		p.Family = product
		out = append(out, p)
	}
	return out
}
