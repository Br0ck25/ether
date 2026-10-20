package personalize

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("personalize", 2, `CREATE TABLE personalize_home_layout (id INTEGER PRIMARY KEY CHECK(id=1), document TEXT NOT NULL);`)
}

func cloneLayout(d core.HomeLayout) core.HomeLayout {
	b, _ := json.Marshal(d)
	var out core.HomeLayout
	_ = json.Unmarshal(b, &out)
	if out.Global == nil {
		out.Global = []core.LayoutPage{}
	}
	if out.Viewers == nil {
		out.Viewers = []core.ViewerLayout{}
	}
	for i := range out.Global {
		if out.Global[i].Rows == nil {
			out.Global[i].Rows = []core.RowPolicy{}
		}
	}
	for i := range out.Viewers {
		if out.Viewers[i].Pages == nil {
			out.Viewers[i].Pages = []core.LayoutPage{}
		}
		for j := range out.Viewers[i].Pages {
			if out.Viewers[i].Pages[j].Rows == nil {
				out.Viewers[i].Pages[j].Rows = []core.RowPolicy{}
			}
		}
	}
	return out
}
func migrateLayout(rules []core.LayoutRule) core.HomeLayout {
	d := core.HomeLayout{Global: []core.LayoutPage{}, Viewers: []core.ViewerLayout{}}
	for _, r := range rules {
		sec := r.Section
		if sec == "" {
			sec = "home"
		}
		p := core.LayoutPage{Section: sec, MaxRows: r.MaxRows, Rows: []core.RowPolicy{}}
		for _, rr := range r.Rows {
			q := core.RowPolicy{Row: rr.HubID, Status: "eligible", When: rr.When}
			switch rr.Action {
			case core.LayoutPin:
				q.Status = "always"
				q.Slot = 1
				if rr.Position != nil {
					q.Slot = *rr.Position + 1
				}
			case core.LayoutHide:
				q.Status = "off"
			case core.LayoutBoost:
				q.Priority = 1.5
			case core.LayoutBury:
				q.Priority = 0.3
			}
			p.Rows = append(p.Rows, q)
		}
		merge := func(ps *[]core.LayoutPage) {
			for i := range *ps {
				if (*ps)[i].Section == sec {
					(*ps)[i].Rows = append((*ps)[i].Rows, p.Rows...)
					if p.MaxRows > 0 {
						(*ps)[i].MaxRows = p.MaxRows
					}
					return
				}
			}
			*ps = append(*ps, p)
		}
		if r.Scope.All || len(r.Scope.UserIDs) == 0 {
			merge(&d.Global)
		} else {
			for _, id := range r.Scope.UserIDs {
				i := 0
				for i < len(d.Viewers) && d.Viewers[i].UserID != id {
					i++
				}
				if i == len(d.Viewers) {
					d.Viewers = append(d.Viewers, core.ViewerLayout{UserID: id, Pages: []core.LayoutPage{}})
				}
				merge(&d.Viewers[i].Pages)
			}
		}
	}
	// Later legacy rules win; canonicalize duplicate targets and overlapping
	// anchors so the migrated document is immediately editable by the new UI.
	canonical := func(ps []core.LayoutPage) {
		for i := range ps {
			rows := []core.RowPolicy{}
			for _, r := range ps[i].Rows {
				next := rows[:0]
				for _, prior := range rows {
					if prior.Row == r.Row {
						continue
					}
					if r.Slot > 0 && prior.Slot == r.Slot {
						prior.Slot = 0
					}
					next = append(next, prior)
				}
				rows = append(next, r)
			}
			ps[i].Rows = rows
		}
	}
	canonical(d.Global)
	for i := range d.Viewers {
		canonical(d.Viewers[i].Pages)
	}
	if len(rules) > 0 {
		d.Notes = []string{"Existing pin, hide, boost and bury rules were converted to global policies and viewer overrides. Slots now start at 1."}
	}
	return d
}
func (s *sqlStore) homeLayout(ctx context.Context, rules []core.LayoutRule) (core.HomeLayout, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT document FROM personalize_home_layout WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		if rules == nil {
			rules, err = s.layout(ctx)
			if err != nil {
				return core.HomeLayout{}, err
			}
		}
		d := migrateLayout(rules)
		if err := s.setHomeLayout(ctx, d); err != nil {
			return d, err
		}
		return d, nil
	}
	if err != nil {
		return core.HomeLayout{}, err
	}
	var d core.HomeLayout
	err = json.Unmarshal([]byte(raw), &d)
	return cloneLayout(d), err
}
func (s *sqlStore) setHomeLayout(ctx context.Context, d core.HomeLayout) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO personalize_home_layout(id,document) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET document=excluded.document`, string(b)); err != nil {
		return err
	}
	// Legacy rules are consumed once. All subsequent reads and writes use the document.
	if _, err = tx.ExecContext(ctx, `DELETE FROM personalize_layout`); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeHomeLayout(d core.HomeLayout) (core.HomeLayout, error) {
	d = cloneLayout(d)
	ve := &ValidationError{}
	pages := func(ps []core.LayoutPage, field string) {
		seen := map[string]bool{}
		for i := range ps {
			p := &ps[i]
			f := fmt.Sprintf("%s[%d]", field, i)
			if p.Section == "" {
				p.Section = "home"
			}
			if p.Section != "home" && p.Section != "movies" && p.Section != "shows" {
				ve.add(f+".section", "must be home, movies or shows")
			}
			if seen[p.Section] {
				ve.add(f+".section", "duplicate section")
			}
			seen[p.Section] = true
			if p.MaxRows < 0 || p.MaxRows > 100 {
				ve.add(f+".maxRows", "must be 0-100")
			}
			for dev, n := range p.DeviceMaxRows {
				if !validDevices[dev] || n < 1 || n > 100 {
					ve.add(f+".deviceMaxRows", "devices must be tv, mobile, web or desktop with limits 1-100")
				}
			}
			ids := map[string]bool{}
			slots := map[int]bool{}
			for j := range p.Rows {
				r := &p.Rows[j]
				rf := fmt.Sprintf("%s.rows[%d]", f, j)
				if strings.TrimSpace(r.Row) == "" {
					ve.add(rf+".row", "is required")
				}
				if _, err := path.Match(r.Row, ""); err != nil {
					ve.add(rf+".row", "invalid pattern")
				}
				if ids[r.Row] {
					ve.add(rf+".row", "duplicate row")
				}
				ids[r.Row] = true
				if r.Status != "always" && r.Status != "eligible" && r.Status != "off" {
					ve.add(rf+".status", "must be always, eligible or off")
				}
				for name, n := range map[string]int{"slot": r.Slot, "minSlot": r.MinSlot, "maxSlot": r.MaxSlot, "maxPerPage": r.MaxPerPage} {
					if n < 0 || n > 100 {
						ve.add(rf+"."+name, "must be 0-100")
					}
				}
				if r.MinSlot > 0 && r.MaxSlot > 0 && r.MinSlot > r.MaxSlot {
					ve.add(rf+".maxSlot", "must not precede minSlot")
				}
				if math.IsNaN(r.Priority) || math.IsInf(r.Priority, 0) || r.Priority < 0 || r.Priority > 10 {
					ve.add(rf+".priority", "must be 0-10")
				}
				if r.MaxDaysPerWeek < 0 || r.MaxDaysPerWeek > 7 {
					ve.add(rf+".maxDaysPerWeek", "must be 0-7")
				}
				if r.Slot > 0 {
					if slots[r.Slot] {
						ve.add(rf+".slot", "another row owns this slot")
					}
					slots[r.Slot] = true
					if r.Status != "off" {
						r.Status = "always"
					}
					r.MinSlot = 0
					r.MaxSlot = 0
				}
				if w := r.When; w != nil {
					_, err := normalizeLayout([]core.LayoutRule{{Rows: []core.LayoutRowRule{{HubID: r.Row, Action: "pin", When: w}}}})
					if err != nil {
						ve.add(rf+".when", "%s", err)
					}
					for _, m := range w.Months {
						if m < 1 || m > 12 {
							ve.add(rf+".when.months", "must be 1-12")
						}
					}
					for _, dp := range w.Dayparts {
						if dp != "morning" && dp != "afternoon" && dp != "evening" && dp != "late" {
							ve.add(rf+".when.dayparts", "invalid daypart")
						}
					}
					for _, date := range []string{w.StartDate, w.EndDate} {
						if date != "" {
							if _, err := time.Parse("2006-01-02", date); err != nil {
								ve.add(rf+".when", "dates must be YYYY-MM-DD")
							}
						}
					}
					if w.StartDate != "" && w.EndDate != "" && w.StartDate > w.EndDate {
						ve.add(rf+".when.endDate", "must follow startDate")
					}
				}
			}
		}
	}
	pages(d.Global, "layout.global")
	seen := map[int64]bool{}
	for i, v := range d.Viewers {
		if v.UserID <= 0 || seen[v.UserID] {
			ve.add(fmt.Sprintf("layout.viewers[%d].userId", i), "must be a unique positive viewer id")
		}
		seen[v.UserID] = true
		pages(v.Pages, fmt.Sprintf("layout.viewers[%d].pages", i))
	}
	return d, ve.orNil()
}
func (e *Engine) HomeLayout(ctx context.Context) (core.HomeLayout, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneLayout(e.homeLayout), nil
}
func (e *Engine) SetHomeLayout(ctx context.Context, d core.HomeLayout) (core.HomeLayout, error) {
	d, err := normalizeHomeLayout(d)
	if err != nil {
		return d, err
	}
	d.UpdatedAt = e.now()
	if err = e.store.setHomeLayout(ctx, d); err != nil {
		return d, err
	}
	e.mu.Lock()
	e.homeLayout = cloneLayout(d)
	e.layoutVer++
	e.mu.Unlock()
	e.notify("layout")
	return cloneLayout(d), nil
}
func pageFor(ps []core.LayoutPage, section string) *core.LayoutPage {
	for i := range ps {
		if ps[i].Section == section {
			return &ps[i]
		}
	}
	return nil
}
func matchingPolicy(p *core.LayoutPage, row core.HomeRow) *core.RowPolicy {
	if p == nil {
		return nil
	}
	rank := 0
	var best *core.RowPolicy
	for i := range p.Rows {
		r := &p.Rows[i]
		n := 0
		if rowMatches(r.Row, row.HubID, row.Kind) {
			n = 1
			if r.Row == row.Kind {
				n = 2
			}
			if r.Row == row.HubID {
				n = 3
			}
		}
		if n >= rank && n > 0 {
			best = r
			rank = n
		}
	}
	return best
}
func weekEligible(user int64, key string, hc core.HubContext, n int) bool {
	if n <= 0 || n >= 7 {
		return true
	}
	h := fnv.New32a()
	fmt.Fprintf(h, "%d:%s", user, key)
	day := (int(hc.Now.Weekday()) + 6) % 7
	return (day+int(h.Sum32()%7))%7 < n
}

// arrangeLayout is the final, shared composition pass, including front-end rows.
// Constraints are hard: an impossible row is explained as hidden, never placed
// outside its bounds. Explicit viewer policies replace complete global policies.
func arrangeLayout(rows []core.HomeRow, d core.HomeLayout, user int64, hc core.HubContext, section string, def int) []core.HomeRow {
	if section == "" {
		section = "home"
	}
	g := pageFor(d.Global, section)
	var v *core.LayoutPage
	for _, vl := range d.Viewers {
		if vl.UserID == user {
			v = pageFor(vl.Pages, section)
		}
	}
	maxRows := def
	for _, p := range []*core.LayoutPage{g, v} {
		if p != nil {
			if p.MaxRows > 0 {
				maxRows = p.MaxRows
			}
			if p.DeviceMaxRows[hc.Device] > 0 {
				maxRows = p.DeviceMaxRows[hc.Device]
			}
		}
	}
	if maxRows <= 0 {
		maxRows = 14
	}
	type candidate struct {
		r    core.HomeRow
		p    *core.RowPolicy
		rank float64
	}
	var pool []candidate
	hidden := []core.HomeRow{}
	counts := map[string]int{}
	for _, row := range rows {
		row.Hidden = false
		row.HiddenReason = ""
		row.Slot = 0
		row.AnchorSlot = 0
		row.Pinned = false
		row.Layer = "personalized"
		row.Placement = "Chosen by personalization"
		p := matchingPolicy(g, row)
		layer := "global"
		if q := matchingPolicy(v, row); q != nil {
			p = q
			layer = "override"
		}
		row.Policy = p
		row.PolicyLayer = ""
		if p != nil {
			row.PolicyLayer = layer
			if p.Priority > 0 || p.MinSlot > 0 || p.MaxSlot > 0 || p.When != nil || p.MaxPerPage > 0 || p.MaxDaysPerWeek > 0 {
				row.Placement = "Personalized within " + layer + " constraints"
			}
			if layer == "override" {
				row.Layer = layer
				row.Placement = "Viewer override"
			}
			why := ""
			switch {
			case p.Status == "off":
				why = "Off in " + layer + " layer"
			case !whenMatches(p.When, hc):
				why = "Outside schedule"
			case !weekEligible(user, p.Row, hc, p.MaxDaysPerWeek):
				why = "Weekly frequency cap"
			}
			if why != "" {
				row.Layer = layer
				row.Hidden = true
				row.HiddenReason = why
				hidden = append(hidden, row)
				continue
			}
			if p.Status == "always" {
				row.Layer = layer
				row.Placement = "Always included by " + layer
			}
			if p.Slot > 0 {
				row.AnchorSlot = p.Slot
				row.Pinned = true
				row.Layer = layer
				row.Placement = fmt.Sprintf("%s anchor at slot %d", layer, p.Slot)
			}
		}
		pool = append(pool, candidate{row, p, 0})
	}
	// Explanation-only hidden rows are absent from Plex's candidate list.
	// Compute ranking from the eligible pool so their presence in a preview
	// cannot change the relative effect of owner priority weights.
	for i := range pool {
		c := &pool[i]
		c.rank = float64(len(pool) - i)
		if c.p != nil {
			if c.p.Priority > 0 {
				c.rank *= c.p.Priority
			}
			if c.p.Status == "always" {
				c.rank += 10000
			}
			if c.p.Slot > 0 && c.r.Layer == "override" {
				c.rank += 100000
			}
		}
	}
	sort.SliceStable(pool, func(i, j int) bool {
		return pool[i].rank > pool[j].rank
	})
	out := []core.HomeRow{}
	used := make([]bool, len(pool))
	for slot := 1; slot <= maxRows; slot++ {
		pick := -1
		deadline := 101
		for i, c := range pool {
			if used[i] {
				continue
			}
			p := c.p
			if p != nil {
				if p.MaxPerPage > 0 && counts[p.Row] >= p.MaxPerPage {
					continue
				}
				if p.Slot > 0 && p.Slot != slot {
					continue
				}
				if p.MinSlot > slot || p.MaxSlot > 0 && p.MaxSlot < slot {
					continue
				}
				if p.Slot == slot {
					pick = i
					break
				}
				if p.MaxSlot > 0 && p.MaxSlot < deadline {
					pick = i
					deadline = p.MaxSlot
					continue
				}
			}
			if pick < 0 {
				pick = i
			}
		}
		if pick < 0 {
			break
		}
		used[pick] = true
		c := pool[pick]
		c.r.Slot = slot
		if c.p != nil {
			counts[c.p.Row]++
		}
		out = append(out, c.r)
	}
	for i, c := range pool {
		if !used[i] {
			c.r.Hidden = true
			c.r.HiddenReason = "Page length, frequency cap or position constraints"
			hidden = append(hidden, c.r)
		}
	}
	return append(out, hidden...)
}

type draftLayoutKey struct{}

// PreviewLayout assembles an unsaved document without changing persisted state,
// memoized production layouts, impressions, or viewer overrides.
func (e *Engine) PreviewLayout(ctx context.Context, user *core.User, hc core.HubContext, section string, limit int, d core.HomeLayout) (*core.HomePreview, error) {
	d, err := normalizeHomeLayout(d)
	if err != nil {
		return nil, err
	}
	return e.PreviewHome(context.WithValue(ctx, draftLayoutKey{}, d), user, hc, section, limit)
}
func (e *Engine) layoutForContext(ctx context.Context) core.HomeLayout {
	if d, ok := ctx.Value(draftLayoutKey{}).(core.HomeLayout); ok {
		return d
	}
	d, _ := e.HomeLayout(ctx)
	return d
}

// SetExtraHomeRows supplies the additional production rows for owner previews.
// Wire once during startup, before serving requests.
func (e *Engine) SetExtraHomeRows(fn func(context.Context, *core.User, core.HubContext, string, int) []core.Hub) {
	e.extraHomeRows = fn
}
func hubKind(id string) string {
	switch {
	case strings.Contains(id, "continue"), strings.Contains(id, "inprogress"):
		return KindContinue
	case strings.Contains(id, "recentlyadded"):
		return "recently-added"
	case strings.Contains(id, "playlist"):
		return "playlists"
	}
	return id
}
func rowsFromHubs(hubs []core.Hub) []core.HomeRow {
	rows := make([]core.HomeRow, 0, len(hubs))
	for _, h := range hubs {
		rows = append(rows, core.HomeRow{HubID: h.ID, Title: h.Title, Kind: hubKind(h.ID), Source: core.RowSourcePlex, Items: h.Items, Hub: h})
	}
	return rows
}

// FinalizeHome arranges the complete Plex row list after all producers ran.
// Unknown/custom hubs with no catalogue items are deliberately preserved.
func (e *Engine) FinalizeHome(ctx context.Context, user *core.User, hc core.HubContext, section string, hubs []core.Hub) ([]core.Hub, error) {
	if user == nil {
		user = &core.User{}
	}
	if hc.Now.IsZero() {
		hc.Now = e.now()
	}
	hc.Now = hc.Now.In(e.loc)
	def := e.opts.MaxHomeRows
	if section != "" && section != "home" {
		def = e.opts.MaxSectionRows
	}
	rows := rowsFromHubs(hubs)
	for i := range rows {
		e.hubMu.RLock()
		kind := e.hubKinds[rows[i].HubID]
		e.hubMu.RUnlock()
		if kind != "" {
			rows[i].Kind = kind
		}
	}
	out := []core.Hub{}
	for _, r := range arrangeLayout(rows, e.layoutForContext(ctx), user.ID, hc, section, def) {
		if !r.Hidden {
			out = append(out, r.Hub)
		}
	}
	return out, nil
}

func (e *Engine) HomeCandidatesFor(ctx context.Context, u *core.User, hc core.HubContext, limit int) ([]core.Hub, error) {
	return toHubs(e.home(ctx, u, hc, "", limit, false).Rows), nil
}
func (e *Engine) SectionCandidatesFor(ctx context.Context, u *core.User, t core.MediaType, hc core.HubContext, limit int) ([]core.Hub, error) {
	return toHubs(e.home(ctx, u, hc, t, limit, false).Rows), nil
}
func (e *Engine) LayoutDefaults() map[string]int {
	return map[string]int{"home": e.opts.MaxHomeRows, "movies": e.opts.MaxSectionRows, "shows": e.opts.MaxSectionRows}
}

func effectiveHomePolicy(d core.HomeLayout, user int64, section string, row core.HomeRow) (*core.RowPolicy, string) {
	if section == "" {
		section = "home"
	}
	for _, v := range d.Viewers {
		if v.UserID == user {
			if p := matchingPolicy(pageFor(v.Pages, section), row); p != nil {
				return p, "override"
			}
		}
	}
	return matchingPolicy(pageFor(d.Global, section), row), "global"
}

func selectedCollections(d core.HomeLayout, user int64, section string, hc core.HubContext) []string {
	ids := []string{}
	seen := map[string]bool{}
	pages := append([]core.LayoutPage{}, d.Global...)
	for _, v := range d.Viewers {
		if v.UserID == user {
			pages = append(pages, v.Pages...)
		}
	}
	for _, page := range pages {
		if page.Section != section {
			continue
		}
		for _, r := range page.Rows {
			key := strings.TrimPrefix(strings.TrimPrefix(r.Row, "collection:"), "collection.")
			if key == r.Row || strings.ContainsAny(key, "*?[") {
				continue
			}
			p, _ := effectiveHomePolicy(d, user, section, core.HomeRow{HubID: "collection." + key, Kind: "collection:" + key})
			if p != nil && p.Status != "off" && whenMatches(p.When, hc) && weekEligible(user, p.Row, hc, p.MaxDaysPerWeek) && !seen[key] {
				seen[key] = true
				ids = append(ids, key)
			}
		}
	}
	return ids
}

// legacyLayoutView is a compatibility projection, never another saved model.
// The current Designer reads the complete HomeLayout document directly.
func legacyLayoutView(d core.HomeLayout) []core.LayoutRule {
	out := []core.LayoutRule{}
	appendPages := func(pages []core.LayoutPage, userID int64) {
		for _, p := range pages {
			r := core.LayoutRule{ID: fmt.Sprintf("layout-%d-%s", userID, p.Section), Section: p.Section, MaxRows: p.MaxRows, Scope: core.LayoutScope{All: userID == 0}}
			if userID != 0 {
				r.Scope.UserIDs = []int64{userID}
			}
			for _, row := range p.Rows {
				rr := core.LayoutRowRule{HubID: row.Row, When: row.When}
				switch {
				case row.Status == "off":
					rr.Action = core.LayoutHide
				case row.Status == "always":
					rr.Action = core.LayoutPin
					if row.Slot > 0 {
						position := row.Slot - 1
						rr.Position = &position
					}
				case row.Priority > 1:
					rr.Action = core.LayoutBoost
				case row.Priority > 0 && row.Priority < 1:
					rr.Action = core.LayoutBury
				default:
					continue
				}
				r.Rows = append(r.Rows, rr)
			}
			if len(r.Rows) > 0 || r.MaxRows > 0 {
				out = append(out, r)
			}
		}
	}
	appendPages(d.Global, 0)
	for _, v := range d.Viewers {
		appendPages(v.Pages, v.UserID)
	}
	return out
}
