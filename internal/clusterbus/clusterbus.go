// Package clusterbus is the small surface components use to take part in a
// cluster: publishing and receiving events on the cluster bus, knowing their
// own node ID and which nodes are alive, and dropping in-memory caches when
// another node writes the shared database (Invalidator).
//
// Components take a Bus or Node through a setter or constructor option and
// treat nil as "standalone": a standalone server never publishes, so its
// behaviour is exactly that of a single process. internal/app adapts the
// cluster runtime with FromCluster, and only when the database is shared.
package clusterbus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/braedonsaunders/ether/internal/cluster"
)

// TopicResync is delivered locally after the bus (re)connected: events may
// have been missed, so every bus-maintained cache must be dropped.
const TopicResync = cluster.TopicResync

// MaxPayload is the largest event payload the bus carries.
const MaxPayload = cluster.MaxPayload

// Bus publishes and receives cluster events. Publish also delivers to this
// node's own subscribers (synchronously), so receivers that must ignore
// their own events tag payloads with an Origin.
type Bus interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	Subscribe(topic string, fn func(payload []byte)) (unsubscribe func())
}

// Node is a Bus plus this node's identity and the cluster's liveness view.
type Node interface {
	Bus
	// NodeID is this node's stable ID.
	NodeID() string
	// LiveNodes is the set of node IDs that heartbeated recently.
	LiveNodes(ctx context.Context) (map[string]bool, error)
}

type adapter struct{ c cluster.Cluster }

// FromCluster adapts the cluster runtime.
func FromCluster(c cluster.Cluster) Node { return adapter{c} }

func (a adapter) Publish(ctx context.Context, topic string, payload []byte) error {
	return a.c.Publish(ctx, topic, payload)
}
func (a adapter) Subscribe(topic string, fn func([]byte)) func() { return a.c.Subscribe(topic, fn) }
func (a adapter) NodeID() string                                 { return a.c.Self().ID }
func (a adapter) LiveNodes(ctx context.Context) (map[string]bool, error) {
	nodes, err := a.c.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.Live {
			out[n.ID] = true
		}
	}
	return out, nil
}

// NewOrigin returns a random ID for one component instance, used to
// recognise (and skip) its own events when the bus echoes them back.
func NewOrigin() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Tag prefixes payload with origin ("origin\npayload").
func Tag(origin string, payload []byte) []byte {
	out := make([]byte, 0, len(origin)+1+len(payload))
	out = append(out, origin...)
	out = append(out, '\n')
	return append(out, payload...)
}

// Untag splits a Tag-ged payload.
func Untag(msg []byte) (origin string, payload []byte) {
	i := bytes.IndexByte(msg, '\n')
	if i < 0 {
		return string(msg), nil
	}
	return string(msg[:i]), msg[i+1:]
}

// PublishTimeout bounds one Publish made on behalf of a local write.
const PublishTimeout = 5 * time.Second
