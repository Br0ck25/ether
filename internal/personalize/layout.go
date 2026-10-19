package personalize

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
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

// normalizeLayout validates legacy layout rules (the flat pin / hide /
// boost / bury list that predates the layered model) and fills defaults
// (ids, section). PUT layout still accepts them; convertLegacyRules turns
// them into a HomeLayout.
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
