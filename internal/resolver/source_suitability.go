package resolver

import (
	"strings"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// DiscoveryCandidate is local, name-based negative evidence only. It does not
// certify quality or fetch a provider. Feed and playback share this policy.
func (s *Service) DiscoveryCandidate(t *core.Title, r core.Release) string {
	return sourceSuitability(r, t, s.ProfileFor(core.ClientCaps{HDR10: true, DolbyVision: true}, nil), s.resolverCfg(), false)
}

func sourceSuitability(r core.Release, t *core.Title, p core.QualityProfile, c config.Resolver, explicit bool) string {
	// Reparse the name: saved feed payloads can contain older parser results.
	info := releaseparse.Parse(r.Name)
	if (info.Source == core.SourceCam || r.Info.Source == core.SourceCam) && !(c.AllowCam && p.AllowCam) {
		return "cam not allowed"
	}
	if explicit || !releaseparse.ExplicitDub(r.Name) || len(info.Languages) == 0 {
		return ""
	}
	want := append([]string(nil), p.PreferredLanguages...)
	if t != nil && t.Language != "" {
		want = append(want, assumedLanguage(t))
	}
	if len(want) == 0 {
		return ""
	}
	for _, lang := range info.Languages {
		if lang == "multi" {
			return ""
		}
		for _, pref := range want {
			if strings.EqualFold(lang, assumedLanguage(&core.Title{Language: pref})) {
				return ""
			}
		}
	}
	return "dub audio does not match the original or preferred languages"
}
