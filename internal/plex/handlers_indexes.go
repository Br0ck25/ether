package plex

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

// handleIndexBIF serves the sparse BIF snapshot for a currently available
// part. Like direct part requests, it is scoped to the authenticated
// viewer's active playback session.
func (s *Server) handleIndexBIF(w http.ResponseWriter, r *http.Request) {
	key, releaseID, ok := s.indexPart(r)
	if !ok || chi.URLParam(r, "index") != "sd" || s.d.Trickplay == nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	data, ok := s.d.Trickplay.TrickplayBIF(releaseID)
	if !ok || len(data) == 0 {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if raw := r.URL.Query().Get("interval"); raw != "" {
		interval, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || interval < 10_000 {
			writeHTTPError(w, http.StatusNotFound)
			return
		}
		if interval > 10_000 {
			data = resampleBIF(data, time.Duration(interval)*time.Millisecond)
			if len(data) == 0 {
				writeHTTPError(w, http.StatusNotFound)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, fmt.Sprintf("%s.bif", key), s.now(), bytes.NewReader(data))
}

func resampleBIF(data []byte, interval time.Duration) []byte {
	b, err := transcode.ParseBIF(data)
	if err != nil || b.Len() == 0 {
		return nil
	}
	frames := make([]transcode.BIFFrame, 0, b.Len())
	var last time.Duration
	for i := 0; i < b.Len(); i++ {
		at := b.Time(i)
		if i > 0 && at-last < interval {
			continue
		}
		frames = append(frames, transcode.BIFFrame{Time: at, JPEG: b.Frame(i)})
		last = at
	}
	out, err := transcode.MarshalBIF(frames)
	if err != nil {
		return nil
	}
	return out
}

func (s *Server) ensureTrickplay(ctx context.Context, ps *core.PlaySession, offset time.Duration) {
	if s.d.Trickplay == nil || s.d.Playback == nil || ps == nil || ps.Resolution == nil {
		return
	}
	input := s.d.Playback.InputURL(ps.ID)
	releaseID := ps.Resolution.Release.ID
	if input == "" || releaseID == "" {
		return
	}
	s.d.Trickplay.EnsureTrickplay(ctx, core.TrickplaySource{
		ReleaseID: releaseID, InputURL: input, Info: ps.Info, AvailableUntil: offset,
	})
}

func (s *Server) cancelTrickplayIfIdle(ps *core.PlaySession) {
	if ps == nil || ps.Resolution == nil || s.d.Playback == nil {
		return
	}
	releaseID := ps.Resolution.Release.ID
	if releaseID == "" {
		return
	}
	for _, active := range s.d.Playback.Sessions() {
		if active != nil && active.Resolution != nil && active.Resolution.Release.ID == releaseID {
			return
		}
	}
	if canceler, ok := s.d.Trickplay.(core.TrickplayCanceler); ok {
		canceler.CancelTrickplay(releaseID)
	}
}

// handleIndexFrame serves a single generated JPEG frame. Offset is
// expressed in milliseconds, matching Plex Web's indexes/{index}/{offset}
// request form.
func (s *Server) handleIndexFrame(w http.ResponseWriter, r *http.Request) {
	_, releaseID, ok := s.indexPart(r)
	if !ok || chi.URLParam(r, "index") != "sd" || s.d.Trickplay == nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	ms, err := strconv.ParseInt(chi.URLParam(r, "offset"), 10, 64)
	if err != nil || ms < 0 {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	frame, ok := s.d.Trickplay.TrickplayFrame(releaseID, time.Duration(ms)*time.Millisecond)
	if !ok {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	setArtworkHeaders(w.Header(), "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frame)
}

// indexPart resolves a virtual part to the active session for this viewer,
// applying the same title visibility and playback policy as handlePart.
func (s *Server) indexPart(r *http.Request) (core.RatingKey, string, bool) {
	pr := FromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "part"), 10, 64)
	if err != nil || pr.User == nil ||
		s.d.Playback == nil || s.d.Catalog == nil {
		return 0, "", false
	}
	key, wantedReleaseID, found := s.partTitle(id)
	if !found {
		return 0, "", false
	}
	title, err := s.d.Catalog.Get(r.Context(), key)
	if err != nil || !s.visibleTitle(r.Context(), pr.User, title) {
		return 0, "", false
	}
	if policy, ok := s.d.AccessPolicy.(core.PlaybackAccessPolicy); ok && !policy.CanPlay(pr.User, pr.ClientCaps(0).Remote, s.now()) {
		return 0, "", false
	}
	ps, ok := s.d.Playback.SessionFor(pr.User.ID, pr.ClientID, key)
	if !ok || ps == nil || ps.Resolution == nil || ps.Resolution.Release.ID == "" {
		return 0, "", false
	}
	if wantedReleaseID != "" && ps.Resolution.Release.ID != wantedReleaseID {
		return 0, "", false
	}
	return key, ps.Resolution.Release.ID, true
}
