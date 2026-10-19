package playback

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// headerLen is the size of the fake media header that names the release.
const headerLen = 64

// contentAt is the byte at offset off of the fake media file of release id.
func contentAt(id string, off int64) byte {
	if off < headerLen {
		h := fmt.Sprintf("ID=%s;", id)
		if off < int64(len(h)) {
			return h[off]
		}
		return '.'
	}
	var seed byte
	for i := 0; i < len(id); i++ {
		seed = seed*31 + id[i]
	}
	return byte(off*7) ^ seed ^ byte(off>>9)
}

func expected(id string, off, n int64) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = contentAt(id, off+int64(i))
	}
	return b
}

// fakeRelease is a release the fake engine can open.
type fakeRelease struct {
	ID       string
	Name     string // file name inside the release
	Size     int64
	Duration time.Duration
	// Reads touching [FailFrom, FailTo) fail with ErrArticleMissing.
	FailFrom, FailTo int64
	OpenErr          error
	ReadErr          error
}

type fakeSource struct {
	rel    *fakeRelease
	closed atomic.Bool
	reads  atomic.Int64
}

func (s *fakeSource) Size() int64       { return s.rel.Size }
func (s *fakeSource) Name() string      { return s.rel.Name }
func (s *fakeSource) ReleaseID() string { return s.rel.ID }
func (s *fakeSource) Close() error      { s.closed.Store(true); return nil }

func (s *fakeSource) ReadAt(p []byte, off int64) (int, error) {
	s.reads.Add(1)
	if s.closed.Load() {
		return 0, fmt.Errorf("read on closed source %s", s.rel.ID)
	}
	if off < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	if off >= s.rel.Size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if off+n > s.rel.Size {
		n = s.rel.Size - off
	}
	var err error
	failure := s.rel.ReadErr
	if failure == nil {
		failure = core.ErrArticleMissing
	}
	if s.rel.FailTo > s.rel.FailFrom && off < s.rel.FailTo && off+n > s.rel.FailFrom {
		if off >= s.rel.FailFrom {
			return 0, fmt.Errorf("segment at %d: %w", off, failure)
		}
		n = s.rel.FailFrom - off
		err = fmt.Errorf("segment at %d: %w", s.rel.FailFrom, failure)
	}
	for i := int64(0); i < n; i++ {
		p[i] = contentAt(s.rel.ID, off+i)
	}
	if err == nil && n < int64(len(p)) {
		err = io.EOF
	}
	return int(n), err
}

type prefetchCall struct {
	ReleaseID      string
	Offset, Length int64
}

type fakeEngine struct {
	mu       sync.Mutex
	releases map[string]*fakeRelease
	opens    []string
	sources  []*fakeSource
	prefetch []prefetchCall
}

func newFakeEngine(rels ...*fakeRelease) *fakeEngine {
	e := &fakeEngine{releases: map[string]*fakeRelease{}}
	for _, r := range rels {
		e.releases[r.ID] = r
	}
	return e
}

func (e *fakeEngine) Open(ctx context.Context, releaseID string, nzb []byte) (core.MediaSource, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opens = append(e.opens, releaseID)
	r := e.releases[releaseID]
	if r == nil {
		return nil, fmt.Errorf("unknown release %s", releaseID)
	}
	if string(nzb) != "nzb-"+releaseID {
		return nil, fmt.Errorf("wrong nzb %q for %s", nzb, releaseID)
	}
	if r.OpenErr != nil {
		return nil, r.OpenErr
	}
	s := &fakeSource{rel: r}
	e.sources = append(e.sources, s)
	return s, nil
}

func (e *fakeEngine) Prefetch(ctx context.Context, releaseID string, nzb []byte, offset, length int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prefetch = append(e.prefetch, prefetchCall{releaseID, offset, length})
	return nil
}

func (e *fakeEngine) openCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.opens)
}

type fakeResolver struct {
	mu         sync.Mutex
	candidates []core.Release // best first
	bad        map[string]string
	calls      int
	excludes   [][]string
	delay      time.Duration
	started    chan struct{} // closed on first Resolve, if set
}

func newFakeResolver(ids ...string) *fakeResolver {
	r := &fakeResolver{bad: map[string]string{}}
	for _, id := range ids {
		r.candidates = append(r.candidates, core.Release{ID: id, Name: "Release." + id, PubDate: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)})
	}
	return r
}

func (r *fakeResolver) Resolve(ctx context.Context, req core.ResolveRequest) (*core.Resolution, error) {
	r.mu.Lock()
	r.calls++
	r.excludes = append(r.excludes, slices.Clone(req.Exclude))
	delay := r.delay
	if r.started != nil && r.calls == 1 {
		close(r.started)
	}
	r.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var pick []core.Release
	for _, c := range r.candidates {
		if _, bad := r.bad[c.ID]; bad || slices.Contains(req.Exclude, c.ID) {
			continue
		}
		pick = append(pick, c)
	}
	if len(pick) == 0 {
		return nil, core.ErrNotAvailable
	}
	return &core.Resolution{Title: req.Title.Key, Release: pick[0], NZB: []byte("nzb-" + pick[0].ID), Alternates: pick[1:], ResolvedAt: time.Now()}, nil
}

func (r *fakeResolver) MarkBad(ctx context.Context, releaseID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bad[releaseID] = reason
	return nil
}

func (r *fakeResolver) Available(ctx context.Context, key core.RatingKey) (bool, bool) {
	return false, false
}

func (r *fakeResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *fakeResolver) isBad(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.bad[id]
	return ok
}

type swapCall struct {
	URL  string
	Info core.MediaInfo
	At   time.Duration
}

type fakeHLS struct {
	id    string
	mu    sync.Mutex
	swaps []swapCall
	swapC chan swapCall
}

func (h *fakeHLS) ID() string                                                 { return h.id }
func (h *fakeHLS) ServePlaylist(w http.ResponseWriter, r *http.Request)       {}
func (h *fakeHLS) ServeSegment(w http.ResponseWriter, r *http.Request, n int) {}
func (h *fakeHLS) LastAccess() time.Time                                      { return time.Time{} }
func (h *fakeHLS) Close() error                                               { return nil }
func (h *fakeHLS) SwapInput(ctx context.Context, inputURL string, info core.MediaInfo, at time.Duration) error {
	c := swapCall{inputURL, info, at}
	h.mu.Lock()
	h.swaps = append(h.swaps, c)
	h.mu.Unlock()
	if h.swapC != nil {
		h.swapC <- c
	}
	return nil
}

type fakeTranscoder struct {
	engine   *fakeEngine
	client   *http.Client
	mu       sync.Mutex
	probes   int
	failAll  bool
	sessions map[string]*fakeHLS
	stopped  []string
}

func newFakeTranscoder(e *fakeEngine) *fakeTranscoder {
	return &fakeTranscoder{engine: e, client: &http.Client{Timeout: 5 * time.Second}, sessions: map[string]*fakeHLS{}}
}

// Probe reads the header through the loopback URL, like ffprobe would.
func (t *fakeTranscoder) Probe(ctx context.Context, inputURL string) (*core.MediaInfo, error) {
	t.mu.Lock()
	t.probes++
	failAll := t.failAll
	t.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, inputURL, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", headerLen-1))
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("probe read: %w", err)
	}
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("probe status %d", resp.StatusCode)
	}
	if failAll {
		return nil, fmt.Errorf("ffprobe: invalid data")
	}
	h := string(b)
	id, _, _ := strings.Cut(strings.TrimPrefix(h, "ID="), ";")
	t.engine.mu.Lock()
	rel := t.engine.releases[id]
	t.engine.mu.Unlock()
	if rel == nil {
		return nil, fmt.Errorf("probe: unknown header %q", h)
	}
	container := "mkv"
	if strings.HasSuffix(rel.Name, ".ts") {
		container = "mpegts"
	}
	return &core.MediaInfo{
		Container: container,
		Duration:  rel.Duration,
		Size:      rel.Size,
		Streams:   []core.StreamInfo{{Index: 0, Type: "video", Codec: "h264", Height: 1080}},
	}, nil
}

func (t *fakeTranscoder) Decide(info core.MediaInfo, caps core.ClientCaps, opts core.DecideOptions) core.Decision {
	return core.Decision{Kind: core.DecisionDirectPlay}
}

func (t *fakeTranscoder) StartHLS(ctx context.Context, req core.HLSRequest) (core.HLSSession, error) {
	return nil, fmt.Errorf("not implemented")
}

func (t *fakeTranscoder) Session(id string) (core.HLSSession, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.sessions[id]
	return h, ok
}

func (t *fakeTranscoder) Stop(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = append(t.stopped, id)
	delete(t.sessions, id)
}

func (t *fakeTranscoder) HWAccel() core.HWAccel { return core.HWAccel{Name: "none"} }

func (t *fakeTranscoder) addHLS(id string) *fakeHLS {
	h := &fakeHLS{id: id, swapC: make(chan swapCall, 8)}
	t.mu.Lock()
	t.sessions[id] = h
	t.mu.Unlock()
	return h
}

type fakeCatalog struct {
	core.Catalog // unused methods panic
	titles       map[core.RatingKey]*core.Title
}

func (c *fakeCatalog) Get(ctx context.Context, key core.RatingKey) (*core.Title, error) {
	if t, ok := c.titles[key]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, core.ErrNotFound
}

type fakeCache struct {
	mu     sync.Mutex
	pins   map[string]map[string]bool
	log    []string
	budget int64
	bytes  map[string]int64
}

func newFakeCache() *fakeCache {
	return &fakeCache{pins: map[string]map[string]bool{}, bytes: map[string]int64{}}
}

func (c *fakeCache) Get(releaseID string, segment int) ([]byte, bool)     { return nil, false }
func (c *fakeCache) Put(releaseID string, segment int, data []byte) error { return nil }
func (c *fakeCache) Usage() (int64, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return 0, c.budget
}
func (c *fakeCache) ReleaseBytes(releaseID string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes[releaseID]
}
func (c *fakeCache) Pins() map[string][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string][]string{}
	for id, set := range c.pins {
		for r := range set {
			out[id] = append(out[id], r)
		}
	}
	return out
}
func (c *fakeCache) Pin(releaseID, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pins[releaseID] == nil {
		c.pins[releaseID] = map[string]bool{}
	}
	c.pins[releaseID][reason] = true
	c.log = append(c.log, "pin "+releaseID+" "+reason)
	return nil
}
func (c *fakeCache) Unpin(releaseID, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pins[releaseID], reason)
	if len(c.pins[releaseID]) == 0 {
		delete(c.pins, releaseID)
	}
	c.log = append(c.log, "unpin "+releaseID+" "+reason)
	return nil
}
func (c *fakeCache) pinned(releaseID, reason string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pins[releaseID][reason]
}

type fakeState struct {
	core.State
	mu       sync.Mutex
	statuses map[[2]int64]core.WatchStatus
}

func newFakeState() *fakeState { return &fakeState{statuses: map[[2]int64]core.WatchStatus{}} }

func (s *fakeState) set(user int64, key core.RatingKey, offset time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[[2]int64{user, int64(key)}] = core.WatchStatus{Key: key, ViewOffset: offset}
}

func (s *fakeState) setStatus(user int64, st core.WatchStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[[2]int64{user, int64(st.Key)}] = st
}

func (s *fakeState) Get(ctx context.Context, userID int64, key core.RatingKey) (core.WatchStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statuses[[2]int64{userID, int64(key)}], nil
}

func (s *fakeState) InProgressAny(ctx context.Context) ([]core.RatingKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.RatingKey
	for k, st := range s.statuses {
		if st.ViewOffset > 0 {
			out = append(out, core.RatingKey(k[1]))
		}
	}
	return out, nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// harness wires a Manager to fakes.
type harness struct {
	t        *testing.T
	m        *Manager
	engine   *fakeEngine
	resolver *fakeResolver
	tr       *fakeTranscoder
	catalog  *fakeCatalog
	cache    *fakeCache
	state    *fakeState
	clock    *fakeClock
	cfg      config.Config
	dataDir  string
	noCache  bool // build the Manager without a segment cache
}

var (
	movieKey = core.MovieKey(603)
	movie2   = core.MovieKey(604)
	movie3   = core.MovieKey(605)
	epKey    = core.EpisodeKey(1396, 1, 1)
	owner    = &core.User{ID: 1, Username: "owner", IsOwner: true}
	friend   = &core.User{ID: 2, Username: "friend"}
)

func defaultCaps(client string) core.ClientCaps {
	return core.ClientCaps{Product: "Plex Web", ClientID: client, MaxHeight: 1080,
		VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac", "eac3"}, Containers: []string{"mkv", "mp4"}}
}

type harnessOpt func(*harness, *Options)

func newHarness(t *testing.T, rels []*fakeRelease, ids []string, opts ...harnessOpt) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		engine:   newFakeEngine(rels...),
		resolver: newFakeResolver(ids...),
		catalog: &fakeCatalog{titles: map[core.RatingKey]*core.Title{
			movieKey: {Key: movieKey, Type: core.TypeMovie, Title: "The Matrix", Year: 1999, Runtime: 136 * time.Minute},
			movie2:   {Key: movie2, Type: core.TypeMovie, Title: "Reloaded", Year: 2003, Runtime: 138 * time.Minute},
			movie3:   {Key: movie3, Type: core.TypeMovie, Title: "Revolutions", Year: 2003, Runtime: 129 * time.Minute},
			epKey:    {Key: epKey, Type: core.TypeEpisode, ShowTitle: "Breaking Bad", Season: 1, Episode: 1, Title: "Pilot", Runtime: 58 * time.Minute},
		}},
		cache:   newFakeCache(),
		state:   newFakeState(),
		clock:   &fakeClock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)},
		dataDir: t.TempDir(),
	}
	h.tr = newFakeTranscoder(h.engine)
	h.cfg = config.Defaults(h.dataDir)
	h.cfg.Server.DefaultStreamLimit = 0
	o := Options{DataDir: h.dataDir, Now: h.clock.Now, ChunkSize: 4096, ReadAhead: 3, PinInterval: time.Hour}
	for _, f := range opts {
		f(h, &o)
	}
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	quality := config.DefaultQuality()
	var cache core.SegmentCache = h.cache
	if h.noCache {
		cache = nil
	}
	m, err := New(Deps{
		Resolver:   h.resolver,
		Engine:     h.engine,
		Transcoder: h.tr,
		Catalog:    h.catalog,
		Cache:      cache,
		State:      h.state,
		SelectProfile: func(caps core.ClientCaps, user *core.User) core.QualityProfile {
			if caps.MaxHeight >= 2160 {
				return quality.Profiles[0]
			}
			return quality.Profiles[1]
		},
		DB: db,
	}, h.cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	h.m = m
	return h
}

func (h *harness) prepare(user *core.User, key core.RatingKey, client string) *core.PlaySession {
	h.t.Helper()
	ps, err := h.m.Prepare(context.Background(), user, key, defaultCaps(client))
	if err != nil {
		h.t.Fatalf("Prepare(%s, %s): %v", user.Username, key, err)
	}
	return ps
}

func mkv(id string, size int64, dur time.Duration) *fakeRelease {
	return &fakeRelease{ID: id, Name: id + ".mkv", Size: size, Duration: dur}
}
