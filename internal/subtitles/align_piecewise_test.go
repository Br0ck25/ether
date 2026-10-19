package subtitles

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// syntheticDialogue builds n unique, matchable lines starting at start,
// spaced 20s apart.
func syntheticDialogue(start time.Duration, n int) []Cue {
	var out []Cue
	for i := 0; i < n; i++ {
		at := start + time.Duration(i)*20*time.Second
		out = append(out, Cue{Start: at, End: at + 2*time.Second,
			Text: fmt.Sprintf("This is line number %d of the story unfolding here", i)})
	}
	return out
}

func remapCues(in []Cue, f func(time.Duration) time.Duration) []Cue {
	out := make([]Cue, len(in))
	for i, c := range in {
		out[i] = Cue{Start: f(c.Start), End: f(c.End), Text: c.Text}
	}
	return out
}

func assertMapNear(t *testing.T, pw *Piecewise, at, want time.Duration) {
	t.Helper()
	got, ok := pw.Map(at)
	if !ok {
		t.Fatalf("Map(%v) not ok", at)
	}
	if d := got - want; d < -600*time.Millisecond || d > 600*time.Millisecond {
		t.Fatalf("Map(%v) = %v, want ~%v", at, got, want)
	}
}

func TestAlignPiecewiseConstantOffset(t *testing.T) {
	from := syntheticDialogue(time.Minute, 60)
	// "To" release has a 12s longer studio logo.
	to := remapCues(from, func(d time.Duration) time.Duration { return d + 12*time.Second })
	pw, err := AlignPiecewise(from, to, AlignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pw.Segments) != 1 || math.Abs(pw.Segments[0].Slope-1) > 0.01 {
		t.Fatalf("segments %+v", pw.Segments)
	}
	assertMapNear(t, pw, 10*time.Second, 22*time.Second) // before first line: extrapolated
	assertMapNear(t, pw, 10*time.Minute, 10*time.Minute+12*time.Second)
}

func TestAlignPiecewisePALRate(t *testing.T) {
	from := syntheticDialogue(time.Minute, 80)
	rate := 23.976 / 25.0 // film -> PAL
	to := remapCues(from, func(d time.Duration) time.Duration {
		return time.Duration(float64(d) * rate)
	})
	pw, err := AlignPiecewise(from, to, AlignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(pw.Segments[0].Slope-rate) > 0.002 {
		t.Fatalf("slope %f want %f", pw.Segments[0].Slope, rate)
	}
	assertMapNear(t, pw, 20*time.Minute, time.Duration(float64(20*time.Minute)*rate))
}

func TestAlignPiecewiseInsertedScene(t *testing.T) {
	from := syntheticDialogue(time.Minute, 90)
	cut := 15 * time.Minute
	// "To" release has a 45s scene after 15:00 that "from" lacks.
	to := remapCues(from, func(d time.Duration) time.Duration {
		if d >= cut {
			return d + 45*time.Second
		}
		return d
	})
	pw, err := AlignPiecewise(from, to, AlignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pw.Segments) != 2 {
		t.Fatalf("want 2 segments, got %+v", pw.Segments)
	}
	assertMapNear(t, pw, 10*time.Minute, 10*time.Minute)
	assertMapNear(t, pw, 20*time.Minute, 20*time.Minute+45*time.Second)
}

func TestAlignPiecewiseGarbledCues(t *testing.T) {
	from := syntheticDialogue(time.Minute, 50)
	to := remapCues(from, func(d time.Duration) time.Duration { return d + 3*time.Second })
	for i := range to { // punctuation, case, speaker labels, a dropped word
		to[i].Text = fmt.Sprintf("NARRATOR: %s!!", to[i].Text)
		if i%3 == 0 {
			to[i].Text = fmt.Sprintf("This is line %d of the story unfolding here.", i)
		}
	}
	pw, err := AlignPiecewise(from, to, AlignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertMapNear(t, pw, 5*time.Minute, 5*time.Minute+3*time.Second)
}

func TestAlignPiecewiseRejects(t *testing.T) {
	from := syntheticDialogue(time.Minute, 50)
	// A translation: same timing shape, different words entirely.
	translated := make([]Cue, len(from))
	for i, c := range from {
		translated[i] = Cue{Start: c.Start, End: c.End,
			Text: fmt.Sprintf("Ceci est la ligne numero %d de lhistoire qui se deroule", i)}
	}
	if _, err := AlignPiecewise(from, translated, AlignOptions{}); err == nil {
		t.Fatal("translated cues aligned")
	}
	// Too few lines in common.
	if _, err := AlignPiecewise(from[:6], from[:6], AlignOptions{}); err == nil {
		t.Fatal("six cues aligned")
	}
	// Wildly inconsistent timing (shuffle every other cue far away).
	chaotic := remapCues(from, func(d time.Duration) time.Duration { return d })
	for i := range chaotic {
		if i%2 == 0 {
			chaotic[i].Start += time.Duration(i) * time.Second
			chaotic[i].End = chaotic[i].Start + time.Second
		}
	}
	if _, err := AlignPiecewise(from, chaotic, AlignOptions{}); err == nil {
		t.Fatal("chaotic cues aligned")
	}
}
