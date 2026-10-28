package catalog

import "math"

// Estimate is a rough catalogue-size prediction for given thresholds.
// It is a heuristic from TMDB's documented popularity behaviour (a daily-
// recomputed, heavily right-skewed score) and public catalogue counts.
// Treat it as order-of-magnitude guidance for the setup UI, not a measurement.
type Estimate struct {
	Movies int `json:"movies"`
	Shows  int `json:"shows"`
	Total  int `json:"total"`
}

// Anchor points: (threshold, approximate count at-or-above) for released titles.
var moviePopAnchors = []struct{ x, y float64 }{
	{100, 800}, {50, 2500}, {20, 9000}, {10, 22000}, {5, 48000}, {2, 120000}, {1, 230000}, {0.5, 380000},
}
var tvPopAnchors = []struct{ x, y float64 }{
	{100, 400}, {50, 1200}, {20, 4000}, {10, 9000}, {5, 19000}, {2, 45000}, {1, 80000}, {0.5, 130000},
}
var movieVoteAnchors = []struct{ x, y float64 }{
	{10000, 900}, {5000, 2200}, {1000, 11000}, {500, 20000}, {100, 52000}, {50, 76000}, {10, 150000}, {1, 320000},
}
var tvVoteAnchors = []struct{ x, y float64 }{
	{10000, 250}, {5000, 600}, {1000, 3200}, {500, 6000}, {100, 17000}, {50, 26000}, {10, 55000}, {1, 120000},
}

func interp(anchors []struct{ x, y float64 }, x float64) float64 {
	if x <= 0 {
		return 0
	}
	// Anchors are descending in x / ascending in y as x falls.
	if x >= anchors[0].x {
		// Extrapolate the first segment in log-log space.
		a, b := anchors[0], anchors[1]
		return math.Exp(lerpLog(x, a.x, b.x, a.y, b.y))
	}
	for i := 0; i+1 < len(anchors); i++ {
		a, b := anchors[i], anchors[i+1]
		if x <= a.x && x >= b.x {
			return math.Exp(lerpLog(x, a.x, b.x, a.y, b.y))
		}
	}
	a, b := anchors[len(anchors)-2], anchors[len(anchors)-1]
	y := math.Exp(lerpLog(x, a.x, b.x, a.y, b.y))
	return y
}

func lerpLog(x, x1, x2, y1, y2 float64) float64 {
	if x1 == x2 {
		return math.Log(y1)
	}
	t := (math.Log(x) - math.Log(x1)) / (math.Log(x2) - math.Log(x1))
	return math.Log(y1) + t*(math.Log(y2)-math.Log(y1))
}

// EstimateCatalogSize estimates browsable titles with MeetsThreshold's OR rule.
// The union of the popularity set and vote set overlaps substantially; the
// estimate uses max + 40% of the smaller set as the non-overlapping remainder.
func EstimateCatalogSize(minPopularity float64, minVotes int) Estimate {
	union := func(pop, votes float64) int {
		if pop == 0 {
			return int(math.Round(votes))
		}
		if votes == 0 {
			return int(math.Round(pop))
		}
		hi, lo := pop, votes
		if votes > hi {
			hi, lo = votes, pop
		}
		return int(math.Round(hi + 0.4*lo))
	}
	var mp, mv, tp, tv float64
	if minPopularity > 0 {
		mp = interp(moviePopAnchors, minPopularity)
		tp = interp(tvPopAnchors, minPopularity)
	}
	if minVotes > 0 {
		mv = interp(movieVoteAnchors, float64(minVotes))
		tv = interp(tvVoteAnchors, float64(minVotes))
	}
	if minPopularity <= 0 && minVotes <= 0 {
		// Everything: ~900k movies / ~180k shows on TMDB (order of magnitude).
		return Estimate{Movies: 900000, Shows: 180000, Total: 1080000}
	}
	e := Estimate{Movies: union(mp, mv), Shows: union(tp, tv)}
	e.Total = e.Movies + e.Shows
	return e
}

// EstimateTitles is a convenience wrapper returning only the total.
func EstimateTitles(minPopularity float64, minVotes int) int {
	return EstimateCatalogSize(minPopularity, minVotes).Total
}
