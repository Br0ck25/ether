package playback

import (
	"context"
	"strconv"

	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/core"
)

// Cluster-wide title invalidation. Every node pre-resolves detail pages
// into its own warm entries, so an owner change that must reach the next
// Play (a pin, unpin, block, unblock or forgotten search made on one node)
// is broadcast as "invalidate:playback-title" with the rating key, and
// every other node drops its warm entries for that title. A bus resync
// drops every warm entry. A standalone server has no bus and only drops
// its own entries.

// startTitleInvalidation subscribes when the Manager is clustered.
func (m *Manager) startTitleInvalidation(bus clusterbus.Bus) {
	m.titleInval = clusterbus.NewInvalidator(bus, "playback-title", m.log, m.applyTitleInvalidation)
}

// InvalidateTitleEverywhere is InvalidateTitle on this node and, in a
// cluster, on every other node. It returns how many local entries were
// dropped.
func (m *Manager) InvalidateTitleEverywhere(key core.RatingKey) int {
	n := m.InvalidateTitle(key)
	if key != 0 {
		m.titleInval.Notify(strconv.FormatInt(int64(key), 10))
	}
	return n
}

func (m *Manager) applyTitleInvalidation(_ context.Context, hints []string, all bool) {
	if all {
		m.mu.Lock()
		clear(m.warm)
		m.mu.Unlock()
		return
	}
	for _, h := range hints {
		if k, err := strconv.ParseInt(h, 10, 64); err == nil {
			m.InvalidateTitle(core.RatingKey(k))
		}
	}
}
