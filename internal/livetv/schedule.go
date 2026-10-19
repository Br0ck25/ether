package livetv

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"time"
)

// Schedule is deterministic across restarts and query windows. A shuffle is a
// stable hash ordering, not process-global randomness. Blocks start at an exact
// UTC wall-clock boundary; the last item is clipped to avoid guide gaps.
func Schedule(c Channel, from, until time.Time) []Programme {
	if !until.After(from) || c.Kind != "virtual" {
		return nil
	}
	if c.Epoch.IsZero() {
		c.Epoch = time.Unix(0, 0).UTC()
	}
	var out []Programme
	emit := func(items []Item, shuffle bool, anchor, end time.Time, salt int64) {
		if len(items) == 0 || !end.After(from) || !until.After(anchor) {
			return
		}
		ordered := append([]Item(nil), items...)
		if shuffle {
			sort.SliceStable(ordered, func(i, j int) bool { return orderHash(c.Seed+salt, ordered[i]) < orderHash(c.Seed+salt, ordered[j]) })
		}
		var cycle time.Duration
		for _, item := range ordered {
			if item.DurationMS <= 0 {
				return
			}
			cycle += time.Duration(item.DurationMS) * time.Millisecond
		}
		if cycle <= 0 {
			return
		}
		cursor := anchor
		if from.After(cursor) {
			cursor = cursor.Add((from.Sub(cursor) / cycle) * cycle)
		}
		for cursor.Before(until) && cursor.Before(end) && len(out) < 20000 {
			for _, item := range ordered {
				finish := cursor.Add(time.Duration(item.DurationMS) * time.Millisecond)
				if finish.After(end) {
					finish = end
				}
				if finish.After(from) && cursor.Before(until) && finish.After(cursor) {
					out = append(out, Programme{ID: fmt.Sprintf("%s-%d", c.ID, cursor.UnixMilli()), ChannelID: c.ID, Start: cursor, End: finish, Item: item})
				}
				cursor = finish
				if !cursor.Before(end) || !cursor.Before(until) {
					break
				}
			}
		}
	}
	if len(c.Blocks) == 0 {
		emit(c.Items, c.Shuffle, c.Epoch, until.Add(24*time.Hour), 0)
	} else {
		day := time.Date(from.UTC().Year(), from.UTC().Month(), from.UTC().Day(), 0, 0, 0, 0, time.UTC)
		for ; day.Before(until); day = day.AddDate(0, 0, 1) {
			// Base programming fills every interval not claimed by a time block.
			blocks := append([]Block(nil), c.Blocks...)
			sort.Slice(blocks, func(i, j int) bool { return blocks[i].Start < blocks[j].Start })
			prev := 0
			for _, b := range blocks {
				if b.Start > prev {
					emit(c.Items, c.Shuffle, day.Add(time.Duration(prev)*time.Minute), day.Add(time.Duration(b.Start)*time.Minute), day.Unix())
				}
				emit(b.Items, b.Shuffle, day.Add(time.Duration(b.Start)*time.Minute), day.Add(time.Duration(b.End)*time.Minute), day.Unix()+int64(b.Start))
				prev = b.End
			}
			if prev < 1440 {
				emit(c.Items, c.Shuffle, day.Add(time.Duration(prev)*time.Minute), day.Add(24*time.Hour), day.Unix())
			}
		}
	}
	return out
}
func orderHash(seed int64, item Item) uint64 {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d/%d/%s", seed, item.Key, item.Title)))
	return binary.BigEndian.Uint64(h[:8])
}
