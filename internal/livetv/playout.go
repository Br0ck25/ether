package livetv

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const segmentDuration = 4 * time.Second

type channelLimiter interface {
	BeginChannel(*core.User, core.ClientCaps, string) error
	EndChannel(string)
}

type track struct {
	programme  Programme
	hls        core.HLSSession
	playbackID string
	offset     time.Duration
	sequence   int64
	count      int
}
type session struct {
	consumed        bool
	reserved        bool
	ready           bool
	nextSequence    int64
	discontinuities int64
	relay           core.HLSSession
	relayInfo       *core.MediaInfo
	dash            core.DASHSession
	mu              sync.Mutex
	info            SessionInfo
	user            core.User
	caps            core.ClientCaps
	last            time.Time
	tracks          []*track
	resources       map[string]string
	closed          bool
}

func (ss *session) close(s *Service) {
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return
	}
	ss.closed = true
	tracks := ss.tracks
	relay := ss.relay
	dash := ss.dash
	reserved := ss.reserved
	ss.mu.Unlock()
	if relay != nil {
		relay.Close()
	}
	if dash != nil {
		dash.Close()
	}
	for _, t := range tracks {
		s.stopTrack(t)
	}
	if reserved {
		if limiter, ok := s.o.Playback.(channelLimiter); ok {
			limiter.EndChannel(ss.info.ID)
		}
	}
}

func (s *Service) Tune(ctx context.Context, u *core.User, channelID string, caps core.ClientCaps) (SessionInfo, error) {
	if u == nil || (s.o.CanPlay != nil && !s.o.CanPlay(u, caps.Remote, s.o.Now())) {
		return SessionInfo{}, core.ErrUnauthorized
	}
	c, err := s.Channel(ctx, channelID)
	if err != nil {
		return SessionInfo{}, err
	}
	ps, err := s.programmes(ctx, c, s.o.Now(), s.o.Now().Add(time.Second))
	if err != nil {
		return SessionInfo{}, err
	}
	var p *Programme
	if len(ps) > 0 {
		p = &ps[0]
	}
	if !s.allowed(ctx, u, c, p) {
		return SessionInfo{}, core.ErrUnauthorized
	}
	if c.Kind == "virtual" && p == nil {
		return SessionInfo{}, core.ErrNotAvailable
	}
	// A client can abandon a successful tune before requesting any media.
	// Reclaim only its unused session; other viewers/active players stay intact.
	if caps.ClientID != "" {
		var abandoned []*session
		s.mu.Lock()
		for id, old := range s.sessions {
			old.mu.Lock()
			unused := old.ready && !old.consumed && old.info.UserID == u.ID && old.caps.ClientID == caps.ClientID
			old.mu.Unlock()
			if unused {
				delete(s.sessions, id)
				abandoned = append(abandoned, old)
			}
		}
		s.mu.Unlock()
		for _, old := range abandoned {
			old.close(s)
		}
	}
	var playbackSessions []*core.PlaySession
	if s.o.Playback != nil {
		playbackSessions = s.o.Playback.Sessions()
	}
	s.mu.Lock()
	count := 0
	for _, ss := range s.sessions {
		if ss.info.UserID == u.ID {
			count++
		}
	}
	limit := u.StreamLimit
	if s.o.StreamLimit != nil {
		limit = s.o.StreamLimit(u)
	}
	if s.o.Playback != nil {
		for _, ps := range playbackSessions {
			if ps.UserID == u.ID && ps.PlaybackGroup == "" {
				count++
			}
		}
	}
	if limit > 0 && count >= limit {
		s.mu.Unlock()
		return SessionInfo{}, core.ErrStreamLimit
	}
	ss := &session{info: SessionInfo{ID: liveSessionID(), UserID: u.ID, Channel: c, Programme: p, StartedAt: s.o.Now()}, user: *u, caps: caps, last: s.o.Now(), resources: map[string]string{}}
	s.sessions[ss.info.ID] = ss
	s.mu.Unlock()
	if limiter, ok := s.o.Playback.(channelLimiter); ok {
		if err = limiter.BeginChannel(u, caps, ss.info.ID); err != nil {
			s.Stop(ctx, ss.info.ID, u)
			return SessionInfo{}, err
		}
		ss.mu.Lock()
		if ss.closed {
			ss.mu.Unlock()
			limiter.EndChannel(ss.info.ID)
			return SessionInfo{}, core.ErrNotFound
		}
		ss.reserved = true
		ss.mu.Unlock()
	}

	if c.Kind == "virtual" {
		ss.mu.Lock()
		_, err = s.prepareTrack(ctx, ss, *p)
		ss.mu.Unlock()
		if err != nil {
			s.Stop(ctx, ss.info.ID, u)
			return SessionInfo{}, err
		}
	} else {
		ss.mu.Lock()
		err = s.startIPTV(ctx, ss)
		ss.mu.Unlock()
		if err != nil {
			s.Stop(ctx, ss.info.ID, u)
			return SessionInfo{}, err
		}
	}
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return SessionInfo{}, core.ErrNotFound
	}
	ss.ready = true
	info := ss.info
	if ss.relayInfo != nil {
		info.MediaInfo = ss.relayInfo
	}
	ss.mu.Unlock()
	info.Channel.StreamURL = ""
	return info, nil
}
func (s *Service) getSession(ctx context.Context, id string, u *core.User) (*session, error) {
	s.mu.Lock()
	ss := s.sessions[id]
	s.mu.Unlock()
	if ss == nil {
		return nil, core.ErrNotFound
	}
	if u == nil || ss.info.UserID != u.ID || s.o.CanPlay != nil && !s.o.CanPlay(u, ss.caps.Remote, s.o.Now()) {
		return nil, core.ErrUnauthorized
	}
	c, err := s.Channel(ctx, ss.info.Channel.ID)
	if err != nil {
		return nil, err
	}
	ps, err := s.programmes(ctx, c, s.o.Now(), s.o.Now().Add(time.Second))
	if err != nil {
		return nil, err
	}
	var p *Programme
	if len(ps) > 0 {
		p = &ps[0]
	}
	if !s.allowed(ctx, u, c, p) {
		return nil, core.ErrUnauthorized
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed {
		return nil, core.ErrNotFound
	}
	if !ss.ready {
		return nil, core.ErrNotAvailable
	}
	ss.last = s.o.Now()
	ss.user = *u
	return ss, nil
}
func (s *Service) Session(ctx context.Context, id string, u *core.User) (SessionInfo, error) {
	ss, err := s.getSession(ctx, id, u)
	if err != nil {
		return SessionInfo{}, err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	info := ss.info
	if ss.relayInfo != nil {
		info.MediaInfo = ss.relayInfo
	}
	info.Channel.StreamURL = ""
	ps, err := s.programmes(ctx, ss.info.Channel, s.o.Now(), s.o.Now().Add(time.Second))
	if err == nil && len(ps) > 0 {
		info.Programme = &ps[0]
	}
	return info, err
}
func (s *Service) Sessions(ctx context.Context, u *core.User) []SessionInfo {
	s.mu.Lock()
	var ids []string
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	out := []SessionInfo{}
	for _, id := range ids {
		if info, err := s.Session(ctx, id, u); err == nil {
			out = append(out, info)
		}
	}
	return out
}
func (s *Service) Stop(ctx context.Context, id string, u *core.User) error {
	s.mu.Lock()
	ss := s.sessions[id]
	if ss == nil {
		s.mu.Unlock()
		return core.ErrNotFound
	}
	if u == nil || ss.info.UserID != u.ID && !u.IsOwner {
		s.mu.Unlock()
		return core.ErrUnauthorized
	}
	delete(s.sessions, id)
	s.mu.Unlock()
	ss.close(s)
	return nil
}
func trackCount(span time.Duration) int {
	n := int(span / segmentDuration)
	if rem := span % segmentDuration; n == 0 || rem >= segmentDuration/4 {
		n++
	}
	return n
}
func (s *Service) prepareTrack(ctx context.Context, ss *session, p Programme) (*track, error) {
	if ss.closed {
		return nil, core.ErrNotFound
	}
	for _, t := range ss.tracks {
		if t.programme.ID == p.ID {
			return t, nil
		}
	}
	if s.o.Playback == nil || s.o.Transcoder == nil {
		return nil, core.ErrNotAvailable
	}
	if !s.allowed(ctx, &ss.user, ss.info.Channel, &p) {
		return nil, core.ErrUnauthorized
	}
	offset := time.Duration(0)
	if len(ss.tracks) == 0 {
		offset = (p.Offset(s.o.Now()) / segmentDuration) * segmentDuration
	}
	caps := ss.caps
	caps.ClientID = "livetv-" + ss.info.ID + "-" + strconv.FormatInt(p.Start.UnixMilli(), 10)
	var ps *core.PlaySession
	var err error
	if grouped, ok := s.o.Playback.(interface {
		PrepareChannel(context.Context, *core.User, core.RatingKey, core.ClientCaps, string) (*core.PlaySession, error)
	}); ok {
		ps, err = grouped.PrepareChannel(ctx, &ss.user, p.Item.Key, caps, ss.info.ID)
	} else {
		ps, err = s.o.Playback.Prepare(ctx, &ss.user, p.Item.Key, caps)
	}
	if err != nil {
		return nil, err
	}
	info := ps.Info
	ss.info.MediaInfo = &info
	info.Duration = p.End.Sub(p.Start)
	decision := s.o.Transcoder.Decide(info, caps, core.DecideOptions{DirectStream: true, AudioStream: -1, SubtitleStream: -1})
	if decision.Kind == core.DecisionDirectPlay {
		decision.Kind = core.DecisionRemux
	}
	decision.Container = "mpegts"
	hs, err := s.o.Transcoder.StartHLS(s.ctx, core.HLSRequest{SessionID: "livetv-" + ss.info.ID + "-" + strconv.FormatInt(p.Start.UnixMilli(), 10), InputURL: s.o.Playback.InputURL(ps.ID), Info: info, Decision: decision, StartOffset: offset, SegmentDuration: segmentDuration})
	if err != nil {
		s.o.Playback.Stop(ps.ID)
		return nil, err
	}
	tr := &track{programme: p, hls: hs, playbackID: ps.ID, offset: offset, count: trackCount(info.Duration - offset)}
	tr.sequence = ss.nextSequence
	ss.nextSequence += int64(tr.count)
	s.transMu.Lock()
	s.transcodes[ps.ID] = hs
	s.transMu.Unlock()
	ss.tracks = append(ss.tracks, tr)
	return tr, nil
}
func writeStreamError(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	switch err {
	case core.ErrNotFound:
		code = 404
	case core.ErrUnauthorized:
		code = 403
	case core.ErrStreamLimit:
		code = 429
	case core.ErrNotAvailable:
		code = 503
	}
	http.Error(w, http.StatusText(code), code)
}

// ServePlaylist creates a sliding live playlist. Each programme has its own
// transcoder, with a discontinuity at the exact programme boundary. Offsets are
// measured in programme time and never in release byte positions.
func (s *Service) ServePlaylist(w http.ResponseWriter, r *http.Request, id string, u *core.User) {
	ss, err := s.getSession(r.Context(), id, u)
	if err != nil {
		writeStreamError(w, err)
		return
	}
	ss.mu.Lock()
	ss.consumed = true
	ss.mu.Unlock()
	if ss.relay != nil {
		s.serveRelayPlaylist(w, r, ss)
		return
	}
	if ss.info.Channel.Kind == "iptv" {
		s.ServeResource(w, r, id, "index.m3u8", u)
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := s.o.Now()
	ps, err := s.programmes(r.Context(), ss.info.Channel, now, now.Add(24*time.Second))
	if err != nil {
		writeStreamError(w, err)
		return
	}
	if len(ps) > 3 {
		ps = ps[:3]
	}
	// At most three source handles belong to one viewing session. Trim old
	// programme handles before reserving the next ones, retaining recent handles
	// when longer programmes leave room for lagging clients.
	desired := map[string]bool{}
	for _, p := range ps {
		desired[p.ID] = true
	}
	needed := 0
	for _, p := range ps {
		exists := false
		for _, tr := range ss.tracks {
			if tr.programme.ID == p.ID {
				exists = true
			}
		}
		if !exists {
			needed++
		}
	}
	for len(ss.tracks) > 0 && (len(ss.tracks)+needed > 3 || ss.tracks[0].programme.End.Before(now.Add(-24*time.Second))) {
		old := ss.tracks[0]
		if desired[old.programme.ID] {
			break
		}
		ss.tracks = ss.tracks[1:]
		ss.discontinuities++
		s.stopTrack(old)
	}
	for _, p := range ps {
		if _, err = s.prepareTrack(r.Context(), ss, p); err != nil {
			if err == core.ErrUnauthorized && p.Start.After(now) {
				break
			}
			writeStreamError(w, err)
			return
		}
	}

	type seg struct {
		seq  int64
		at   time.Time
		dur  time.Duration
		uri  string
		disc bool
	}
	var segments []seg
	target := 4
	discSequence := ss.discontinuities
	for ti, tr := range ss.tracks {
		for n := 0; n < tr.count; n++ {
			at := tr.programme.Start.Add(tr.offset + time.Duration(n)*segmentDuration)
			dur := segmentDuration
			if n == tr.count-1 {
				dur = tr.programme.End.Sub(at)
			}
			if at.Before(now.Add(-24*time.Second)) || at.After(now.Add(20*time.Second)) {
				continue
			}
			if int(math.Ceil(dur.Seconds())) > target {
				target = int(math.Ceil(dur.Seconds()))
			}
			uri := fmt.Sprintf("program/%d/%d.ts", tr.programme.Start.UnixMilli(), n)
			uri = withAuth(uri, r)
			segments = append(segments, seg{tr.sequence + int64(n), at, dur, uri, ti > 0 && n == 0})
		}
		if tr.programme.End.Before(now.Add(-24 * time.Second)) {
			discSequence++
		}
	}
	if len(segments) == 0 {
		writeStreamError(w, core.ErrNotAvailable)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", target, segments[0].seq, discSequence)
	for _, seg := range segments {
		if seg.disc {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n#EXTINF:%.6f,\n%s\n", seg.at.UTC().Format(time.RFC3339Nano), seg.dur.Seconds(), seg.uri)
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "HEAD" {
		fmt.Fprint(w, b.String())
	}
}
func withAuth(raw string, r *http.Request) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	token := r.URL.Query().Get("X-Plex-Token")
	if token == "" {
		token = r.Header.Get("X-Plex-Token")
	}
	if token != "" {
		q.Set("X-Plex-Token", token)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func (s *Service) ServeSegment(w http.ResponseWriter, r *http.Request, id string, start int64, n int, u *core.User) {
	ss, err := s.getSession(r.Context(), id, u)
	if err != nil {
		writeStreamError(w, err)
		return
	}
	ss.mu.Lock()
	var tr *track
	for _, t := range ss.tracks {
		if t.programme.Start.UnixMilli() == start {
			tr = t
			break
		}
	}
	if tr == nil || n < 0 || n >= tr.count {
		ss.mu.Unlock()
		writeStreamError(w, core.ErrNotFound)
		return
	}
	if !s.allowed(r.Context(), u, ss.info.Channel, &tr.programme) {
		ss.mu.Unlock()
		writeStreamError(w, core.ErrUnauthorized)
		return
	}
	hs := tr.hls
	ss.mu.Unlock()
	hs.ServeSegment(w, r, n)
}

// Failover keeps the channel timestamp when Playback resolves another release.
func (s *Service) Failover(ctx context.Context, id string, u *core.User) error {
	ss, err := s.getSession(ctx, id, u)
	if err != nil {
		return err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := s.o.Now()
	for _, tr := range ss.tracks {
		if !now.Before(tr.programme.Start) && now.Before(tr.programme.End) {
			at := tr.programme.Offset(now)
			ps, err := s.o.Playback.Failover(ctx, tr.playbackID, at)
			if err != nil {
				return err
			}
			return tr.hls.SwapInput(ctx, s.o.Playback.InputURL(ps.ID), ps.Info, at)
		}
	}
	return core.ErrNotAvailable
}

// startIPTV recognizes HLS without assuming URL suffixes (many owners use
// signed extensionless URLs). Other HTTP streams are remuxed through ffmpeg,
// whose existing input layer reconnects on network errors with bounded delay.
func (s *Service) startIPTV(ctx context.Context, ss *session) error {
	if ss.closed {
		return core.ErrNotFound
	}
	c := ss.info.Channel
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.StreamURL, nil)
	res, err := s.upstream(ctx, c.StreamURL, req)
	if err != nil {
		return err
	}
	prefix := make([]byte, 7)
	n, readErr := io.ReadFull(res.Body, prefix)
	res.Body.Close()
	if res.StatusCode != 200 {
		return core.ErrNotAvailable
	}
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return readErr
	}
	isHLS := strings.HasPrefix(strings.TrimSpace(string(prefix[:n])), "#EXTM3U") || strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "mpegurl")
	var info *core.MediaInfo
	if isHLS && !c.Transcode {
		analyze := len(ss.caps.VideoCodecs) > 0 || len(ss.caps.AudioCodecs) > 0 || strings.Contains(strings.ToLower(ss.caps.Product), "apple") || strings.EqualFold(ss.caps.Platform, "tvos") || strings.EqualFold(ss.caps.Platform, "ios")
		if s.o.Transcoder != nil && analyze {
			info, err = s.o.Transcoder.Probe(ctx, c.StreamURL)
			if err != nil {
				return err
			}
			if info == nil || info.Video() == nil {
				return core.ErrNotAvailable
			}
			copy := *info
			copy.Duration = 0
			info = &copy
			ss.info.MediaInfo = info
			// A playlist is a transport, not evidence that its codecs are
			// playable. MPEG-2/MP2 HLS needs conversion on Apple TV too.
			dec := s.o.Transcoder.Decide(*info, ss.caps, core.DecideOptions{DirectPlay: true, DirectStream: true, AudioStream: -1, SubtitleStream: -1})
			if !dec.VideoCopy || !dec.AudioCopy {
				return s.startIPTVRelay(ss, *info)
			}
		}
		ss.resources["index.m3u8"] = c.StreamURL
		return nil
	}
	if s.o.Transcoder == nil {
		return core.ErrNotAvailable
	}
	info, err = s.o.Transcoder.Probe(ctx, c.StreamURL)
	if err != nil {
		return err
	}
	if info == nil {
		return core.ErrNotAvailable
	}
	copy := *info
	copy.Duration = 0
	ss.info.MediaInfo = &copy
	return s.startIPTVRelay(ss, copy)
}
func (s *Service) serveRelayPlaylist(w http.ResponseWriter, r *http.Request, ss *session) {
	rec := httptest.NewRecorder()
	ss.relay.ServePlaylist(rec, r)
	if rec.Code != 200 {
		http.Error(w, "IPTV playout unavailable", rec.Code)
		return
	}
	// An unknown-duration transcoder initially has no completed segments.
	// Native players can reject that empty EVENT playlist immediately. Use
	// the session's bounded segment wait before returning the first playlist.
	if !relayPlaylistHasMedia(rec.Body.String()) {
		ready := &relayReadinessWriter{header: make(http.Header)}
		ss.relay.ServeSegment(ready, r, 0)
		if ready.status >= 400 {
			http.Error(w, "IPTV playout unavailable", ready.status)
			return
		}
		rec = httptest.NewRecorder()
		ss.relay.ServePlaylist(rec, r)
		if rec.Code != 200 || !relayPlaylistHasMedia(rec.Body.String()) {
			http.Error(w, "IPTV playout unavailable", http.StatusBadGateway)
			return
		}
	}
	lines := strings.Split(rec.Body.String(), "\n")
	for i, line := range lines {
		if line != "" && !strings.HasPrefix(line, "#") {
			base := path.Base(strings.SplitN(line, "?", 2)[0])
			n, err := strconv.Atoi(strings.TrimSuffix(base, ".ts"))
			if err != nil {
				http.Error(w, "invalid relay segment", 502)
				return
			}
			lines[i] = withAuth(fmt.Sprintf("/livetv/sessions/%s/0/relay/%d.ts", ss.info.ID, n), r)
		}
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "HEAD" {
		fmt.Fprint(w, strings.Join(lines, "\n"))
	}
}
func (s *Service) ServeRelaySegment(w http.ResponseWriter, r *http.Request, id string, n int, u *core.User) {
	ss, err := s.getSession(r.Context(), id, u)
	if err != nil {
		writeStreamError(w, err)
		return
	}
	if ss.relay == nil || n < 0 {
		writeStreamError(w, core.ErrNotFound)
		return
	}
	ss.relay.ServeSegment(w, r, n)
}

// TranscodeFor lets the playback manager's existing release failover locate
// programme sessions, preserving the same media timestamp after a source swap.
func (s *Service) TranscodeFor(playbackID string) (core.HLSSession, bool) {
	s.transMu.RLock()
	defer s.transMu.RUnlock()
	hs, ok := s.transcodes[playbackID]
	return hs, ok
}

// ViewingSessions reports one trusted reservation unit per live viewer. It is
// used by ordinary playback's limit accounting, including HLS passthrough
// viewers that do not own an ffmpeg/source session.
func (s *Service) ViewingSessions() []*core.PlaySession {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.mu.Unlock()
	out := make([]*core.PlaySession, 0, len(sessions))
	for _, ss := range sessions {
		ss.mu.Lock()
		if !ss.closed {
			p := &core.PlaySession{ID: ss.info.ID, PlaybackGroup: ss.info.ID, UserID: ss.info.UserID, Username: ss.user.Username, ClientID: ss.caps.ClientID, Client: ss.caps, TitleName: ss.info.Channel.Name, StartedAt: ss.info.StartedAt, Decision: core.DecisionRemux, DecisionReason: "Live channel playout"}
			if ss.info.Programme != nil {
				p.Key = ss.info.Programme.Item.Key
				p.TitleName = ss.info.Programme.Item.Title
				p.Info.Duration = ss.info.Programme.End.Sub(ss.info.Programme.Start)
			}
			out = append(out, p)
		}
		ss.mu.Unlock()
	}
	return out
}
