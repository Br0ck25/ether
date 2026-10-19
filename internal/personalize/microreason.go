package personalize

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Micro-reason decision header (roadmap item #31): on a detail page the
// viewer is deciding, so the header answers the three questions that
// decide it on a weeknight — is it for me, why, and when does it end:
//
//	"96% Match · Directed by Denis Villeneuve · Ends at 10:45 PM"
//
// The match percentage is calibrated, not decorative: viewers whose taste
// model is still thin get no percentage rather than a fake one, and the
// scale tops out at 98 so not everything is "99% Match". The finish time
// is now + remaining runtime (resume-aware; a show's runtime is its
// episode runtime) in the server's local time zone.

// matchConfidence is the taste confidence below which no percentage is
// shown: the model does not know the viewer well enough yet.
const matchConfidence = 0.25

// MicroReason implements core.MicroReasoner. It only reads t.
func (e *Engine) MicroReason(ctx context.Context, user *core.User, t *core.Title, now time.Time) core.MicroReason {
	var mr core.MicroReason
	if t == nil || user == nil {
		return mr
	}
	if now.IsZero() {
		now = e.now()
	}
	now = now.In(e.loc)
	var ts *Taste
	e.mu.Lock()
	e.users[user.ID] = *user // a queued build must see the real restrictions
	if m := e.models[user.ID]; m != nil {
		ts = m.taste
	}
	e.mu.Unlock()
	if ts == nil {
		// Do not trigger a build for a cosmetic header; queue one.
		e.request(user.ID)
	}
	if ts != nil && ts.Confidence >= matchConfidence {
		fit, why := ts.Fit(t)
		if fit > 0 && !genreVetoed(ts, t) {
			mr.MatchPercent = matchPercent(fit)
			mr.HasMatch = true
		}
		// A named person or franchise is a stronger hook than Fit's
		// largest weighted part (often the genre), so prefer those when
		// the affinity is really there.
		mr.Reason = personReason(ts, t)
		if mr.Reason == "" && t.CollectionID != 0 && ts.Franchise[t.CollectionID] > 0.2 && t.CollectionName != "" {
			mr.Reason = "Part of " + t.CollectionName
		}
		if mr.Reason == "" {
			mr.Reason = why
		}
		if mr.Reason == "" {
			mr.Reason = becauseYouWatched(ts, t)
		}
	}
	if rem := e.remainingRuntime(ctx, user, t); rem > 0 {
		mr.EndsAt = now.Add(rem)
		mr.HasEnd = true
	}
	mr.Header = formatMicroHeader(mr, e.clock24())
	return mr
}

// matchPercent maps a taste fit (0..1 shown) onto 60..98: most titles a
// viewer opens are plausible, so the interesting range is spread wide,
// and 99% is never claimed.
func matchPercent(fit float64) int {
	p := int(60 + 38*clamp(fit, 0, 1) + 0.5)
	if p > 98 {
		p = 98
	}
	return p
}

// personReason names the strongest person on the title the viewer has a
// real affinity for ("Directed by …", "With …"), or "" below threshold.
func personReason(ts *Taste, t *core.Title) string {
	best, reason := 0.45, ""
	see := func(p core.Person, verb string) {
		if w := ts.Person[p.TMDB]; p.TMDB != 0 && w > best {
			best, reason = w, verb+" "+p.Name
		}
	}
	for _, p := range t.Directors {
		see(p, "Directed by")
	}
	for i, p := range t.Writers {
		if i < 3 {
			see(p, "Written by")
		}
	}
	for i, p := range t.Cast {
		if i < 5 {
			see(p, "With")
		}
	}
	return reason
}

// becauseYouWatched finds the finished favourite most similar to t, for
// titles whose own features carry no strong taste reason.
func becauseYouWatched(ts *Taste, t *core.Title) string {
	best, name := 0.35, ""
	for i := range ts.Evidence {
		ev := &ts.Evidence[i]
		if ev.Kind != EvidenceFinished || ev.Weight < 0.5 || ev.Key == tasteKey(t.Key) {
			continue
		}
		if s := titleSimilarity(&ev.Title, t); s > best {
			best, name = s, ev.Title.Title
		}
	}
	if name == "" {
		return ""
	}
	return "Because you watched " + name
}

// remainingRuntime is how much is left to watch: the title's runtime
// (episode runtime for a show) minus the resume offset when in progress.
// Zero when unknown or already finished.
func (e *Engine) remainingRuntime(ctx context.Context, user *core.User, t *core.Title) time.Duration {
	rt := t.Runtime
	var ws core.WatchStatus
	if e.state != nil {
		ws, _ = e.state.Get(ctx, user.ID, t.Key)
	}
	if rt <= 0 && ws.Duration > 0 {
		rt = ws.Duration
	}
	if rt <= 0 {
		return 0
	}
	if ws.ViewCount > 0 && ws.ViewOffset == 0 {
		return rt // a rewatch starts from the beginning
	}
	if ws.ViewOffset > 0 && ws.ViewOffset < rt {
		return rt - ws.ViewOffset
	}
	return rt
}

// formatMicroHeader joins the parts that exist, e.g.
// "96% Match · Directed by Denis Villeneuve · Ends at 10:45 PM".
func formatMicroHeader(mr core.MicroReason, h24 bool) string {
	var parts []string
	if mr.HasMatch {
		parts = append(parts, fmt.Sprintf("%d%% Match", mr.MatchPercent))
	}
	if mr.Reason != "" {
		parts = append(parts, mr.Reason)
	}
	if mr.HasEnd {
		parts = append(parts, "Ends at "+formatClock(mr.EndsAt, h24))
	}
	return strings.Join(parts, " · ")
}

func (e *Engine) clock24() bool {
	style := ""
	if e.opts.ClockFormat != nil {
		style = strings.TrimSpace(e.opts.ClockFormat())
	}
	return clockIs24h(style)
}

// clockIs24h interprets a style ("12h"/"24h") or a locale ("en-GB").
// Locales that conventionally use a 12-hour clock are listed; everything
// else with a region is 24-hour, and a bare language/empty value (Ether's
// default catalogue language is en-US) is 12-hour.
func clockIs24h(style string) bool {
	switch strings.ToLower(style) {
	case "12h", "12", "ampm":
		return false
	case "24h", "24", "military":
		return true
	}
	loc := strings.ReplaceAll(strings.ToUpper(style), "_", "-")
	parts := strings.SplitN(loc, "-", 2)
	if len(parts) < 2 || parts[1] == "" {
		return false
	}
	switch parts[1] {
	case "US", "CA", "AU", "NZ", "PH", "IN", "PK", "BD", "EG", "SA", "MX", "CO", "PE":
		return false
	}
	return true
}

func formatClock(t time.Time, h24 bool) string {
	if h24 {
		return t.Format("15:04")
	}
	return t.Format("3:04 PM")
}

var _ core.MicroReasoner = (*Engine)(nil)
