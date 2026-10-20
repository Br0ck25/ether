package markers

import (
	"sort"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Stinger-aware credits: a credits marker that spans a mid-/post-credits
// scene makes "Skip Credits" skip dialogue the viewer came for. Stingers
// are detected from chapter layout and from dialogue resuming inside the
// credits range, then cut out of the credits markers. PMS represents the
// result as multiple credits markers with final="1" on the last one only.

// Stinger is a scene inside the credits range that must not be skipped.
type Stinger struct {
	Start, End time.Duration
}

// DialogueCue is one already-available subtitle cue time (text not needed).
type DialogueCue struct {
	Start, End time.Duration
}

func normChapter(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("-", " ", "_", " ").Replace(s)
}

func isCreditsName(s string) bool {
	switch normChapter(s) {
	case "credits", "end credits", "closing credits", "ending", "final credits":
		return true
	}
	return false
}

func isStingerName(s string) bool {
	n := normChapter(s)
	for _, frag := range []string{"post credit", "postcredit", "mid credit", "midcredit",
		"after credit", "aftercredit", "stinger"} {
		if strings.Contains(n, frag) {
			return true
		}
	}
	return false
}

// StingersFromChapters finds post-credits scenes in the chapter layout:
// explicitly named stinger chapters, and the structural pattern of a short
// chapter sandwiched after a long credits chapter near the tail.
func StingersFromChapters(info core.MediaInfo) []Stinger {
	var out []Stinger
	for i, c := range info.Chapters {
		c.End = chapterEnd(c, info.Duration)
		if c.Start < 0 || c.End <= c.Start || c.End > info.Duration {
			continue
		}
		switch {
		case isStingerName(c.Title) && c.Start >= info.Duration/2:
			out = append(out, Stinger{c.Start, c.End})
		case i > 0 && c.Start >= info.Duration*3/4 && c.End-c.Start <= 6*time.Minute &&
			!isCreditsName(c.Title) && !isStingerName(c.Title):
			prev := info.Chapters[i-1]
			if isCreditsName(prev.Title) && prev.End-prev.Start >= 45*time.Second &&
				c.Start-prev.End <= time.Second {
				out = append(out, Stinger{c.Start, c.End})
			}
		}
	}
	return out
}

const (
	// dialogueGap is the cue silence that separates a stinger from the
	// credits crawl (or its song lyrics) around it.
	dialogueGap = 20 * time.Second
	// maxStinger bounds a resumed-dialogue cluster; longer is just the film.
	maxStinger = 10 * time.Minute
)

// StingersFromDialogue finds dialogue clusters that resume well inside the
// credits range after a long cue gap: the shape of a mid-/post-credits
// scene. Lyrics running continuously from the credits start never qualify,
// because their first cue follows the credits start without a long gap
// and their cues continue without one.
func StingersFromDialogue(cues []DialogueCue, creditsStart, duration time.Duration) []Stinger {
	sorted := append([]DialogueCue(nil), cues...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	var out []Stinger
	lastEnd := creditsStart
	var cluster []DialogueCue
	flush := func() {
		if len(cluster) >= 2 {
			start, end := cluster[0].Start, cluster[len(cluster)-1].End
			if start >= creditsStart+dialogueGap && end-start >= 3*time.Second &&
				end-start <= maxStinger && end <= duration {
				out = append(out, Stinger{start, end})
			}
		}
		cluster = nil
	}
	for _, c := range sorted {
		if c.Start < creditsStart || c.End <= c.Start {
			continue
		}
		if len(cluster) > 0 && c.Start-lastEnd >= dialogueGap {
			flush()
		}
		cluster = append(cluster, c)
		if c.End > lastEnd {
			lastEnd = c.End
		}
	}
	flush()
	return out
}

// mergeStingers unions overlapping stinger ranges from all detectors.
func mergeStingers(in []Stinger) []Stinger {
	if len(in) == 0 {
		return nil
	}
	sorted := append([]Stinger(nil), in...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	out := []Stinger{sorted[0]}
	for _, s := range sorted[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			if s.End > last.End {
				last.End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// ApplyStingers cuts stinger ranges out of credits markers, splitting one
// credits marker into the pieces before and after each stinger. Pieces
// under two seconds are dropped (a breath of crawl is not skippable).
// Afterwards exactly the last credits marker may be Final, and only when
// it reaches the end of the media — the shape PMS emits.
func ApplyStingers(markers []Marker, stingers []Stinger, duration time.Duration) []Marker {
	stingers = mergeStingers(stingers)
	var out []Marker
	for _, m := range markers {
		if m.Type != "credits" || len(stingers) == 0 {
			out = append(out, m)
			continue
		}
		pieces := [][2]int64{{m.StartMS, m.EndMS}}
		for _, s := range stingers {
			ss, se := s.Start.Milliseconds(), s.End.Milliseconds()
			var next [][2]int64
			for _, p := range pieces {
				if se <= p[0] || ss >= p[1] {
					next = append(next, p)
					continue
				}
				if ss > p[0] {
					next = append(next, [2]int64{p[0], ss})
				}
				if se < p[1] {
					next = append(next, [2]int64{se, p[1]})
				}
			}
			pieces = next
		}
		for _, p := range pieces {
			if p[1]-p[0] < 2000 {
				continue
			}
			part := m
			part.StartMS, part.EndMS, part.Final = p[0], p[1], false
			out = append(out, part)
		}
	}
	out = finalizeCredits(out, duration)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMS != out[j].StartMS {
			return out[i].StartMS < out[j].StartMS
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// finalizeCredits enforces the PMS rule for multiple credits markers:
// final="1" on the last one (when it reaches the tail), never earlier.
func finalizeCredits(markers []Marker, duration time.Duration) []Marker {
	last := -1
	for i, m := range markers {
		if m.Type == "credits" && (last < 0 || m.StartMS >= markers[last].StartMS) {
			last = i
		}
	}
	for i := range markers {
		if markers[i].Type != "credits" {
			markers[i].Final = false
			continue
		}
		markers[i].Final = i == last && duration > 0 &&
			markers[i].EndMS >= duration.Milliseconds()-1500
	}
	return markers
}
