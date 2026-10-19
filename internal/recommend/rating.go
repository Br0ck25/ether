package recommend

import (
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// contentSeverity maps US movie and TV ratings onto one severity ladder so
// a per-user MaxContentRating ("PG-13", "TV-14", …) can gate both sections.
// Unrecognised ratings return 0 and are never filtered out: Ether's
// catalogue is TMDB-driven and many titles carry non-US or no rating, and
// hiding all of those would empty the library for restricted users.
func contentSeverity(r string) int {
	switch strings.ToUpper(strings.TrimSpace(r)) {
	case "G", "TV-G", "TV-Y", "TVY", "E":
		return 1
	case "PG", "TV-PG", "TV-Y7", "TVY7":
		return 2
	case "PG-13", "PG13", "TV-14", "TV14":
		return 3
	case "R", "TV-MA", "TVMA", "MA":
		return 4
	case "NC-17", "NC17", "X", "18+", "AO":
		return 5
	}
	return 0
}

// ContentRatingAllowed reports whether a title rating is within a user's
// maximum. An empty max allows everything.
func ContentRatingAllowed(rating, max string) bool {
	maxSev := contentSeverity(max)
	if maxSev == 0 {
		return true
	}
	sev := contentSeverity(rating)
	return sev == 0 || sev <= maxSev
}

// FilterTitles drops titles above the user's content-rating limit,
// preserving order.
func FilterTitles(titles []core.Title, user *core.User) []core.Title {
	if user == nil || user.MaxContentRating == "" {
		return titles
	}
	out := titles[:0:0]
	for _, t := range titles {
		if ContentRatingAllowed(t.ContentRating, user.MaxContentRating) {
			out = append(out, t)
		}
	}
	return out
}
