package plex

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

// ---------------------------------------------------------------------------
// Play session tracking (timeline-driven; feeds /status/sessions and
// "playing" notifications).

type playEntry struct {
	id              string // userID/clientID/sessionIdentifier
	sessionKey      int
	user            core.User
	client          Request // product/platform/device snapshot (no User)
	key             core.RatingKey
	state           string
	offset          time.Duration
	duration        time.Duration
	playQueueItemID int64
	transcodeID     string
	startedAt       time.Time
	updatedAt       time.Time
}

type sessionTracker struct {
	mu      sync.Mutex
	next    int
	entries map[string]*playEntry
}

func newSessionTracker() *sessionTracker { return &sessionTracker{entries: map[string]*playEntry{}} }

func trackID(userID int64, clientID, sessionID string) string {
	return fmt.Sprintf("%d/%s/%s", userID, clientID, sessionID)
}

// update records a timeline event and returns a copy of the entry.
func (t *sessionTracker) update(id string, fn func(e *playEntry)) playEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[id]
	if !ok {
		t.next++
		e = &playEntry{id: id, sessionKey: t.next}
		t.entries[id] = e
	}
	fn(e)
	return *e
}

func (t *sessionTracker) remove(id string) (playEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[id]
	if !ok {
		return playEntry{}, false
	}
	delete(t.entries, id)
	return *e, true
}

func (t *sessionTracker) list() []playEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]playEntry, 0, len(t.entries))
	for _, e := range t.entries {
		out = append(out, *e)
	}
	return out
}

func (t *sessionTracker) bySessionKey(k int) (playEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.entries {
		if e.sessionKey == k {
			return *e, true
		}
	}
	return playEntry{}, false
}

// expire drops entries without a timeline update for ttl and returns them.
func (t *sessionTracker) expire(now time.Time, ttl time.Duration) []playEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []playEntry
	for id, e := range t.entries {
		if now.Sub(e.updatedAt) > ttl {
			out = append(out, *e)
			delete(t.entries, id)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Notification fan-out

type subscriber struct {
	userID  int64
	owner   bool
	ch      chan notification
	filters map[string]bool // empty = all
}

type notification struct {
	typ  string
	data []byte // JSON NotificationContainer
}

type notifier struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
	done   chan struct{}
}

func newNotifier() *notifier {
	return &notifier{subs: map[*subscriber]struct{}{}, done: make(chan struct{})}
}

func (n *notifier) subscribe(filters []string, user *core.User) (*subscriber, bool) {
	sub := &subscriber{userID: user.ID, owner: user.IsOwner, ch: make(chan notification, 64), filters: map[string]bool{}}
	for _, f := range filters {
		if f = strings.TrimSpace(f); f != "" {
			sub.filters[f] = true
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, false
	}
	n.subs[sub] = struct{}{}
	return sub, true
}

func (n *notifier) unsubscribe(sub *subscriber) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.subs, sub)
}

// publish delivers to every interested subscriber, dropping for slow ones.
func (n *notifier) publish(nc model.NotificationContainer, userID int64) {
	data, err := model.Marshal(nc, model.JSON)
	if err != nil {
		return
	}
	data = []byte(strings.TrimSuffix(string(data), "\n"))
	msg := notification{typ: nc.Type, data: data}
	n.mu.Lock()
	defer n.mu.Unlock()
	for sub := range n.subs {
		if userID != 0 && sub.userID != userID && !sub.owner {
			continue
		}
		if len(sub.filters) > 0 && !sub.filters[nc.Type] {
			continue
		}
		select {
		case sub.ch <- msg:
		default:
		}
	}
}

func (n *notifier) close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.closed {
		n.closed = true
		close(n.done)
	}
}

func notificationFilters(r *http.Request) []string {
	q := r.URL.Query()
	v := q.Get("filters")
	if v == "" {
		v = q.Get("filter")
	}
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// handleWebsocket serves /:/websockets/notifications.
func (s *Server) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Plex Web connects from app.plex.tv; tokens, not origins, gate access.
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.log.Debug("websocket accept failed", "err", err)
		return
	}
	defer c.CloseNow()
	sub, ok := s.notifier.subscribe(notificationFilters(r), FromContext(r.Context()).User)
	if !ok {
		c.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	defer s.notifier.unsubscribe(sub)

	ctx := c.CloseRead(r.Context())
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.notifier.done:
			c.Close(websocket.StatusGoingAway, "server shutting down")
			return
		case <-ping.C:
			if s.activeViewer(r.Context(), sub.userID) == nil {
				return
			}
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case msg := <-sub.ch:
			if s.activeViewer(r.Context(), sub.userID) == nil {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, msg.data)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// handleEventSource serves /:/eventsource/notifications (Server-Sent Events).
func (s *Server) handleEventSource(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError)
		return
	}
	sub, ok := s.notifier.subscribe(notificationFilters(r), FromContext(r.Context()).User)
	if !ok {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	defer s.notifier.unsubscribe(sub)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 5000\n\n")
	fl.Flush()
	ping := time.NewTicker(10 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.notifier.done:
			return
		case <-ping.C:
			if s.activeViewer(r.Context(), sub.userID) == nil {
				return
			}
			if _, err := fmt.Fprint(w, "event: ping\ndata: {}\n\n"); err != nil {
				return
			}
			fl.Flush()
		case msg := <-sub.ch:
			if s.activeViewer(r.Context(), sub.userID) == nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.typ, msg.data); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// notifyPlaying publishes a PlaySessionStateNotification.
func (s *Server) notifyPlaying(e playEntry, state string) {
	if state == "" {
		state = e.state
	}
	s.notifier.publish(model.NotificationContainer{
		Type: "playing",
		Size: 1,
		PlaySessionStateNotification: []model.PlaySessionStateNotification{{
			SessionKey:       fmt.Sprint(e.sessionKey),
			ClientIdentifier: e.client.ClientID,
			GUID:             "",
			RatingKey:        e.key.String(),
			URL:              "",
			Key:              "/library/metadata/" + e.key.String(),
			ViewOffset:       ms(e.offset),
			PlayQueueItemID:  e.playQueueItemID,
			State:            state,
			TranscodeSession: e.transcodeID,
		}},
	}, e.user.ID)
}

// notifyTimeline publishes a library timeline entry (watched-state change).
func (s *Server) notifyTimeline(key core.RatingKey, t core.MediaType, title string, userID int64) {
	sec, _ := s.sectionFor(t)
	typeNum := map[core.MediaType]int{core.TypeMovie: 1, core.TypeShow: 2, core.TypeSeason: 3, core.TypeEpisode: 4}[t]
	s.notifier.publish(model.NotificationContainer{
		Type: "timeline",
		Size: 1,
		TimelineEntry: []model.TimelineEntry{{
			Identifier: model.LibraryIdentifier,
			SectionID:  fmt.Sprint(sec.ID),
			ItemID:     key.String(),
			Type:       typeNum,
			Title:      title,
			State:      5,
			UpdatedAt:  s.now().Unix(),
		}},
	}, userID)
}
