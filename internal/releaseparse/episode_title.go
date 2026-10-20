package releaseparse

import (
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// EpisodeTitleMatches rejects an explicitly different episode title between
// SxxExx and the technical metadata. Unnamed scene releases remain eligible.
// A title is only extracted when a recognized metadata boundary follows it.
func EpisodeTitleMatches(name, want string) bool {
	if strings.TrimSpace(want) == "" {
		return true
	}
	generic := strings.Fields(strings.ToLower(want))
	if len(generic) == 2 && generic[0] == "episode" {
		if _, err := strconv.Atoi(generic[1]); err == nil {
			return true
		}
	}
	spaced := spacify(name)
	loc := reSE.FindStringIndex(spaced)
	if loc == nil {
		loc = reXEpisode.FindStringIndex(spaced)
	}
	if loc == nil {
		return true
	}
	tail := strings.Trim(spaced[loc[1]:], " -")
	var words []string
	boundary := false
	for _, w := range strings.Fields(tail) {
		u := strings.ToUpper(strings.Trim(w, "-"))
		base := strings.SplitN(u, "-", 2)[0]
		technical := false
		for _, kw := range boundaryKeywords {
			if u == kw || base == kw {
				technical = true
				break
			}
		}
		for _, l := range languages {
			if u == l.token {
				technical = true
				break
			}
		}
		switch u {
		case "AAC", "AC3", "EAC3", "DDP", "DDP5", "DTS", "TRUEHD", "READNFO", "UNCENSORED", "EXTENDED":
			technical = true
		}
		if technical {
			boundary = true
			break
		}
		words = append(words, w)
	}
	if !boundary || len(words) == 0 {
		return true
	}
	got := normalizeEpisodeTitle(strings.Join(words, " "))
	expected := normalizeEpisodeTitle(want)
	if got == "" || got == "the" || got == "a" || got == "an" {
		return true
	}
	return got == expected || strings.HasPrefix(expected, got+" ")
}

// Catalogue and scene names express multipart suffixes differently:
// "Stone and Sky (2)", "Stone and Sky Part 2" and "Stone and Sky Pt02".
// Normalize the suffix without dropping its number, so different parts
// remain different episodes. Ordinary uses of the word "part" are kept.
func normalizeEpisodeTitle(title string) string {
	words := strings.Fields(NormalizeTitle(title))
	if len(words) == 0 {
		return ""
	}
	last := len(words) - 1
	if n, err := strconv.Atoi(words[last]); err == nil && n > 0 {
		words[last] = strconv.Itoa(n)
		if last > 0 && (words[last-1] == "part" || words[last-1] == "pt") {
			words = append(words[:last-1], words[last])
		}
	} else {
		for _, prefix := range []string{"part", "pt"} {
			if suffix, ok := strings.CutPrefix(words[last], prefix); ok {
				if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
					words[last] = strconv.Itoa(n)
					break
				}
			}
		}
	}
	return strings.Join(words, " ")
}

// EpisodeSeriesYear reads only the series portion of a TV name. A year in
// an episode title must not be mistaken for the show's premiere year.
func EpisodeSeriesYear(name string, parsedYear int) int {
	spaced := spacify(name)
	loc := reSE.FindStringIndex(spaced)
	if loc == nil {
		loc = reXEpisode.FindStringIndex(spaced)
	}
	if loc == nil {
		loc = reSeasonOnly.FindStringIndex(spaced)
	}
	if loc == nil {
		return parsedYear
	}
	info := core.ReleaseInfo{}
	return parseYear(spaced[:loc[0]], &info)
}
