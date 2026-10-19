package releaseparse

import "strings"

// editionPatterns are checked in order; earlier entries win when a release
// name carries more than one marker ("IMAX Extended" is the Extended cut).
var editionPatterns = []struct {
	needles []string
	name    string
}{
	{[]string{"director's cut", "directors cut", "director cut"}, "Director's Cut"},
	{[]string{"final cut"}, "Final Cut"},
	{[]string{"ultimate cut", "ultimate edition"}, "Ultimate Edition"},
	{[]string{"extended cut", "extended edition", "extended"}, "Extended"},
	{[]string{"special edition"}, "Special Edition"},
	{[]string{"anniversary edition"}, "Anniversary Edition"},
	{[]string{"open matte"}, "Open Matte"},
	{[]string{"imax enhanced", "imax"}, "IMAX"},
	{[]string{"unrated cut", "unrated"}, "Unrated"},
	{[]string{"uncut"}, "Uncut"},
	{[]string{"remastered"}, "Remastered"},
	{[]string{"restored", "restoration"}, "Restored"},
	{[]string{"theatrical cut", "theatrical"}, "Theatrical"},
	{[]string{"criterion"}, "Criterion"},
	{[]string{"international cut"}, "International Cut"},
	{[]string{"european cut"}, "European Cut"},
	{[]string{"american cut"}, "American Cut"},
	{[]string{"assembly cut"}, "Assembly Cut"},
	{[]string{"workprint"}, "Workprint"},
}

// NormalizeEdition maps an edition label found in a release name (or an
// already-normalized one) to the canonical cut name, "" when there is no
// explicit cut. Two releases belong to the same cut when their normalized
// editions match.
func NormalizeEdition(name string) string { return ParseEdition(name) }

// ParseEdition identifies explicit cut labels without guessing from runtime.
func ParseEdition(name string) string {
	s := strings.ToLower(name)
	s = strings.NewReplacer(".", " ", "_", " ", "’", "'", "-", " ", "(", " ", ")", " ", "[", " ", "]", " ").Replace(s)
	s = " " + strings.Join(strings.Fields(s), " ") + " "
	for _, p := range editionPatterns {
		for _, n := range p.needles {
			// s is space-padded, so this requires whole-word matches.
			if strings.Contains(s, " "+n+" ") {
				return p.name
			}
		}
	}
	// Abbreviated cuts: a bare "DC" token (not the comics brand at title
	// start) means Director's Cut in scene naming ("Movie.1999.DC.1080p").
	if strings.Contains(s, " dc ") && !strings.HasPrefix(strings.TrimSpace(s), "dc ") {
		return "Director's Cut"
	}
	return ""
}
