// Package cluster is Ether's multi-node runtime: node identity, liveness
// heartbeats, leader election and a small cross-node event bus.
//
// A standalone server (SQLite) uses Solo: it is always the leader, its bus
// is in-process and it is the only member. A clustered node (PostgreSQL)
// uses Postgres: heartbeats in the cluster_nodes table, leadership through a
// PostgreSQL session advisory lock held on a dedicated connection, and an
// event bus over LISTEN/NOTIFY.
//
// Singleton work (catalogue sync, plex.tv publication, scheduled backups…)
// runs through RunAsLeader so exactly one node performs it at a time.
package cluster

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"
)

// Modes reported by Cluster.Mode.
const (
	ModeSolo     = "solo"
	ModePostgres = "postgres"
)

// Well-known topics.
const (
	// TopicMembers is published when a node joins, leaves or changes its
	// addresses. The payload is the node ID.
	TopicMembers = "cluster:members"
	// TopicResync is delivered locally (never sent to other nodes) after the
	// event bus reconnects: notifications may have been missed, so caches
	// that rely on bus invalidations should drop everything.
	TopicResync = "bus:resync"
)

// MaxPayload is the largest event payload Publish accepts. PostgreSQL
// limits a NOTIFY payload to 8000 bytes; the envelope (topic, sender) must
// fit as well.
const MaxPayload = 7 * 1024

// maxTopic bounds topic names.
const maxTopic = 64

var (
	// ErrPayloadTooLarge is returned by Publish for payloads over MaxPayload
	// (or whose encoded envelope would exceed PostgreSQL's NOTIFY limit).
	ErrPayloadTooLarge = errors.New("cluster: event payload too large (max 7 KB; PostgreSQL NOTIFY is limited to 8000 bytes)")
	// ErrBadTopic is returned by Publish for an empty, overlong or binary
	// topic.
	ErrBadTopic = errors.New("cluster: invalid event topic")
)

// Caps are a node's capabilities.
type Caps struct {
	// Transcode is true when ffmpeg is available on the node.
	Transcode bool `json:"transcode"`
	// HWAccel names the hardware transcoder ("videotoolbox", "nvenc", …);
	// empty or "none" for software only.
	HWAccel string `json:"hwAccel,omitempty"`
	// Weight is the node's share weight for account-wide budgets such as
	// Usenet connections (config server.clusterWeight; 0 means 1).
	Weight int `json:"weight,omitempty"`
}

// Node is one cluster member.
type Node struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// Addrs are the connection URIs this node would publish to plex.tv
	// (LAN, WAN and custom URLs, as the registrar builds them).
	Addrs     []string  `json:"addrs"`
	Caps      Caps      `json:"caps"`
	Load      int       `json:"load"` // active playback sessions
	StartedAt time.Time `json:"startedAt"`
	LastSeen  time.Time `json:"lastSeen"`
	Leader    bool      `json:"leader"`
	// Live is true when the node heartbeated recently (always true from
	// Nodes, may be false from AllNodes).
	Live bool `json:"live"`
}

// Cluster is the runtime the rest of the app uses.
type Cluster interface {
	// Mode is ModeSolo or ModePostgres.
	Mode() string
	// Self describes this node.
	Self() Node
	// IsLeader reports whether this node currently holds leadership.
	IsLeader() bool
	// Epoch is the leadership epoch this node acquired (0 while not
	// leading; always 1 for Solo). Every acquisition anywhere in the
	// cluster increments it, so a singleton writer can fence a write with
	// "... WHERE (SELECT epoch FROM cluster_leader WHERE id=1) = ?".
	Epoch() int64
	// OnLeaderChange calls fn (synchronously, from the election goroutine;
	// keep it quick) whenever leadership is gained or lost.
	OnLeaderChange(fn func(leader bool)) (unsubscribe func())
	// RunAsLeader runs fn while this node leads, cancelling its context as
	// soon as leadership is lost and starting it again when regained. It
	// blocks until ctx ends and the last fn has returned. A fn that returns
	// on its own is not restarted until leadership is lost and regained.
	RunAsLeader(ctx context.Context, name string, fn func(ctx context.Context))
	// Publish sends payload (≤ MaxPayload) to every subscriber of topic on
	// every node, this one included (local subscribers synchronously).
	Publish(ctx context.Context, topic string, payload []byte) error
	// Subscribe registers fn for topic. fn runs on the bus goroutine (or
	// the publisher's, for local events); it must not block and must treat
	// payload as read-only.
	Subscribe(topic string, fn func(payload []byte)) (unsubscribe func())
	// Nodes lists live members, ordered by name.
	Nodes(ctx context.Context) ([]Node, error)
	// AllNodes lists every recorded member, dead or alive, ordered by name.
	AllNodes(ctx context.Context) ([]Node, error)
	// Start begins heartbeats, election and the bus (no-op for Solo).
	Start(ctx context.Context) error
	// Close leaves the cluster: stops background work, releases
	// leadership and removes this node's heartbeat row.
	Close() error
}

// Local describes this node to the runtime. Addrs, Caps and Load are
// optional and read on every heartbeat.
type Local struct {
	Identity Identity
	Version  string
	Addrs    func(ctx context.Context) []string
	Caps     func() Caps
	Load     func() int
}

func (l Local) caps() Caps {
	if l.Caps == nil {
		return Caps{}
	}
	return l.Caps()
}

func (l Local) load() int {
	if l.Load == nil {
		return 0
	}
	return l.Load()
}

// PeerAddrs returns the connection URIs of every node except selfID that
// heartbeated within grace of now, ordered by node name (then ID) and
// de-duplicated. The registrar appends them to the leader's own URIs.
func PeerAddrs(nodes []Node, selfID string, now time.Time, grace time.Duration) []string {
	peers := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.ID == selfID || now.Sub(n.LastSeen) > grace {
			continue
		}
		peers = append(peers, n)
	}
	sort.SliceStable(peers, func(i, j int) bool {
		if peers[i].Name != peers[j].Name {
			return peers[i].Name < peers[j].Name
		}
		return peers[i].ID < peers[j].ID
	})
	var out []string
	for _, n := range peers {
		for _, a := range n.Addrs {
			if a != "" && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}
