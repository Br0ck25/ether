package personalize

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// The layered Home Designer model (core.HomeLayout): a Global template
// every viewer starts from, the engine's personalization within it, and
// per-viewer overrides on top. This file validates documents, converts
// the legacy rule list, and resolves the effective policy for a row.

// Limits of the layout model.
const (
	maxLayoutSlot     = 50
	maxLayoutPageRows = 50
	maxPolicyPerPage  = 10
	minPriority       = 0.1
	maxPriority       = 5.0
)

var layoutSections = []string{"home", "movies", "shows"}

var validDayparts = map[string]bool{core.DaypartMorning: true, core.DaypartAfternoon: true, core.DaypartEvening: true, core.DaypartLate: true}

// frontEndRows are rows the Plex front end builds outside the engine's
// arrangement: policies cannot target them.
var frontEndRows = map[string]bool{KindRecentlyAdded: true, KindPlaylists: true}

// normalizeHomeLayout validates a layout document and puts it in
// canonical form: sections defaulted and ordered, statuses filled in,
// empty pages and viewer layers dropped. Notes are cleared.
func normalizeHomeLayout(doc *core.HomeLayout) (*core.HomeLayout, error) {
	ve := &ValidationError{}
	out := &core.HomeLayout{Global: []core.LayoutPage{}, Viewers: []core.ViewerLayout{}}
	if doc == nil {
		return out, nil
	}
	out.Global = normalizePages(ve, "global", doc.Global)
	seen := map[int64]bool{}
	for i, v := range doc.Viewers {
		f := fmt.Sprintf("viewers[%d]", i)
		if v.UserID <= 0 {
			ve.add(f+".userId", "must be a viewer id")
			continue
		}
		if seen[v.UserID] {
			ve.add(f+".userId", "viewer %d has two override layers", v.UserID)
			continue
		}
		seen[v.UserID] = true
		pages := normalizePages(ve, f+".pages", v.Pages)
		if len(pages) > 0 {
			out.Viewers = append(out.Viewers, core.ViewerLayout{UserID: v.UserID, Pages: pages})
		}
	}
	sort.Slice(out.Viewers, func(i, j int) bool { return out.Viewers[i].UserID < out.Viewers[j].UserID })
	return out, ve.orNil()
}

func normalizePages(ve *ValidationError, field string, pages []core.LayoutPage) []core.LayoutPage {
	bySection := map[string]core.LayoutPage{}
	for i, p := range pages {
		f := fmt.Sprintf("%s[%d]", field, i)
		p.Section = strings.ToLower(strings.TrimSpace(p.Section))
		if p.Section == "" {
			p.Section = "home"
		}
		if !validSection(p.Section) {
			ve.add(f+".section", "must be home, movies or shows")
			continue
		}
		if _, dup := bySection[p.Section]; dup {
			ve.add(f+".section", "%s appears twice", p.Section)
			continue
		}
		if p.MaxRows < 0 || p.MaxRows > maxLayoutPageRows {
			ve.add(f+".maxRows", "must be between 0 and %d", maxLayoutPageRows)
		}
		var dm map[string]int
		for d, n := range p.DeviceMaxRows {
			if !validDevices[d] {
				ve.add(f+".deviceMaxRows", "devices are tv, mobile, web or desktop")
				continue
			}
			if n < 0 || n > maxLayoutPageRows {
				ve.add(f+".deviceMaxRows."+d, "must be between 0 and %d", maxLayoutPageRows)
				continue
			}
			if n > 0 {
				if dm == nil {
					dm = map[string]int{}
				}
				dm[d] = n
			}
		}
		p.DeviceMaxRows = dm
		p.Rows = normalizePolicies(ve, f+".rows", p.Section, p.Rows)
		if len(p.Rows) == 0 && p.MaxRows == 0 && len(p.DeviceMaxRows) == 0 {
			continue // an empty page is the defaults (Global) or inherits everything (override)
		}
		bySection[p.Section] = p
	}
	out := []core.LayoutPage{}
	for _, s := range layoutSections {
		if p, ok := bySection[s]; ok {
			out = append(out, p)
		}
	}
	return out
}

func validSection(s string) bool {
	for _, v := range layoutSections {
		if v == s {
			return true
		}
	}
	return false
}

func normalizePolicies(ve *ValidationError, field, section string, rows []core.RowPolicy) []core.RowPolicy {
	out := make([]core.RowPolicy, 0, len(rows))
	keys := map[string]bool{}
	slots := map[int]string{}
	for j, p := range rows {
		f := fmt.Sprintf("%s[%d]", field, j)
		p.Row = strings.TrimSpace(p.Row)
		p.Status = strings.ToLower(strings.TrimSpace(p.Status))
		ok := true
		bad := func(sub, format string, args ...any) {
			ve.add(f+sub, format, args...)
			ok = false
		}
		switch {
		case p.Row == "":
			bad(".row", "is required")
		default:
			if _, err := path.Match(p.Row, ""); err != nil {
				bad(".row", "is not a valid pattern")
			}
		}
		if keys[p.Row] {
			bad(".row", "%s has two policies on this page", p.Row)
		}
		switch p.Status {
		case "":
			p.Status = core.RowEligible
			if p.Slot > 0 {
				p.Status = core.RowAlways
			}
		case core.RowAlways, core.RowEligible, core.RowOff:
		default:
			bad(".status", "must be always, eligible or off")
		}
		if p.Slot < 0 || p.Slot > maxLayoutSlot {
			bad(".slot", "must be between 1 and %d", maxLayoutSlot)
		}
		if p.MinSlot < 0 || p.MinSlot > maxLayoutSlot {
			bad(".minSlot", "must be between 1 and %d", maxLayoutSlot)
		}
		if p.MaxSlot < 0 || p.MaxSlot > maxLayoutSlot {
			bad(".maxSlot", "must be between 1 and %d", maxLayoutSlot)
		}
		if p.MinSlot > 0 && p.MaxSlot > 0 && p.MinSlot > p.MaxSlot {
			bad(".minSlot", "never above slot %d and never below slot %d cannot both hold", p.MinSlot, p.MaxSlot)
		}
		if p.Slot > 0 {
			if p.MinSlot > 0 || p.MaxSlot > 0 {
				bad(".slot", "an anchored row cannot also have position bounds")
			}
			if p.Status == core.RowOff {
				bad(".slot", "a row that is off cannot be anchored")
			}
			if p.Status == core.RowEligible {
				p.Status = core.RowAlways // an anchor guarantees the row
			}
			if other, dup := slots[p.Slot]; dup {
				bad(".slot", "slot %d is already anchored to %s", p.Slot, other)
			}
		}
		if math.IsNaN(p.Priority) || p.Priority != 0 && (p.Priority < minPriority || p.Priority > maxPriority) {
			bad(".priority", "must be between %g and %g", minPriority, maxPriority)
		}
		if p.Priority == 1 {
			p.Priority = 0
		}
		if p.MaxPerPage < 0 || p.MaxPerPage > maxPolicyPerPage {
			bad(".maxPerPage", "must be between 1 and %d", maxPolicyPerPage)
		}
		if p.MaxDaysPerWeek < 0 || p.MaxDaysPerWeek > 7 {
			bad(".maxDaysPerWeek", "must be between 1 and 6 (7 = every day)")
		}
		if p.MaxDaysPerWeek == 7 {
			p.MaxDaysPerWeek = 0
		}
		if p.When != nil {
			w := *p.When
			validateWhen(ve, f+".when", &w)
			p.When = &w
			if !hasWhen(p.When) {
				p.When = nil
			}
		}
		if frontEndRows[p.Row] {
			bad(".row", "%s is built by Plex outside the engine's arrangement", infoFor(p.Row).title)
		}
		if p.Row == KindContinue || p.Row == "home.continue" || p.Row == "continue" {
			if section != "home" {
				bad(".row", "Continue Watching is arranged by the engine on Home only")
			}
			if p.Status == core.RowOff {
				bad(".status", "Continue Watching is Plex's own row: it can be anchored or bounded, not turned off")
			}
			if p.When != nil || p.MaxDaysPerWeek > 0 {
				bad(".when", "Continue Watching is Plex's own row: it cannot be scheduled or capped")
			}
		}
		if ok {
			keys[p.Row] = true
			if p.Slot > 0 {
				slots[p.Slot] = p.Row
			}
			out = append(out, p)
		}
	}
	// Anchors first, by slot; then the rest in the owner's order.
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].Slot > 0, out[j].Slot > 0
		if ai != aj {
			return ai
		}
		return ai && out[i].Slot < out[j].Slot
	})
	return out
}

func validateWhen(ve *ValidationError, f string, w *core.LayoutWhen) {
	for _, d := range w.Days {
		if d < 0 || d > 6 {
			ve.add(f+".days", "days are 0 (Sunday) to 6 (Saturday)")
			break
		}
	}
	if w.FromHour != nil && (*w.FromHour < 0 || *w.FromHour > 23) {
		ve.add(f+".fromHour", "must be 0-23")
	}
	if w.ToHour != nil && (*w.ToHour < 0 || *w.ToHour > 24) {
		ve.add(f+".toHour", "must be 0-24")
	}
	for _, d := range w.Devices {
		if !validDevices[d] {
			ve.add(f+".devices", "devices are tv, mobile, web or desktop")
			break
		}
	}
	for _, d := range w.Dayparts {
		if !validDayparts[d] {
			ve.add(f+".dayparts", "dayparts are morning, afternoon, evening or late")
			break
		}
	}
	for _, m := range w.Months {
		if m < 1 || m > 12 {
			ve.add(f+".months", "months are 1 (January) to 12 (December)")
			break
		}
	}
	var start, end time.Time
	var err error
	if w.StartDate != "" {
		if start, err = time.Parse(time.DateOnly, w.StartDate); err != nil {
			ve.add(f+".startDate", "must be a date (YYYY-MM-DD)")
		}
	}
	if w.EndDate != "" {
		if end, err = time.Parse(time.DateOnly, w.EndDate); err != nil {
			ve.add(f+".endDate", "must be a date (YYYY-MM-DD)")
		}
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		ve.add(f+".endDate", "is before the start date")
	}
	w.Days = sortedUnique(w.Days)
	w.Months = sortedUnique(w.Months)
}

func sortedUnique(v []int) []int {
	if len(v) == 0 {
		return nil
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	out := s[:1]
	for _, x := range s[1:] {
		if x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}

func hasWhen(w *core.LayoutWhen) bool {
	return w != nil && (len(w.Days) > 0 || w.FromHour != nil || w.ToHour != nil || len(w.Devices) > 0 ||
		len(w.Dayparts) > 0 || len(w.Months) > 0 || w.StartDate != "" || w.EndDate != "")
}

// cloneLayout deep-copies a document (the engine hands out copies).
func cloneLayout(doc *core.HomeLayout) *core.HomeLayout {
	if doc == nil {
		return &core.HomeLayout{Global: []core.LayoutPage{}, Viewers: []core.ViewerLayout{}}
	}
	out := &core.HomeLayout{UpdatedAt: doc.UpdatedAt, Notes: append([]string(nil), doc.Notes...)}
	out.Global = clonePages(doc.Global)
	out.Viewers = make([]core.ViewerLayout, len(doc.Viewers))
	for i, v := range doc.Viewers {
		out.Viewers[i] = core.ViewerLayout{UserID: v.UserID, Pages: clonePages(v.Pages)}
	}
	return out
}

func clonePages(ps []core.LayoutPage) []core.LayoutPage {
	out := make([]core.LayoutPage, len(ps))
	for i, p := range ps {
		q := p
		if p.DeviceMaxRows != nil {
			q.DeviceMaxRows = make(map[string]int, len(p.DeviceMaxRows))
			for k, v := range p.DeviceMaxRows {
				q.DeviceMaxRows[k] = v
			}
		}
		q.Rows = make([]core.RowPolicy, len(p.Rows))
		for j, r := range p.Rows {
			q.Rows[j] = clonePolicy(r)
		}
		out[i] = q
	}
	return out
}

func clonePolicy(r core.RowPolicy) core.RowPolicy {
	if r.When != nil {
		w := *r.When
		w.Days = append([]int(nil), w.Days...)
		w.Devices = append([]string(nil), w.Devices...)
		w.Dayparts = append([]string(nil), w.Dayparts...)
		w.Months = append([]int(nil), w.Months...)
		if w.FromHour != nil {
			v := *w.FromHour
			w.FromHour = &v
		}
		if w.ToHour != nil {
			v := *w.ToHour
			w.ToHour = &v
		}
		r.When = &w
	}
	return r
}

// ---- legacy rules ------------------------------------------------------------------

// convertLegacyRules turns the old flat rule list (pin, hide, boost, bury
// with scopes and schedules) into a layered document, with notes for the
// owner wherever the conversion is not exact:
//
//   - server-wide rules become Global policies, rules for specific
//     viewers become each viewer's overrides;
//   - pin at position p anchors the row to slot p+1 (top = slot 1);
//     hide turns it off; boost is priority ×1.5; bury priority ×0.3;
//   - maxRows becomes the page length;
//   - a hide limited to days, hours or devices becomes a schedule for the
//     complement (the row only appears outside the old window); other
//     scheduled rules keep their effect unscheduled and are listed in the
//     notes.
func convertLegacyRules(rules []core.LayoutRule) (*core.HomeLayout, []string) {
	type pageKey struct {
		user    int64 // 0 = global
		section string
	}
	pages := map[pageKey]*core.LayoutPage{}
	var order []pageKey
	page := func(k pageKey) *core.LayoutPage {
		if p := pages[k]; p != nil {
			return p
		}
		p := &core.LayoutPage{Section: k.section, Rows: []core.RowPolicy{}}
		pages[k] = p
		order = append(order, k)
		return p
	}
	var notes []string
	// Server-wide rules first, as the old engine applied them.
	sorted := append([]core.LayoutRule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool {
		gi := sorted[i].Scope.All || len(sorted[i].Scope.UserIDs) == 0
		gj := sorted[j].Scope.All || len(sorted[j].Scope.UserIDs) == 0
		return gi && !gj
	})
	for _, r := range sorted {
		sec := r.Section
		if sec == "" {
			sec = "home"
		}
		users := []int64{0}
		if !r.Scope.All && len(r.Scope.UserIDs) > 0 {
			users = r.Scope.UserIDs
		}
		for _, uid := range users {
			p := page(pageKey{uid, sec})
			if r.MaxRows > 0 {
				p.MaxRows = r.MaxRows
			}
			for _, rr := range r.Rows {
				if frontEndRows[rr.HubID] {
					notes = append(notes, fmt.Sprintf("Dropped a %s rule for %s: Plex builds that row outside the engine's arrangement.", rr.Action, rr.HubID))
					continue
				}
				pol := findPolicy(p, rr.HubID)
				when := rr.When
				if hasWhen(when) && rr.Action == core.LayoutHide {
					if c, ok := complementWhen(when); ok {
						pol.When = c
						if pol.Status == core.RowOff {
							pol.Status = core.RowEligible
						}
						continue
					}
					notes = append(notes, fmt.Sprintf("“Hide %s (%s)” could not be expressed as a schedule; the row is now off at all times.", rr.HubID, describeWhen(when)))
				} else if hasWhen(when) {
					notes = append(notes, fmt.Sprintf("“%s %s (%s)” now applies at all times; add a schedule if it should not.", titleCase(rr.Action), rr.HubID, describeWhen(when)))
				}
				switch rr.Action {
				case core.LayoutPin:
					slot := 1
					if rr.Position != nil {
						slot = *rr.Position + 1
					}
					pol.Status, pol.Slot, pol.MinSlot, pol.MaxSlot = core.RowAlways, slot, 0, 0
				case core.LayoutHide:
					if pol.Row == KindContinue || pol.Row == "home.continue" {
						continue // Plex's own row was never removable
					}
					pol.Status, pol.Slot = core.RowOff, 0
				case core.LayoutBoost:
					pol.Priority = 1.5
				case core.LayoutBury:
					pol.Priority = 0.3
				}
			}
		}
	}
	doc := &core.HomeLayout{Global: []core.LayoutPage{}, Viewers: []core.ViewerLayout{}}
	viewers := map[int64]*core.ViewerLayout{}
	var uids []int64
	for _, k := range order {
		p := pages[k]
		// Two anchors on one slot (the old engine stacked pins): later
		// anchors move to the next free slot.
		used := map[int]bool{}
		for i := range p.Rows {
			if s := p.Rows[i].Slot; s > 0 {
				for used[s] && s < maxLayoutSlot {
					s++
				}
				if s != p.Rows[i].Slot {
					notes = append(notes, fmt.Sprintf("%s was pinned to an occupied position; it is now anchored to slot %d.", p.Rows[i].Row, s))
				}
				p.Rows[i].Slot = s
				used[s] = true
			}
		}
		if k.user == 0 {
			doc.Global = append(doc.Global, *p)
			continue
		}
		v := viewers[k.user]
		if v == nil {
			v = &core.ViewerLayout{UserID: k.user}
			viewers[k.user] = v
			uids = append(uids, k.user)
		}
		v.Pages = append(v.Pages, *p)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	for _, id := range uids {
		doc.Viewers = append(doc.Viewers, *viewers[id])
	}
	return doc, notes
}

func findPolicy(p *core.LayoutPage, row string) *core.RowPolicy {
	for i := range p.Rows {
		if p.Rows[i].Row == row {
			return &p.Rows[i]
		}
	}
	p.Rows = append(p.Rows, core.RowPolicy{Row: row, Status: core.RowEligible})
	return &p.Rows[len(p.Rows)-1]
}

// complementWhen returns the schedule outside w, when w restricts a single
// dimension (days, hours or devices).
func complementWhen(w *core.LayoutWhen) (*core.LayoutWhen, bool) {
	dims := 0
	if len(w.Days) > 0 {
		dims++
	}
	if w.FromHour != nil || w.ToHour != nil {
		dims++
	}
	if len(w.Devices) > 0 {
		dims++
	}
	if dims != 1 {
		return nil, false
	}
	switch {
	case len(w.Days) > 0:
		in := map[int]bool{}
		for _, d := range w.Days {
			in[d] = true
		}
		var days []int
		for d := 0; d < 7; d++ {
			if !in[d] {
				days = append(days, d)
			}
		}
		if len(days) == 0 {
			return nil, false
		}
		return &core.LayoutWhen{Days: days}, true
	case len(w.Devices) > 0:
		in := map[string]bool{}
		for _, d := range w.Devices {
			in[d] = true
		}
		var devs []string
		for _, d := range []string{core.DeviceTV, core.DeviceMobile, core.DeviceWeb, core.DeviceDesktop} {
			if !in[d] {
				devs = append(devs, d)
			}
		}
		if len(devs) == 0 {
			return nil, false
		}
		return &core.LayoutWhen{Devices: devs}, true
	default:
		from, to := 0, 24
		if w.FromHour != nil {
			from = *w.FromHour
		}
		if w.ToHour != nil {
			to = *w.ToHour
		}
		if from == 0 && to == 24 || from == to {
			return nil, false
		}
		nf, nt := to%24, from
		return &core.LayoutWhen{FromHour: &nf, ToHour: &nt}, true
	}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---- schedules --------------------------------------------------------------------

// whenMatches evaluates a schedule against the request context.
func whenMatches(w *core.LayoutWhen, hc core.HubContext) bool {
	if w == nil {
		return true
	}
	if len(w.Days) > 0 && !containsInt(w.Days, int(hc.Now.Weekday())) {
		return false
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
	if len(w.Devices) > 0 && !containsStr(w.Devices, hc.Device) {
		return false
	}
	if len(w.Dayparts) > 0 {
		dp := hc.Daypart
		if dp == "" {
			dp = core.DaypartAt(hc.Now)
		}
		if !containsStr(w.Dayparts, dp) {
			return false
		}
	}
	if len(w.Months) > 0 && !containsInt(w.Months, int(hc.Now.Month())) {
		return false
	}
	if w.StartDate != "" || w.EndDate != "" {
		d := hc.Now.Format(time.DateOnly)
		if w.StartDate != "" && d < w.StartDate || w.EndDate != "" && d > w.EndDate {
			return false
		}
	}
	return true
}

func containsInt(v []int, x int) bool {
	for _, y := range v {
		if y == x {
			return true
		}
	}
	return false
}

func containsStr(v []string, x string) bool {
	for _, y := range v {
		if y == x {
			return true
		}
	}
	return false
}

var weekdayShort = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
var monthShort = []string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// describeWhen is a schedule in words: "Weekdays · 18:00–23:00 · tv".
func describeWhen(w *core.LayoutWhen) string {
	if !hasWhen(w) {
		return "always"
	}
	var parts []string
	if len(w.Days) > 0 {
		d := sortedUnique(w.Days)
		switch {
		case len(d) == 5 && d[0] == 1 && d[4] == 5:
			parts = append(parts, "Weekdays")
		case len(d) == 2 && d[0] == 0 && d[1] == 6:
			parts = append(parts, "Weekends")
		default:
			names := make([]string, len(d))
			for i, x := range d {
				names[i] = weekdayShort[x]
			}
			parts = append(parts, strings.Join(names, ", "))
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
		parts = append(parts, fmt.Sprintf("%02d:00–%02d:00", from, to))
	}
	if len(w.Dayparts) > 0 {
		parts = append(parts, strings.Join(w.Dayparts, ", "))
	}
	if len(w.Devices) > 0 {
		parts = append(parts, strings.Join(w.Devices, ", "))
	}
	if len(w.Months) > 0 {
		names := make([]string, 0, len(w.Months))
		for _, m := range sortedUnique(w.Months) {
			names = append(names, monthShort[m])
		}
		parts = append(parts, strings.Join(names, ", "))
	}
	switch {
	case w.StartDate != "" && w.EndDate != "":
		parts = append(parts, w.StartDate+" to "+w.EndDate)
	case w.StartDate != "":
		parts = append(parts, "from "+w.StartDate)
	case w.EndDate != "":
		parts = append(parts, "until "+w.EndDate)
	}
	return strings.Join(parts, " · ")
}

// ---- resolution -------------------------------------------------------------------

// layoutIndex is a validated document ready for per-request resolution.
type layoutIndex struct {
	doc     *core.HomeLayout
	global  map[string]*core.LayoutPage
	viewers map[int64]map[string]*core.LayoutPage
}

func newLayoutIndex(doc *core.HomeLayout) *layoutIndex {
	if doc == nil {
		doc = &core.HomeLayout{}
	}
	ix := &layoutIndex{doc: doc, global: map[string]*core.LayoutPage{}, viewers: map[int64]map[string]*core.LayoutPage{}}
	for i := range doc.Global {
		ix.global[doc.Global[i].Section] = &doc.Global[i]
	}
	for i := range doc.Viewers {
		v := &doc.Viewers[i]
		m := map[string]*core.LayoutPage{}
		for j := range v.Pages {
			m[v.Pages[j].Section] = &v.Pages[j]
		}
		ix.viewers[v.UserID] = m
	}
	return ix
}

// pageView is the layers that apply to one viewer's page.
type pageView struct {
	global, viewer *core.LayoutPage
}

func (ix *layoutIndex) page(userID int64, section string) pageView {
	if ix == nil {
		return pageView{}
	}
	return pageView{global: ix.global[section], viewer: ix.viewers[userID][section]}
}

// effPolicy is the policy that applies to a row and the layer it came
// from.
type effPolicy struct {
	p     *core.RowPolicy
	layer string // core.LayerGlobal or core.LayerOverride
}

func (e effPolicy) ok() bool { return e.p != nil }

// group identifies the policy for frequency and per-page caps.
func (e effPolicy) group() string { return e.layer + "|" + e.p.Row }

// matchRank is how specifically a policy's Row targets a row: 3 = its
// hub id, 2 = its kind, 1 = a glob, 0 = no match. Hub ids also match
// without their screen prefix, so "because.123" targets "home.because.123".
func matchRank(pattern, hubID, kind string) int {
	stem := hubID
	for _, p := range []string{"home.", "movie.", "tv."} {
		if s, ok := strings.CutPrefix(hubID, p); ok {
			stem = s
			break
		}
	}
	if pattern == hubID || pattern == stem {
		return 3
	}
	if pattern == kind {
		return 2
	}
	if strings.ContainsAny(pattern, "*?[") {
		for _, cand := range []string{hubID, stem, kind} {
			if ok, _ := path.Match(pattern, cand); ok {
				return 1
			}
		}
	}
	return 0
}

func bestPolicy(p *core.LayoutPage, hubID, kind string) *core.RowPolicy {
	if p == nil {
		return nil
	}
	var best *core.RowPolicy
	bestRank := 0
	for i := range p.Rows {
		if r := matchRank(p.Rows[i].Row, hubID, kind); r > bestRank {
			best, bestRank = &p.Rows[i], r
		}
	}
	return best
}

// policyFor resolves a row's effective policy: the viewer's override if
// one matches, else the Global policy, else none (engine defaults).
func (pv pageView) policyFor(hubID, kind string) effPolicy {
	if p := bestPolicy(pv.viewer, hubID, kind); p != nil {
		return effPolicy{p, core.LayerOverride}
	}
	if p := bestPolicy(pv.global, hubID, kind); p != nil {
		return effPolicy{p, core.LayerGlobal}
	}
	return effPolicy{}
}

// maxRows resolves the page length for a device: the viewer's device
// setting, their page setting, the Global device setting, the Global page
// setting, then the engine default.
func (pv pageView) maxRows(device string, def int) (int, string) {
	for _, l := range []struct {
		p     *core.LayoutPage
		layer string
	}{{pv.viewer, core.LayerOverride}, {pv.global, core.LayerGlobal}} {
		if l.p == nil {
			continue
		}
		if n := l.p.DeviceMaxRows[device]; n > 0 {
			return n, l.layer
		}
		if l.p.MaxRows > 0 {
			return l.p.MaxRows, l.layer
		}
	}
	return def, ""
}

// collectionIDs lists owner collections the layers place on this page by
// exact kind ("collection:<id>"), Global first (audience applies) and the
// viewer's own (audience ignored).
func (pv pageView) collectionIDs() (global, viewer []string) {
	pick := func(p *core.LayoutPage) []string {
		if p == nil {
			return nil
		}
		var out []string
		for _, r := range p.Rows {
			if r.Status == core.RowOff || strings.ContainsAny(r.Row, "*?[") {
				continue
			}
			if id, ok := strings.CutPrefix(r.Row, KindCollectionPrefix); ok && id != "" {
				out = append(out, id)
			}
		}
		return out
	}
	viewer = pick(pv.viewer)
	inViewer := map[string]bool{}
	for _, id := range viewer {
		inViewer[id] = true
	}
	for _, id := range pick(pv.global) {
		if !inViewer[id] {
			global = append(global, id)
		}
	}
	// A viewer's "off" for a Global placement removes it.
	if pv.viewer != nil {
		kept := global[:0]
		for _, id := range global {
			if p := bestPolicy(pv.viewer, "collection."+id, KindCollectionPrefix+id); p != nil && p.Status == core.RowOff {
				continue
			}
			kept = append(kept, id)
		}
		global = kept
	}
	return global, viewer
}

// overrideCount is how many settings a viewer overrides on a page.
func overrideCount(p *core.LayoutPage) int {
	if p == nil {
		return 0
	}
	n := len(p.Rows)
	if p.MaxRows > 0 {
		n++
	}
	return n + len(p.DeviceMaxRows)
}

// ordinal is 1st, 2nd, 3rd, 4th …
func ordinal(n int) string {
	suf := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suf = "st"
	case n%10 == 2:
		suf = "nd"
	case n%10 == 3:
		suf = "rd"
	}
	return strconv.Itoa(n) + suf
}
