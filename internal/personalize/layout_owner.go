package personalize

import (
	"context"
	"fmt"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Owner API for the layered Home Designer (internal/admin layout_api.go).

// loadLayout reads the saved document, converting the legacy rule list
// the first time the layered model starts on a database that has one.
func (e *Engine) loadLayout(ctx context.Context) (*core.HomeLayout, error) {
	doc, ok, err := e.store.homeLayout(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		norm, verr := normalizeHomeLayout(doc)
		if verr != nil {
			// Keep what is still valid (a newer build may have tightened
			// a limit); the owner sees and fixes the rest.
			e.log.Warn("personalize: saved Home layout has invalid entries; they are ignored", "err", verr)
		}
		norm.UpdatedAt, norm.Notes = doc.UpdatedAt, doc.Notes
		return norm, nil
	}
	rules, err := e.store.legacyLayout(ctx)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return &core.HomeLayout{Global: []core.LayoutPage{}, Viewers: []core.ViewerLayout{}}, nil
	}
	conv, notes := convertLegacyRules(rules)
	norm, verr := normalizeHomeLayout(conv)
	if verr != nil {
		notes = append(notes, "Some converted rules were invalid and were dropped: "+verr.Error()+".")
	}
	norm.Notes = append([]string{fmt.Sprintf("Converted %d layout %s from the previous Home Designer.", len(rules), plural(len(rules), "rule", "rules"))}, notes...)
	now := e.now()
	if err := e.store.setHomeLayout(ctx, norm, now); err != nil {
		return nil, err
	}
	norm.UpdatedAt = now.UTC()
	e.log.Info("personalize: converted legacy layout rules to the layered Home Designer", "rules", len(rules), "notes", len(notes))
	return norm, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// HomeLayout returns the Home Designer document (a copy).
func (e *Engine) HomeLayout(ctx context.Context) (*core.HomeLayout, error) {
	ix, _ := e.layoutState()
	if ix == nil {
		return cloneLayout(nil), nil
	}
	return cloneLayout(ix.doc), nil
}

// SetHomeLayout validates and saves the document; it applies to the next
// /hubs request (assembled screens are keyed by the layout version).
func (e *Engine) SetHomeLayout(ctx context.Context, doc *core.HomeLayout) (*core.HomeLayout, error) {
	norm, err := normalizeHomeLayout(doc)
	if err != nil {
		return nil, err
	}
	now := e.now()
	if err := e.store.setHomeLayout(ctx, norm, now); err != nil {
		return nil, err
	}
	norm.UpdatedAt = now.UTC()
	e.mu.Lock()
	e.layout = newLayoutIndex(norm)
	e.layoutVer++
	e.mu.Unlock()
	return e.HomeLayout(ctx)
}

// ConvertLayoutRules turns legacy layout rules into a layered document
// (not saved), with Notes wherever the conversion is approximate.
func (e *Engine) ConvertLayoutRules(rules []core.LayoutRule) (*core.HomeLayout, error) {
	norm, err := normalizeLayout(rules)
	if err != nil {
		return nil, err
	}
	conv, notes := convertLegacyRules(norm)
	doc, err := normalizeHomeLayout(conv)
	if err != nil {
		return nil, err
	}
	doc.Notes = notes
	return doc, nil
}

// LayoutDefaults are the engine's page lengths per section (rows it
// arranges; Plex's own rows not counted).
func (e *Engine) LayoutDefaults() map[string]int {
	return map[string]int{"home": e.opts.MaxHomeRows, "movies": e.opts.MaxSectionRows, "shows": e.opts.MaxSectionRows}
}

// LayoutImpact compares, for each viewer, the page the saved layout gives
// them in this context with the page the draft would give them. Viewers
// whose model is not built yet are compared on the model they would get
// now.
func (e *Engine) LayoutImpact(ctx context.Context, draft *core.HomeLayout, hc core.HubContext, sectionArg string, users []core.User) ([]core.LayoutImpact, error) {
	norm, err := normalizeHomeLayout(draft)
	if err != nil {
		return nil, err
	}
	ix := newLayoutIndex(norm)
	sid, section, err := e.resolveSection(sectionArg)
	if err != nil {
		return nil, err
	}
	if hc.Now.IsZero() {
		hc.Now = e.now()
	}
	hc.Now = hc.Now.In(e.loc)
	hc.Daypart, hc.Weekend, hc.SectionID = core.DaypartAt(hc.Now), core.IsWeekend(hc.Now), sid
	if !validDevices[hc.Device] {
		return nil, &ValidationError{Problems: []Problem{{"device", "must be tv, mobile, web or desktop"}}}
	}
	saved, _ := e.layoutState()
	out := make([]core.LayoutImpact, 0, len(users))
	for i := range users {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		u := users[i]
		before := e.home(ctx, &u, hc, section, 12, homeOpts{draft: saved})
		after := e.home(ctx, &u, hc, section, 12, homeOpts{draft: ix})
		im := diffPages(before.Rows, after.Rows)
		im.UserID, im.Title = u.ID, u.Title
		if im.Title == "" {
			im.Title = u.Username
		}
		im.Overrides = overrideCount(ix.viewers[u.ID][sectionName(section)])
		out = append(out, im)
	}
	return out, nil
}

// diffPages compares the rows on two pages.
func diffPages(before, after []core.HomeRow) core.LayoutImpact {
	pos := func(rows []core.HomeRow) (map[string]int, map[string]string) {
		p, titles := map[string]int{}, map[string]string{}
		n := 0
		for _, r := range rows {
			if r.Hidden {
				continue
			}
			p[r.HubID] = n
			titles[r.HubID] = r.Title
			n++
		}
		return p, titles
	}
	bp, bt := pos(before)
	ap, at := pos(after)
	im := core.LayoutImpact{Added: []string{}, Removed: []string{}}
	for _, r := range after {
		if r.Hidden {
			continue
		}
		if i, ok := bp[r.HubID]; !ok {
			im.Added = append(im.Added, at[r.HubID])
		} else if i != ap[r.HubID] {
			im.Moved++
		}
	}
	for _, r := range before {
		if r.Hidden {
			continue
		}
		if _, ok := ap[r.HubID]; !ok {
			im.Removed = append(im.Removed, bt[r.HubID])
		}
	}
	im.Changed = len(im.Added) > 0 || len(im.Removed) > 0 || im.Moved > 0
	return im
}

// renumber gives the visible rows consecutive slots (after the front end
// adds a row of its own in the preview).
func renumber(rows []core.HomeRow) {
	n := 0
	for i := range rows {
		if rows[i].Hidden {
			continue
		}
		n++
		rows[i].Slot = n
	}
}

// AvailableRows lists the row kinds a layout can target, plus owner
// collection rows recently served.
func (e *Engine) AvailableRows(ctx context.Context) ([]core.RowDescriptor, error) {
	e.rec.flush(ctx)
	shows, err := e.store.rowShowsSince(ctx, e.now().AddDate(0, 0, -30))
	if err != nil {
		return nil, err
	}
	var extra []core.RowDescriptor
	for kind, st := range shows {
		if strings.HasPrefix(kind, KindCollectionPrefix) {
			extra = append(extra, core.RowDescriptor{HubID: kind, Title: st.title, Kind: kind,
				Description: "Owner collection row (" + st.hubID + ")."})
		}
	}
	return descriptors(extra), nil
}
