package stream

import (
	"container/list"
	"sync"
)

// segKey identifies a decoded segment in memory: the release state (by NZB
// digest) and the global segment number.
type segKey struct {
	rel string
	seg int
}

// segData is a decoded segment plus where it sits in its posted file.
type segData struct {
	rangeExact bool // decoded length matches an explicit yEnc part range
	data       []byte
	begin      int64 // 0-based offset of data[0] in the posted file
	fileSize   int64 // 0 if unknown
	total      int   // parts in the post, 0 if unknown
	name       string
}

func (s *segData) end() int64 { return s.begin + int64(len(s.data)) }

// hotBuffer is a byte-bounded LRU of decoded segments shared by every
// source of an Engine.
type hotBuffer struct {
	mu    sync.Mutex
	limit int64
	used  int64
	ll    *list.List
	items map[segKey]*list.Element
}

type hotEntry struct {
	key segKey
	seg *segData
}

func newHotBuffer(limit int64) *hotBuffer {
	return &hotBuffer{limit: limit, ll: list.New(), items: map[segKey]*list.Element{}}
}

func (h *hotBuffer) get(k segKey) (*segData, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	el, ok := h.items[k]
	if !ok {
		return nil, false
	}
	h.ll.MoveToFront(el)
	return el.Value.(*hotEntry).seg, true
}

func (h *hotBuffer) has(k segKey) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.items[k]
	return ok
}

func (h *hotBuffer) put(k segKey, s *segData) {
	if h.limit <= 0 || int64(len(s.data)) > h.limit {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if el, ok := h.items[k]; ok {
		old := el.Value.(*hotEntry)
		h.used += int64(len(s.data)) - int64(len(old.seg.data))
		old.seg = s
		h.ll.MoveToFront(el)
	} else {
		h.items[k] = h.ll.PushFront(&hotEntry{key: k, seg: s})
		h.used += int64(len(s.data))
	}
	for h.used > h.limit {
		el := h.ll.Back()
		if el == nil {
			break
		}
		e := el.Value.(*hotEntry)
		h.ll.Remove(el)
		delete(h.items, e.key)
		h.used -= int64(len(e.seg.data))
	}
}

// dropRelease removes every segment of a release state.
func (h *hotBuffer) dropRelease(rel string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for el := h.ll.Front(); el != nil; {
		next := el.Next()
		e := el.Value.(*hotEntry)
		if e.key.rel == rel {
			h.ll.Remove(el)
			delete(h.items, e.key)
			h.used -= int64(len(e.seg.data))
		}
		el = next
	}
}

func (h *hotBuffer) usage() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.used
}
