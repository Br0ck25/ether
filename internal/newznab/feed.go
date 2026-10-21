package newznab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/braedonsaunders/ether/internal/core"
)

// FeedSources reports capabilities without making a request. Built-in sites
// remain searchable but are not treated as paginated feeds without that contract.
func (m *Manager) FeedSources() []core.ReleaseFeed {
	out := []core.ReleaseFeed{}
	for _, c := range m.Clients() {
		cfg := c.Config()
		if !cfg.Enabled {
			continue
		}
		raw, _ := json.Marshal(cfg)
		sum := sha256.Sum256(raw)
		f := core.ReleaseFeed{ID: c.indexerID(), Name: c.Name(), Generation: hex.EncodeToString(sum[:]), Protocol: cfg.Protocol, Supported: !cfg.IsBuiltin()}
		if !f.Supported {
			f.Reason = "This site does not expose a paginated recent-release feed; on-demand search remains available."
		}
		out = append(out, f)
	}
	return out
}

// Recent fetches one page, charging the same account-wide limits as Search.
// It never invokes title searches, capability discovery, or implicit retries.
func (m *Manager) Recent(ctx context.Context, id string, offset, limit int) ([]core.Release, error) {
	for _, c := range m.Clients() {
		if c.indexerID() != id {
			continue
		}
		cfg := c.Config()
		if !cfg.Enabled || cfg.IsBuiltin() {
			return nil, fmt.Errorf("recent-release feed is not enabled")
		}
		movies, tv := cfg.MovieCategories, cfg.TVCategories
		if len(movies) == 0 {
			movies = []int{2000}
		}
		if len(tv) == 0 {
			tv = []int{5000}
		}
		cats := append(append([]int{}, movies...), tv...)
		return c.doSearch(ctx, url.Values{"t": {"search"}, "extended": {"1"}, "cat": {joinCats(cats)}, "offset": {strconv.Itoa(max(0, offset))}, "limit": {strconv.Itoa(min(100, max(1, limit)))}})
	}
	return nil, fmt.Errorf("release-feed source no longer configured")
}
