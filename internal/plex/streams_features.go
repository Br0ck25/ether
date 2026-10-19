package plex

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/markers"
	"github.com/braedonsaunders/ether/internal/plex/model"
	"github.com/braedonsaunders/ether/internal/trackprefs"
)

// TrackPreferences is optional. Selections are stream slice positions; saved
// show choices match language/flags so they survive changes in release order.
type TrackPreferences interface {
	Select(context.Context, int64, core.RatingKey, core.MediaInfo, string) (trackprefs.Selection, error)
	Remember(context.Context, int64, core.RatingKey, string, *core.StreamInfo) error
	GetRules(context.Context, int64) (trackprefs.Rules, error)
}
type MarkerProvider interface {
	Markers(context.Context, core.RatingKey, core.MediaInfo) ([]markers.Marker, error)
}

func (s *Server) preferredSelection(ctx context.Context, u *core.User, key core.RatingKey, info core.MediaInfo, original string, sel selection) selection {
	if s.d.TrackPreferences == nil || u == nil {
		return sel
	}
	if original == "" && key.Kind() == core.KindEpisode && s.d.Catalog != nil {
		if show, err := s.d.Catalog.Get(ctx, core.ShowKey(key.TMDB())); err == nil && show != nil {
			original = show.Language
		}
	}
	p, err := s.d.TrackPreferences.Select(ctx, u.ID, key, info, original)
	if err != nil {
		s.log.Warn("stream preferences unavailable", "error", err)
		return sel
	}
	if !sel.audioSet {
		sel.audio, sel.audioSet = p.Audio, true
	}
	if !sel.subtitleSet {
		sel.subtitle, sel.subtitleSet = p.Subtitle, true
	}
	return sel
}
func (s *Server) applyStreamPreferences(ctx context.Context, pr *Request, p *playRequest, t *core.Title, info core.MediaInfo) {
	sel := selection{audio: p.audioSel, audioSet: p.audioSel >= 0, subtitle: p.subSel, subtitleSet: p.subSet}
	sel = s.preferredSelection(ctx, pr.User, p.key, info, t.Language, sel)
	p.audioSel = sel.audio
	p.subSel, p.subSet = sel.subtitle, sel.subtitleSet
	if s.d.TrackPreferences != nil && pr.User != nil {
		rules, err := s.d.TrackPreferences.GetRules(ctx, pr.User.ID)
		if err == nil {
			channels := 0
			if p.audioSel >= 0 && p.audioSel < len(info.Streams) {
				channels = info.Streams[p.audioSel].Channels
			}
			p.opts.AudioFilters = rules.AudioFilters(channels)
		}
	}
}
func (s *Server) rememberStreamRequest(r *http.Request, user *core.User, key core.RatingKey) {
	if s.d.TrackPreferences == nil || user == nil {
		return
	}
	for _, kind := range []string{"audio", "subtitle"} {
		v := r.URL.Query().Get(kind + "StreamID")
		if v == "" {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		var track *core.StreamInfo
		if kind == "subtitle" && id == 0 {
		} else {
			ref, ok := s.streams.lookup(id)
			if !ok || ref.key != key || ref.info.Type != kind {
				continue
			}
			track = &ref.info
		}
		if err = s.d.TrackPreferences.Remember(r.Context(), user.ID, key, kind, track); err != nil {
			s.log.Warn("save stream preference", "error", err)
		}
	}
}
func (s *Server) addStreamFeatures(ctx context.Context, pr *Request, t *core.Title, m *model.Metadata) {
	info, _ := s.mediaInfoFor(ctx, pr, t)
	s.addProbedStreamFeatures(ctx, t.Key, info, m)
}
func (s *Server) addProbedStreamFeatures(ctx context.Context, key core.RatingKey, info core.MediaInfo, m *model.Metadata) {
	if info.Estimated {
		return
	}
	for i, c := range info.Chapters {
		if c.Start < 0 || c.End <= c.Start || c.End > info.Duration {
			continue
		}
		m.Chapter = append(m.Chapter, model.Chapter{ID: c.ID, Index: i, Title: c.Title, StartTimeOffset: c.Start.Milliseconds(), EndTimeOffset: c.End.Milliseconds()})
	}
	ranges := markers.FromChapters(info)
	if s.d.Markers != nil {
		var err error
		ranges, err = s.d.Markers.Markers(ctx, key, info)
		if err != nil {
			s.log.Debug("marker provider unavailable", "error", err)
		}
	}
	for i, v := range ranges {
		if v.Type != "intro" && v.Type != "credits" {
			continue
		}
		m.Marker = append(m.Marker, model.Marker{ID: int64(i + 1), Type: v.Type, StartTimeOffset: v.StartMS, EndTimeOffset: v.EndMS, Final: model.Ptr(v.Final)})
	}
}

// SubtitleSource resolves cached OpenSubtitles and synchronized sidecars.
type SubtitleSource interface {
	OpenFormat(string, string) (io.ReadCloser, string, error)
}
