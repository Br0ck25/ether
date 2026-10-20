package clusterbus

import (
	"context"
	"errors"
	"sync"
)

// Hub is an in-memory cluster bus shared by several nodes in one process,
// for tests: Publish delivers synchronously to every subscriber on every
// node (the publisher's own included, as the PostgreSQL bus does) and
// enforces MaxPayload. Nodes are live until Kill.
type Hub struct {
	mu    sync.RWMutex
	next  int
	subs  map[string]map[int]hubSub
	nodes map[string]bool // id -> live
}

type hubSub struct {
	node string
	fn   func([]byte)
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[string]map[int]hubSub{}, nodes: map[string]bool{}} }

// Node joins the hub as id.
func (h *Hub) Node(id string) Node {
	h.mu.Lock()
	h.nodes[id] = true
	h.mu.Unlock()
	return &hubNode{h: h, id: id}
}

// Kill marks a node dead: it drops out of LiveNodes and receives nothing.
func (h *Hub) Kill(id string) {
	h.mu.Lock()
	h.nodes[id] = false
	h.mu.Unlock()
}

// Resync delivers TopicResync to node id's subscribers, as a bus reconnect
// would.
func (h *Hub) Resync(id string) { h.deliver(TopicResync, nil, id) }

func (h *Hub) deliver(topic string, payload []byte, only string) {
	h.mu.RLock()
	var fns []func([]byte)
	for _, s := range h.subs[topic] {
		if !h.nodes[s.node] || (only != "" && s.node != only) {
			continue
		}
		fns = append(fns, s.fn)
	}
	h.mu.RUnlock()
	for _, fn := range fns {
		fn(append([]byte(nil), payload...))
	}
}

type hubNode struct {
	h  *Hub
	id string
}

// ErrPayloadTooLarge mirrors the cluster bus limit.
var ErrPayloadTooLarge = errors.New("clusterbus: payload too large")

func (n *hubNode) Publish(_ context.Context, topic string, payload []byte) error {
	if len(payload) > MaxPayload {
		return ErrPayloadTooLarge
	}
	n.h.mu.RLock()
	live := n.h.nodes[n.id]
	n.h.mu.RUnlock()
	if !live {
		return errors.New("clusterbus: node is dead")
	}
	n.h.deliver(topic, payload, "")
	return nil
}

func (n *hubNode) Subscribe(topic string, fn func([]byte)) func() {
	h := n.h
	h.mu.Lock()
	h.next++
	id := h.next
	if h.subs[topic] == nil {
		h.subs[topic] = map[int]hubSub{}
	}
	h.subs[topic][id] = hubSub{node: n.id, fn: fn}
	h.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs[topic], id)
			h.mu.Unlock()
		})
	}
}

func (n *hubNode) NodeID() string { return n.id }

func (n *hubNode) LiveNodes(context.Context) (map[string]bool, error) {
	n.h.mu.RLock()
	defer n.h.mu.RUnlock()
	out := map[string]bool{}
	for id, live := range n.h.nodes {
		if live {
			out[id] = true
		}
	}
	return out, nil
}
