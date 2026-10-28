package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogEntry is one captured log record.
type LogEntry struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// LogBuffer is a concurrency-safe ring of recent log records. It backs the
// Logs page; install its Handler (usually teed with the console handler
// via TeeHandler) as the server logger.
type LogBuffer struct {
	mu      sync.Mutex
	entries []LogEntry
	next    int
	full    bool
	cap     int
}

// NewLogBuffer creates a ring holding at most capacity entries
// (default 2000 when capacity <= 0).
func NewLogBuffer(capacity int) *LogBuffer {
	if capacity <= 0 {
		capacity = 2000
	}
	return &LogBuffer{entries: make([]LogEntry, capacity), cap: capacity}
}

// Add appends an entry, evicting the oldest when full.
func (b *LogBuffer) Add(e LogEntry) {
	b.mu.Lock()
	b.entries[b.next] = e
	b.next++
	if b.next >= b.cap {
		b.next = 0
		b.full = true
	}
	b.mu.Unlock()
}

// Len reports how many entries are retained.
func (b *LogBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.full {
		return b.cap
	}
	return b.next
}

// Clear drops all entries.
func (b *LogBuffer) Clear() {
	b.mu.Lock()
	b.next, b.full = 0, false
	for i := range b.entries {
		b.entries[i] = LogEntry{}
	}
	b.mu.Unlock()
}

// Entries returns up to limit entries, oldest first. level filters to that
// level or more severe ("info" includes warn+error); q is a
// case-insensitive substring over message and attrs. Empty filters match all.
func (b *LogBuffer) Entries(limit int, level, q string) []LogEntry {
	b.mu.Lock()
	ordered := make([]LogEntry, 0, b.cap)
	if b.full {
		ordered = append(ordered, b.entries[b.next:]...)
		ordered = append(ordered, b.entries[:b.next]...)
	} else {
		ordered = append(ordered, b.entries[:b.next]...)
	}
	b.mu.Unlock()

	minLevel := parseLevel(level)
	q = strings.ToLower(strings.TrimSpace(q))
	out := ordered[:0:0]
	for _, e := range ordered {
		if minLevel != nil && levelRank(e.Level) < *minLevel {
			continue
		}
		if q != "" && !entryMatches(e, q) {
			continue
		}
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func entryMatches(e LogEntry, qLower string) bool {
	if strings.Contains(strings.ToLower(e.Message), qLower) {
		return true
	}
	for k, v := range e.Attrs {
		if strings.Contains(strings.ToLower(k+"="+v), qLower) {
			return true
		}
	}
	return false
}

func levelRank(level string) int {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return 0
	case "INFO":
		return 1
	case "WARN":
		return 2
	case "ERROR":
		return 3
	}
	return 1
}

func parseLevel(level string) *int {
	if strings.TrimSpace(level) == "" {
		return nil
	}
	r := levelRank(level)
	return &r
}

// Handler returns an slog.Handler that records into the buffer at
// slog.LevelDebug and above.
func (b *LogBuffer) Handler() slog.Handler {
	return &logHandler{buf: b, level: slog.LevelDebug}
}

type logHandler struct {
	buf   *LogBuffer
	level slog.Level
	attrs []slog.Attr
	group string
}

func (h *logHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *logHandler) Handle(_ context.Context, rec slog.Record) error {
	attrs := map[string]string{}
	for _, a := range h.attrs {
		flattenAttr(attrs, h.group, a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		flattenAttr(attrs, h.group, a)
		return true
	})
	h.buf.Add(LogEntry{
		Time:    rec.Time,
		Level:   rec.Level.String(),
		Message: rec.Message,
		Attrs:   attrs,
	})
	return nil
}

func flattenAttr(dst map[string]string, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, child := range a.Value.Group() {
			flattenAttr(dst, key, child)
		}
		return
	}
	dst[key] = fmt.Sprint(a.Value.Any())
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cloned := *h
	cloned.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &cloned
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	cloned := *h
	if cloned.group == "" {
		cloned.group = name
	} else {
		cloned.group = cloned.group + "." + name
	}
	return &cloned
}

// teeHandler forwards each record to several handlers.
type teeHandler struct{ handlers []slog.Handler }

// TeeHandler returns an slog.Handler that writes to next and records into
// buf (e.g. console + in-memory ring for the Logs page).
func TeeHandler(next slog.Handler, buf *LogBuffer) slog.Handler {
	hs := []slog.Handler{}
	if next != nil {
		hs = append(hs, next)
	}
	if buf != nil {
		hs = append(hs, buf.Handler())
	}
	return &teeHandler{handlers: hs}
}

func (t *teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range t.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (t *teeHandler) Handle(ctx context.Context, rec slog.Record) error {
	var firstErr error
	for _, h := range t.handlers {
		if h.Enabled(ctx, rec.Level) {
			if err := h.Handle(ctx, rec); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return &teeHandler{handlers: hs}
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		hs[i] = h.WithGroup(name)
	}
	return &teeHandler{handlers: hs}
}
