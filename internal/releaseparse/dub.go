package releaseparse

// ExplicitDub reports a dub declaration in release metadata, not a title word.
// Language parsing alone is not enough: foreign originals remain welcome.
func ExplicitDub(name string) bool {
	spaced := spacify(name)
	_, floor := parseYearAt(spaced)
	for _, tag := range []string{"DUB", "DUBBED"} {
		if languageTagAfter(spaced, tag, floor) {
			return true
		}
	}
	return false
}
