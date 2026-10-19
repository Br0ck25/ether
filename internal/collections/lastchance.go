package collections

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"time"
)

// LastChance reports the next configured rotation boundary only when a title
// belongs to this viewer's current subset and is absent from the next subset.
// It means leaving a rotating collection row, never disappearing from Ether.
// The method uses already-built members and the current local index. It never
// triggers a build, fetches a source, or invents a media-availability deadline.
func (s *Service) LastChance(ctx context.Context, user *core.User, key core.RatingKey) (time.Time, error) {
	var expires time.Time
	if user == nil {
		return expires, nil
	}
	snap := s.idx.current()
	if snap == nil {
		return expires, nil
	}
	rows, err := s.loadRows(ctx)
	if err != nil {
		return expires, err
	}
	now := s.now()
	for _, row := range rows {
		if ctx.Err() != nil {
			return time.Time{}, ctx.Err()
		}
		sp := row.spec
		if !visible(row, user) || sp.Rotate == nil || sp.Rotate.Size <= 0 || (!sp.Targets.CollectionsTab && !sp.Targets.HomeRow && !sp.Targets.SectionRow) || !scheduleAllows(sp.Targets.Schedule, "", now) {
			continue
		}
		ms := s.cached(row.id)
		if ms == nil {
			if row.lastBuilt.IsZero() || row.buildHash == "" {
				continue
			}
			keys, e := s.loadMembers(ctx, row.id)
			if e != nil {
				return time.Time{}, e
			}
			ms = s.newMemberSet(row.spec, keys, row.lastBuilt, row.buildHash)
		}
		// Retain serving's exact filtering/order/limit before comparing rotations.
		keys := s.filterForUser(ctx, ms.keys, ms.personal, ms.personalX, user, snap)
		if sp.Sort == "personal" {
			keys = s.personalOrder(ctx, row.id, ms.hash, user, keys)
		}
		if sp.Limit > 0 && len(keys) > sp.Limit {
			keys = keys[:sp.Limit]
		}
		if len(keys) <= sp.Rotate.Size {
			continue
		}
		contains := func(keys []core.RatingKey) bool {
			for _, k := range keys {
				if k == key {
					return true
				}
			}
			return false
		}
		if !contains(rotate(keys, sp.Rotate.Size, sp.Rotate.Every, row.publicID(), now)) {
			continue
		}
		boundary := nextRotationBoundary(sp.Rotate.Every, now)
		if contains(rotate(keys, sp.Rotate.Size, sp.Rotate.Every, row.publicID(), boundary)) {
			continue
		}
		if expires.IsZero() || boundary.Before(expires) {
			expires = boundary
		}
	}
	return expires, nil
}

// Use calendar arithmetic in the collection clock's zone, including DST.
func nextRotationBoundary(every string, now time.Time) time.Time {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if every != "weekly" {
		return midnight.AddDate(0, 0, 1)
	}
	days := (8 - int(now.Weekday())) % 7
	if days == 0 {
		days = 7
	}
	return midnight.AddDate(0, 0, days)
}
