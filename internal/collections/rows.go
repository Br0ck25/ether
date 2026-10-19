package collections

import (
	"context"
	"errors"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// rowItems is how many titles a collection row carries.
const rowItems = 24

// HomeRowsFor returns owner collections promoted to Home or section rows
// for a user in a context: enabled, targeted at the user, inside their
// schedule (days, hours, devices), with the user's content-rating limit,
// personal rules, personal order and rotation applied. Each row says
// whether it belongs on Home, in section hubs, or both; Hub.Type tells the
// caller which section ("" for mixed collections).
func (s *Service) HomeRowsFor(ctx context.Context, user *core.User, hc core.HubContext) ([]core.CollectionRow, error) {
	rows, err := s.loadRows(ctx)
	if err != nil {
		return nil, err
	}
	now := hc.Now
	if now.IsZero() {
		now = s.now()
	}
	var out []core.CollectionRow
	for _, row := range rows {
		sp := row.spec
		if !visible(row, user) || (!sp.Targets.HomeRow && !sp.Targets.SectionRow) {
			continue
		}
		if !scheduleAllows(sp.Targets.Schedule, hc.Device, now) {
			continue
		}
		if cr, ok := s.rowFor(ctx, row, user); ok {
			out = append(out, cr)
		}
	}
	return out, nil
}

var _ core.CollectionRowLookup = (*Service)(nil)

// CollectionRowsByID implements core.CollectionRowLookup: the rows of
// specific collections for a user, whatever their Home/section targeting
// and schedule, because the owner placed them on a page in the Home
// Designer (whose own schedules then apply). Disabled collections are
// skipped; ignoreAudience serves collections whose audience does not
// include the user (a per-viewer placement). Personal collections of other
// users are never served.
func (s *Service) CollectionRowsByID(ctx context.Context, user *core.User, _ core.HubContext, ids []string, ignoreAudience bool) ([]core.CollectionRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	rows, err := s.loadRows(ctx)
	if err != nil {
		return nil, err
	}
	var out []core.CollectionRow
	for _, row := range rows {
		if !want[row.publicID()] || !row.enabled {
			continue
		}
		if row.owner != 0 && (user == nil || user.ID != row.owner) {
			continue
		}
		if !ignoreAudience && !visible(row, user) {
			continue
		}
		if cr, ok := s.rowFor(ctx, row, user); ok {
			cr.HomeRow, cr.SectionRow = true, true
			out = append(out, cr)
		}
	}
	return out, nil
}

// rowFor builds a collection's row for a user: its members (personal
// rules, order and rotation applied), first page of titles.
func (s *Service) rowFor(ctx context.Context, row *specRow, user *core.User) (core.CollectionRow, bool) {
	sp := row.spec
	keys, err := s.serve(ctx, row, user, sp.Refresh == RefreshLive)
	if err != nil {
		if !errors.Is(err, errNotBuilt) {
			s.log.Debug("collections: row", "id", row.publicID(), "err", err)
		}
		return core.CollectionRow{}, false
	}
	if len(keys) == 0 {
		return core.CollectionRow{}, false
	}
	page := keys
	if len(page) > rowItems {
		page = page[:rowItems]
	}
	items, err := loadTitles(ctx, s.db, page)
	if err != nil || len(items) == 0 {
		return core.CollectionRow{}, false
	}
	hub := core.Hub{
		ID: "collection." + row.publicID(), Title: sp.Name, Style: "shelf",
		Promoted: sp.Targets.HomeRow, Items: items, More: len(keys) > len(items), Key: row.key(),
	}
	switch sp.MediaType {
	case MediaMovie:
		hub.Type, hub.Context = core.TypeMovie, "hub.movie.collection"
	case MediaShow:
		hub.Type, hub.Context = core.TypeShow, "hub.tv.collection"
	default:
		hub.Context = "hub.home.collection"
	}
	if sp.Targets.HomeRow {
		hub.Context = "hub.home.collection"
	}
	return core.CollectionRow{Hub: hub, CollectionID: row.publicID(),
		HomeRow: sp.Targets.HomeRow, SectionRow: sp.Targets.SectionRow}, true
}

// scheduleAllows applies a row schedule. Days are 0 (Sunday) to 6; hours
// are local, FromHour inclusive, ToHour exclusive, wrapping past midnight.
// An unknown device passes a device filter.
func scheduleAllows(sc *core.CollectionSchedule, device string, now time.Time) bool {
	if sc == nil {
		return true
	}
	if len(sc.Days) > 0 {
		ok := false
		for _, d := range sc.Days {
			if d == int(now.Weekday()) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if sc.FromHour != nil || sc.ToHour != nil {
		from, to := 0, 24
		if sc.FromHour != nil {
			from = *sc.FromHour
		}
		if sc.ToHour != nil {
			to = *sc.ToHour
		}
		h := now.Hour()
		switch {
		case from == to:
			// All day.
		case from < to:
			if h < from || h >= to {
				return false
			}
		default: // wraps midnight, e.g. 22 → 2
			if h < from && h >= to {
				return false
			}
		}
	}
	if len(sc.Devices) > 0 && device != "" {
		ok := false
		for _, d := range sc.Devices {
			if d == device {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
