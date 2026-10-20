package cardigann

import (
	"fmt"
	"strings"
)

// .NET regular expressions accept named Unicode blocks (\p{IsCyrillic},
// \p{IsCJKUnifiedIdeographs}); regexp2 and Go do not. netBlocks maps the
// .NET block names definitions use (and their common neighbours) to code
// point ranges, from the Unicode block table .NET documents.
var netBlocks = map[string][2]rune{
	"BasicLatin":                          {0x0000, 0x007F},
	"Latin-1Supplement":                   {0x0080, 0x00FF},
	"LatinExtended-A":                     {0x0100, 0x017F},
	"LatinExtended-B":                     {0x0180, 0x024F},
	"IPAExtensions":                       {0x0250, 0x02AF},
	"SpacingModifierLetters":              {0x02B0, 0x02FF},
	"CombiningDiacriticalMarks":           {0x0300, 0x036F},
	"Greek":                               {0x0370, 0x03FF},
	"GreekandCoptic":                      {0x0370, 0x03FF},
	"Cyrillic":                            {0x0400, 0x04FF},
	"CyrillicSupplement":                  {0x0500, 0x052F},
	"Armenian":                            {0x0530, 0x058F},
	"Hebrew":                              {0x0590, 0x05FF},
	"Arabic":                              {0x0600, 0x06FF},
	"Devanagari":                          {0x0900, 0x097F},
	"Bengali":                             {0x0980, 0x09FF},
	"Thai":                                {0x0E00, 0x0E7F},
	"Georgian":                            {0x10A0, 0x10FF},
	"HangulJamo":                          {0x1100, 0x11FF},
	"LatinExtendedAdditional":             {0x1E00, 0x1EFF},
	"GreekExtended":                       {0x1F00, 0x1FFF},
	"GeneralPunctuation":                  {0x2000, 0x206F},
	"CurrencySymbols":                     {0x20A0, 0x20CF},
	"LetterlikeSymbols":                   {0x2100, 0x214F},
	"NumberForms":                         {0x2150, 0x218F},
	"Arrows":                              {0x2190, 0x21FF},
	"MathematicalOperators":               {0x2200, 0x22FF},
	"BoxDrawing":                          {0x2500, 0x257F},
	"GeometricShapes":                     {0x25A0, 0x25FF},
	"MiscellaneousSymbols":                {0x2600, 0x26FF},
	"CJKRadicalsSupplement":               {0x2E80, 0x2EFF},
	"CJKSymbolsandPunctuation":            {0x3000, 0x303F},
	"Hiragana":                            {0x3040, 0x309F},
	"Katakana":                            {0x30A0, 0x30FF},
	"Bopomofo":                            {0x3100, 0x312F},
	"HangulCompatibilityJamo":             {0x3130, 0x318F},
	"KatakanaPhoneticExtensions":          {0x31F0, 0x31FF},
	"EnclosedCJKLettersandMonths":         {0x3200, 0x32FF},
	"CJKCompatibility":                    {0x3300, 0x33FF},
	"CJKUnifiedIdeographsExtensionA":      {0x3400, 0x4DBF},
	"CJKUnifiedIdeographs":                {0x4E00, 0x9FFF},
	"HangulSyllables":                     {0xAC00, 0xD7AF},
	"CJKCompatibilityIdeographs":          {0xF900, 0xFAFF},
	"AlphabeticPresentationForms":         {0xFB00, 0xFB4F},
	"ArabicPresentationForms-A":           {0xFB50, 0xFDFF},
	"CJKCompatibilityForms":               {0xFE30, 0xFE4F},
	"ArabicPresentationForms-B":           {0xFE70, 0xFEFF},
	"HalfwidthandFullwidthForms":          {0xFF00, 0xFFEF},
	"Specials":                            {0xFFF0, 0xFFFF},
	"CombiningMarksforSymbols":            {0x20D0, 0x20FF},
	"CombiningDiacriticalMarksforSymbols": {0x20D0, 0x20FF},
}

// translateNETBlocks rewrites \p{IsName} / \P{IsName} into explicit
// ranges: inside a character class "Ѐ-ӿ", outside it a class of
// its own ("[Ѐ-ӿ]", or "[^...]" for \P). Unknown names are left
// for the regex compiler to reject. A negated block inside a class cannot
// be expressed as a range and is reported.
func translateNETBlocks(pattern string) (string, error) {
	if !strings.Contains(pattern, "{Is") {
		return pattern, nil
	}
	var b strings.Builder
	inClass := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '\\' && i+1 < len(pattern) {
			n := pattern[i+1]
			if (n == 'p' || n == 'P') && strings.HasPrefix(pattern[i+2:], "{Is") {
				end := strings.IndexByte(pattern[i+2:], '}')
				if end > 0 {
					name := pattern[i+5 : i+2+end]
					if r, ok := netBlocks[name]; ok {
						rng := fmt.Sprintf(`\u%04X-\u%04X`, r[0], r[1])
						switch {
						case inClass && n == 'P':
							return "", fmt.Errorf(`negated block \P{Is%s} inside a character class is not supported`, name)
						case inClass:
							b.WriteString(rng)
						case n == 'P':
							b.WriteString("[^" + rng + "]")
						default:
							b.WriteString("[" + rng + "]")
						}
						i += 2 + end
						continue
					}
				}
			}
			b.WriteByte(c)
			b.WriteByte(n)
			i++
			continue
		}
		switch {
		case c == '[' && !inClass:
			inClass = true
		case c == ']' && inClass:
			inClass = false
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}
