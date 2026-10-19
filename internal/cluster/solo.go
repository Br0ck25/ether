package cluster

import (
	"context"
	"log/slog"
	"time"
)

// Solo is the standalone runtime: the only member, always the leader, with
// an in-process bus. It starts no goroutines.
type Solo struct {
	local   Local
	started time.Time
	bus     *localBus
	lead    *leadership
}

var _ Cluster = (*Solo)(nil)

// NewSolo returns the standalone runtime for local.
func NewSolo(local Local, log *slog.Logger) *Solo {
	if log == nil {
		log = slog.Default()
	}
	l := newLeadership()
	l.set(true, 1)
	return &Solo{local: local, started: time.Now(), bus: newLocalBus(log), lead: l}
}

func (s *Solo) Mode() string { return ModeSolo }

// Self describes this node; Addrs is read with a short timeout.
func (s *Solo) Self() Node {
	n := Node{
		ID: s.local.Identity.NodeID, Name: s.local.Identity.Name, Version: s.local.Version,
		Caps: s.local.caps(), Load: s.local.load(), StartedAt: s.started, LastSeen: time.Now(),
		Leader: true, Live: true,
	}
	if s.local.Addrs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		n.Addrs = s.local.Addrs(ctx)
		cancel()
	}
	return n
}

func (s *Solo) IsLeader() bool { return true }
func (s *Solo) Epoch() int64   { return 1 }

// OnLeaderChange never fires: a standalone server is always the leader.
func (s *Solo) OnLeaderChange(fn func(bool)) func() { return s.lead.subscribe(fn) }

// RunAsLeader runs fn(ctx) directly.
func (s *Solo) RunAsLeader(ctx context.Context, _ string, fn func(context.Context)) {
	fn(ctx)
}

// Publish delivers to local subscribers after the same validation the
// clustered bus applies, so oversized events fail in standalone tests too.
func (s *Solo) Publish(_ context.Context, topic string, payload []byte) error {
	if _, err := encodeEnvelope(topic, s.local.Identity.NodeID, "", payload); err != nil {
		return err
	}
	s.bus.deliver(topic, payload)
	return nil
}

func (s *Solo) Subscribe(topic string, fn func([]byte)) func() { return s.bus.subscribe(topic, fn) }

func (s *Solo) Nodes(context.Context) ([]Node, error)    { return []Node{s.Self()}, nil }
func (s *Solo) AllNodes(context.Context) ([]Node, error) { return []Node{s.Self()}, nil }
func (s *Solo) Start(context.Context) error              { return nil }
func (s *Solo) Close() error                             { return nil }
