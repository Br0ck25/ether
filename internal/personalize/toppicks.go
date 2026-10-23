package personalize

import (
	"fmt"

	"github.com/braedonsaunders/ether/internal/core"
)

// Continue Watching opens discovery when populated, followed by personal picks,
// new releases, trending, and a viewing-history row. These
// are defaults only: explicit owner/viewer policies reserve their own slots.
var leadKinds = []string{KindTop10, KindTop10Today}

const recentlyAddedMinSlot = 8

func discoveryPriority(kind string) int {
	switch kind {
	case KindContinue:
		return 0
	case KindTop10:
		return 1
	case KindTop10Today:
		return 2
	case KindNewReleases:
		return 3
	case KindTrending:
		return 4
	case KindBecause, KindCrowd:
		return 5
	default:
		return 6
	}
}

func rowHasItems(r core.HomeRow) bool {
	return len(r.Items) > 0 || r.Hub.ItemCount > 0
}

func leadDefaults(rows []core.HomeRow, g, v *core.LayoutPage, maxRows int) (lead string, defaults map[string]*core.RowPolicy) {
	defaults = map[string]*core.RowPolicy{}
	covered := func(r core.HomeRow) bool { return matchingPolicy(g, r) != nil || matchingPolicy(v, r) != nil }
	reserved := map[int]bool{}
	for _, r := range rows {
		p := matchingPolicy(g, r)
		if q := matchingPolicy(v, r); q != nil {
			p = q
		}
		if p != nil && p.Status != "off" {
			if p.Slot > 0 {
				reserved[p.Slot] = true
			} else if p.MaxSlot > 0 {
				// Leave bounded owner placements room before default anchors.
				for n := max(1, p.MinSlot); n <= min(maxRows, p.MaxSlot); n++ {
					reserved[n] = true
				}
			}
		}
	}
	slot := 1
	place := func(r core.HomeRow) {
		for reserved[slot] {
			slot++
		}
		if slot > maxRows {
			return
		}
		defaults[r.HubID] = &core.RowPolicy{Row: r.HubID, Status: "always", Slot: slot}
		reserved[slot] = true
		slot++
	}
	groups := [][]string{{KindContinue}, leadKinds, {KindNewReleases}, {KindTrending}, {KindBecause, KindCrowd}}
	for _, kinds := range groups {
		// Only one headline per purpose; the remaining history/theme rows
		// keep their stable personalized order further down the page.
		found := false
		for _, kind := range kinds {
			for _, r := range rows {
				if r.Kind != kind || !rowHasItems(r) || covered(r) {
					continue
				}
				place(r)
				if lead == "" {
					lead = r.HubID
				}
				found = true
				break
			}
			if found {
				break
			}
		}
	}
	for _, r := range rows {
		if r.Kind == KindRecentlyAdded && !covered(r) {
			defaults[r.HubID] = &core.RowPolicy{Row: r.HubID, Status: "eligible", MinSlot: max(1, min(recentlyAddedMinSlot, maxRows))}
		}
	}
	return lead, defaults
}

// mixDiscoveryTypes preserves ranking within each type while interleaving
// movies and shows on Home. A sparse type never forces a short row. Existing
// presentations reserve their cards separately, so feedback does not shuffle
// the shelves a viewer is already browsing.
func mixDiscoveryTypes(items []candItem) []candItem {
	var movies, shows []candItem
	for _, it := range items {
		switch it.t.Type {
		case core.TypeMovie:
			movies = append(movies, it)
		case core.TypeShow:
			shows = append(shows, it)
		default:
			return items
		}
	}
	if len(movies) == 0 || len(shows) == 0 {
		return items
	}
	out := make([]candItem, 0, len(items))
	movieFirst := items[0].t.Type == core.TypeMovie
	for len(movies) > 0 || len(shows) > 0 {
		if len(movies) > 0 && (movieFirst || len(shows) == 0) {
			out = append(out, movies[0])
			movies = movies[1:]
		} else {
			out = append(out, shows[0])
			shows = shows[1:]
		}
		movieFirst = !movieFirst
	}
	return out
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
