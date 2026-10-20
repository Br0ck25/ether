package personalize

import (
	"context"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Cross-surface dedupe. A Movies or TV Shows page (with DedupeHome on, the
// default) never repeats what the viewer's Home leads with in the same
// context: the titles in the first rows a person actually sees there, and
// the Home's own seeded rows (the same Because-you-watched seed, the same
// person). It is computed from the memoised Home assembly and the same
// layout pass the front end runs, so it is deterministic and cheap.

// Home lead window: the first leadRows rows of the Home as arranged for
// its page length, and the first leadItems titles of each.
const (
	leadRows  = 6
	leadItems = 10
)

// homeLead is what the Home leads with.
type homeLead struct {
	keys map[core.RatingKey]bool // taste keys (episodes fold onto shows)
	rows map[string]bool         // row ids without the screen prefix
	sig  uint64
}

// homeLeadFor computes the Home lead for a section request: the Home
// assembly for the same viewer and context (memoised, or the draft layout
// in a preview), arranged for the Home's page length.
func (e *Engine) homeLeadFor(ctx context.Context, user *core.User, hc core.HubContext, layout core.HomeLayout, limit int, wait bool) *homeLead {
	hhc := hc
	hhc.SectionID = 0
	p := e.home(ctx, user, hhc, "", limit, wait)
	rows := arrangeLayout(p.Rows, layout, userID(user), hhc, surfHome, e.opts.MaxHomeRows)
	return leadFromRows(rows, min(limit, leadItems))
}

// leadFromRows collects the lead from arranged Home rows.
func leadFromRows(rows []core.HomeRow, items int) *homeLead {
	l := &homeLead{keys: map[core.RatingKey]bool{}, rows: map[string]bool{}}
	seen := 0
	for _, r := range rows {
		if r.Hidden {
			continue
		}
		if seen >= leadRows {
			break
		}
		seen++
		if r.Source == core.RowSourcePlex {
			continue // Continue Watching: in-progress titles never recur as picks
		}
		l.rows[strings.TrimPrefix(r.HubID, "home.")] = true
		for i, t := range r.Items {
			if i >= items {
				break
			}
			l.keys[tasteKey(t.Key)] = true
		}
	}
	h := fnv.New64a()
	keys := make([]int64, 0, len(l.keys))
	for k := range l.keys {
		keys = append(keys, int64(k))
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		_, _ = h.Write([]byte(strconv.FormatInt(k, 36) + ","))
	}
	ids := make([]string, 0, len(l.rows))
	for id := range l.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		_, _ = h.Write([]byte(id + ";"))
	}
	l.sig = h.Sum64() | 1 // never 0, which means "no dedupe"
	return l
}

// holdBackHomeLead removes the Home lead's titles from a ranked row. A row
// the exclusion would leave too thin keeps them at the back instead and is
// demoted (surfaceMult).
func (a *assembly) holdBackHomeLead(c *candRow, items []candItem) []candItem {
	if a.lead == nil || len(a.lead.keys) == 0 || c.source == core.RowSourcePlex {
		return items
	}
	if a.demoted == nil {
		a.demoted = map[*candRow]bool{}
	}
	if a.held == nil {
		a.held = map[*candRow][]core.RatingKey{}
	}
	kept := make([]candItem, 0, len(items))
	var held []candItem
	var visible []core.RatingKey
	cap := a.rowCap(c)
	for i, it := range items {
		if a.lead.keys[tasteKey(it.t.Key)] {
			held = append(held, it)
			if i < cap {
				visible = append(visible, it.t.Key)
			}
			continue
		}
		kept = append(kept, it)
	}
	if len(held) == 0 {
		return items
	}
	if len(kept) < max(c.minItems, 4) {
		a.demoted[c] = true
		return append(kept, held...)
	}
	a.held[c] = visible
	return kept
}

// countHeld counts the distinct Home-lead titles kept off rows that made
// it onto the page (core.HomePreview.HomeDeduped).
func (a *assembly) countHeld(out []core.HomeRow) {
	on := map[string]bool{}
	for _, r := range out {
		on[r.HubID] = true
	}
	keys := map[core.RatingKey]bool{}
	for c, ks := range a.held {
		if !on[a.hubID(c)] {
			continue
		}
		for _, k := range ks {
			keys[k] = true
		}
	}
	a.homeDeduped = len(keys)
}
