package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/markers"
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

// markerDialogue is markers.Options.Dialogue: cue times from subtitle text
// the system already holds for the played release (cached sidecars), for
// stinger detection. It never fetches video or touches the network.
func (a *App) markerDialogue(_ context.Context, _ core.RatingKey, info core.MediaInfo) ([]markers.DialogueCue, bool) {
	cues, ok := a.subtitles.CuesForInfo(info)
	if !ok {
		return nil, false
	}
	out := make([]markers.DialogueCue, len(cues))
	for i, c := range cues {
		out[i] = markers.DialogueCue{Start: c.Start, End: c.End}
	}
	return out, true
}
