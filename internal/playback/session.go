package playback

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ether-media/ether/internal/core"
)

// identity is what makes two Prepare calls the same playback.
type identity struct {
	userID   int64
	clientID string
	key      core.RatingKey
}

// generation is one opened source of a session. A failover creates a new
// generation; the old one is retired and closed once in-flight reads end.
type generation struct {
	sess  *session
	n     int
	token string // loopback path component; unguessable
	src   core.MediaSource
	res   *core.Resolution
	info  core.MediaInfo
	tmap  TimeMap

	// articleMissing records that a read hit core.ErrArticleMissing.
	articleMissing atomic.Bool

	mu      sync.Mutex
	refs    int
	retired bool
	closed  bool
}

// acquire takes a read reference; false once the generation is retired.
func (g *generation) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.retired {
		return false
	}
	g.refs++
	return true
}

func (g *generation) release() {
	g.mu.Lock()
	g.refs--
	closeNow := g.retired && g.refs == 0 && !g.closed
	if closeNow {
		g.closed = true
	}
	g.mu.Unlock()
	if closeNow {
		g.closeSource()
	}
}

// retire stops new reads and closes the source when the last read ends.
func (g *generation) retire() {
	g.mu.Lock()
	if g.retired {
		g.mu.Unlock()
		return
	}
	g.retired = true
	closeNow := g.refs == 0 && !g.closed
	if closeNow {
		g.closed = true
	}
	g.mu.Unlock()
	if closeNow {
		g.closeSource()
	}
}

func (g *generation) closeSource() {
	if g.src != nil {
		_ = g.src.Close()
	}
}

func (g *generation) releaseID() string {
	if g.res == nil {
		return ""
	}
	return g.res.Release.ID
}

// session is the internal, mutable form of a core.PlaySession.
type session struct {
	id        string
	ident     identity
	user      core.User
	caps      core.ClientCaps
	title     *core.Title
	titleName string
	profile   string
	startedAt time.Time

	// virtual is the byte/time layout of the first generation: the file the
	// client was told about. Direct-play byte offsets are always in these
	// coordinates, even after a failover swapped the underlying release.
	virtual     TimeMap
	etag        string
	modTime     time.Time
	contentType string
	container   string
	name        string

	failSem chan struct{} // serialises failovers
	bytes   atomic.Int64
	rate    rateMeter

	mu         sync.Mutex
	cur        *generation
	gens       int
	tried      []string
	alternates []core.Release
	failovers  int
	lastActive time.Time
	stopAt     time.Time // set by a "stopped" timeline; expiry after a grace
	decision   core.DecisionKind
	explicit   bool // decision set by SetDecision
	closed     bool
	registered bool
}

func newSession(id string, ident identity, user *core.User, caps core.ClientCaps, title *core.Title, profile string, now time.Time) *session {
	return &session{
		id:         id,
		ident:      ident,
		user:       *user,
		caps:       caps,
		title:      title,
		titleName:  displayTitle(title),
		profile:    profile,
		startedAt:  now,
		lastActive: now,
		failSem:    make(chan struct{}, 1),
		rate:       rateMeter{},
	}
}

// adopt makes g the session's first generation and fixes the virtual file.
func (s *session) adopt(g *generation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = g
	s.gens = g.n + 1
	s.alternates = g.res.Alternates
	s.virtual = g.tmap
	s.container = g.info.Container
	s.name = g.src.Name()
	s.contentType = contentTypeFor(g.info.Container, s.name)
	sum := sha1.Sum(fmt.Appendf(nil, "%s:%d", g.releaseID(), g.tmap.Size))
	s.etag = `"` + hex.EncodeToString(sum[:10]) + `"`
	s.modTime = g.res.Release.PubDate
	if s.modTime.IsZero() {
		s.modTime = s.startedAt
	}
	s.modTime = s.modTime.UTC().Truncate(time.Second)
}

func (s *session) current() *generation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

func (s *session) nextGen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.gens
	s.gens++
	return n
}

func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *session) touch(now time.Time) {
	s.mu.Lock()
	s.lastActive = now
	s.stopAt = time.Time{}
	s.mu.Unlock()
}

func (s *session) markDirectPlay() {
	s.mu.Lock()
	if !s.explicit {
		s.decision = core.DecisionDirectPlay
	}
	s.mu.Unlock()
}

func (s *session) addBytes(n int, now time.Time) {
	if n <= 0 {
		return
	}
	s.bytes.Add(int64(n))
	s.rate.add(int64(n), now)
}

// toSource maps a virtual (client-facing) offset into generation g.
func (s *session) toSource(g *generation, v int64) int64 {
	s.mu.Lock()
	virtual := s.virtual
	s.mu.Unlock()
	if g.n == 0 {
		return v
	}
	return MapOffset(v, virtual, g.tmap)
}

func (s *session) snapshot(now time.Time) *core.PlaySession {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps := &core.PlaySession{
		ID:          s.id,
		UserID:      s.ident.userID,
		ClientID:    s.ident.clientID,
		Key:         s.ident.key,
		StartedAt:   s.startedAt,
		TitleName:   s.titleName,
		Username:    s.user.Username,
		Client:      s.caps,
		Decision:    s.decision,
		BytesServed: s.bytes.Load(),
		SpeedBps:    s.rate.rate(now),
		LastActive:  s.lastActive,
		Failovers:   s.failovers,
	}
	if s.user.Username == "" {
		ps.Username = s.user.Title
	}
	if g := s.cur; g != nil {
		res := *g.res
		ps.Resolution = &res
		ps.Source = g.src
		ps.Info = g.info
	}
	return ps
}

func displayTitle(t *core.Title) string {
	if t == nil {
		return ""
	}
	if t.Type == core.TypeEpisode {
		show := t.ShowTitle
		if show == "" {
			show = "?"
		}
		return fmt.Sprintf("%s - S%02dE%02d - %s", show, t.Season, t.Episode, t.Title)
	}
	if t.Year > 0 {
		return fmt.Sprintf("%s (%d)", t.Title, t.Year)
	}
	return t.Title
}

// lock acquires a channel semaphore, honouring ctx.
func lock(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func unlock(sem chan struct{}) { <-sem }

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("playback: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// rateMeter tracks bytes per second over a short sliding window.
type rateMeter struct {
	mu      sync.Mutex
	buckets [rateWindow + 1]int64
	secs    [rateWindow + 1]int64
}

const rateWindow = 5 // seconds

func (r *rateMeter) add(n int64, now time.Time) {
	sec := now.Unix()
	i := int(sec % int64(len(r.buckets)))
	r.mu.Lock()
	if r.secs[i] != sec {
		r.secs[i], r.buckets[i] = sec, 0
	}
	r.buckets[i] += n
	r.mu.Unlock()
}

// rate is the average over the last rateWindow complete seconds, in bytes
// per second.
func (r *rateMeter) rate(now time.Time) int64 {
	sec := now.Unix()
	r.mu.Lock()
	defer r.mu.Unlock()
	var sum int64
	for i := range r.buckets {
		if d := sec - r.secs[i]; d >= 1 && d <= rateWindow {
			sum += r.buckets[i]
		}
	}
	return sum / rateWindow
}
