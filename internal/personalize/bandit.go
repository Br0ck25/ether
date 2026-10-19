package personalize

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
)

// arm is the evidence for one row kind: how many row-days it was shown
// and the sum of play rewards it earned (see playRecord.reward).
type arm struct {
	shows  float64
	reward float64
}

// Bandit tunables.
const (
	// globalPriorStrength is how many row-days of evidence the kind's
	// hand-set prior is worth in the server-wide estimate.
	globalPriorStrength = 30
	// userPriorStrength is how many row-days the server-wide estimate is
	// worth in a viewer's own estimate: a viewer's behaviour takes over
	// once they have seen a row kind on a few dozen days.
	userPriorStrength = 40
)

// rowRate estimates a viewer's play rate for a row kind and draws a
// Thompson sample from its Beta posterior. The prior flows from the
// hand-set kind prior, through the server-wide evidence, into the viewer's
// own evidence. explore (0..1) scales how far the sample may stray from
// the posterior mean: 0 = pure exploitation, 1 = full Thompson sampling.
// The rng is seeded per viewer, day and kind, so a viewer's Home is stable
// within a day and re-explores daily.
func rowRate(prior float64, global, user arm, explore float64, rng *rand.Rand) (mean, sample float64) {
	g := (global.reward + globalPriorStrength*prior) / (global.shows + globalPriorStrength)
	g = clamp(g, 0.001, 0.999)
	alpha := user.reward + userPriorStrength*g
	beta := math.Max(user.shows-user.reward, 0) + userPriorStrength*(1-g)
	mean = alpha / (alpha + beta)
	if rng == nil || explore <= 0 {
		return mean, mean
	}
	s := betaSample(rng, alpha, beta)
	return mean, clamp(mean+explore*(s-mean), 0.0005, 1)
}

// betaSample draws from Beta(a, b) via two Gamma draws.
func betaSample(rng *rand.Rand, a, b float64) float64 {
	x := gammaSample(rng, a)
	y := gammaSample(rng, b)
	if x+y == 0 {
		return a / (a + b)
	}
	return x / (x + y)
}

// gammaSample draws from Gamma(k, 1) (Marsaglia–Tsang; boosted for k < 1).
func gammaSample(rng *rand.Rand, k float64) float64 {
	if k <= 0 {
		return 0
	}
	if k < 1 {
		u := rng.Float64()
		return gammaSample(rng, k+1) * math.Pow(u, 1/k)
	}
	d := k - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// seeded returns a deterministic rng for the given parts.
func seeded(parts ...any) *rand.Rand {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(fmt.Sprint(p)))
		_, _ = h.Write([]byte{0})
	}
	s := h.Sum64()
	return rand.New(rand.NewPCG(s, s^0x9e3779b97f4a7c15))
}

// unit returns a deterministic value in [0, 1) for the given parts.
func unit(parts ...any) float64 { return seeded(parts...).Float64() }
