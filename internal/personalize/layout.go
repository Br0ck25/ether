package personalize

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// ValidationError lists problems with an owner-supplied document (layout
// rules, taste overrides), addressed by dotted field path.
type ValidationError struct {
	Problems []Problem
}

// Problem is one validation failure.
type Problem struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 0 {
		return "invalid"
	}
	p := e.Problems[0]
	return p.Field + ": " + p.Message
}

// InvalidFields returns (field, message) pairs; the admin API maps them to
// its validation error shape.
func (e *ValidationError) InvalidFields() [][2]string {
	out := make([][2]string, len(e.Problems))
	for i, p := range e.Problems {
		out[i] = [2]string{p.Field, p.Message}
	}
	return out
}

func (e *ValidationError) add(field, format string, args ...any) {
	e.Problems = append(e.Problems, Problem{field, fmt.Sprintf(format, args...)})
}

func (e *ValidationError) orNil() error {
	if len(e.Problems) == 0 {
		return nil
	}
	return e
}

var validDevices = map[string]bool{core.DeviceTV: true, core.DeviceMobile: true, core.DeviceWeb: true, core.DeviceDesktop: true}

// normalizeLayout validates rules and fills defaults (ids, section).
func normalizeLayout(rules []core.LayoutRule) ([]core.LayoutRule, error) {
	ve := &ValidationError{}
	out := make([]core.LayoutRule, 0, len(rules))
	ids := map[string]bool{}
	for i, r := range rules {
		f := fmt.Sprintf("rules[%d]", i)
		switch r.Section {
		case "":
			r.Section = "home"
		case "home", "movies", "shows":
		default:
			ve.add(f+".section", "must be home, movies or shows")
		}
		if r.MaxRows < 0 || r.MaxRows > 100 {
			ve.add(f+".maxRows", "must be between 0 and 100")
		}
		if !r.Scope.All && len(r.Scope.UserIDs) == 0 {
			r.Scope.All = true
		}
		if r.ID == "" || ids[r.ID] {
			r.ID = newRuleID()
		}
		ids[r.ID] = true
		if r.Rows == nil {
			r.Rows = []core.LayoutRowRule{}
		}
		for j, rr := range r.Rows {
			rf := fmt.Sprintf("%s.rows[%d]", f, j)
			if strings.TrimSpace(rr.HubID) == "" {
				ve.add(rf+".hubId", "is required")
			} else if _, err := path.Match(rr.HubID, ""); err != nil {
				ve.add(rf+".hubId", "is not a valid pattern")
			}
			switch rr.Action {
			case core.LayoutPin, core.LayoutHide, core.LayoutBoost, core.LayoutBury:
			default:
				ve.add(rf+".action", "must be pin, hide, boost or bury")
			}
			if rr.Position != nil && (*rr.Position < 0 || *rr.Position > 100) {
				ve.add(rf+".position", "must be between 0 and 100")
			}
			if w := rr.When; w != nil {
				for _, d := range w.Days {
					if d < 0 || d > 6 {
						ve.add(rf+".when.days", "days are 0 (Sunday) to 6 (Saturday)")
						break
					}
				}
				if w.FromHour != nil && (*w.FromHour < 0 || *w.FromHour > 23) {
					ve.add(rf+".when.fromHour", "must be 0-23")
				}
				if w.ToHour != nil && (*w.ToHour < 0 || *w.ToHour > 24) {
					ve.add(rf+".when.toHour", "must be 0-24")
				}
				for _, d := range w.Devices {
					if !validDevices[d] {
						ve.add(rf+".when.devices", "devices are tv, mobile, web or desktop")
						break
					}
				}
			}
		}
		out = append(out, r)
	}
	return out, ve.orNil()
}

func newRuleID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "r" + hex.EncodeToString(b[:])
}

// whenMatches evaluates a schedule against the request context.
func whenMatches(w *core.LayoutWhen, hc core.HubContext) bool {
	if w == nil {
		return true
	}
	if len(w.Months) > 0 {
		ok := false
		for _, m := range w.Months {
			if m == int(hc.Now.Month()) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if len(w.Dayparts) > 0 {
		ok := false
		for _, d := range w.Dayparts {
			if d == core.DaypartAt(hc.Now) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	date := hc.Now.Format("2006-01-02")
	if w.StartDate != "" && date < w.StartDate || w.EndDate != "" && date > w.EndDate {
		return false
	}
	if len(w.Days) > 0 {
		ok := false
		for _, d := range w.Days {
			if d == int(hc.Now.Weekday()) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if w.FromHour != nil || w.ToHour != nil {
		from, to := 0, 24
		if w.FromHour != nil {
			from = *w.FromHour
		}
		if w.ToHour != nil {
			to = *w.ToHour
		}
		h := hc.Now.Hour()
		if from <= to {
			if h < from || h >= to {
				return false
			}
		} else if h < from && h >= to { // wraps midnight
			return false
		}
	}
	if len(w.Devices) > 0 {
		ok := false
		for _, d := range w.Devices {
			if d == hc.Device {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// rowMatches reports whether a rule's hubId targets a row: by exact hub
// id, by kind, or by glob over either. Hub ids also match without their
// screen prefix, so "because.*" covers "home.because.123" and
// "movie.because.123".
func rowMatches(pattern, hubID, kind string) bool {
	stem := hubID
	for _, p := range []string{"home.", "movie.", "tv."} {
		if s, ok := strings.CutPrefix(hubID, p); ok {
			stem = s
			break
		}
	}
	for _, cand := range []string{hubID, stem, kind} {
		if pattern == cand {
			return true
		}
		if strings.ContainsAny(pattern, "*?[") {
			if ok, _ := path.Match(pattern, cand); ok {
				return true
			}
		}
	}
	return false
}

// decision is the layout outcome for one row.
type decision struct {
	hidden, pinned bool
	position       int
	boost, bury    bool
}

// layoutPlan is the rules that apply to one viewer, section and context.
type layoutPlan struct {
	rules   []core.LayoutRowRule // in application order; later wins
	maxRows int
}

// planFor selects the rules that apply. Server-wide rules apply first,
// then rules scoped to specific users, so a per-user rule overrides a
// server-wide one.
func planFor(rules []core.LayoutRule, userID int64, section string, hc core.HubContext) layoutPlan {
	var global, scoped []core.LayoutRule
	for _, r := range rules {
		sec := r.Section
		if sec == "" {
			sec = "home"
		}
		if sec != section || !r.Scope.AppliesTo(userID) {
			continue
		}
		if r.Scope.All || len(r.Scope.UserIDs) == 0 {
			global = append(global, r)
		} else {
			scoped = append(scoped, r)
		}
	}
	var p layoutPlan
	for _, r := range append(global, scoped...) {
		if r.MaxRows > 0 {
			p.maxRows = r.MaxRows
		}
		for _, rr := range r.Rows {
			if whenMatches(rr.When, hc) {
				p.rules = append(p.rules, rr)
			}
		}
	}
	return p
}

func (p layoutPlan) decide(hubID, kind string) decision {
	var d decision
	for _, rr := range p.rules {
		if !rowMatches(rr.HubID, hubID, kind) {
			continue
		}
		switch rr.Action {
		case core.LayoutPin:
			d.pinned, d.hidden = true, false
			d.position = 0
			if rr.Position != nil {
				d.position = *rr.Position
			}
		case core.LayoutHide:
			d.hidden, d.pinned = true, false
		case core.LayoutBoost:
			d.boost, d.bury = true, false
		case core.LayoutBury:
			d.bury, d.boost = true, false
		}
	}
	return d
}

// placePinned inserts pinned rows at their positions (0 = top) into the
// ranked list, in ascending position order.
func placePinned[T any](ranked []T, pinned []T, pos func(T) int) []T {
	sort.SliceStable(pinned, func(i, j int) bool { return pos(pinned[i]) < pos(pinned[j]) })
	out := append([]T{}, ranked...)
	for _, p := range pinned {
		i := pos(p)
		if i > len(out) {
			i = len(out)
		}
		out = append(out, p)
		copy(out[i+1:], out[i:])
		out[i] = p
	}
	return out
}
