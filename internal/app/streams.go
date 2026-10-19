package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
)

func (a *App) enrichSubtitles(ctx context.Context, t *core.Title, u *core.User, res *core.Resolution, src core.MediaSource, info *core.MediaInfo) {
	if u != nil {
		copy := *u
		rules, err := a.trackprefs.GetRules(ctx, u.ID)
		if err == nil && len(rules.SubtitleLanguages) > 0 {
			copy.SubtitleLanguages = rules.SubtitleLanguages
		}
		u = &copy
	}
	a.subtitles.Enrich(ctx, t, u, res, src, info)
	if !a.cfg.Get().Markers.DetectCached {
		return
	}
	file, err := a.localmedia.PreferredFile(ctx, t.Key)
	if err != nil {
		return
	}
	// Detection is queued only for a complete owner-selected local source.
	_ = a.markers.ScheduleCached(ctx, t.Key, file.Info, file.Path)
}
