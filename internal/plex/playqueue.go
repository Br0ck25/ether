package plex

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

// Play queues are what every Plex app plays from: the app POSTs
// /playQueues with a content URI, then plays the selected item and walks
// the queue for "next". They are per-device and short-lived, so Ether keeps
// them in memory.

type pqItem struct {
	id  int64
	key core.RatingKey
}

type playQueue struct {
	id            int64
	userID        int64
	clientID      string
	items         []pqItem
	original      []pqItem // order before shuffle
	selected      int      // index into items
	shuffled      bool
	sourceURI     string
	playlistID    core.RatingKey
	playlistTitle string
	version       int
	updatedAt     time.Time
}

type playQueueStore struct {
	mu     sync.Mutex
	nextID int64
	nextIt int64
	queues map[int64]*playQueue
}

const maxQueues = 2000

func newPlayQueueStore() *playQueueStore {
	return &playQueueStore{nextID: 1, nextIt: 1, queues: map[int64]*playQueue{}}
}

func (s *playQueueStore) newItemsLocked(keys []core.RatingKey) []pqItem {
	out := make([]pqItem, len(keys))
	for i, k := range keys {
		out[i] = pqItem{id: s.nextIt, key: k}
		s.nextIt++
	}
	return out
}

func (s *playQueueStore) create(userID int64, clientID string, keys []core.RatingKey, now time.Time) *playQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queues) >= maxQueues {
		// Drop the oldest.
		var oldest *playQueue
		for _, q := range s.queues {
			if oldest == nil || q.updatedAt.Before(oldest.updatedAt) {
				oldest = q
			}
		}
		delete(s.queues, oldest.id)
	}
	q := &playQueue{id: s.nextID, userID: userID, clientID: clientID, items: s.newItemsLocked(keys), version: 1, updatedAt: now}
	s.nextID++
	s.queues[q.id] = q
	return q
}

// with runs fn on the queue under the store lock and returns a snapshot.
func (s *playQueueStore) with(id, userID int64, now time.Time, fn func(q *playQueue)) (playQueue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queues[id]
	if !ok || q.userID != userID {
		return playQueue{}, false
	}
	if fn != nil {
		fn(q)
		q.updatedAt = now
	}
	return q.snapshot(), true
}

func (q *playQueue) snapshot() playQueue {
	cp := *q
	cp.items = append([]pqItem(nil), q.items...)
	cp.original = append([]pqItem(nil), q.original...)
	return cp
}

func (s *playQueueStore) get(id, userID int64) (playQueue, bool) {
	return s.with(id, userID, time.Time{}, nil)
}

// selectItem marks the item a client reports playing (timeline).
func (s *playQueueStore) selectItem(userID, itemID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.queues {
		if q.userID != userID {
			continue
		}
		for i, it := range q.items {
			if it.id == itemID {
				q.selected = i
				return
			}
		}
	}
}

func (s *playQueueStore) expire(now time.Time, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, q := range s.queues {
		if now.Sub(q.updatedAt) > ttl {
			delete(s.queues, id)
		}
	}
}

// addLocked appends (or inserts after the selected item); the caller holds
// s.mu (it runs inside with).
func (s *playQueueStore) addLocked(q *playQueue, keys []core.RatingKey, next bool) {
	items := s.newItemsLocked(keys)
	if next && len(q.items) > 0 {
		at := q.selected + 1
		q.items = append(q.items[:at], append(items, q.items[at:]...)...)
	} else {
		q.items = append(q.items, items...)
	}
	if q.shuffled {
		q.original = append(q.original, items...)
	}
	q.version++
}

// ---------------------------------------------------------------------------
// Handlers

func (s *Server) handlePlayQueueCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pr := FromContext(ctx)
	if pr.User == nil {
		writeHTTPError(w, http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	if t := q.Get("type"); t != "" && t != "video" {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	var keys []core.RatingKey
	var plKey core.RatingKey
	var plTitle string
	uri := q.Get("uri")
	switch {
	case q.Get("playlistID") != "":
		k, err := core.ParseRatingKey(q.Get("playlistID"))
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest)
			return
		}
		pl, err := s.playlist(ctx, pr.User, k)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		keys, plKey, plTitle = append([]core.RatingKey(nil), pl.Items...), pl.Key, pl.Title
		if uri == "" {
			uri = "library://playlist/item/%2Fplaylists%2F" + pl.Key.String()
		}
	case uri != "":
		var err error
		keys, err = s.continuousKeys(ctx, pr, uri, q.Get("continuous") == "1")
		if err != nil {
			s.log.Debug("play queue uri", "uri", uri, "err", err)
			if errors.Is(err, core.ErrNotFound) {
				writeHTTPError(w, http.StatusNotFound)
			} else {
				writeHTTPError(w, http.StatusBadRequest)
			}
			return
		}
	default:
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	if len(keys) == 0 {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}
	pq := s.queues.create(pr.User.ID, pr.ClientID, keys, s.now())
	snap, _ := s.queues.with(pq.id, pr.User.ID, s.now(), func(p *playQueue) {
		p.sourceURI = uri
		p.playlistID, p.playlistTitle = plKey, plTitle
		if start := keyFromPath(q.Get("key")); start != 0 {
			for i, it := range p.items {
				if it.key == start {
					p.selected = i
					break
				}
			}
		}
		if q.Get("shuffle") == "1" {
			shuffleQueue(p)
		}
	})
	s.writePlayQueue(w, r, snap)
}

// continuousKeys resolves a play queue URI; with continuous=1 a single
// episode expands to the rest of its show.
func (s *Server) continuousKeys(ctx context.Context, pr *Request, uri string, continuous bool) ([]core.RatingKey, error) {
	keys, err := s.resolveURI(ctx, pr, uri)
	if err != nil {
		return nil, err
	}
	if !continuous || len(keys) != 1 || keys[0].Kind() != core.KindEpisode {
		return keys, nil
	}
	ep := keys[0]
	show := ep.Parent().Parent()
	eps, err := s.metadataGrandchildren(ctx, pr, show)
	if err != nil {
		return keys, nil
	}
	for i, e := range eps {
		if e.Key == ep {
			return capKeys(titleKeys(s.visibleTitles(pr.User, eps[i:]))), nil
		}
	}
	return keys, nil
}

// keyFromPath extracts a rating key from "/library/metadata/123" or "123".
func keyFromPath(p string) core.RatingKey {
	if p == "" {
		return 0
	}
	if i := strings.Index(p, "/library/metadata/"); i >= 0 {
		p = p[i+len("/library/metadata/"):]
	}
	p, _, _ = strings.Cut(p, "/")
	p, _, _ = strings.Cut(p, "?")
	k, err := core.ParseRatingKey(p)
	if err != nil {
		return 0
	}
	return k
}

func shuffleQueue(p *playQueue) {
	if p.shuffled || len(p.items) < 2 {
		return
	}
	p.original = append([]pqItem(nil), p.items...)
	sel := p.items[p.selected]
	rest := append(append([]pqItem(nil), p.items[:p.selected]...), p.items[p.selected+1:]...)
	rand.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	p.items = append([]pqItem{sel}, rest...)
	p.selected = 0
	p.shuffled = true
	p.version++
}

func unshuffleQueue(p *playQueue) {
	if !p.shuffled {
		return
	}
	sel := p.items[p.selected].id
	present := map[int64]bool{}
	for _, it := range p.items {
		present[it.id] = true
	}
	var items []pqItem
	for _, it := range p.original {
		if present[it.id] {
			items = append(items, it)
		}
	}
	p.items, p.original, p.shuffled = items, nil, false
	for i, it := range p.items {
		if it.id == sel {
			p.selected = i
		}
	}
	p.version++
}

func (s *Server) pqParam(w http.ResponseWriter, r *http.Request) (int64, *core.User, bool) {
	u := FromContext(r.Context()).User
	id, err := strconv.ParseInt(chi.URLParam(r, "pq"), 10, 64)
	if err != nil || u == nil {
		writeHTTPError(w, http.StatusNotFound)
		return 0, nil, false
	}
	return id, u, true
}

func (s *Server) handlePlayQueue(w http.ResponseWriter, r *http.Request) {
	id, u, ok := s.pqParam(w, r)
	if !ok {
		return
	}
	snap, ok := s.queues.get(id, u.ID)
	if !ok {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	s.writePlayQueue(w, r, snap)
}

// handlePlayQueueMutate covers PUT /playQueues/{id} (add) and the
// shuffle/unshuffle/reset/clear/remove/move sub-resources.
func (s *Server) handlePlayQueueMutate(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, u, ok := s.pqParam(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		pr := FromContext(ctx)
		q := r.URL.Query()
		var addKeys []core.RatingKey
		if op == "add" {
			var err error
			switch {
			case q.Get("uri") != "":
				addKeys, err = s.resolveURI(ctx, pr, q.Get("uri"))
			case q.Get("playlistID") != "":
				var k core.RatingKey
				if k, err = core.ParseRatingKey(q.Get("playlistID")); err == nil {
					addKeys, err = s.expandKey(ctx, pr, k)
				}
			}
			if err != nil {
				writeHTTPError(w, http.StatusBadRequest)
				return
			}
		}
		itemID, _ := strconv.ParseInt(chi.URLParam(r, "item"), 10, 64)
		var bad bool
		snap, ok := s.queues.with(id, u.ID, s.now(), func(p *playQueue) {
			switch op {
			case "add":
				s.queues.addLocked(p, addKeys, q.Get("next") == "1")
			case "shuffle":
				shuffleQueue(p)
			case "unshuffle":
				unshuffleQueue(p)
			case "reset":
				p.selected = 0
				p.version++
			case "clear":
				// Keep the playing item, drop the rest.
				if len(p.items) > 0 {
					p.items = []pqItem{p.items[p.selected]}
				}
				p.selected = 0
				p.original = nil
				p.version++
			case "remove":
				idx := indexOfItem(p.items, itemID)
				if idx < 0 {
					bad = true
					return
				}
				p.items = append(p.items[:idx], p.items[idx+1:]...)
				if idx < p.selected || p.selected >= len(p.items) {
					p.selected = max(0, p.selected-1)
				}
				p.version++
			case "move":
				idx := indexOfItem(p.items, itemID)
				if idx < 0 {
					bad = true
					return
				}
				selID := p.items[p.selected].id
				it := p.items[idx]
				rest := append(append([]pqItem(nil), p.items[:idx]...), p.items[idx+1:]...)
				to := 0
				if after, err := strconv.ParseInt(q.Get("after"), 10, 64); err == nil && after > 0 {
					if a := indexOfItem(rest, after); a >= 0 {
						to = a + 1
					}
				}
				p.items = append(rest[:to], append([]pqItem{it}, rest[to:]...)...)
				p.selected = max(0, indexOfItem(p.items, selID))
				p.version++
			}
		})
		if !ok || bad {
			writeHTTPError(w, http.StatusNotFound)
			return
		}
		s.writePlayQueue(w, r, snap)
	}
}

func indexOfItem(items []pqItem, id int64) int {
	for i, it := range items {
		if it.id == id {
			return i
		}
	}
	return -1
}

// writePlayQueue renders a queue window around the selected item.
func (s *Server) writePlayQueue(w http.ResponseWriter, r *http.Request, pq playQueue) {
	ctx := r.Context()
	pr := FromContext(ctx)
	q := r.URL.Query()
	window := 50
	if n, err := strconv.Atoi(q.Get("window")); err == nil && n > 0 {
		window = min(n, 200)
	}
	lo := 0
	if q.Get("includeBefore") != "0" {
		lo = max(0, pq.selected-window/2)
	} else {
		lo = pq.selected
	}
	hi := min(len(pq.items), lo+window)
	if q.Get("includeAfter") == "0" {
		hi = min(len(pq.items), pq.selected+1)
	}
	if lo > len(pq.items) {
		lo = len(pq.items)
	}
	slice := pq.items[lo:hi]
	keys := make([]core.RatingKey, len(slice))
	for i, it := range slice {
		keys[i] = it.key
	}
	titles := s.getTitles(ctx, dedupeKeys(keys))
	byKey := make(map[core.RatingKey]*core.Title, len(titles))
	for i := range titles {
		byKey[titles[i].Key] = &titles[i]
	}
	state := s.stateFor(ctx, pr, titles)

	var selItem pqItem
	if pq.selected < len(pq.items) {
		selItem = pq.items[pq.selected]
	}
	mc := libraryContainer()
	mc.Extra = []model.Attr{
		model.A("playQueueID", pq.id),
		model.A("playQueueSelectedItemID", selItem.id),
		model.A("playQueueSelectedItemOffset", pq.selected),
		model.A("playQueueSelectedMetadataItemID", selItem.key.String()),
		model.A("playQueueShuffled", pq.shuffled),
		model.A("playQueueSourceURI", pq.sourceURI),
		model.A("playQueueTotalCount", len(pq.items)),
		model.A("playQueueVersion", pq.version),
	}
	if pq.playlistID != 0 {
		mc.Extra = append(mc.Extra, model.A("playQueuePlaylistID", pq.playlistID.String()), model.A("playQueuePlaylistTitle", pq.playlistTitle))
	}
	for _, it := range slice {
		t, ok := byKey[it.key]
		if !ok {
			continue
		}
		md := s.metadata(ctx, pr, t, renderOpts{detail: true, media: true, state: state})
		md.PlayQueueItemID = it.id
		mc.Metadata = append(mc.Metadata, md)
	}
	mc.Size = len(mc.Metadata)
	s.write(w, r, mc)
}

func dedupeKeys(ks []core.RatingKey) []core.RatingKey {
	seen := make(map[core.RatingKey]bool, len(ks))
	out := ks[:0:0]
	for _, k := range ks {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}
