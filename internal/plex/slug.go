package plex

import (
	"strings"
	"unicode"
)

// slugify builds a PMS-style slug ("The 'Burbs" -> "the-burbs",
// "Coyote vs. Acme" -> "coyote-vs-acme", "9-1-1" -> "9-1-1"): lower case
// ASCII letters and digits separated by single hyphens, apostrophes
// dropped, common Latin accents folded. PMS's slugs come from plex.tv and
// sometimes carry a disambiguating year; Ether has no such registry, so it
// uses the title alone. Titles with no Latin letters or digits get no slug.
func slugify(title string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r == '\'' || r == '’' || r == '‘' || r == '`':
			continue
		case r == '&':
			if b.Len() > 0 && !hyphen {
				b.WriteByte('-')
			}
			b.WriteString("and")
			hyphen = false
			continue
		}
		if f, ok := foldLatin[r]; ok {
			b.WriteString(f)
			hyphen = false
			continue
		}
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if b.Len() > 0 && !hyphen {
			b.WriteByte('-')
			hyphen = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// foldLatin folds accented Latin letters (lower case) to ASCII.
var foldLatin = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'æ': "ae", 'ç': "c", 'ć': "c", 'č': "c", 'ď': "d", 'đ': "d", 'ð': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ğ': "g", 'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i", 'į': "i", 'ı': "i",
	'ł': "l", 'ñ': "n", 'ń': "n", 'ň': "n", 'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'ő': "o",
	'œ': "oe", 'ř': "r", 'ś': "s", 'š': "s", 'ş': "s", 'ß': "ss", 'ť': "t", 'ţ': "t", 'þ': "th",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ý': "y", 'ÿ': "y", 'ź': "z", 'ż': "z", 'ž': "z",
}
