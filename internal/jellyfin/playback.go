package jellyfin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

// play is one playback an app started: a viewer watching one title on one
// device. Its id is the PlaySessionId apps echo in every later request, and
// doubles as the transcoder session id.
type play struct {
	id       string
	userID   int64
	deviceID string
	key      core.RatingKey
	playID   string // the core playback session
	offset   time.Duration
	started  bool
	touched  time.Time
}

// idlePlay is how long a play may go without any request before it is
// ended and its source released.
const idlePlay = 30 * time.Minute

func newPlayID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "j" + hex.EncodeToString(b)
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *Server) devKey(sess *session) string {
	return strconv.FormatInt(sess.User.ID, 10) + "|" + sess.DeviceID
}

func (s *Server) rememberCaps(sess *session, c core.ClientCaps) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.capsByDev) > 4096 {
		s.capsByDev = map[string]core.ClientCaps{}
	}
	s.capsByDev[s.devKey(sess)] = c
}

// capsOf is what the device last declared, or the defaults.
func (s *Server) capsOf(sess *session) core.ClientCaps {
	s.mu.Lock()
	c, ok := s.capsByDev[s.devKey(sess)]
	s.mu.Unlock()
	if ok {
		c.Remote = sess.Remote
		return c
	}
	return capsFor(sess, nil, 0)
}

func (s *Server) getPlay(id string, sess *session) *play {
	s.mu.Lock()
	defer s.mu.Unlock()
	pl := s.plays[id]
	if pl == nil || pl.userID != sess.User.ID {
		return nil
	}
	pl.touched = s.now()
	return pl
}

func (s *Server) putPlay(pl *play) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pl.touched = s.now()
	s.plays[pl.id] = pl
}

// endPlay releases everything a play holds. The source is released only
// when no other play of the same device still reads it.
func (s *Server) endPlay(pl *play) {
	s.mu.Lock()
	delete(s.plays, pl.id)
	shared := false
	for _, o := range s.plays {
		if o.playID == pl.playID && o.playID != "" {
			shared = true
		}
	}
	s.mu.Unlock()
	if s.d.Transcoder != nil {
		s.d.Transcoder.Stop(pl.id)
	}
	if pl.playID != "" && !shared && s.d.Playback != nil {
		s.d.Playback.Stop(pl.playID)
	}
}

// Close ends every play.
func (s *Server) Close() {
	s.mu.Lock()
	all := make([]*play, 0, len(s.plays))
	for _, p := range s.plays {
		all = append(all, p)
	}
	s.mu.Unlock()
	for _, p := range all {
		s.endPlay(p)
	}
}

// Sweep ends plays that have gone quiet. The app calls it periodically.
func (s *Server) Sweep() {
	cutoff := s.now().Add(-idlePlay)
	s.mu.Lock()
	var stale []*play
	for _, p := range s.plays {
		if p.touched.Before(cutoff) {
			stale = append(stale, p)
		}
	}
	s.mu.Unlock()
	for _, p := range stale {
		s.endPlay(p)
	}
	s.sweepLive()
}

// ---------------------------------------------------------------------------
// Deciding

// playError maps a failure to Jellyfin's PlaybackInfo error codes.
func playError(err error) string {
	switch {
	case errors.Is(err, core.ErrStreamLimit):
		return "RateLimitExceeded"
	case errors.Is(err, core.ErrUnauthorized):
		return "NotAllowed"
	}
	return "NoCompatibleStream"
}

type playRequest struct {
	caps          core.ClientCaps
	opts          core.DecideOptions
	offset        time.Duration
	audioIndex    int
	subtitleIndex int // -1 none, -2 not chosen
	allowDirect   bool
	allowStream   bool
	allowTrans    bool
}

// prepare resolves the title and decides how to deliver it.
func (s *Server) prepare(ctx context.Context, sess *session, t *core.Title, pr playRequest) (*core.PlaySession, core.Decision, error) {
	if s.d.Playback == nil {
		return nil, core.Decision{}, errors.New("playback not configured")
	}
	caps := pr.caps
	if policy, ok := s.d.AccessPolicy.(core.PlaybackAccessPolicy); ok && !policy.CanPlay(sess.User, caps.Remote, s.now()) {
		return nil, core.Decision{}, core.ErrUnauthorized
	}
	opts := pr.opts
	if policy, ok := s.d.AccessPolicy.(core.BitrateAccessPolicy); ok {
		if limit := policy.BitrateLimit(sess.User); limit > 0 && (caps.MaxBitrateKbps == 0 || limit < caps.MaxBitrateKbps) {
			caps.MaxBitrateKbps = limit
		}
	}
	if limit := s.remoteCapKbps(); caps.Remote && limit > 0 && (caps.MaxBitrateKbps == 0 || limit < caps.MaxBitrateKbps) {
		caps.MaxBitrateKbps = limit
	}
	opts.MaxBitrateKbps, opts.MaxHeight = caps.MaxBitrateKbps, caps.MaxHeight
	ps, err := s.d.Playback.Prepare(ctx, sess.User, t.Key, caps)
	if err != nil {
		return nil, core.Decision{}, err
	}
	opts.AudioStream, opts.SubtitleStream = pr.audioIndex, -1
	if opts.AudioStream < 0 {
		opts.AudioStream = preferredAudio(ps.Info, sess.User.AudioLanguages)
	}
	if pr.subtitleIndex >= 0 {
		opts.SubtitleStream = pr.subtitleIndex
	}
	opts.DirectPlay, opts.DirectStream = pr.allowDirect, pr.allowStream
	dec, err := s.decide(ps, caps, opts)
	if err != nil {
		s.d.Playback.Stop(ps.ID)
		return nil, core.Decision{}, err
	}
	// A picture subtitle (PGS, VobSub) cannot ride along a transcode, so it
	// is burned in; a sidecar one is delivered as a separate stream. Text
	// subtitles inside the video are shown by the app on direct play only:
	// pulling them out would mean reading the whole remote file.
	if opts.SubtitleStream >= 0 && dec.Kind != core.DecisionDirectPlay && imageSubtitle(ps.Info, opts.SubtitleStream) {
		opts.BurnSubtitles = true
		if dec, err = s.decide(ps, caps, opts); err != nil {
			s.d.Playback.Stop(ps.ID)
			return nil, core.Decision{}, err
		}
	}
	if dec.Kind != core.DecisionDirectPlay && !pr.allowTrans {
		s.d.Playback.Stop(ps.ID)
		return nil, core.Decision{}, core.ErrNotAvailable
	}
	return ps, dec, nil
}

func (s *Server) decide(ps *core.PlaySession, caps core.ClientCaps, opts core.DecideOptions) (core.Decision, error) {
	if s.d.Transcoder != nil {
		return s.d.Transcoder.Decide(ps.Info, caps, opts), nil
	}
	dec := transcode.Decide(ps.Info, caps, opts, transcode.Policy{RemoteMaxBitrateKbps: s.remoteCapKbps()})
	if dec.Kind != core.DecisionDirectPlay {
		return dec, errors.New("this client requires media conversion; install or configure FFmpeg")
	}
	return dec, nil
}

// remoteCapKbps is the owner's bitrate ceiling for clients outside the LAN.
func (s *Server) remoteCapKbps() int {
	if s.d.Config == nil {
		return 0
	}
	return s.d.Config().Transcode.RemoteMaxBitrateKbps
}

// imageSubtitle reports whether the stream is an embedded picture subtitle.
func imageSubtitle(info core.MediaInfo, index int) bool {
	for _, st := range info.Streams {
		if st.Index == index {
			return !st.External && transcode.IsImageSubtitle(st.Codec)
		}
	}
	return false
}

// preferredAudio picks the first audio stream in one of the viewer's
// languages, or -1 to let the decision choose the default.
func preferredAudio(info core.MediaInfo, langs []string) int {
	for _, want := range langs {
		for _, st := range info.Streams {
			if st.Type == "audio" && st.Language != "" && strings.HasPrefix(strings.ToLower(st.Language), strings.ToLower(want)) {
				return st.Index
			}
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// PlaybackInfo

type playbackBody struct {
	DeviceProfile       *deviceProfile `json:"DeviceProfile"`
	MaxStreamingBitrate int            `json:"MaxStreamingBitrate"`
	StartTimeTicks      int64          `json:"StartTimeTicks"`
	AudioStreamIndex    *int           `json:"AudioStreamIndex"`
	SubtitleStreamIndex *int           `json:"SubtitleStreamIndex"`
	MediaSourceID       string         `json:"MediaSourceId"`
	EnableDirectPlay    *bool          `json:"EnableDirectPlay"`
	EnableDirectStream  *bool          `json:"EnableDirectStream"`
	EnableTranscoding   *bool          `json:"EnableTranscoding"`
	IsPlayback          *bool          `json:"IsPlayback"`
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func (s *Server) handlePlaybackInfo(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	if c, ok := s.liveChannelFor(ctx, sess, chi.URLParam(r, "id")); ok {
		s.livePlaybackInfo(w, r, c)
		return
	}
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	t, err := s.d.Catalog.Get(ctx, key)
	if err != nil || !s.visibleOne(ctx, sess.User, t) || (t.Type != core.TypeMovie && t.Type != core.TypeEpisode) {
		s.writeErr(w, core.ErrNotFound)
		return
	}
	var body playbackBody
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body)
	}
	p := queryParams(r)
	if v := p.int("maxstreamingbitrate", 0); v > 0 {
		body.MaxStreamingBitrate = v
	}
	if v := p.int64("starttimeticks"); v > 0 {
		body.StartTimeTicks = v
	}
	for name, dst := range map[string]**int{"audiostreamindex": &body.AudioStreamIndex, "subtitlestreamindex": &body.SubtitleStreamIndex} {
		if p.has(name) {
			n := p.int(name, -1)
			*dst = &n
		}
	}
	for name, dst := range map[string]**bool{"enabledirectplay": &body.EnableDirectPlay, "enabledirectstream": &body.EnableDirectStream,
		"enabletranscoding": &body.EnableTranscoding, "isplayback": &body.IsPlayback} {
		if v, set := p.bool(name); set {
			*dst = &v
		}
	}
	caps := capsFor(sess, body.DeviceProfile, body.MaxStreamingBitrate/1000)
	if body.DeviceProfile == nil {
		caps = s.capsOf(sess)
		if body.MaxStreamingBitrate > 0 {
			caps.MaxBitrateKbps = body.MaxStreamingBitrate / 1000
		}
	} else {
		s.rememberCaps(sess, caps)
	}

	// An app that only wants the stream list (not to play) gets the
	// estimate: resolving a release for every detail page would be wasteful.
	if !boolOr(body.IsPlayback, true) {
		src := s.estimatedSourceCaps(ctx, sess, t, caps)
		writeJSON(w, http.StatusOK, map[string]any{"MediaSources": []mediaSource{src}, "PlaySessionId": ""})
		return
	}
	pr := playRequest{caps: caps, offset: fromTicks(body.StartTimeTicks), audioIndex: -1, subtitleIndex: -2,
		allowDirect: boolOr(body.EnableDirectPlay, true), allowStream: boolOr(body.EnableDirectStream, true),
		allowTrans: boolOr(body.EnableTranscoding, true)}
	if body.AudioStreamIndex != nil {
		pr.audioIndex = *body.AudioStreamIndex
	}
	if body.SubtitleStreamIndex != nil {
		pr.subtitleIndex = *body.SubtitleStreamIndex
	}
	ps, dec, err := s.prepare(ctx, sess, t, pr)
	if err != nil {
		s.log.Info("jellyfin: playback not possible", "key", key, "user", sess.User.ID, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"MediaSources": []mediaSource{}, "PlaySessionId": "", "ErrorCode": playError(err)})
		return
	}
	pl := &play{id: newPlayID(), userID: sess.User.ID, deviceID: sess.DeviceID, key: key, playID: ps.ID, offset: pr.offset}
	s.putPlay(pl)
	src := s.sourceFor(sess, t, ps.Info, &dec, pl, pr)
	writeJSON(w, http.StatusOK, map[string]any{"MediaSources": []mediaSource{src}, "PlaySessionId": pl.id})
}

func (s *Server) estimatedSource(ctx context.Context, sess *session, t *core.Title) mediaSource {
	return s.estimatedSourceCaps(ctx, sess, t, s.capsOf(sess))
}

func (s *Server) estimatedSourceCaps(ctx context.Context, sess *session, t *core.Title, caps core.ClientCaps) mediaSource {
	var info core.MediaInfo
	if s.d.Playback != nil {
		info = s.d.Playback.Estimate(ctx, t, caps)
	}
	if info.Duration == 0 {
		info.Duration = t.Runtime
	}
	src := s.sourceFor(sess, t, info, nil, nil, playRequest{subtitleIndex: -2})
	return src
}

// sourceFor describes the media to the app. dec is nil for an estimate.
func (s *Server) sourceFor(sess *session, t *core.Title, info core.MediaInfo, dec *core.Decision, pl *play, pr playRequest) mediaSource {
	id := itemID(t.Key)
	subURL := func(index int, ext string) string {
		return fmt.Sprintf("/videos/%s/%s/subtitles/%d/stream.%s", id, id, index, ext)
	}
	direct := dec == nil || dec.Kind == core.DecisionDirectPlay
	src := mediaSource{
		Protocol: "File", ID: id, Type: "Default", Container: info.Container, Size: info.Size, Name: t.Title,
		RunTimeTicks: ticks(firstDuration(info.Duration, t.Runtime)), VideoType: "VideoFile", Bitrate: info.BitrateKbps * 1000,
		MediaStreams: streamsFor(info, subURL, direct), MediaAttachments: []struct{}{}, Formats: []string{},
		RequiredHTTPHeaders: map[string]string{}, SupportsProbing: false, ETag: id[16:],
		DefaultAudioStreamIndex: -1,
	}
	none := -1
	src.DefaultSubtitleStreamIndex = &none
	for _, c := range info.Chapters {
		src.Chapters = append(src.Chapters, chapter{StartPositionTicks: ticks(c.Start), Name: c.Title})
	}
	if dec != nil {
		src.DefaultAudioStreamIndex = dec.AudioStream
		if dec.SubtitleStream >= 0 {
			idx := dec.SubtitleStream
			src.DefaultSubtitleStreamIndex = &idx
			if dec.BurnSubtitles {
				for i := range src.MediaStreams {
					if src.MediaStreams[i].Index == idx {
						src.MediaStreams[i].DeliveryMethod = "Encode"
					}
				}
			}
		}
	}
	switch {
	case dec == nil:
		src.SupportsDirectPlay, src.SupportsDirectStream = true, true
	case dec.Kind == core.DecisionDirectPlay:
		src.SupportsDirectPlay, src.SupportsDirectStream = true, true
	default:
		src.SupportsTranscoding = true
		container := "ts"
		if dec.Container != transcode.ContainerMPEGTS {
			container = "mp4"
		}
		src.TranscodingSubProtocol, src.TranscodingContainer = "hls", container
		q := []string{"MediaSourceId", id, "PlaySessionId", pl.id, "DeviceId", sess.DeviceID, "api_key", sess.Token,
			"SegmentContainer", container, "MinSegments", "1", "BreakOnNonKeyFrames", "True"}
		if pr.audioIndex >= 0 {
			q = append(q, "AudioStreamIndex", strconv.Itoa(pr.audioIndex))
		}
		if pr.subtitleIndex >= 0 {
			q = append(q, "SubtitleStreamIndex", strconv.Itoa(pr.subtitleIndex))
		}
		if pr.offset > 0 {
			q = append(q, "StartTimeTicks", strconv.FormatInt(ticks(pr.offset), 10))
		}
		if pr.caps.MaxBitrateKbps > 0 {
			q = append(q, "MaxStreamingBitrate", strconv.Itoa(pr.caps.MaxBitrateKbps*1000))
		}
		src.TranscodingURL = withQuery("/videos/"+id+"/master.m3u8", q...)
	}
	return src
}

func firstDuration(ds ...time.Duration) time.Duration {
	for _, d := range ds {
		if d > 0 {
			return d
		}
	}
	return 0
}

func (s *Server) handleOpenLiveStream(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "live streams are not supported", http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// Direct play

// playFor returns the play an app means: the one its PlaySessionId names, or
// a new one when the app builds stream URLs without asking first.
func (s *Server) playFor(ctx context.Context, sess *session, key core.RatingKey, t *core.Title, p params, pr playRequest) (*play, *core.PlaySession, core.Decision, error) {
	if id := p.get("playsessionid"); id != "" {
		if pl := s.getPlay(id, sess); pl != nil && pl.key == key {
			if ps, ok := s.d.Playback.Session(pl.playID); ok && ps != nil {
				return pl, ps, core.Decision{}, nil
			}
		}
	}
	ps, dec, err := s.prepare(ctx, sess, t, pr)
	if err != nil {
		return nil, nil, dec, err
	}
	id := p.get("playsessionid")
	if !safeID.MatchString(id) || s.getPlay(id, sess) != nil {
		id = newPlayID()
	}
	pl := &play{id: id, userID: sess.User.ID, deviceID: sess.DeviceID, key: key, playID: ps.ID, offset: pr.offset}
	s.putPlay(pl)
	return pl, ps, dec, nil
}

func (s *Server) requestFor(sess *session, p params) playRequest {
	pr := playRequest{caps: s.capsOf(sess), audioIndex: -1, subtitleIndex: -2, allowDirect: true, allowStream: true, allowTrans: true,
		offset: fromTicks(p.int64("starttimeticks"))}
	if p.has("audiostreamindex") {
		pr.audioIndex = p.int("audiostreamindex", -1)
	}
	if p.has("subtitlestreamindex") {
		pr.subtitleIndex = p.int("subtitlestreamindex", -2)
	}
	for _, name := range []string{"maxstreamingbitrate", "videobitrate", "maxvideobitrate"} {
		if v := p.int(name, 0); v > 0 && (pr.caps.MaxBitrateKbps == 0 || v/1000 < pr.caps.MaxBitrateKbps) {
			pr.caps.MaxBitrateKbps = v / 1000
		}
	}
	if v := p.int("maxheight", 0); v > 0 && (pr.caps.MaxHeight == 0 || v < pr.caps.MaxHeight) {
		pr.caps.MaxHeight = v
	}
	return pr
}

func (s *Server) videoTitle(w http.ResponseWriter, r *http.Request, sess *session) (core.RatingKey, *core.Title, bool) {
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return 0, nil, false
	}
	t, err := s.d.Catalog.Get(r.Context(), key)
	if err != nil || !s.visibleOne(r.Context(), sess.User, t) || (t.Type != core.TypeMovie && t.Type != core.TypeEpisode) {
		s.writeErr(w, core.ErrNotFound)
		return 0, nil, false
	}
	if policy, ok := s.d.AccessPolicy.(core.PlaybackAccessPolicy); ok && !policy.CanPlay(sess.User, sess.Remote, s.now()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return 0, nil, false
	}
	return key, t, true
}

// handleStream serves the title's bytes with range support: Jellyfin's
// static stream, Emby's original, and the download link.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	key, t, ok := s.videoTitle(w, r, sess)
	if !ok {
		return
	}
	p := queryParams(r)
	pr := s.requestFor(sess, p)
	var ps *core.PlaySession
	if existing, found := s.d.Playback.SessionFor(sess.User.ID, sess.DeviceID, key); found && existing != nil && p.get("playsessionid") == "" {
		ps = existing
	} else {
		_, got, _, err := s.playFor(r.Context(), sess, key, t, p, pr)
		if err != nil {
			s.log.Info("jellyfin: stream failed", "key", key, "err", err)
			if errors.Is(err, core.ErrStreamLimit) {
				http.Error(w, "stream limit reached", http.StatusTooManyRequests)
				return
			}
			http.Error(w, "this title could not be played right now", http.StatusServiceUnavailable)
			return
		}
		ps = got
	}
	if strings.HasSuffix(r.URL.Path, "/download") {
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(t.Title, `"`, "")+`.`+ps.Info.Container+`"`)
	}
	s.d.Playback.ServeRange(w, r, ps.ID)
}

// ---------------------------------------------------------------------------
// HLS

// handleMaster starts (or restarts, after a seek) the HLS session and
// returns the master playlist.
func (s *Server) handleMaster(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	if s.d.Transcoder == nil || s.d.Playback == nil {
		http.Error(w, "transcoding is not available", http.StatusServiceUnavailable)
		return
	}
	key, t, ok := s.videoTitle(w, r, sess)
	if !ok {
		return
	}
	p := queryParams(r)
	pr := s.requestFor(sess, p)
	pr.allowDirect = false // the app asked for a stream: remux at least
	pl, ps, dec, err := s.playFor(ctx, sess, key, t, p, pr)
	if err != nil {
		s.log.Info("jellyfin: start HLS failed", "key", key, "err", err)
		s.writeErr(w, err)
		return
	}
	// Decide again for this request: the app may have changed tracks or
	// quality, and a seek restarts from a new offset.
	opts := core.DecideOptions{DirectPlay: false, DirectStream: true, MaxBitrateKbps: pr.caps.MaxBitrateKbps, MaxHeight: pr.caps.MaxHeight,
		AudioStream: pr.audioIndex, SubtitleStream: -1}
	if opts.AudioStream < 0 {
		opts.AudioStream = preferredAudio(ps.Info, sess.User.AudioLanguages)
	}
	if pr.subtitleIndex >= 0 {
		opts.SubtitleStream = pr.subtitleIndex
		opts.BurnSubtitles = imageSubtitle(ps.Info, pr.subtitleIndex)
	}
	dec = s.d.Transcoder.Decide(ps.Info, pr.caps, opts)
	if dec.Kind == core.DecisionDirectPlay {
		dec.Kind, dec.VideoCopy, dec.AudioCopy, dec.Container = core.DecisionRemux, true, true, transcode.ContainerMPEGTS
	}
	if dec.Container == "" {
		dec.Container = transcode.ContainerMPEGTS
	}
	if existing, found := s.d.Transcoder.Session(pl.id); found && existing != nil {
		s.mu.Lock()
		same := pl.started && pl.offset == pr.offset
		s.mu.Unlock()
		if same {
			s.writeMaster(w, r, sess, pl, existing, ps.Info, dec)
			return
		}
		s.d.Transcoder.Stop(pl.id)
	}
	hs, err := s.d.Transcoder.StartHLS(context.WithoutCancel(ctx), core.HLSRequest{
		SessionID: pl.id, InputURL: s.d.Playback.InputURL(ps.ID), Info: ps.Info, Decision: dec, StartOffset: pr.offset,
	})
	if err != nil {
		s.log.Warn("jellyfin: start HLS session", "session", pl.id, "key", key, "err", err)
		http.Error(w, "could not start the stream", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	pl.started, pl.offset, pl.touched = true, pr.offset, s.now()
	s.mu.Unlock()
	s.writeMaster(w, r, sess, pl, hs, ps.Info, dec)
}

func withToken(uri, token string) string {
	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	return uri + sep + "api_key=" + url.QueryEscape(token)
}

func (s *Server) writeMaster(w http.ResponseWriter, r *http.Request, sess *session, pl *play, hs core.HLSSession, info core.MediaInfo, dec core.Decision) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	if ls, ok := hs.(core.HLSLadderSession); ok && len(ls.Rungs()) > 1 {
		for _, rg := range ls.Rungs() {
			line := fmt.Sprintf("#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=%d", rg.Bandwidth)
			if rg.AverageBandwidth > 0 {
				line += fmt.Sprintf(",AVERAGE-BANDWIDTH=%d", rg.AverageBandwidth)
			}
			if rg.Width > 0 && rg.Height > 0 {
				line += fmt.Sprintf(",RESOLUTION=%dx%d", rg.Width, rg.Height)
			}
			if rg.Codecs != "" {
				line += fmt.Sprintf(",CODECS=%q", rg.Codecs)
			}
			if rg.FrameRate > 0 {
				line += fmt.Sprintf(",FRAME-RATE=%.3f", rg.FrameRate)
			}
			b.WriteString(line + "\n" + withToken("hls/"+pl.id+"/"+rg.ID+"/index.m3u8", sess.Token) + "\n")
		}
	} else {
		bw := info.BitrateKbps
		if !dec.VideoCopy && dec.BitrateKbps > 0 {
			bw = dec.BitrateKbps
		}
		if bw <= 0 {
			bw = 8000
		}
		line := fmt.Sprintf("#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=%d", bw*1000)
		if v := info.Video(); v != nil && v.Width > 0 && v.Height > 0 {
			wd, ht := v.Width, v.Height
			if !dec.VideoCopy && dec.MaxHeight > 0 && ht > dec.MaxHeight {
				wd, ht = wd*dec.MaxHeight/ht, dec.MaxHeight
			}
			line += fmt.Sprintf(",RESOLUTION=%dx%d", wd, ht)
			if v.FrameRate > 0 {
				line += fmt.Sprintf(",FRAME-RATE=%.3f", v.FrameRate)
			}
		}
		line += transcode.HLSVariantAttributes(info, dec)
		b.WriteString(line + "\n" + withToken("hls/"+pl.id+"/base/index.m3u8", sess.Token) + "\n")
	}
	body := b.String()
	h := w.Header()
	h.Set("Content-Type", "application/vnd.apple.mpegurl")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}

// manifestResponse buffers a playlist so its URIs can carry the token.
type manifestResponse struct {
	http.ResponseWriter
	body   bytes.Buffer
	status int
}

func (w *manifestResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *manifestResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

var manifestURI = regexp.MustCompile(`(URI|initialization|media)="([^"]+)"`)

func (s *Server) serveManifest(w http.ResponseWriter, r *http.Request, token string, serve http.HandlerFunc) {
	rec := &manifestResponse{ResponseWriter: w}
	serve(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	body := rec.body.String()
	if rec.status == http.StatusOK {
		body = manifestURI.ReplaceAllStringFunc(body, func(attr string) string {
			m := manifestURI.FindStringSubmatch(attr)
			return m[1] + `="` + withToken(m[2], token) + `"`
		})
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
				lines[i] = withToken(t, token)
			}
		}
		body = strings.Join(lines, "\n")
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(rec.status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}

// handleHLS serves a session's playlists and segments.
func (s *Server) handleHLS(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	pl := s.getPlay(chi.URLParam(r, "sid"), sess)
	if pl == nil || s.d.Transcoder == nil {
		http.NotFound(w, r)
		return
	}
	hs, ok := s.d.Transcoder.Session(pl.id)
	if !ok || hs == nil {
		http.NotFound(w, r)
		return
	}
	rest := chi.URLParam(r, "*")
	name := rest
	if i := strings.LastIndexByte(rest, '/'); i >= 0 {
		name = rest[i+1:]
	}
	if ls, isLadder := hs.(core.HLSLadderSession); isLadder {
		if rungID, _, found := strings.Cut(rest, "/"); found {
			for _, rg := range ls.Rungs() {
				if rg.ID != rungID {
					continue
				}
				if strings.HasSuffix(name, ".m3u8") {
					s.serveManifest(w, r, sess.Token, func(w http.ResponseWriter, r *http.Request) { ls.ServeRungPlaylist(w, r, rungID) })
					return
				}
				n, ok := segmentNumber(name)
				if !ok {
					http.NotFound(w, r)
					return
				}
				ls.ServeRungSegment(w, r, rungID, n)
				return
			}
		}
	}
	if strings.HasSuffix(name, ".m3u8") {
		s.serveManifest(w, r, sess.Token, hs.ServePlaylist)
		return
	}
	n, ok := segmentNumber(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	hs.ServeSegment(w, r, n)
}

// segmentNumber extracts the segment index from names like "00012.ts",
// "12.ts" or "segment-12.m4s".
func segmentNumber(name string) (int, bool) {
	if name == transcode.InitSegmentName || name == transcode.MatroskaHeaderName {
		return transcode.InitSegment, true
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	end := len(base)
	start := end
	for start > 0 && base[start-1] >= '0' && base[start-1] <= '9' {
		start--
	}
	if start == end {
		return 0, false
	}
	n, err := strconv.Atoi(base[start:end])
	return n, err == nil
}

// handleStopEncoding ends a transcode the app no longer needs.
func (s *Server) handleStopEncoding(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if pl := s.getPlay(queryParams(r).get("playsessionid"), sess); pl != nil {
		s.endPlay(pl)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Subtitles

var subtitleTypes = map[string]string{"srt": "application/x-subrip", "vtt": "text/vtt", "ass": "text/x-ssa", "ssa": "text/x-ssa"}

// handleSubtitle serves a sidecar subtitle (OpenSubtitles matches). Subtitle
// tracks inside the video are played by the app itself on direct play, or
// burned in when transcoding.
func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	key, ok := parseItemID(chi.URLParam(r, "id"))
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if !ok || err != nil || s.d.Playback == nil {
		http.NotFound(w, r)
		return
	}
	t, err := s.d.Catalog.Get(r.Context(), key)
	if err != nil || !s.visibleOne(r.Context(), sess.User, t) {
		http.NotFound(w, r)
		return
	}
	ps, found := s.d.Playback.SessionFor(sess.User.ID, sess.DeviceID, key)
	if !found || ps == nil {
		http.NotFound(w, r)
		return
	}
	var ref *core.StreamInfo
	for i := range ps.Info.Streams {
		if ps.Info.Streams[i].Index == index && ps.Info.Streams[i].Type == "subtitle" {
			ref = &ps.Info.Streams[i]
		}
	}
	if ref == nil || !ref.External || ref.ExternalURL == "" {
		http.NotFound(w, r)
		return
	}
	format := strings.ToLower(chi.URLParam(r, "ext"))
	src := ref.ExternalURL
	if strings.HasPrefix(src, "ether-subtitle:") {
		if s.d.Subtitles == nil {
			http.NotFound(w, r)
			return
		}
		reader, actual, err := s.d.Subtitles.OpenFormat(src, format)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer reader.Close()
		ct := subtitleTypes[actual]
		if ct == "" {
			ct = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = io.Copy(w, io.LimitReader(reader, 32<<20))
		return
	}
	u, err := url.Parse(src)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, src, nil)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		resp, err := s.d.HTTP.Do(req)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.NotFound(w, r)
			return
		}
		ct := subtitleTypes[format]
		if ct == "" {
			ct = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 32<<20))
		return
	}
	if err == nil && u.Scheme == "file" {
		f, err := os.Open(u.Path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, filepath.Base(u.Path), st.ModTime(), f)
		return
	}
	http.NotFound(w, r)
}

// ---------------------------------------------------------------------------
// Skip markers

// handleMediaSegments serves intro and credits markers (Jellyfin 10.10's
// Media Segments), which the apps turn into Skip buttons.
func (s *Server) handleMediaSegments(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	empty := map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0}
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok || s.d.Markers == nil || s.d.Playback == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	t, err := s.d.Catalog.Get(r.Context(), key)
	if err != nil || !s.visibleOne(r.Context(), sess.User, t) {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	info := s.d.Playback.Estimate(r.Context(), t, s.capsOf(sess))
	if ps, found := s.d.Playback.SessionFor(sess.User.ID, sess.DeviceID, key); found && ps != nil {
		info = ps.Info
	}
	ms, err := s.d.Markers.Markers(r.Context(), key, info)
	if err != nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	var items []map[string]any
	for _, m := range ms {
		typ := ""
		switch strings.ToLower(m.Type) {
		case "intro":
			typ = "Intro"
		case "credits", "outro":
			typ = "Outro"
		case "recap":
			typ = "Recap"
		case "preview":
			typ = "Preview"
		default:
			continue
		}
		items = append(items, map[string]any{
			"Id": itemID(key)[:16] + fmt.Sprintf("%016x", len(items)+1), "ItemId": itemID(key), "Type": typ,
			"StartTicks": m.StartMS * 10_000, "EndTicks": m.EndMS * 10_000,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
}

// ---------------------------------------------------------------------------
// Progress

type playingBody struct {
	ItemID string `json:"ItemId"`
	Item   struct {
		ID string `json:"Id"`
	} `json:"Item"`
	PositionTicks int64  `json:"PositionTicks"`
	IsPaused      bool   `json:"IsPaused"`
	PlaySessionID string `json:"PlaySessionId"`
	EventName     string `json:"EventName"`
}

func (s *Server) readPlaying(w http.ResponseWriter, r *http.Request) (playingBody, core.RatingKey, bool) {
	var b playingBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return b, 0, false
	}
	key, ok := parseItemID(firstNonEmpty(b.ItemID, b.Item.ID))
	if !ok || isView(key) || (key.Kind() != core.KindMovie && key.Kind() != core.KindEpisode) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return b, 0, false
	}
	return b, key, true
}

// report records a progress report as Plex's timeline does.
func (s *Server) report(ctx context.Context, sess *session, b playingBody, key core.RatingKey, state string) {
	offset := fromTicks(b.PositionTicks)
	var duration time.Duration
	var ps *core.PlaySession
	if s.d.Playback != nil {
		if p, ok := s.d.Playback.SessionFor(sess.User.ID, sess.DeviceID, key); ok && p != nil {
			ps = p
			duration = p.Info.Duration
		}
	}
	if duration == 0 {
		if t, err := s.d.Catalog.Get(ctx, key); err == nil {
			duration = t.Runtime
		}
	}
	// A visible title is the only thing a viewer may record progress on.
	if t, err := s.d.Catalog.Get(ctx, key); err != nil || !s.visibleOne(ctx, sess.User, t) {
		return
	}
	if err := s.d.State.Progress(ctx, sess.User.ID, key, offset, duration, state); err != nil {
		s.log.Warn("jellyfin: record progress", "key", key, "err", err)
	}
	if s.d.OnTimeline != nil {
		s.d.OnTimeline(sess.User, key, offset, duration, state)
	}
	if ps != nil && (state == "playing" || state == "paused") {
		if t, ok := s.d.Playback.(interface{ Touch(string) }); ok {
			t.Touch(ps.ID)
		}
	}
	if pl := s.getPlay(b.PlaySessionID, sess); pl != nil && s.d.Transcoder != nil {
		if out, ok := s.d.Transcoder.Session(pl.id); ok {
			if act, ok := out.(core.SessionActivity); ok {
				act.PlaybackState(state)
			}
		}
	}
}

func (s *Server) handlePlaying(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	b, key, ok := s.readPlaying(w, r)
	if !ok {
		return
	}
	s.report(r.Context(), sess, b, key, "playing")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlayingProgress(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	b, key, ok := s.readPlaying(w, r)
	if !ok {
		return
	}
	state := "playing"
	if b.IsPaused || strings.EqualFold(b.EventName, "pause") {
		state = "paused"
	}
	s.report(r.Context(), sess, b, key, state)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlayingPing(w http.ResponseWriter, r *http.Request) {
	if pl := s.getPlay(queryParams(r).get("playsessionid"), sessionFrom(r.Context())); pl != nil && s.d.Playback != nil {
		if t, ok := s.d.Playback.(interface{ Touch(string) }); ok {
			t.Touch(pl.playID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlayingStopped(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	b, key, ok := s.readPlaying(w, r)
	if !ok {
		return
	}
	s.report(r.Context(), sess, b, key, "stopped")
	if pl := s.getPlay(b.PlaySessionID, sess); pl != nil {
		s.endPlay(pl)
	} else if s.d.Playback != nil {
		if ps, found := s.d.Playback.SessionFor(sess.User.ID, sess.DeviceID, key); found && ps != nil {
			s.d.Playback.Stop(ps.ID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
