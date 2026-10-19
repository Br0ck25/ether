package releaseparse

import (
	"strings"
	"unicode"

	"github.com/braedonsaunders/ether/internal/core"
)

// diacritics maps common accented latin runes to their base letters.
var diacritics = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'æ': 'a', 'ā': 'a',
	'ç': 'c', 'ć': 'c', 'č': 'c',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e', 'ę': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i', 'ı': 'i',
	'ñ': 'n', 'ń': 'n',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o', 'ō': 'o', 'œ': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ū': 'u',
	'ý': 'y', 'ÿ': 'y',
	'ß': 's', 'ś': 's', 'š': 's', 'ş': 's',
	'ž': 'z', 'ź': 'z', 'ż': 'z',
	'ł': 'l', 'ð': 'd', 'þ': 't', 'ğ': 'g', 'ř': 'r', 'ť': 't', 'ď': 'd',
}

// NormalizeTitle lowercases, strips diacritics and punctuation, expands
// "&" to "and", and drops a leading article, for title comparisons.
func NormalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "’", "")
	var b strings.Builder
	for _, r := range s {
		if base, ok := diacritics[r]; ok {
			b.WriteRune(base)
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	fields := strings.Fields(b.String())
	if len(fields) > 1 {
		switch fields[0] {
		case "the", "a", "an", "le", "la", "les", "der", "die", "das", "el", "il":
			fields = fields[1:]
		}
	}
	return strings.Join(fields, " ")
}

// TitlesMatch reports whether two raw titles are the same after normalisation.
func TitlesMatch(a, b string) bool {
	na, nb := NormalizeTitle(a), NormalizeTitle(b)
	return na != "" && na == nb
}

// TitleMatches reports whether a parsed release title matches the wanted title.
func TitleMatches(parsed core.ReleaseInfo, want string) bool {
	return TitlesMatch(parsed.Title, want)
}

// TitleYearMatches is TitleMatches plus a year check with ±1 tolerance.
// A zero year on either side skips the year check.
func TitleYearMatches(parsed core.ReleaseInfo, want string, wantYear int) bool {
	if !TitleMatches(parsed, want) {
		return false
	}
	return YearMatches(parsed.Year, wantYear)
}

// YearMatches reports whether two years agree within ±1 (zero = unknown).
func YearMatches(got, want int) bool {
	if got == 0 || want == 0 {
		return true
	}
	d := got - want
	return d >= -1 && d <= 1
}

// EpisodeMatches reports whether parsed info is exactly the wanted episode
// (not a season pack, season matches, episode is listed).
func EpisodeMatches(parsed core.ReleaseInfo, season, episode int) bool {
	if parsed.FullSeason || len(parsed.Episodes) == 0 {
		return false
	}
	seasonOK := len(parsed.Seasons) == 0
	for _, s := range parsed.Seasons {
		if s == season {
			seasonOK = true
		}
	}
	if !seasonOK {
		return false
	}
	for _, e := range parsed.Episodes {
		if e == episode {
			return true
		}
	}
	return false
}

// IsObfuscated reports whether a name/title looks like a hash or random
// string with no readable words.
func IsObfuscated(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return true
	}
	fields := strings.Fields(t)
	if len(fields) == 1 && len(fields[0]) >= 12 {
		w := fields[0]
		hasDigit, hasLetter := false, false
		for _, r := range w {
			if unicode.IsDigit(r) {
				hasDigit = true
			}
			if unicode.IsLetter(r) {
				hasLetter = true
			}
		}
		if hasDigit && hasLetter {
			return true
		}
	}
	// No vowel in a longish single token is a strong obfuscation signal.
	if len(fields) == 1 && len(fields[0]) >= 10 {
		lower := strings.ToLower(fields[0])
		if !strings.ContainsAny(lower, "aeiou") {
			return true
		}
	}
	return false
}

// HasUsableInfo reports whether parsed info carries enough technical
// detail to consider a release whose title cannot be verified.
func HasUsableInfo(info core.ReleaseInfo) bool {
	return info.Height > 0 || info.Source != core.SourceUnknown || info.VideoCodec != ""
}
