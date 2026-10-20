package app

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/cluster"
	"github.com/braedonsaunders/ether/internal/usenet"
)

// Account-wide outbound budgets in a cluster.
//
// Usenet providers limit connections per account, so every node opening
// the configured count would multiply it by the number of nodes. A
// clustered node divides each provider's configured connections among the
// live nodes by weight (usenet.Shares, config server.clusterWeight) and
// resizes its pools whenever membership changes and every budgetPoll (which
// also catches crashed nodes expiring). A standalone server keeps the
// configured counts.

// budgetPoll re-reads membership for budget shares.
const budgetPoll = 30 * time.Second

// budgetMembers is the live membership budgets are divided over.
type budgetMembers struct {
	self    string
	weights map[string]int // node ID -> weight
}

// liveBudgetMembers reads the live nodes; self is always included (its own
// heartbeat may not be visible yet).
func liveBudgetMembers(nodes []cluster.Node, self cluster.Node) budgetMembers {
	m := budgetMembers{self: self.ID, weights: map[string]int{}}
	for _, n := range nodes {
		if n.Live {
			m.weights[n.ID] = n.Caps.Weight
		}
	}
	m.weights[self.ID] = self.Caps.Weight
	return m
}

// applyBudgets divides the account-wide budgets over m.
func (a *App) applyBudgets(m budgetMembers) {
	a.fetcher.SetConnectionShare(func(configured int) int {
		return usenet.ShareOf(configured, m.self, m.weights)
	})
}

// runBudgets keeps this node's budget shares in step with membership
// (clustered only).
func (a *App) runBudgets(ctx context.Context) {
	if !a.clustered() {
		return
	}
	poke := make(chan struct{}, 1)
	defer a.cluster.Subscribe(cluster.TopicMembers, func([]byte) {
		select {
		case poke <- struct{}{}:
		default:
		}
	})()
	t := time.NewTicker(budgetPoll)
	defer t.Stop()
	last := ""
	for {
		nodes, err := a.cluster.Nodes(ctx)
		if err == nil {
			m := liveBudgetMembers(nodes, a.cluster.Self())
			if key := m.key(); key != last {
				last = key
				a.log.Info("cluster: dividing account-wide budgets", "nodes", len(m.weights))
				a.applyBudgets(m)
			}
		} else if ctx.Err() == nil {
			a.log.Debug("cluster: read members for budgets", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-poke:
		}
	}
}

// key identifies the membership and weights, to skip no-op updates.
func (m budgetMembers) key() string {
	ids := slices.Sorted(maps.Keys(m.weights))
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%s=%d;", id, m.weights[id])
	}
	return b.String()
}
