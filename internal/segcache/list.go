package segcache

// entry is one cached segment. It is an intrusive node of exactly one lruList
// (Cache.lru or Cache.parked) while it is in the index. All fields are guarded
// by Cache.mu.
type entry struct {
	prev, next       *entry
	relPrev, relNext *entry
	rel              *release
	seg              int
	size             int64 // payload bytes (file size minus header)
	lastUsed         int64 // unix nanos; drives LRU order
	touched          int64 // unix nanos of the last mtime update on disk
	parked           bool  // true while in Cache.parked instead of Cache.lru
}

// lruList is a nil-terminated intrusive doubly linked list. head is the most
// recently used entry, tail the least. The zero value is an empty list and the
// struct is safe to copy (no self-referencing sentinel).
type lruList struct {
	head, tail *entry
	n          int
}

func (l *lruList) pushFront(e *entry) {
	e.prev = nil
	e.next = l.head
	if l.head != nil {
		l.head.prev = e
	} else {
		l.tail = e
	}
	l.head = e
	l.n++
}

func (l *lruList) pushBack(e *entry) {
	e.next = nil
	e.prev = l.tail
	if l.tail != nil {
		l.tail.next = e
	} else {
		l.head = e
	}
	l.tail = e
	l.n++
}

func (l *lruList) remove(e *entry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		l.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		l.tail = e.prev
	}
	e.prev, e.next = nil, nil
	l.n--
}
