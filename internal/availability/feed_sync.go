package availability

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// A checkpoint has two boundaries: the newest observed release, and a pending
// historical gap. Updating the head must never discard that unfinished gap.
// Offsets are only hints: a resumed page must contain the saved tail at its
// expected position, otherwise we walk again from the head (deletions/reordering).
type feedCheckpoint struct {
	head, target, tail string
	offset             int
}

const bootstrapTarget = "__ether_recent_history__"

func (s *Service) checkpoint(ctx context.Context, f core.ReleaseFeed) (feedCheckpoint, error) {
	var c feedCheckpoint
	var gap int
	err := s.db.QueryRowContext(ctx, `SELECT anchor,catchup_target,catchup_tail,catchup_offset,gap FROM availability_feeds WHERE source_id=? AND generation=?`, f.ID, f.Generation).Scan(&c.head, &c.target, &c.tail, &c.offset, &gap)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	// Old versions advanced past gaps. Recover the recent feed history instead
	// of presenting the lost boundary as proof of complete coverage.
	if gap != 0 && c.target == "" {
		c.target = bootstrapTarget
	}
	return c, err
}
func (s *Service) saveCheckpoint(ctx context.Context, f core.ReleaseFeed, c feedCheckpoint, message string, count int, complete bool) error {
	synced := int64(0)
	if complete {
		synced = s.now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO availability_feeds(source_id,generation,anchor,checked_at,synced_at,gap,message,discovered,catchup_target,catchup_tail,catchup_offset) VALUES(?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(source_id) DO UPDATE SET generation=excluded.generation,anchor=excluded.anchor,checked_at=excluded.checked_at,
 synced_at=CASE WHEN excluded.synced_at>0 THEN excluded.synced_at WHEN availability_feeds.generation<>excluded.generation THEN 0 ELSE availability_feeds.synced_at END,
 gap=excluded.gap,message=excluded.message,discovered=availability_feeds.discovered+excluded.discovered,
 catchup_target=excluded.catchup_target,catchup_tail=excluded.catchup_tail,catchup_offset=excluded.catchup_offset`, f.ID, f.Generation, c.head, s.now().Unix(), synced, boolInt(c.target != ""), message, count, c.target, c.tail, c.offset)
	return err
}

// Reserve all remaining scheduled head polls today before spending on deeper
// pages. This is enforced by the same atomic global counter, not a preflight
// estimate that concurrent consumers can overspend. Each indexer still applies
// its own foreground reserve and upstream rate-limit backoff.
func (s *Service) catchupContext(ctx context.Context, current core.ReleaseFeed) context.Context {
	cfg := s.cfg().Availability
	ctx = core.WithForegroundReserve(ctx, cfg.ReservePercent)
	return core.WithBackgroundBudget(ctx, func(ctx context.Context, kind, source string, n int64) error {
		cfg := s.cfg().Availability
		if !cfg.Monitor {
			return context.Canceled
		}
		interval := time.Duration(max(5, cfg.PollMinutes)) * time.Minute
		now := s.now().UTC()
		midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		reserve := int64(0)
		for _, f := range s.d.Feeds.FeedSources() {
			if !f.Supported || !selected(cfg.IndexerIDs, f.ID) {
				continue
			}
			_, at, _, err := s.feedState(ctx, f)
			if err != nil {
				return err
			}
			next := time.Unix(at, 0).Add(interval)
			if f.ID == current.ID {
				next = now.Add(interval)
			}
			if next.Before(now) {
				next = now
			}
			if next.Before(midnight) {
				reserve += int64((midnight.Sub(next)-1)/interval) + 1
			}
		}
		return s.spend(ctx, "feedRequests", n, max(0, int64(cfg.FeedRequests)-reserve))
	})
}

func (s *Service) syncFeed(parent context.Context, f core.ReleaseFeed) error {
	old, err := s.checkpoint(parent, f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	c := old
	if c.head == "" && c.target == "" {
		c.target = bootstrapTarget
	}
	limit := min(20, max(1, s.cfg().Availability.MaxPages))
	head := ""
	offset := 0
	count := 0
	foundHead := old.head == ""
	target := c.target
	expectedTail := ""
	resumed := false
	// If an existing gap is pending we cannot move its offset until we locate
	// the previous head: new releases may have shifted every subsequent page.
	canCheckpoint := old.target == ""
	policy := s.policy()
	finish := func() error {
		return s.saveCheckpoint(parent, f, feedCheckpoint{head: head}, "Feed is current within the recent-release window", count, true)
	}
	for page := 0; page < limit; page++ {
		requestCtx := s.budgetContext(ctx, true)
		if page > 0 {
			requestCtx = s.catchupContext(ctx, f)
		}
		releases, e := s.d.Feeds.Recent(requestCtx, f.ID, offset, 100)
		if e != nil {
			// Successful pages remain useful. Preserve an exact resumable boundary;
			// never discard a prior gap merely because the next request was denied.
			if head != "" && canCheckpoint {
				c.head = head
			}
			if head != "" && c.target == "" {
				c.target = old.head
			}
			if parent.Err() == nil {
				_ = s.saveCheckpoint(parent, f, c, safeReason(e), count, false)
			}
			return e
		}
		if len(releases) == 0 {
			// A resumed offset may have moved beyond the end after deletions. Verify
			// from the head before accepting the end of the feed as coverage.
			if expectedTail != "" {
				offset = 100
				expectedTail = ""
				resumed = true
				continue
			}
			if head == "" {
				head = old.head
			}
			return finish()
		}
		if head == "" {
			head = releases[0].ID
		}
		if expectedTail != "" && releases[0].ID != expectedTail {
			offset = 100
			expectedTail = ""
			resumed = true
			// Don't jump over unexamined items when the indexer's ordering changes.
			continue
		}
		expectedTail = ""
		headIndex := -1
		reachedTarget := false
		oldPage := true
		for i := range releases {
			releases[i].Info = releaseparse.Parse(releases[i].Name)
			if releases[i].ID == old.head {
				headIndex = i
			}
			if target != "" && releases[i].ID == target {
				reachedTarget = true
			}
			if releases[i].PubDate.IsZero() || releases[i].PubDate.After(s.now().Add(-61*24*time.Hour)) {
				oldPage = false
			}
		}
		if e = s.retainFeed(ctx, f, releases); e != nil {
			return e
		}
		matched, e := s.d.Catalog.ReleaseMatches(ctx, releases)
		if e != nil {
			return e
		}
		for _, r := range releases {
			for _, t := range matched[r.ID] {
				if e = s.remember(ctx, f, t, r, policy); e != nil {
					return e
				}
				count++
			}
		}
		if reachedTarget || len(releases) < 100 || oldPage {
			return finish()
		}
		if headIndex >= 0 {
			foundHead = true
			canCheckpoint = true
			if target == "" {
				return finish()
			}
			if !resumed && old.tail != "" {
				shifted := old.offset + offset + headIndex
				// The entire current page is already observed. Resume only beyond it.
				if shifted >= offset+len(releases) {
					c = feedCheckpoint{head: head, target: target, offset: shifted, tail: old.tail}
					offset = shifted
					expectedTail = old.tail
					resumed = true
					continue
				}
			}
		}
		if foundHead || old.target == "" {
			canCheckpoint = true
			c.head = head
			c.target = target
			if c.target == "" {
				c.target = old.head
			}
			c.offset = offset + len(releases) - 1
			c.tail = releases[len(releases)-1].ID
		}
		offset += len(releases)
	}
	if c.target == "" {
		c.target = bootstrapTarget
	}
	return s.saveCheckpoint(parent, f, c, "Catching up on recent releases; progress is saved and fresh arrivals are polled first", count, false)
}
