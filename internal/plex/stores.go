package plex

import (
	"context"
	"crypto/rand"
	"hash/fnv"
	"strconv"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// ---------------------------------------------------------------------------
// Stream ids

// streamRef is what a Stream id points at.
type streamRef struct {
	key   core.RatingKey
	index int // position in MediaInfo.Streams
	info  core.StreamInfo
}

// streamRegistry hands out stable Stream ids. Ids are a hash of (ratingKey,
// stream position) so they survive restarts; the registry resolves them back
// (for /library/streams/{id} and audioStreamID/subtitleStreamID params) and
// probes past the rare collision.
type streamRegistry struct {
	mu   sync.Mutex
	byID map[int64]streamRef
}

const maxStreamRefs = 200_000

func newStreamRegistry() *streamRegistry { return &streamRegistry{byID: map[int64]streamRef{}} }

func streamHash(key core.RatingKey, index int) int64 {
	h := fnv.New64a()
	h.Write([]byte(strconv.FormatInt(int64(key), 10) + "/" + strconv.Itoa(index)))
	// Keep ids small and positive so every client parses them as int32.
	return int64(h.Sum64()%2_000_000_000) + 1
}

func (r *streamRegistry) register(key core.RatingKey, index int, info core.StreamInfo) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byID) >= maxStreamRefs {
		r.byID = map[int64]streamRef{}
	}
	id := streamHash(key, index)
	for {
		ref, ok := r.byID[id]
		if !ok || (ref.key == key && ref.index == index) {
			r.byID[id] = streamRef{key: key, index: index, info: info}
			return id
		}
		id++ // collision: probe
	}
}

func (r *streamRegistry) lookup(id int64) (streamRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.byID[id]
	return ref, ok
}

// ---------------------------------------------------------------------------
// Stream selections (PUT /library/parts/{id}?audioStreamID=&subtitleStreamID=)

type selection struct {
	audio       int // position in MediaInfo.Streams
	audioSet    bool
	subtitle    int // position, -1 = none
	subtitleSet bool
}

type selectionStore struct {
	mu sync.Mutex
	m  map[[2]int64]selection
}

func newSelectionStore() *selectionStore { return &selectionStore{m: map[[2]int64]selection{}} }

func (s *selectionStore) get(userID, partID int64) selection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[[2]int64{userID, partID}]
}

func (s *selectionStore) update(userID, partID int64, fn func(*selection)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) > 100_000 {
		s.m = map[[2]int64]selection{}
	}
	k := [2]int64{userID, partID}
	sel := s.m[k]
	fn(&sel)
	s.m[k] = sel
}

// ---------------------------------------------------------------------------
// Transcode bindings: client transcode session id -> play session/decision

type transcodeEntry struct {
	id          string // client "session" param
	streamToken string // independent random capability; never a client-chosen ID
	playID      string // core.PlaySession.ID
	userID      int64
	clientID    string
	key         core.RatingKey
	decision    core.Decision
	info        core.MediaInfo
	caps        core.ClientCaps
	opts        core.DecideOptions
	offset      time.Duration
	started     bool // StartHLS ran
	updatedAt   time.Time
}

type transcodeTable struct {
	mu sync.Mutex
	m  map[string]*transcodeEntry
}

func newTranscodeTable() *transcodeTable { return &transcodeTable{m: map[string]*transcodeEntry{}} }

func (t *transcodeTable) put(e *transcodeEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if old, ok := t.m[e.id]; ok {
		if old.userID != e.userID || old.clientID != e.clientID {
			return
		}
		e.streamToken = old.streamToken
	}
	if e.streamToken == "" {
		e.streamToken = rand.Text()
	}
	cp := *e
	t.m[e.id] = &cp
}

// claim reserves a client-chosen ID before any playback/transcoder work. A
// different viewer or device cannot replace, reuse, stop, or probe that binding.
func (t *transcodeTable) claim(id string, userID int64, clientID string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.m[id]; ok {
		return e.userID == userID && e.clientID == clientID
	}
	t.m[id] = &transcodeEntry{id: id, userID: userID, clientID: clientID, streamToken: rand.Text(), updatedAt: now}
	return true
}

func (t *transcodeTable) get(id string) (*transcodeEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[id]
	if !ok {
		return nil, false
	}
	cp := *e
	return &cp, true
}

func (t *transcodeTable) touch(id string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.m[id]; ok {
		e.updatedAt = now
	}
}

func (t *transcodeTable) remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, id)
}

// forClient lists the transcode sessions of a client for a title.
func (t *transcodeTable) forClient(userID int64, clientID string, key core.RatingKey) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for id, e := range t.m {
		if e.userID == userID && e.clientID == clientID && (key == 0 || e.key == key) {
			out = append(out, id)
		}
	}
	return out
}

func (t *transcodeTable) expire(now time.Time, ttl time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, e := range t.m {
		if now.Sub(e.updatedAt) > ttl {
			delete(t.m, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Facet cache: genre/country keys -> titles for filter resolution.

type facetCache struct {
	mu sync.Mutex
	m  map[string]facetEntry
}

type facetEntry struct {
	vals []core.FacetValue
	at   time.Time
}

func newFacetCache() *facetCache { return &facetCache{m: map[string]facetEntry{}} }

const facetTTL = 10 * time.Minute

func (s *Server) facet(ctx context.Context, t core.MediaType, name string) ([]core.FacetValue, error) {
	k := string(t) + "/" + name
	s.facets.mu.Lock()
	e, ok := s.facets.m[k]
	s.facets.mu.Unlock()
	if ok && s.now().Sub(e.at) < facetTTL {
		return e.vals, nil
	}
	vals, err := s.d.Catalog.Facet(ctx, t, name)
	if err != nil {
		return nil, err
	}
	s.facets.mu.Lock()
	s.facets.m[k] = facetEntry{vals: vals, at: s.now()}
	s.facets.mu.Unlock()
	return vals, nil
}

// facetTitle maps a filter value (a facet key or already a title) to the
// facet title the catalogue filters on.
func (s *Server) facetTitle(ctx context.Context, t core.MediaType, name, value string) string {
	vals, err := s.facet(ctx, t, name)
	if err != nil {
		return value
	}
	for _, v := range vals {
		if v.Key == value {
			return v.Title
		}
	}
	return value
}

// byPlayID returns the transcode session ids fed by a playback session.
func (t *transcodeTable) byPlayID(playID string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var ids []string
	for id, e := range t.m {
		if e.playID == playID && e.started {
			ids = append(ids, id)
		}
	}
	return ids
}
