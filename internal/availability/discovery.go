package availability

import (
	"context"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type discoveryValidator interface {
	DiscoveryCandidate(*core.Title, core.Release) string
}

// FilterDiscovery hides recent movies only when all current observations are
// explicitly unsuitable, or a recent resolve is still cooling off. Missing
// evidence and old films do
// not become title-wide negatives. It never runs searches or health checks.
func (s *Service) FilterDiscovery(ctx context.Context, items []core.Title) []core.Title {
	v, ok := s.d.Validator.(discoveryValidator)
	if !ok {
		return items
	}
	titles := map[core.RatingKey]*core.Title{}
	keys := []core.RatingKey{}
	for i := range items {
		t := &items[i]
		if t.Type == core.TypeMovie && !t.Released.IsZero() && t.Released.After(s.now().AddDate(0, -4, 0)) {
			if titles[t.Key] == nil {
				keys = append(keys, t.Key)
				titles[t.Key] = t
			}
		}
	}
	if len(keys) == 0 {
		return items
	}
	feeds := map[string]string{}
	for _, f := range s.d.Feeds.FeedSources() {
		feeds[f.ID] = f.Generation
	}
	unavailable := map[core.RatingKey]bool{}
	if source, ok := s.d.Validator.(interface {
		KnownUnavailable(context.Context, []core.RatingKey) map[core.RatingKey]bool
	}); ok {
		unavailable = source.KnownUnavailable(ctx, keys)
	}
	seen, acceptable := map[core.RatingKey]bool{}, map[core.RatingKey]bool{}
	for start := 0; start < len(keys); start += 500 {
		batch := keys[start:min(start+500, len(keys))]
		marks := make([]string, len(batch))
		args := make([]any, 0, len(batch)+1)
		for i, k := range batch {
			marks[i] = "?"
			args = append(args, k)
		}
		args = append(args, s.now().Add(-30*24*time.Hour).Unix())
		rows, err := s.db.QueryContext(ctx, `SELECT rating_key,name,source_id,generation FROM availability_releases WHERE rating_key IN (`+strings.Join(marks, ",")+`) AND last_seen>=?`, args...)
		if err != nil {
			return items
		}
		for rows.Next() {
			var k core.RatingKey
			var name, source, generation string
			if rows.Scan(&k, &name, &source, &generation) != nil {
				rows.Close()
				return items
			}
			if current, exists := feeds[source]; !exists || current != generation {
				continue
			}
			seen[k] = true
			if v.DiscoveryCandidate(titles[k], core.Release{Name: name}) == "" {
				acceptable[k] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return items
		}
	}
	out := make([]core.Title, 0, len(items))
	for _, t := range items {
		if !unavailable[t.Key] && (!seen[t.Key] || acceptable[t.Key]) {
			out = append(out, t)
		}
	}
	return out
}
