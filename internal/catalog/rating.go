package catalog

import (
	"strconv"
	"strings"
)

// Content-rating ordering. All scales map onto a unified severity rank:
//
//	1  everyone      G, TV-G, TV-Y, U
//	2  parental      PG, TV-PG, TV-Y7
//	3  teens         PG-13, TV-14, 12/15-ish, M
//	4  adults        R, TV-MA, MA15+
//	5  adults-only   NC-17, 18/R18+
//
// Unknown / unrated ratings rank 0. Restricted viewers cannot access them.
func RatingRank(rating string) int {
	r := strings.ToUpper(strings.TrimSpace(rating))
	r = strings.ReplaceAll(r, " ", "")
	if r == "" {
		return 0
	}
	switch r {
	case "G", "TV-G", "TV-Y", "U", "AL", "0", "FSK0", "TV-Y7-FV":
		if r == "TV-Y7-FV" {
			return 2
		}
		return 1
	case "PG", "TV-PG", "TV-Y7", "6", "7", "9", "10", "FSK6", "FSK12":
		// FSK12 sits between PG and PG-13; keep at 2 so a PG limit allows it only loosely — map to 3 below handled by numeric fallback? Keep 3 for 12.
		if r == "FSK12" {
			return 3
		}
		return 2
	case "PG-13", "TV-14", "12", "12A", "13", "14", "15", "M", "16", "FSK16":
		if r == "16" || r == "FSK16" {
			return 4
		}
		return 3
	case "R", "TV-MA", "MA", "MA15+", "MA15", "17", "18A":
		return 4
	case "NC-17", "NC17", "X", "18", "18+", "R18+", "R18", "FSK18", "AO":
		return 5
	case "NR", "NOTRATED", "UNRATED", "APPROVED", "PASSED":
		return 0
	}
	// Numeric age ratings used by many regions ("12", "16 ans", "18+").
	digits := ""
	for _, ch := range r {
		if ch >= '0' && ch <= '9' {
			digits += string(ch)
		} else if digits != "" {
			break
		}
	}
	if digits != "" {
		if n, err := strconv.Atoi(digits); err == nil {
			switch {
			case n <= 6:
				return 1
			case n <= 11:
				return 2
			case n <= 15:
				return 3
			case n <= 17:
				return 4
			default:
				return 5
			}
		}
	}
	return 0
}

// RatingExceeds reports whether rating is stricter than max ("" max = unrestricted).
func RatingExceeds(rating, max string) bool {
	if strings.TrimSpace(max) == "" {
		return false
	}
	mr := RatingRank(max)
	if mr == 0 {
		return true
	}
	r := RatingRank(rating)
	return r == 0 || r > mr
}

// WithinRatingLimit is the negation, for readability at call sites.
func WithinRatingLimit(rating, max string) bool { return !RatingExceeds(rating, max) }
