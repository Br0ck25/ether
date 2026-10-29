package jellyfin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv"
)

// LiveTV is the part of Ether's Live TV service the Jellyfin apps use: the
// owner's virtual channels, their guide, and tuning a channel to an HLS
// stream. IPTV channels are not offered here.
type LiveTV interface {
	AllowedChannels(ctx context.Context, u *core.User) ([]livetv.Channel, error)
	Guide(ctx context.Context, u *core.User, from, until time.Time) ([]livetv.Programme, error)
	TuneWithSession(ctx context.Context, u *core.User, channelID string, caps core.ClientCaps, clientSessionID string) (livetv.SessionInfo, error)
	Session(ctx context.Context, id string, u *core.User) (livetv.SessionInfo, error)
	ServePlaylist(w http.ResponseWriter, r *http.Request, id string, u *core.User)
	ServeSegment(w http.ResponseWriter, r *http.Request, id string, start int64, n int, u *core.User)
	Stop(ctx context.Context, id string, u *core.User) error
}

const (
	liveIdle     = 2 * time.Minute // a tuned channel nobody is fetching is released
	guideMaxDays = 7
)

// liveRef is what a Live TV item id stands for. Channel and programme ids are
// not catalogue keys, so they are hashed into 32-digit ids and remembered.
type liveRef struct {
	kind byte // 'c' channel, 'p' programme
	raw  string
}

type liveStream struct {
	session string
	user    *core.User
	touched time.Time
}

type liveState struct {
	mu      sync.Mutex
	ids     map[string]liveRef
	streams map[string]*liveStream // by user:channel
}

func (s *Server) liveID(kind byte, raw string) string {
	sum := sha256.Sum256(append([]byte{kind}, raw...))
	id := hex.EncodeToString(sum[:16])
	s.live.mu.Lock()
	if s.live.ids == nil || len(s.live.ids) > 50000 {
		s.live.ids = map[string]liveRef{}
	}
	s.live.ids[id] = liveRef{kind, raw}
	s.live.mu.Unlock()
	return id
}

func (s *Server) liveRefOf(id string) (liveRef, bool) {
	id = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	r, ok := s.live.ids[id]
	return r, ok
}

// liveChannels are the virtual channels this viewer may watch, in order.
func (s *Server) liveChannels(ctx context.Context, u *core.User) []livetv.Channel {
	if s.d.LiveTV == nil {
		return nil
	}
	all, err := s.d.LiveTV.AllowedChannels(ctx, u)
	if err != nil {
		s.log.Warn("jellyfin: live tv channels unavailable", "err", err)
		return nil
	}
	var out []livetv.Channel
	for _, c := range all {
		if c.Kind == "virtual" {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, aerr := strconv.ParseFloat(out[i].Number, 64)
		b, berr := strconv.ParseFloat(out[j].Number, 64)
		if aerr == nil && berr == nil && a != b {
			return a < b
		}
		return out[i].Number < out[j].Number
	})
	return out
}

func (s *Server) liveChannel(ctx context.Context, u *core.User, raw string) (livetv.Channel, bool) {
	for _, c := range s.liveChannels(ctx, u) {
		if c.ID == raw {
			return c, true
		}
	}
	return livetv.Channel{}, false
}

func (s *Server) channelItem(c livetv.Channel, cur *livetv.Programme) item {
	out := item{
		Name: c.Name, ServerID: s.serverID, ID: s.liveID('c', c.ID), Type: "TvChannel", MediaType: "Video",
		ChannelNumber: c.Number, SortName: c.Number, CanDownload: false, LocationType: "Remote",
		ImageTags: map[string]string{}, ProviderIDs: map[string]string{},
	}
	if cur != nil {
		p := s.programItem(c, *cur)
		out.CurrentProgram = &p
	}
	return out
}

func (s *Server) programItem(c livetv.Channel, p livetv.Programme) item {
	start, end := p.Start.UTC(), p.End.UTC()
	out := item{
		Name: p.Item.Title, ServerID: s.serverID, ID: s.liveID('p', p.ID), Type: "Program", MediaType: "Video",
		ChannelID: s.liveID('c', c.ID), ChannelName: c.Name, ChannelNumber: c.Number,
		StartDate: &start, EndDate: &end, RunTimeTicks: ticks(p.End.Sub(p.Start)),
		Overview: p.Item.Summary, OfficialRating: p.Item.Rating, IsLive: true, LocationType: "Remote",
		IsMovie: p.Item.Key.Kind() == core.KindMovie, IsSeries: p.Item.Key.Kind() == core.KindEpisode,
		ImageTags: map[string]string{}, ProviderIDs: map[string]string{},
	}
	out.SortName = out.Name
	return out
}

// guide lists programmes of the viewer's channels in [from, until).
func (s *Server) guide(ctx context.Context, u *core.User, from, until time.Time, channelIDs map[string]bool) ([]item, []livetv.Programme, map[string]livetv.Channel) {
	byID := map[string]livetv.Channel{}
	for _, c := range s.liveChannels(ctx, u) {
		byID[c.ID] = c
	}
	if len(byID) == 0 || s.d.LiveTV == nil {
		return nil, nil, byID
	}
	if until.Sub(from) > guideMaxDays*24*time.Hour {
		until = from.Add(guideMaxDays * 24 * time.Hour)
	}
	ps, err := s.d.LiveTV.Guide(ctx, u, from, until)
	if err != nil {
		s.log.Warn("jellyfin: live tv guide unavailable", "err", err)
		return nil, nil, byID
	}
	var kept []livetv.Programme
	var items []item
	for _, p := range ps {
		c, ok := byID[p.ChannelID]
		if !ok || (len(channelIDs) > 0 && !channelIDs[p.ChannelID]) {
			continue
		}
		kept = append(kept, p)
		items = append(items, s.programItem(c, p))
	}
	return items, kept, byID
}

// ---------------------------------------------------------------------------
// Listings

func (s *Server) handleLiveTVInfo(w http.ResponseWriter, r *http.Request) {
	enabled := s.d.LiveTV != nil && len(s.liveChannels(r.Context(), sessionFrom(r.Context()).User)) > 0
	users := []string{}
	if enabled {
		users = append(users, userID(sessionFrom(r.Context()).User.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"Services": []any{}, "IsEnabled": enabled, "EnabledUsers": users})
}

func (s *Server) handleGuideInfo(w http.ResponseWriter, r *http.Request) {
	now := s.now().UTC()
	writeJSON(w, http.StatusOK, map[string]any{"StartDate": now, "EndDate": now.Add(guideMaxDays * 24 * time.Hour)})
}

func (s *Server) handleLiveChannels(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	p := queryParams(r)
	chans := s.liveChannels(ctx, sess.User)
	if fav, set := p.bool("isfavorite"); set && fav {
		var keep []livetv.Channel
		for _, c := range chans {
			if c.Favorite {
				keep = append(keep, c)
			}
		}
		chans = keep
	}
	total := len(chans)
	start := max(p.int("startindex", 0), 0)
	if start > len(chans) {
		start = len(chans)
	}
	chans = chans[start:]
	if limit := p.int("limit", 0); limit > 0 && limit < len(chans) {
		chans = chans[:limit]
	}
	now := s.now()
	current := map[string]livetv.Programme{}
	if add, set := p.bool("addcurrentprogram"); !set || add {
		if _, kept, _ := s.guide(ctx, sess.User, now, now.Add(time.Second), nil); kept != nil {
			for _, g := range kept {
				current[g.ChannelID] = g
			}
		}
	}
	out := make([]item, 0, len(chans))
	for _, c := range chans {
		var cur *livetv.Programme
		if g, ok := current[c.ID]; ok {
			cur = &g
		}
		out = append(out, s.channelItem(c, cur))
	}
	writeJSON(w, http.StatusOK, itemsResult{Items: out, TotalRecordCount: total, StartIndex: start})
}

func (s *Server) handleLiveChannel(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ref, ok := s.liveRefOf(chi.URLParam(r, "id"))
	if !ok || ref.kind != 'c' {
		http.NotFound(w, r)
		return
	}
	c, ok := s.liveChannel(r.Context(), sess.User, ref.raw)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var cur *livetv.Programme
	now := s.now()
	if _, kept, _ := s.guide(r.Context(), sess.User, now, now.Add(time.Second), map[string]bool{c.ID: true}); len(kept) > 0 {
		cur = &kept[0]
	}
	writeJSON(w, http.StatusOK, s.channelItem(c, cur))
}

type programQuery struct {
	ChannelIDs   []string
	MinStartDate string
	MaxStartDate string
	MinEndDate   string
	MaxEndDate   string
	HasAired     *bool
	IsAiring     *bool
	IsMovie      *bool
	IsSeries     *bool
	// IsSports, IsKids and IsNews are asked for by the apps' Live TV tabs;
	// Ether does not tag programmes with them, so a true filter matches none.
	IsSports   *bool
	IsKids     *bool
	IsNews     *bool
	Limit      int
	StartIndex int
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// handlePrograms answers GET and POST /LiveTv/Programs.
func (s *Server) handlePrograms(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	var q programQuery
	p := queryParams(r)
	q.ChannelIDs = p.list("channelids")
	q.MinStartDate, q.MaxStartDate = p.get("minstartdate"), p.get("maxstartdate")
	q.MinEndDate, q.MaxEndDate = p.get("minenddate"), p.get("maxenddate")
	if v, set := p.bool("hasaired"); set {
		q.HasAired = &v
	}
	if v, set := p.bool("isairing"); set {
		q.IsAiring = &v
	}
	for name, dst := range map[string]**bool{"ismovie": &q.IsMovie, "isseries": &q.IsSeries, "issports": &q.IsSports, "iskids": &q.IsKids, "isnews": &q.IsNews} {
		if v, set := p.bool(name); set {
			*dst = &v
		}
	}
	q.Limit, q.StartIndex = p.int("limit", 0), p.int("startindex", 0)
	if r.Method == http.MethodPost {
		var body programQuery
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) == nil {
			if len(body.ChannelIDs) > 0 {
				q.ChannelIDs = body.ChannelIDs
			}
			for dst, v := range map[*string]string{&q.MinStartDate: body.MinStartDate, &q.MaxStartDate: body.MaxStartDate, &q.MinEndDate: body.MinEndDate, &q.MaxEndDate: body.MaxEndDate} {
				if v != "" {
					*dst = v
				}
			}
			if body.HasAired != nil {
				q.HasAired = body.HasAired
			}
			if body.IsAiring != nil {
				q.IsAiring = body.IsAiring
			}
			for dst, v := range map[**bool]*bool{&q.IsMovie: body.IsMovie, &q.IsSeries: body.IsSeries, &q.IsSports: body.IsSports, &q.IsKids: body.IsKids, &q.IsNews: body.IsNews} {
				if v != nil {
					*dst = v
				}
			}
			if body.Limit > 0 {
				q.Limit = body.Limit
			}
			if body.StartIndex > 0 {
				q.StartIndex = body.StartIndex
			}
		}
	}
	writeJSON(w, http.StatusOK, s.programList(ctx, sess, q))
}

func (s *Server) programList(ctx context.Context, sess *session, q programQuery) itemsResult {
	now := s.now()
	from := now
	if t, ok := parseTime(q.MinEndDate); ok {
		from = t
	} else if t, ok := parseTime(q.MinStartDate); ok {
		from = t
	}
	until := from.Add(24 * time.Hour)
	if t, ok := parseTime(q.MaxStartDate); ok {
		until = t
	} else if t, ok := parseTime(q.MaxEndDate); ok {
		until = t
	}
	if q.IsAiring != nil && *q.IsAiring {
		from, until = now, now.Add(time.Second)
	}
	ids := map[string]bool{}
	for _, id := range q.ChannelIDs {
		if ref, ok := s.liveRefOf(id); ok && ref.kind == 'c' {
			ids[ref.raw] = true
		}
	}
	if len(q.ChannelIDs) > 0 && len(ids) == 0 {
		return itemsResult{Items: []item{}}
	}
	items, kept, _ := s.guide(ctx, sess.User, from, until, ids)
	out := make([]item, 0, len(items))
	for _, tag := range []*bool{q.IsSports, q.IsKids, q.IsNews} {
		if tag != nil && *tag {
			return itemsResult{Items: []item{}}
		}
	}
	for i, it := range items {
		if q.HasAired != nil && !*q.HasAired && !kept[i].End.After(now) {
			continue
		}
		if q.IsMovie != nil && *q.IsMovie != it.IsMovie || q.IsSeries != nil && *q.IsSeries != it.IsSeries {
			continue
		}
		out = append(out, it)
	}
	total := len(out)
	if q.StartIndex > 0 {
		out = out[min(q.StartIndex, len(out)):]
	}
	if q.Limit > 0 && q.Limit < len(out) {
		out = out[:q.Limit]
	}
	return itemsResult{Items: out, TotalRecordCount: total, StartIndex: q.StartIndex}
}

func (s *Server) handleRecommendedPrograms(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	airing := true
	q := programQuery{IsAiring: &airing, Limit: queryParams(r).int("limit", 24)}
	writeJSON(w, http.StatusOK, s.programList(r.Context(), sess, q))
}

func (s *Server) handleProgram(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if it, ok := s.liveItem(r.Context(), sess, chi.URLParam(r, "id")); ok {
		writeJSON(w, http.StatusOK, it)
		return
	}
	http.NotFound(w, r)
}

// liveItem resolves a Live TV item id (a channel or a programme) to its DTO.
func (s *Server) liveItem(ctx context.Context, sess *session, id string) (item, bool) {
	ref, ok := s.liveRefOf(id)
	if !ok {
		return item{}, false
	}
	switch ref.kind {
	case 'c':
		c, ok := s.liveChannel(ctx, sess.User, ref.raw)
		if !ok {
			return item{}, false
		}
		var cur *livetv.Programme
		now := s.now()
		if _, kept, _ := s.guide(ctx, sess.User, now, now.Add(time.Second), map[string]bool{c.ID: true}); len(kept) > 0 {
			cur = &kept[0]
		}
		return s.channelItem(c, cur), true
	case 'p':
		i := strings.LastIndexByte(ref.raw, '-')
		if i < 0 {
			return item{}, false
		}
		ms, err := strconv.ParseInt(ref.raw[i+1:], 10, 64)
		if err != nil {
			return item{}, false
		}
		c, ok := s.liveChannel(ctx, sess.User, ref.raw[:i])
		if !ok {
			return item{}, false
		}
		at := time.UnixMilli(ms)
		if _, kept, _ := s.guide(ctx, sess.User, at, at.Add(time.Second), map[string]bool{c.ID: true}); len(kept) > 0 {
			return s.programItem(c, kept[0]), true
		}
	}
	return item{}, false
}

// handleEmptyLive answers the recording and timer lists, which Ether has none of.
func (s *Server) handleEmptyLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, itemsResult{Items: []item{}})
}

// ---------------------------------------------------------------------------
// Playback

// liveChannelFor is the channel a Live TV item id plays: itself, or a
// programme's channel.
func (s *Server) liveChannelFor(ctx context.Context, sess *session, id string) (livetv.Channel, bool) {
	ref, ok := s.liveRefOf(id)
	if !ok {
		return livetv.Channel{}, false
	}
	raw := ref.raw
	if ref.kind == 'p' {
		i := strings.LastIndexByte(raw, '-')
		if i < 0 {
			return livetv.Channel{}, false
		}
		raw = raw[:i]
	}
	return s.liveChannel(ctx, sess.User, raw)
}

// livePlaybackInfo is PlaybackInfo for a channel: one transcoding source that
// is the channel's HLS stream.
func (s *Server) livePlaybackInfo(w http.ResponseWriter, r *http.Request, c livetv.Channel) {
	sess := sessionFrom(r.Context())
	id := s.liveID('c', c.ID)
	playID := newPlayID()
	src := mediaSource{
		Protocol: "Http", ID: id, Type: "Default", Container: "ts", Name: c.Name, IsRemote: true,
		SupportsTranscoding: true, IsInfiniteStream: true, VideoType: "VideoFile",
		TranscodingURL:         withToken("/livetv/channels/"+id+"/master.m3u8?MediaSourceId="+id+"&PlaySessionId="+playID, sess.Token),
		TranscodingSubProtocol: "hls", TranscodingContainer: "ts",
		MediaStreams: []mediaStream{
			{Type: "Video", Codec: "h264", Index: 0, IsDefault: true, DisplayTitle: "H264"},
			{Type: "Audio", Codec: "aac", Index: 1, IsDefault: true, Channels: 2, DisplayTitle: "AAC"},
		},
		MediaAttachments: []struct{}{}, Formats: []string{}, RequiredHTTPHeaders: map[string]string{},
		DefaultAudioStreamIndex: 1,
	}
	writeJSON(w, http.StatusOK, map[string]any{"MediaSources": []mediaSource{src}, "PlaySessionId": playID})
}

// liveStreamFor returns the viewer's tuned session on a channel, tuning it
// the first time and again if it has ended.
func (s *Server) liveStreamFor(ctx context.Context, sess *session, c livetv.Channel) (string, error) {
	if s.d.LiveTV == nil {
		return "", core.ErrNotFound
	}
	key := strconv.FormatInt(sess.User.ID, 10) + ":" + c.ID
	s.live.mu.Lock()
	if s.live.streams == nil {
		s.live.streams = map[string]*liveStream{}
	}
	ls := s.live.streams[key]
	s.live.mu.Unlock()
	if ls != nil {
		if _, err := s.d.LiveTV.Session(ctx, ls.session, sess.User); err == nil {
			s.live.mu.Lock()
			ls.touched = s.now()
			s.live.mu.Unlock()
			return ls.session, nil
		}
	}
	caps := s.capsOf(sess)
	info, err := s.d.LiveTV.TuneWithSession(ctx, sess.User, c.ID, caps, caps.ClientID)
	if err != nil {
		return "", err
	}
	s.live.mu.Lock()
	s.live.streams[key] = &liveStream{session: info.ID, user: sess.User, touched: s.now()}
	s.live.mu.Unlock()
	return info.ID, nil
}

func (s *Server) handleLiveMaster(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	c, ok := s.liveChannelFor(r.Context(), sess, chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, err := s.liveStreamFor(r.Context(), sess, c)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.serveManifest(w, r, sess.Token, func(w http.ResponseWriter, r *http.Request) {
		s.d.LiveTV.ServePlaylist(w, r, id, sess.User)
	})
}

func (s *Server) handleLiveSegment(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	c, ok := s.liveChannelFor(r.Context(), sess, chi.URLParam(r, "id"))
	start, err := strconv.ParseInt(chi.URLParam(r, "start"), 10, 64)
	n, nok := segmentNumber(chi.URLParam(r, "seg"))
	if !ok || err != nil || !nok {
		http.NotFound(w, r)
		return
	}
	id, terr := s.liveStreamFor(r.Context(), sess, c)
	if terr != nil {
		s.writeErr(w, terr)
		return
	}
	s.d.LiveTV.ServeSegment(w, r, id, start, n, sess.User)
}

// sweepLive releases channels nobody has fetched for a while.
func (s *Server) sweepLive() {
	if s.d.LiveTV == nil {
		return
	}
	cutoff := s.now().Add(-liveIdle)
	s.live.mu.Lock()
	var stale []*liveStream
	for k, ls := range s.live.streams {
		if ls.touched.Before(cutoff) {
			stale = append(stale, ls)
			delete(s.live.streams, k)
		}
	}
	s.live.mu.Unlock()
	for _, ls := range stale {
		_ = s.d.LiveTV.Stop(context.Background(), ls.session, ls.user)
	}
}
