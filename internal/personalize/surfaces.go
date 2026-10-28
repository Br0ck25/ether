package personalize

import (
	"math"
	"math/rand/v2"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Surfaces. Home is "what to watch right now": short, high-confidence,
// context-aware and mixed. Movies → Recommended explores film taste
// through lenses; TV Shows → Recommended is series-centric. Every row kind
// declares the surfaces it belongs to by default, with a prior weight per
// surface, and the surfaces it can ever fit (kinds.go). The bandit learns
// per (surface, kind) and falls back to the kind's evidence while a
// surface arm is cold.
const (
	surfHome   = "home"
	surfMovies = "movies"
	surfShows  = "shows"
)

// Designer groups (core.RowDescriptor.Group).
const (
	groupPicks       = "picks"
	groupExplore     = "explore"
	groupShows       = "shows"
	groupHousehold   = "household"
	groupCollections = "collections"
	groupPlex        = "plex"
)

var allSurfaces = []string{surfHome, surfMovies, surfShows}

// kindFits returns the surfaces a kind can ever appear on.
func kindFits(k kindInfo) []string {
	if len(k.fits) > 0 {
		return k.fits
	}
	return allSurfaces
}

// fitsSurface reports whether a kind can appear on a surface at all.
func fitsSurface(kind, surface string) bool {
	for _, s := range kindFits(infoFor(kind)) {
		if s == surface {
			return true
		}
	}
	return false
}

// surfaceOfHub maps a served hub id to the surface it was served on.
func surfaceOfHub(hubID string) string {
	switch {
	case strings.HasPrefix(hubID, "movie."):
		return surfMovies
	case strings.HasPrefix(hubID, "tv."):
		return surfShows
	}
	return surfHome
}

// surfaceKey keys a (surface, kind) bandit arm next to the kind-level arm.
func surfaceKey(surface, kind string) string { return surface + "|" + kind }

// surface is the assembly's surface name.
func (a *assembly) surface() string { return sectionName(a.section) }

// surfaceWeight is the row's prior weight on this surface; 0 = not a
// default member. Owner collection rows carry their own targeting
// (ownerCandidate) and weigh 1 wherever they arrive.
func (a *assembly) surfaceWeight(c *candRow) float64 {
	if c.source == core.RowSourceOwner {
		return 1
	}
	return infoFor(c.kind).surfaces[a.surface()]
}

// admit decides whether a candidate row may appear on this surface and
// with which prior weight: default members; new viewers' cold-start
// section rows on Home; and any row an owner policy names explicitly for
// this page, wherever the row fits.
func (a *assembly) admit(c *candRow, hubID string, newViewer bool) (float64, bool) {
	if w := a.surfaceWeight(c); w > 0 {
		return w, true
	}
	if a.section == "" && newViewer && c.coldHome {
		return 1, true
	}
	if a.ownerPlaced(c, hubID) && fitsSurface(c.kind, a.surface()) {
		return 1, true
	}
	return 0, false
}

// ownerPlaced reports whether the effective layout names this row on this
// page explicitly (by kind, hub id or stem; not a wildcard) and does not
// switch it off.
func (a *assembly) ownerPlaced(c *candRow, hubID string) bool {
	if a.layout == nil {
		return false
	}
	p, _ := effectiveHomePolicy(*a.layout, a.user.ID, a.surface(), core.HomeRow{HubID: hubID, Kind: c.kind})
	return p != nil && p.Status != "off" && !strings.ContainsAny(p.Row, "*?[")
}

// surfacePriorStrength is how many row-days of the kind-level estimate a
// surface arm's server-wide evidence is weighed against.
const surfacePriorStrength = 20

// surfaceRowRate is the hierarchical bandit: the kind prior (scaled by the
// surface weight) flows through the kind's server-wide evidence, then the
// surface's server-wide evidence; the viewer's kind-level lift over the
// server applies on top, and the viewer's own surface evidence decides.
// While both surface arms are cold this is exactly rowRate over the kind
// arms (with the weighted prior), so existing learning carries over.
func surfaceRowRate(prior, weight float64, gKind, uKind, gSurf, uSurf arm, explore float64, rng *rand.Rand) (mean, sample float64) {
	if weight <= 0 {
		weight = 1
	}
	if gSurf.shows == 0 && uSurf.shows == 0 {
		return rowRate(prior*weight, gKind, uKind, explore, rng)
	}
	gk := clamp((gKind.reward+globalPriorStrength*prior)/(gKind.shows+globalPriorStrength), 0.001, 0.999)
	uk := (uKind.reward + userPriorStrength*gk) / (uKind.shows + userPriorStrength)
	lift := uk / gk
	gs := clamp((gSurf.reward+surfacePriorStrength*gk*weight)/(gSurf.shows+surfacePriorStrength), 0.001, 0.999)
	m := clamp(gs*lift, 0.001, 0.999)
	// The viewer's kind-level evidence lends the surface prior part of
	// its confidence.
	n0 := userPriorStrength + 0.5*uKind.shows
	alpha := uSurf.reward + n0*m
	beta := math.Max(uSurf.shows-uSurf.reward, 0) + n0*(1-m)
	mean = alpha / (alpha + beta)
	if rng == nil || explore <= 0 {
		return mean, mean
	}
	s := betaSample(rng, alpha, beta)
	return mean, clamp(mean+explore*(s-mean), 0.0005, 1)
}

// banditRate is the Thompson-sampled play rate of a row on this surface.
func (a *assembly) banditRate(c *candRow, rng *rand.Rand) float64 {
	info := infoFor(c.kind)
	s := a.surface()
	pk := priorKind(c.kind)
	w, ok := a.weights[c]
	if !ok {
		w = a.surfaceWeight(c)
	}
	_, rate := surfaceRowRate(info.prior, w, a.global[pk], a.arms[c.kind],
		a.global[surfaceKey(s, pk)], a.arms[surfaceKey(s, c.kind)], a.explore, rng)
	return rate
}

// sectionBoost replaces the Home's editorial priors on a Movies or TV
// Shows page: the lenses lead there, popularity rows follow.
func sectionBoost(kind string) float64 {
	switch kind {
	case KindNewSeasons:
		return 1.6
	case KindFinishShow:
		return 1.5
	case KindTasteCluster, KindBridge:
		return 1.35
	case KindStartNew, KindFranchise, KindSticksLanding:
		return 1.2
	case KindPerson, KindStudio, KindWorld, KindAcclaimed, KindBinge, KindAiringTaste, KindStandalone:
		return 1.1
	case KindTrending, KindNewReleases, KindTopRated:
		return 0.9
	case KindPopular:
		return 0.8
	}
	return 1
}

// surfaceMult is the surface- and tuning-dependent part of a row's score
// (one hook in rowScore): the owner's dials, occasion context, the
// section's editorial priors and demotion of rows the Home already leads
// with.
func (a *assembly) surfaceMult(c *candRow) float64 {
	m := a.dials().rowMult(infoFor(c.kind).lean)
	if c.kind == KindOccasion {
		m *= occasionMult(c.id, a.hc)
	}
	if a.section != "" {
		m *= sectionBoost(c.kind) / editorialBoost(c.kind)
		if a.lead != nil && a.lead.same[c.id] != nil {
			m *= 0.8 // the Home already shows this row
		}
		if a.demoted[c] {
			m *= 0.75 // kept despite repeating the Home's titles
		}
	}
	return m
}

// sectionCap is the per-kind row cap on this surface.
func (a *assembly) kindCap(kind string) int {
	info := infoFor(kind)
	if a.section != "" && info.sectionCap > 0 {
		return info.sectionCap
	}
	return info.cap
}
