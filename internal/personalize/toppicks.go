package personalize

import (
	"fmt"

	"github.com/braedonsaunders/ether/internal/core"
)

// Every page opens with the strongest picks for the viewer. Without an owner
// policy saying otherwise, the lead row takes slot 1, Continue Watching
// follows, and fast-moving catalogue feeds (Recently Added, New &
// Noteworthy) sit lower down where they can't bury the picks.

// leadKinds are the rows that can open a page, best first: the viewer's
// personal Top 10, then the server's Top 10 Today for viewers the taste
// model doesn't know well enough yet.
var leadKinds = []string{KindTop10, KindTop10Today}

const (
	// continueMinSlot keeps Continue Watching right under the lead row.
	continueMinSlot = 2
	// feedMinSlot keeps catalogue feeds below the personal rows.
	recentlyAddedMinSlot = 8
	newReleasesMinSlot   = 5
)

// leadDefaults returns the engine's default placement for rows that no owner
// policy covers on this page: lead is the hub id of the row that opens the
// page ("" when none of the lead kinds is present or slot 1 is the owner's).
func leadDefaults(rows []core.HomeRow, g, v *core.LayoutPage, maxRows int) (lead string, defaults map[string]*core.RowPolicy) {
	defaults = map[string]*core.RowPolicy{}
	ownerSlot1 := false
	for _, p := range []*core.LayoutPage{g, v} {
		if p == nil {
			continue
		}
		for _, r := range p.Rows {
			if r.Slot == 1 && r.Status != "off" {
				ownerSlot1 = true
			}
		}
	}
	covered := func(r core.HomeRow) bool { return matchingPolicy(g, r) != nil || matchingPolicy(v, r) != nil }
	if !ownerSlot1 {
	pick:
		for _, k := range leadKinds {
			for _, r := range rows {
				if r.Kind == k && len(r.Items) > 0 && !covered(r) {
					lead = r.HubID
					break pick
				}
			}
		}
	}
	floor := func(n int) int { return max(1, min(n, maxRows)) }
	for _, r := range rows {
		if covered(r) {
			continue
		}
		switch {
		case r.HubID == lead:
			defaults[r.HubID] = &core.RowPolicy{Row: r.HubID, Status: "always", Slot: 1}
		case r.Kind == KindContinue && lead != "":
			defaults[r.HubID] = &core.RowPolicy{Row: r.HubID, Status: "eligible", MinSlot: continueMinSlot}
		case r.Kind == KindRecentlyAdded:
			defaults[r.HubID] = &core.RowPolicy{Row: r.HubID, Status: "eligible", MinSlot: floor(recentlyAddedMinSlot)}
		case r.Kind == KindNewReleases:
			defaults[r.HubID] = &core.RowPolicy{Row: r.HubID, Status: "eligible", MinSlot: floor(newReleasesMinSlot)}
		}
	}
	return lead, defaults
}

// headlineOK is the bar a title must clear to be one of the viewer's top
// picks: enough ratings to trust and, for viewers whose history shows the
// languages they watch, one of those languages. It keeps obscure titles and
// languages the viewer never watches out of the row that opens the page.
func (ts *Taste) headlineOK(t *core.Title) bool {
	if float64(t.VoteCount) < priorVotes(t.Type)/4 {
		return false
	}
	return ts.watchesLanguage(t.Language)
}

// watchesLanguage reports whether lang is one the viewer watches. With no
// language history every language passes.
func (ts *Taste) watchesLanguage(lang string) bool {
	if ts == nil || lang == "" || len(ts.Language) == 0 {
		return true
	}
	if ts.Language[lang] >= 0.15 {
		return true
	}
	top, best := "", 0.0
	for l, w := range ts.Language {
		if w > best {
			top, best = l, w
		}
	}
	return lang == top
}

// curateFeed trims a catalogue feed row (Recently Added) to titles the
// viewer could plausibly want: trusted ratings and languages they watch.
// The row keeps its order; a feed with almost nothing left is hidden.
func curateFeed(row core.HomeRow, ts *Taste) core.HomeRow {
	if ts == nil || len(row.Items) == 0 {
		return row
	}
	kept := row.Items[:0:0]
	for i := range row.Items {
		t := &row.Items[i]
		if !ts.watchesLanguage(t.Language) {
			continue
		}
		if t.Type == core.TypeMovie || t.Type == core.TypeShow {
			if float64(t.VoteCount) < priorVotes(t.Type)/10 {
				continue
			}
		}
		kept = append(kept, *t)
	}
	if len(kept) < 3 {
		row.Hidden = true
		row.HiddenReason = fmt.Sprintf("Only %d recent additions match this viewer", len(kept))
	}
	row.Items = kept
	return row
}
