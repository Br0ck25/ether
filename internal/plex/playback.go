package plex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
	"github.com/braedonsaunders/ether/internal/transcode"
)

// Decision codes. PMS documents the classes (1xxx ok, 2xxx general error,
// 3xxx direct-play error, 4xxx transcode error) but not every member; these
// are the values observed from real servers.
const (
	codeDirectPlayOK      = 1000
	codeConversionOK      = 1001
	codeGeneralError      = 2000
	codeDirectPlayError   = 3000
	codeTranscodeError    = 4000
	textDirectPlayOK      = "Direct play OK."
	textConversionOK      = "Direct play not available; Conversion OK."
	textDirectPlayDisable = "App cannot direct play this item. Direct play is disabled."
)

// User-visible playback failure messages.
const (
	msgNotAvailable = "Not available yet: no playable release of this title was found. Try again later."
	msgStreamLimit  = "Stream limit reached: stop another stream on this server and try again."
	msgPlayFailed   = "This title could not be played right now. Try again in a moment."
)

func playbackMessage(err error) string {
	switch {
	case errors.Is(err, core.ErrNotAvailable):
		return msgNotAvailable
	case errors.Is(err, core.ErrStreamLimit):
		return msgStreamLimit
	}
	return msgPlayFailed
}

// playRequest is a parsed decision/start request.
type playRequest struct {
	key       core.RatingKey
	caps      core.ClientCaps
	opts      core.DecideOptions
	sessionID string // client transcode session id
	offset    time.Duration
	protocol  string
	audioSel  int // stream position, -1 = default
	subSel    int // stream position, -1 = none/default
	subSet    bool
	// mde: the client runs its own media decision engine (hasMDE=1, sent
	// by current Plex Web, iOS and Apple TV apps).
	mde bool
}

func (s *Server) parsePlayRequest(r *http.Request) (playRequest, error) {
	pr := FromContext(r.Context())
	q := r.URL.Query()
	key := keyFromPath(q.Get("path"))
	if key == 0 {
		key = keyFromPath(q.Get("key"))
	}
	if key == 0 {
		key = keyFromPath(q.Get("ratingKey"))
	}
	if key == 0 || !isPlayableKey(key) {
		return playRequest{}, fmt.Errorf("no playable item in request")
	}
	caps := pr.ClientCaps(s.cfg().Transcode.RemoteMaxBitrateKbps)
	applyDecisionQuery(&caps, q)
	p := playRequest{key: key, caps: caps, audioSel: -1, subSel: -1, protocol: q.Get("protocol"), mde: q.Get("hasMDE") == "1"}
	if p.protocol == "" {
		p.protocol = "hls"
	}
	p.opts = core.DecideOptions{
		DirectPlay:     q.Get("directPlay") != "0",
		DirectStream:   q.Get("directStream") != "0",
		MaxBitrateKbps: caps.MaxBitrateKbps,
		MaxHeight:      parseResolutionHeight(q.Get("videoResolution")),
		AudioStream:    -1,
		SubtitleStream: -1,
	}
	if vb, err := strconv.Atoi(q.Get("videoBitrate")); err == nil && vb > 0 && (p.opts.MaxBitrateKbps == 0 || vb < p.opts.MaxBitrateKbps) {
		p.opts.MaxBitrateKbps = vb
	}
	switch q.Get("subtitles") {
	case "burn":
		p.opts.BurnSubtitles = true
	case "none":
		p.subSet = true
	}
	// Explicit stream ids in the request win over stored selections.
	var userID int64
	if pr.User != nil {
		userID = pr.User.ID
	}
	sel := s.selections.get(userID, int64(key))
	if sel.audioSet {
		p.audioSel = sel.audio
	}
	if sel.subtitleSet {
		p.subSel, p.subSet = sel.subtitle, true
	}
	if id, err := strconv.ParseInt(q.Get("audioStreamID"), 10, 64); err == nil {
		if ref, ok := s.streams.lookup(id); ok && ref.key == key {
			p.audioSel = ref.index
		}
	}
	if v := q.Get("subtitleStreamID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			if id == 0 {
				p.subSel, p.subSet = -1, true
			} else if ref, ok := s.streams.lookup(id); ok && ref.key == key {
				p.subSel, p.subSet = ref.index, true
			}
		}
	}
	p.sessionID = firstNonEmpty(q.Get("session"), q.Get("transcodeSessionId"), pr.SessionID)
	if p.sessionID == "" {
		p.sessionID = sanitizeSessionID(pr.ClientID + "-" + key.String())
	}
	p.sessionID = sanitizeSessionID(p.sessionID)
	if off, err := strconv.ParseFloat(q.Get("offset"), 64); err == nil && off > 0 {
		p.offset = time.Duration(off * float64(time.Second))
	}
	return p, nil
}

func isPlayableKey(k core.RatingKey) bool {
	switch k.Kind() {
	case core.KindMovie, core.KindEpisode, core.KindLocal:
		return true
	}
	return false
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// sanitizeSessionID keeps session ids path-safe.
func sanitizeSessionID(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < 128; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			b = append(b, c)
		}
	}
	return string(b)
}

// applySelections maps stream positions to DecideOptions stream indexes.
func applySelections(p *playRequest, info core.MediaInfo, langs []string) {
	audio := p.audioSel
	if audio < 0 || audio >= len(info.Streams) {
		audio = pickAudio(info, langs)
	}
	if audio >= 0 && audio < len(info.Streams) && info.Streams[audio].Type == "audio" {
		p.opts.AudioStream = info.Streams[audio].Index
	}
	if p.subSet && p.subSel >= 0 && p.subSel < len(info.Streams) && info.Streams[p.subSel].Type == "subtitle" {
		st := info.Streams[p.subSel]
		if !st.External {
			p.opts.SubtitleStream = st.Index
		}
	}
	p.audioSel = audio
}

// prepare resolves the title for this client and makes the decision.
func (s *Server) prepare(ctx context.Context, pr *Request, p *playRequest) (*core.Title, *core.PlaySession, core.Decision, error) {
	if s.d.Playback == nil {
		return nil, nil, core.Decision{}, errors.New("playback not configured")
	}
	t, err := s.d.Catalog.Get(ctx, p.key)
	if err != nil {
		return nil, nil, core.Decision{}, err
	}
	if !s.titleVisible(pr.User, t) {
		return nil, nil, core.Decision{}, core.ErrNotFound
	}
	if policy, ok := s.d.AccessPolicy.(interface {
		CanPlay(*core.User, bool, time.Time) bool
	}); ok && !policy.CanPlay(pr.User, p.caps.Remote, s.now()) {
		return nil, nil, core.Decision{}, core.ErrUnauthorized
	}
	if policy, ok := s.d.AccessPolicy.(interface{ BitrateLimit(*core.User) int }); ok {
		if limit := policy.BitrateLimit(pr.User); limit > 0 && (p.caps.MaxBitrateKbps == 0 || limit < p.caps.MaxBitrateKbps) {
			p.caps.MaxBitrateKbps = limit
			p.opts.MaxBitrateKbps = limit
		}
	}
	if !s.transcodes.claim(p.sessionID, pr.User.ID, pr.ClientID, s.now()) {
		return t, nil, core.Decision{}, core.ErrUnauthorized
	}
	ps, err := s.d.Playback.Prepare(ctx, pr.User, p.key, p.caps)
	if err != nil {
		return t, nil, core.Decision{}, err
	}
	s.ensureTrickplay(ctx, ps, p.offset)
	s.applyStreamPreferences(ctx, pr, p, t, ps.Info)
	applySelections(p, ps.Info, s.audioLanguages(pr.User))
	dec := core.Decision{Kind: core.DecisionDirectPlay, VideoCopy: true, AudioCopy: true, Container: ps.Info.Container, AudioStream: p.opts.AudioStream, SubtitleStream: p.opts.SubtitleStream}
	if s.d.Transcoder != nil {
		dec = s.d.Transcoder.Decide(ps.Info, p.caps, p.opts)
	} else {
		dec = transcode.Decide(ps.Info, p.caps, p.opts, transcode.Policy{RemoteMaxBitrateKbps: s.cfg().Transcode.RemoteMaxBitrateKbps})
		if dec.Kind != core.DecisionDirectPlay {
			s.d.Playback.Stop(ps.ID)
			return t, nil, core.Decision{}, fmt.Errorf("this client requires media conversion; install or configure FFmpeg")
		}
	}
	// On-demand sources can change during failover. A copy-only remux gives
	// MDE clients fresh container headers at the resumed timestamp.
	if p.mde && p.opts.DirectStream && onDemandSession(ps) && dec.Kind == core.DecisionDirectPlay && s.d.Transcoder != nil {
		dec.Kind = core.DecisionRemux
		dec.VideoCopy, dec.AudioCopy = true, true
		dec.Container = transcode.ContainerMPEGTS
		if v := ps.Info.Video(); v != nil && (v.Codec == "hevc" || v.Codec == "av1" || v.Codec == "vp9") {
			dec.Container = transcode.ContainerFMP4
		}
		dec.Reason = "Direct stream preserves playback across source changes."
	}
	if v := ps.Info.Video(); v != nil {
		s.log.Debug("playback media decision", "key", p.key, "client", pr.ClientID, "device", pr.Device, "model", pr.Model, "platformVersion", pr.PlatformVersion, "video", v.Codec, "videoProfile", v.Profile, "height", v.Height, "bitDepth", v.BitDepth, "transfer", v.ColorTransfer, "dvProfile", v.DolbyVisionProfile, "dvLevel", v.DolbyVisionLevel, "dvCompatibility", v.DolbyVisionCompatibilityID, "kind", dec.Kind, "outputContainer", dec.Container, "videoCopy", dec.VideoCopy, "audioCopy", dec.AudioCopy, "hdrCap", p.caps.HDR10, "dvCap", p.caps.DolbyVision, "profileExtra", pr.ProfileExtra)
	}
	s.transcodes.put(&transcodeEntry{
		id: p.sessionID, playID: ps.ID, userID: pr.User.ID, clientID: pr.ClientID, key: p.key,
		decision: dec, info: ps.Info, caps: p.caps, opts: p.opts, offset: p.offset, updatedAt: s.now(),
	})
	return t, ps, dec, nil
}

func onDemandSession(ps *core.PlaySession) bool {
	return ps.Resolution != nil && !strings.HasPrefix(ps.Resolution.Release.ID, "local:")
}

// handleDecision is /video/:/transcode/universal/decision.
func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	if s.handleLiveTVPlayback(w, r) {
		return
	}
	if s.handleExtraPlayback(w, r) {
		return
	}
	ctx := r.Context()
	pr := FromContext(ctx)
	if chi.URLParam(r, "ttype") != "" && chi.URLParam(r, "ttype") != "video" {
		s.writeEmpty(w, r)
		return
	}
	p, err := s.parsePlayRequest(r)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	t, ps, dec, err := s.prepare(ctx, pr, &p)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) && t == nil {
			writeHTTPError(w, http.StatusNotFound)
			return
		}
		if ctx.Err() != nil {
			return
		}
		s.log.Info("playback decision failed", "key", p.key, "user", pr.User.Username, "err", err)
		s.write(w, r, s.decisionError(ctx, pr, t, err, p))
		return
	}
	s.write(w, r, s.decisionContainer(ctx, pr, t, ps, dec, p))
}

// decisionError is the decision response for a failed resolve: no Media,
// and the reason in every *DecisionText so whichever field a client shows
// carries the message instead of a spinner.
func (s *Server) decisionError(ctx context.Context, pr *Request, t *core.Title, err error, p playRequest) *model.MediaContainer {
	msg := playbackMessage(err)
	mc := libraryContainer()
	if t != nil {
		mc = s.itemContainer(t.Type)
		md := s.metadata(ctx, pr, t, renderOpts{detail: true})
		decisionItem(&md) // shaped and typed like a successful decision's item
		mc.Metadata = []model.Metadata{md}
	}
	mc.Extra = []model.Attr{
		model.A("directPlayDecisionCode", codeDirectPlayError),
		model.A("directPlayDecisionText", msg),
		model.A("generalDecisionCode", codeGeneralError),
		model.A("generalDecisionText", msg),
		model.A("transcodeDecisionCode", codeTranscodeError),
		model.A("transcodeDecisionText", msg),
	}
	if p.mde {
		// MDE clients read mdeDecision* too (Plex Web shows the first
		// *DecisionText whose code is >= 2000).
		mc.Extra = append(mc.Extra, model.A("mdeDecisionCode", codeGeneralError), model.A("mdeDecisionText", msg))
	}
	mc.Extra = append(mc.Extra, model.A("resourceSession", firstNonEmpty(pr.ClientID, p.sessionID)))
	mc.Size = len(mc.Metadata)
	mc.SetDecision()
	return mc
}

// decisionItem reshapes a detail item the way PMS's decision responses
// carry it: attributes alphabetical with string-typed ids, no Image or Guid
// children, tag attributes alphabetical with string ids, and
// UltraBlurColors as an array in JSON.
func decisionItem(md *model.Metadata) {
	md.SetAlphabetical()
	md.Image = nil
	md.Guid = nil
	if md.UltraBlurColors != nil {
		u := *md.UltraBlurColors
		u.SetAlphabetical()
		md.UltraBlurList = []model.UltraBlurColors{u}
		md.UltraBlurColors = nil
	}
	for i := range md.Rating_ {
		md.Rating_[i].SetAlphabetical()
	}
	for _, tags := range [][]model.Tag{md.Genre, md.Country, md.Collection, md.Director, md.Writer, md.Role, md.Producer, md.Label, md.Similar} {
		for i := range tags {
			tags[i].SetAlphabetical()
		}
	}
}

func (s *Server) decisionContainer(ctx context.Context, pr *Request, t *core.Title, ps *core.PlaySession, dec core.Decision, p playRequest) *model.MediaContainer {
	mc := s.itemContainer(t.Type)
	md := s.metadata(ctx, pr, t, renderOpts{detail: true})
	decisionItem(&md)
	// With hasMDE=1 and direct play allowed, the client's own decision
	// engine decides: PMS answers with the source media as direct play and
	// only an mdeDecisionCode. Ether's own decision is kept for start.m3u8
	// in case the client then asks for a stream.
	mdeDirect := p.mde && p.opts.DirectPlay && len(p.opts.AudioFilters) == 0 && !(onDemandSession(ps) && p.opts.DirectStream)
	shown := dec
	if mdeDirect {
		shown = core.Decision{Kind: core.DecisionDirectPlay, VideoCopy: true, AudioCopy: true, Container: ps.Info.Container}
	}
	md.Media = []model.Media{s.decisionMedia(t, ps, shown, p, pr.User)}
	s.addProbedStreamFeatures(ctx, t.Key, ps.Info, &md)
	if md.Duration == 0 {
		md.Duration = md.Media[0].Duration
	}
	mc.Metadata = []model.Metadata{md}
	mc.Size = 1
	mc.SetDecision()
	switch {
	case mdeDirect:
		mc.Extra = []model.Attr{
			model.A("mdeDecisionCode", codeDirectPlayOK),
			model.A("mdeDecisionText", textDirectPlayOK),
		}
	case dec.Kind == core.DecisionDirectPlay:
		mc.Extra = []model.Attr{
			model.A("directPlayDecisionCode", codeDirectPlayOK),
			model.A("directPlayDecisionText", textDirectPlayOK),
			model.A("generalDecisionCode", codeDirectPlayOK),
			model.A("generalDecisionText", textDirectPlayOK),
		}
	default:
		dpText := textDirectPlayDisable
		if p.opts.DirectPlay {
			dpText = "App cannot direct play this item."
			if dec.Reason != "" {
				dpText += " " + dec.Reason
			}
		}
		mc.Extra = []model.Attr{
			model.A("directPlayDecisionCode", codeDirectPlayError),
			model.A("directPlayDecisionText", dpText),
			model.A("generalDecisionCode", codeConversionOK),
			model.A("generalDecisionText", textConversionOK),
			model.A("transcodeDecisionCode", codeConversionOK),
			model.A("transcodeDecisionText", textConversionOK),
		}
	}
	// PMS echoes the client identifier as the resource session.
	mc.Extra = append(mc.Extra, model.A("resourceSession", firstNonEmpty(pr.ClientID, p.sessionID)))
	return mc
}

// decisionMedia renders the Media with decision attributes.
func (s *Server) decisionMedia(t *core.Title, ps *core.PlaySession, dec core.Decision, p playRequest, u *core.User) model.Media {
	stamp := ps.StartedAt.Unix()
	if stamp <= 0 {
		stamp = 1
	}
	info := ps.Info
	m := buildMedia(t, info, stamp)
	if ps.Resolution != nil && s.d.Trickplay != nil && s.d.Trickplay.TrickplayAvailable(ps.Resolution.Release.ID) {
		m.Part[0].Indexes = "sd"
	}
	m.Selected = model.Ptr(true)
	m.SetAlphabetical()
	part := &m.Part[0]
	part.Selected = model.Ptr(true)
	part.SetAlphabetical()
	sel := selection{audio: p.audioSel, audioSet: p.audioSel >= 0, subtitle: p.subSel, subtitleSet: p.subSet}
	streams := s.buildStreams(t.Key, info, sel, u)
	for i := range streams {
		streams[i].SetAlphabetical()
		if streams[i].StreamType == 1 {
			// Decisions shorten the video title to the resolution part
			// ("1080p"); the extended title keeps the codec.
			if short, _, ok := strings.Cut(streams[i].DisplayTitle, " ("); ok {
				streams[i].DisplayTitle = short
			}
		}
	}

	if dec.Kind == core.DecisionDirectPlay {
		// Like PMS, list the video and the selected audio and subtitle
		// streams only.
		part.Decision = "directplay"
		var kept []model.Stream
		for _, st := range streams {
			if st.StreamType != 1 && (st.Selected == nil || !*st.Selected) {
				continue
			}
			st.Location = "direct"
			kept = append(kept, st)
		}
		part.Stream = kept
		return m
	}
	// Describe the actual HLS segment container so native MDE does not see
	// HEVC paired with the MPEG-TS container it cannot use.
	m.SetTranscodeOrder()
	part.SetTranscodeOrder()
	m.AspectRatio = 0
	m.Protocol = p.protocol
	m.Container = "mpegts"
	if dec.Container == transcode.ContainerFMP4 {
		m.Container = "mp4"
	}
	part.Container = m.Container
	part.Protocol = p.protocol
	part.Decision = "transcode"
	part.Key = ""
	part.File = ""
	part.Size = 0
	m.OptimizedForStreaming, m.Has64bitOffsets = nil, nil
	part.OptimizedForStreaming, part.Has64bitOffsets = nil, nil
	if r := m.VideoResolution; r != "" && r != "4k" && r != "sd" {
		m.VideoResolution = r + "p"
	}
	if !dec.VideoCopy {
		if dec.VideoCodec != "" {
			m.VideoCodec = plexCodec(dec.VideoCodec)
		}
		if dec.MaxHeight > 0 && m.Height > dec.MaxHeight {
			if m.Height > 0 {
				m.Width = m.Width * dec.MaxHeight / m.Height
			}
			m.Height = dec.MaxHeight
			m.VideoResolution = videoResolution(m.Width, m.Height) + "p"
		}
		if dec.BitrateKbps > 0 {
			m.Bitrate = dec.BitrateKbps
		}
	}
	if !dec.AudioCopy && dec.AudioCodec != "" {
		m.AudioCodec = plexCodec(dec.AudioCodec)
	}
	part.Bitrate, part.Width, part.Height = m.Bitrate, m.Width, m.Height
	var kept []model.Stream
	for _, st := range streams {
		ref, _ := s.streams.lookup(st.ID)
		src := ref.info
		st.Index = nil // segments carry their own stream layout
		switch st.StreamType {
		case 1:
			st.Decision = "copy"
			if !dec.VideoCopy {
				// A transcoded stream is new: PMS drops the source's
				// profile and chroma details and reports 8-bit output.
				st.Decision = "transcode"
				if dec.VideoCodec != "" {
					st.Codec = plexCodec(dec.VideoCodec)
				}
				if dec.MaxHeight > 0 && st.Height > dec.MaxHeight {
					st.Width = st.Width * dec.MaxHeight / max(st.Height, 1)
					st.Height = dec.MaxHeight
				}
				if dec.BitrateKbps > 0 {
					st.Bitrate = dec.BitrateKbps
				}
				st.Profile, st.ChromaSubsampling, st.ScanType = "", "", ""
				st.DOVIPresent, st.DOVIProfile = nil, 0
				if st.Codec == "h264" || dec.ToneMap {
					st.BitDepth = 8
				}
				if dec.ToneMap {
					st.ColorTrc, st.ColorPrimaries, st.ColorSpace = "bt709", "", ""
				}
			}
			st.Location = "segments-av"
		case 2:
			if st.Selected == nil || !*st.Selected {
				continue
			}
			st.Decision = "copy"
			if !dec.AudioCopy {
				st.Decision = "transcode"
				if dec.AudioCodec != "" {
					st.Codec = plexCodec(dec.AudioCodec)
				}
				// Source-only details go, as in PMS's transcoded audio.
				st.Profile, st.AudioChannelLayout, st.SamplingRate, st.Title = "", "", 0, ""
			}
			st.Location = "segments-av"
		case 3:
			if st.Selected == nil || !*st.Selected {
				continue
			}
			switch {
			case dec.BurnSubtitles:
				st.Decision = "burn"
				st.Location = "segments-video"
			case src.External:
				st.Decision = "copy"
				st.Location = "sidecar"
			default:
				st.Decision = "copy"
				st.Location = "segments-subtitle"
			}
		}
		kept = append(kept, st)
	}
	part.Stream = kept
	if p.protocol == core.ProtocolDASH {
		dashDecisionMedia(&m)
	}
	return m
}

// handleStartHLS is /video/:/transcode/universal/start.m3u8.
func (s *Server) handleStartHLS(w http.ResponseWriter, r *http.Request) {
	if s.handleLiveTVPlayback(w, r) {
		return
	}
	if s.handleExtraPlayback(w, r) {
		return
	}
	ctx := r.Context()
	pr := FromContext(ctx)
	if s.d.Transcoder == nil || s.d.Playback == nil {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	p, err := s.parsePlayRequest(r)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	if !s.transcodes.claim(p.sessionID, pr.User.ID, pr.ClientID, s.now()) {
		writeHTTPError(w, http.StatusForbidden)
		return
	}
	entry, ok := s.transcodes.get(p.sessionID)
	var ps *core.PlaySession
	if ok && entry.key == p.key && entry.userID == pr.User.ID {
		ps, ok = s.d.Playback.Session(entry.playID)
	}
	if !ok || ps == nil {
		if _, ps, _, err = s.prepare(ctx, pr, &p); err != nil {
			s.log.Info("start playback failed", "key", p.key, "err", err)
			s.writeErr(w, r, err)
			return
		}
		entry, _ = s.transcodes.get(p.sessionID)
	}
	dec := entry.decision
	if dec.Kind == core.DecisionDirectPlay && !p.opts.DirectPlay {
		// An MDE client got "direct play OK" from the decision, judged the
		// media unplayable itself and now asks for a stream: decide again
		// without direct play so a needed transcode isn't a plain remux.
		applySelections(&p, ps.Info, s.audioLanguages(pr.User))
		dec = s.d.Transcoder.Decide(ps.Info, p.caps, p.opts)
	}
	if dec.Kind == core.DecisionDirectPlay {
		// The client asked for HLS anyway: remux.
		dec.Kind, dec.VideoCopy, dec.AudioCopy, dec.Container = core.DecisionRemux, true, true, "mpegts"
	}
	if dec.Container == "" {
		dec.Container = "mpegts"
	}
	if existing, ok := s.d.Transcoder.Session(p.sessionID); ok && existing != nil {
		if entry.started && entry.offset == p.offset {
			s.writeMasterPlaylist(w, r, p.sessionID, ps.Info, dec)
			return
		}
		s.d.Transcoder.Stop(p.sessionID)
	}
	_, err = s.d.Transcoder.StartHLS(context.WithoutCancel(ctx), core.HLSRequest{
		SessionID:   p.sessionID,
		InputURL:    s.d.Playback.InputURL(ps.ID),
		Info:        ps.Info,
		Decision:    dec,
		StartOffset: p.offset,
	})
	if err != nil {
		s.log.Warn("start HLS session", "session", p.sessionID, "key", p.key, "err", err)
		writeHTTPError(w, http.StatusInternalServerError)
		return
	}
	entry.decision, entry.offset, entry.started, entry.updatedAt = dec, p.offset, true, s.now()
	s.transcodes.put(entry)
	s.writeMasterPlaylist(w, r, p.sessionID, ps.Info, dec)
}

func (s *Server) writeMasterPlaylist(w http.ResponseWriter, r *http.Request, sessionID string, info core.MediaInfo, dec core.Decision) {
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
	entry, _ := s.transcodes.get(sessionID)
	body := "#EXTM3U\n" + line + "\n" + streamGrantURL("session/"+sessionID+"/base/index.m3u8", entry.streamToken) + "\n"
	h := w.Header()
	h.Set("Content-Type", "application/vnd.apple.mpegurl")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}

// handleTranscodeSession serves session/{id}/base/index.m3u8 and segments.
func (s *Server) handleTranscodeSession(w http.ResponseWriter, r *http.Request) {
	if s.d.Transcoder == nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	id := sanitizeSessionID(chi.URLParam(r, "session"))
	hs, ok := s.d.Transcoder.Session(id)
	if !ok || hs == nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	s.transcodes.touch(id, s.now())
	rest := chi.URLParam(r, "*")
	name := rest
	if i := strings.LastIndexByte(rest, '/'); i >= 0 {
		name = rest[i+1:]
	}
	if strings.HasSuffix(name, ".m3u8") {
		s.serveStreamManifest(w, r, id, hs.ServePlaylist)
		return
	}
	n, ok := segmentNumber(name)
	if !ok {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	hs.ServeSegment(w, r, n)
}

// segmentNumber extracts the segment index from names like "00012.ts",
// "12.ts", "segment-12.m4s".
func segmentNumber(name string) (int, bool) {
	if name == transcode.InitSegmentName {
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
	if err != nil {
		return 0, false
	}
	return n, true
}

func (s *Server) handleTranscodePing(w http.ResponseWriter, r *http.Request) {
	if id := sanitizeSessionID(r.URL.Query().Get("session")); id != "" {
		if e, ok := s.transcodes.get(id); ok && e.userID == FromContext(r.Context()).User.ID {
			s.transcodes.touch(id, s.now())
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleTranscodeStop(w http.ResponseWriter, r *http.Request) {
	id := sanitizeSessionID(r.URL.Query().Get("session"))
	if id != "" {
		if e, ok := s.transcodes.get(id); ok && e.userID == FromContext(r.Context()).User.ID {
			if s.d.Transcoder != nil {
				s.d.Transcoder.Stop(id)
			}
			s.transcodes.remove(id)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handleTranscodeSessions is /transcode/sessions.
func (s *Server) handleTranscodeSessions(w http.ResponseWriter, r *http.Request) {
	mc := &model.MediaContainer{}
	u := FromContext(r.Context()).User
	for _, e := range s.sessions.list() {
		if !u.IsOwner && e.user.ID != u.ID {
			continue
		}
		if e.transcodeID == "" {
			continue
		}
		if te, ok := s.transcodes.get(e.transcodeID); ok {
			mc.TranscodeSess = append(mc.TranscodeSess, transcodeSessionModel(te))
		}
	}
	mc.Size = len(mc.TranscodeSess)
	s.write(w, r, mc)
}

func transcodeSessionModel(te *transcodeEntry) model.TranscodeSess {
	ts := model.TranscodeSess{
		Key:        "/transcode/sessions/" + te.id,
		Context:    "streaming",
		Protocol:   "hls",
		Container:  "mpegts",
		Duration:   ms(te.info.Duration),
		Progress:   -1,
		TimeStamp:  float64(te.updatedAt.Unix()),
		AudioCodec: plexCodec(te.decision.AudioCodec),
		VideoCodec: plexCodec(te.decision.VideoCodec),
	}
	if v := te.info.Video(); v != nil {
		ts.SourceVideoCodec = plexCodec(v.Codec)
	}
	for _, st := range te.info.Streams {
		if st.Type == "audio" {
			ts.SourceAudioCodec = plexCodec(st.Codec)
			break
		}
	}
	ts.VideoDecision, ts.AudioDecision = "copy", "copy"
	if !te.decision.VideoCopy {
		ts.VideoDecision = "transcode"
	} else {
		ts.VideoCodec = ts.SourceVideoCodec
	}
	if !te.decision.AudioCopy {
		ts.AudioDecision = "transcode"
	} else {
		ts.AudioCodec = ts.SourceAudioCodec
	}
	if te.decision.BurnSubtitles {
		ts.SubtitleDecision = "burn"
	}
	return ts
}

// ---------------------------------------------------------------------------
// Direct play

// handlePart serves /library/parts/{part}/{changestamp}/{file} with Range
// support through Playback.ServeRange.
func (s *Server) handlePart(w http.ResponseWriter, r *http.Request) {
	if s.handleExtraPart(w, r) {
		return
	}
	ctx := r.Context()
	pr := FromContext(ctx)
	id, err := strconv.ParseInt(chi.URLParam(r, "part"), 10, 64)
	key := core.RatingKey(id)
	if err != nil || !key.Valid() || !isPlayableKey(key) || s.d.Playback == nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	title, titleErr := s.d.Catalog.Get(ctx, key)
	if titleErr != nil || !s.visibleTitle(ctx, pr.User, title) {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if policy, ok := s.d.AccessPolicy.(interface {
		CanPlay(*core.User, bool, time.Time) bool
	}); ok && !policy.CanPlay(pr.User, pr.ClientCaps(0).Remote, s.now()) {
		writeHTTPError(w, http.StatusForbidden)
		return
	}
	ps, ok := s.d.Playback.SessionFor(pr.User.ID, pr.ClientID, key)
	if !ok || ps == nil {
		t, err := s.d.Catalog.Get(ctx, key)
		if err == nil && !s.titleVisible(pr.User, t) {
			err = core.ErrNotFound
		}
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		caps := pr.ClientCaps(s.cfg().Transcode.RemoteMaxBitrateKbps)
		ps, err = s.d.Playback.Prepare(ctx, pr.User, key, caps)
		if err != nil {
			s.log.Info("direct play failed", "key", key, "err", err)
			s.writeErr(w, r, err)
			return
		}
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(chi.URLParam(r, "file"), "\"", "")+"\"")
	}
	s.d.Playback.ServeRange(w, r, ps.ID)
}

// handlePartSelect is PUT /library/parts/{part}?audioStreamID=&subtitleStreamID=.
func (s *Server) handlePartSelect(w http.ResponseWriter, r *http.Request) {
	pr := FromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "part"), 10, 64)
	if err != nil || pr.User == nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	s.selections.update(pr.User.ID, id, func(sel *selection) {
		if v := q.Get("audioStreamID"); v != "" {
			if sid, err := strconv.ParseInt(v, 10, 64); err == nil {
				if ref, ok := s.streams.lookup(sid); ok && int64(ref.key) == id && ref.info.Type == "audio" {
					sel.audio, sel.audioSet = ref.index, true
				}
			}
		}
		if v := q.Get("subtitleStreamID"); v != "" {
			if sid, err := strconv.ParseInt(v, 10, 64); err == nil {
				if sid == 0 {
					sel.subtitle, sel.subtitleSet = -1, true
				} else if ref, ok := s.streams.lookup(sid); ok && int64(ref.key) == id && ref.info.Type == "subtitle" {
					sel.subtitle, sel.subtitleSet = ref.index, true
				}
			}
		}
	})
	s.rememberStreamRequest(r, pr.User, core.RatingKey(id))
	w.WriteHeader(http.StatusOK)
}

var subtitleContentTypes = map[string]string{
	"srt": "application/x-subrip",
	"vtt": "text/vtt",
	"ass": "text/x-ssa",
	"ssa": "text/x-ssa",
}

// handleStream serves external (sidecar) subtitle streams:
// /library/streams/{id}[.ext].
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "stream")
	raw, _, _ = strings.Cut(raw, ".")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	ref, ok := s.streams.lookup(id)
	if !ok || !ref.info.External || ref.info.ExternalURL == "" {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	title, titleErr := s.d.Catalog.Get(r.Context(), ref.key)
	if titleErr != nil || !s.visibleTitle(r.Context(), FromContext(r.Context()).User, title) {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	ct := subtitleContentTypes[plexCodec(ref.info.Codec)]
	if ct == "" {
		ct = "text/plain; charset=utf-8"
	}
	src := ref.info.ExternalURL
	if strings.HasPrefix(src, "ether-subtitle:") {
		if s.d.Subtitles == nil {
			writeHTTPError(w, http.StatusNotFound)
			return
		}
		format := "srt"
		if strings.HasSuffix(chi.URLParam(r, "stream"), ".vtt") {
			format = "vtt"
		}
		reader, actual, err := s.d.Subtitles.OpenFormat(src, format)
		if err != nil {
			writeHTTPError(w, http.StatusNotFound)
			return
		}
		defer reader.Close()
		contentType := subtitleContentTypes[actual]
		if contentType == "" {
			contentType = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = io.Copy(w, io.LimitReader(reader, 32<<20))
		return
	}
	u, err := url.Parse(src)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, src, nil)
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError)
			return
		}
		resp, err := s.d.HTTPClient.Do(req)
		if err != nil {
			writeHTTPError(w, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeHTTPError(w, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 32<<20))
		return
	}
	path := src
	if err == nil && u.Scheme == "file" {
		path = u.Path
	}
	f, err := os.Open(path)
	if err != nil {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}
