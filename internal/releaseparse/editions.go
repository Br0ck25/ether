package releaseparse

import "strings"

// ParseEdition identifies explicit cut labels without guessing from runtime.
func ParseEdition(name string) string {
	s := strings.ToLower(name)
	s = strings.NewReplacer(".", " ", "_", " ", "’", "'", "-", " ").Replace(s)
	switch {
	case strings.Contains(s, "director's cut"), strings.Contains(s, "directors cut"):
		return "Director's Cut"
	case strings.Contains(s, "extended"):
		return "Extended"
	case strings.Contains(s, "theatrical"):
		return "Theatrical"
	case strings.Contains(s, "imax"):
		return "IMAX"
	case strings.Contains(s, "unrated"):
		return "Unrated"
	case strings.Contains(s, "final cut"):
		return "Final Cut"
	}
	return ""
}
