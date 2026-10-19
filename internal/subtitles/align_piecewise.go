package subtitles

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Piecewise alignment of two dialogue timelines of the same title in the
// same language. A constant logo/intro difference is one affine segment
// with slope 1, PAL speed-up is a slope slightly off 1, and a scene that
// only exists in one cut is a break into a new segment with a new offset.

// AlignSegment is one affine run: To = Slope*From + Intercept (millisecond
// space), valid for From times in [FromStart, FromEnd].
type AlignSegment struct {
	FromStart time.Duration `json:"fromStart"`
	FromEnd   time.Duration `json:"fromEnd"`
	Slope     float64       `json:"slope"`
	Intercept time.Duration `json:"intercept"`
	Matched   int           `json:"matched"`
}

// Piecewise is a confident dialogue-based mapping between two releases.
type Piecewise struct {
	Segments []AlignSegment `json:"segments"`
	Matched  int            `json:"matched"`
	// Residual is the median absolute error of matched cues against the fit.
	Residual time.Duration `json:"residual"`
}

// Map converts a timestamp in the "from" timeline to the "to" timeline.
// Times before the first (or after the last) matched cue extrapolate the
// nearest segment, which is how an intro before the first line of dialogue
// still shifts by the logo-length difference.
func (p *Piecewise) Map(at time.Duration) (time.Duration, bool) {
	if p == nil || len(p.Segments) == 0 || at < 0 {
		return 0, false
	}
	seg := p.Segments[len(p.Segments)-1]
	for _, s := range p.Segments {
		if at <= s.FromEnd {
			seg = s
			break
		}
	}
	out := time.Duration(slopeMS(seg, at)) * time.Millisecond
	if out < 0 {
		out = 0
	}
	return out, true
}

func slopeMS(s AlignSegment, at time.Duration) float64 {
	return s.Slope*float64(at.Milliseconds()) + float64(s.Intercept.Milliseconds())
}

// AlignOptions bounds what counts as a confident alignment.
type AlignOptions struct {
	MinMatched     int           // default 10
	MaxResidual    time.Duration // median bound, default 500ms
	MaxResidualP90 time.Duration // default 1s
}

func (o *AlignOptions) defaults() {
	if o.MinMatched <= 0 {
		o.MinMatched = 10
	}
	if o.MaxResidual <= 0 {
		o.MaxResidual = 500 * time.Millisecond
	}
	if o.MaxResidualP90 <= 0 {
		o.MaxResidualP90 = time.Second
	}
}

var errNoAlignment = errors.New("no confident subtitle alignment")

// NormalizeDialogue reduces cue text to a comparison key: tags and
// hearing-impaired annotations dropped, punctuation and case folded away,
// so OCR/punctuation variants of the same line compare near-equal.
func NormalizeDialogue(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = htmlTagRe.ReplaceAllString(s, "")
	// Drop [sound] / (sound) annotations entirely.
	var cleaned strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '[', '(':
			depth++
		case ']', ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				cleaned.WriteRune(r)
			}
		}
	}
	s = cleaned.String()
	// Drop a leading SPEAKER: label.
	if i := strings.Index(s, ":"); i > 0 && i < 26 {
		label := s[:i]
		upper := label == strings.ToUpper(label)
		hasLetter := strings.IndexFunc(label, unicode.IsLetter) >= 0
		if upper && hasLetter {
			s = s[i+1:]
		}
	}
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

type cueKey struct {
	norm   string
	tokens map[string]bool
}

func keyOf(c Cue) cueKey {
	n := NormalizeDialogue(c.Text)
	toks := map[string]bool{}
	for _, t := range strings.Fields(n) {
		toks[t] = true
	}
	return cueKey{norm: n, tokens: toks}
}

func (k cueKey) usable() bool { return len(k.norm) >= 10 && len(k.tokens) >= 2 }

func similarity(a, b cueKey) float64 {
	if a.norm == b.norm {
		return 1
	}
	if len(a.tokens) == 0 || len(b.tokens) == 0 {
		return 0
	}
	inter := 0
	for t := range a.tokens {
		if b.tokens[t] {
			inter++
		}
	}
	union := len(a.tokens) + len(b.tokens) - inter
	return float64(inter) / float64(union)
}

type pair struct {
	from, to int
	sim      float64
}

// candidatePairs finds plausible same-line pairs: exact normalized text, or
// token Jaccard >= 0.75 via a rare-token inverted index. Short throwaway
// lines ("yes.", "come on") are not matchable: they repeat everywhere.
func candidatePairs(from, to []cueKey) []pair {
	exact := map[string][]int{}
	tokenIdx := map[string][]int{}
	for j, k := range to {
		if !k.usable() {
			continue
		}
		exact[k.norm] = append(exact[k.norm], j)
		for t := range k.tokens {
			tokenIdx[t] = append(tokenIdx[t], j)
		}
	}
	var out []pair
	for i, k := range from {
		if !k.usable() {
			continue
		}
		cands := map[int]bool{}
		for _, j := range exact[k.norm] {
			cands[j] = true
		}
		for t := range k.tokens {
			if idx := tokenIdx[t]; len(idx) <= 24 { // skip ubiquitous words
				for _, j := range idx {
					cands[j] = true
				}
			}
		}
		var mine []pair
		for j := range cands {
			if sim := similarity(k, to[j]); sim >= 0.75 {
				mine = append(mine, pair{from: i, to: j, sim: sim})
			}
		}
		sort.Slice(mine, func(a, b int) bool {
			if mine[a].sim != mine[b].sim {
				return mine[a].sim > mine[b].sim
			}
			return mine[a].to < mine[b].to
		})
		if len(mine) > 6 {
			mine = mine[:6]
		}
		out = append(out, mine...)
	}
	if len(out) > 20000 { // repeated-line blowup: keep only exact matches
		kept := out[:0]
		for _, p := range out {
			if p.sim >= 1 {
				kept = append(kept, p)
			}
		}
		out = kept
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].from != out[b].from {
			return out[a].from < out[b].from
		}
		return out[a].to < out[b].to
	})
	return out
}

// monotonicChain is a longest increasing subsequence over candidate pairs:
// the same lines in the same order in both timelines.
func monotonicChain(pairs []pair) []pair {
	if len(pairs) == 0 {
		return nil
	}
	best := make([]float64, len(pairs))
	prev := make([]int, len(pairs))
	bestEnd, bestScore := 0, -1.0
	for i, p := range pairs {
		best[i], prev[i] = 1+p.sim*0.01, -1
		for j := 0; j < i; j++ {
			if pairs[j].from < p.from && pairs[j].to < p.to && best[j]+1+p.sim*0.01 > best[i] {
				best[i], prev[i] = best[j]+1+p.sim*0.01, j
			}
		}
		if best[i] > bestScore {
			bestScore, bestEnd = best[i], i
		}
	}
	var chain []pair
	for i := bestEnd; i >= 0; i = prev[i] {
		chain = append(chain, pairs[i])
	}
	for l, r := 0, len(chain)-1; l < r; l, r = l+1, r-1 {
		chain[l], chain[r] = chain[r], chain[l]
	}
	return chain
}

type point struct{ fromMS, toMS float64 }

func fitAffine(pts []point) (slope, intercept float64, ok bool) {
	if len(pts) < 2 {
		return 0, 0, false
	}
	var sx, sy float64
	for _, p := range pts {
		sx += p.fromMS
		sy += p.toMS
	}
	mx, my := sx/float64(len(pts)), sy/float64(len(pts))
	var num, den float64
	for _, p := range pts {
		num += (p.fromMS - mx) * (p.toMS - my)
		den += (p.fromMS - mx) * (p.fromMS - mx)
	}
	if den < 1 { // all cues at (nearly) one instant: slope unknowable, use 1
		return 1, pts[0].toMS - pts[0].fromMS, true
	}
	slope = num / den
	return slope, my - slope*mx, true
}

func residMS(slope, intercept float64, p point) float64 {
	return math.Abs(slope*p.fromMS + intercept - p.toMS)
}

const (
	segBreakMS = 800.0 // a matched cue further than this from the fit starts a new segment
	minSlope   = 0.90  // PAL is 0.959/1.0427; anything wider is a different edit, not a rate
	maxSlope   = 1.11
)

// segmentPoints greedily extends affine segments; a cue that no longer fits
// (a cut difference) opens a new segment. Adjacent segments that turn out
// compatible (the break was one outlier cue) are merged back together, and
// segments with too few points to trust are dropped.
func segmentPoints(pts []point) [][]point {
	if len(pts) == 0 {
		return nil
	}
	var segs [][]point
	cur := []point{pts[0]}
	for _, p := range pts[1:] {
		if len(cur) >= 3 {
			slope, intercept, _ := fitAffine(cur)
			if slope < minSlope || slope > maxSlope || residMS(slope, intercept, p) > segBreakMS {
				segs = append(segs, cur)
				cur = nil
			}
		}
		cur = append(cur, p)
	}
	segs = append(segs, cur)
	// Merge neighbours whose joint fit is still tight.
	merged := segs[:0]
	for _, s := range segs {
		if len(merged) > 0 {
			joined := append(append([]point(nil), merged[len(merged)-1]...), s...)
			slope, intercept, _ := fitAffine(joined)
			tight := slope >= minSlope && slope <= maxSlope
			for _, p := range joined {
				if residMS(slope, intercept, p) > segBreakMS*1.5 {
					tight = false
					break
				}
			}
			if tight {
				merged[len(merged)-1] = joined
				continue
			}
		}
		merged = append(merged, s)
	}
	out := merged[:0]
	for _, s := range merged {
		if len(s) >= 3 {
			out = append(out, s)
		}
	}
	return out
}

// AlignPiecewise builds the mapping from the "from" cue timeline to the
// "to" cue timeline, or fails when there is no confident alignment (too
// few matched lines, or matched lines that do not fit within the residual
// bound — typically a different language or an unrelated subtitle file).
func AlignPiecewise(from, to []Cue, opts AlignOptions) (*Piecewise, error) {
	opts.defaults()
	if len(from) == 0 || len(to) == 0 {
		return nil, errNoAlignment
	}
	fromKeys := make([]cueKey, len(from))
	for i, c := range from {
		fromKeys[i] = keyOf(c)
	}
	toKeys := make([]cueKey, len(to))
	for i, c := range to {
		toKeys[i] = keyOf(c)
	}
	chain := monotonicChain(candidatePairs(fromKeys, toKeys))
	pts := make([]point, 0, len(chain))
	for _, pr := range chain {
		pts = append(pts, point{
			fromMS: float64(from[pr.from].Start.Milliseconds()),
			toMS:   float64(to[pr.to].Start.Milliseconds()),
		})
	}
	segs := segmentPoints(pts)
	pw := &Piecewise{}
	var residuals []float64
	for _, sp := range segs {
		slope, intercept, ok := fitAffine(sp)
		if !ok || slope < minSlope || slope > maxSlope {
			continue
		}
		pw.Segments = append(pw.Segments, AlignSegment{
			FromStart: time.Duration(sp[0].fromMS) * time.Millisecond,
			FromEnd:   time.Duration(sp[len(sp)-1].fromMS) * time.Millisecond,
			Slope:     slope,
			Intercept: time.Duration(intercept) * time.Millisecond,
			Matched:   len(sp),
		})
		pw.Matched += len(sp)
		for _, p := range sp {
			residuals = append(residuals, residMS(slope, intercept, p))
		}
	}
	if pw.Matched < opts.MinMatched || len(pw.Segments) == 0 {
		return nil, fmt.Errorf("%w: %d matched cues", errNoAlignment, pw.Matched)
	}
	// A handful of coincidental matches in a sea of dialogue is not an
	// alignment of these two files.
	if float64(pw.Matched) < 0.08*float64(min(len(from), len(to))) {
		return nil, fmt.Errorf("%w: matched ratio too low", errNoAlignment)
	}
	sort.Float64s(residuals)
	median := residuals[len(residuals)/2]
	p90 := residuals[min(len(residuals)-1, len(residuals)*9/10)]
	if median > float64(opts.MaxResidual.Milliseconds()) || p90 > float64(opts.MaxResidualP90.Milliseconds()) {
		return nil, fmt.Errorf("%w: median residual %v", errNoAlignment, time.Duration(median)*time.Millisecond)
	}
	pw.Residual = time.Duration(median) * time.Millisecond
	return pw, nil
}
