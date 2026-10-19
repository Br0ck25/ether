package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// LayeredPersonalization is additive: old engine implementations and API
// clients can continue using Layout/SetLayout during migration.
type LayeredPersonalization interface {
	HomeLayout(context.Context) (core.HomeLayout, error)
	SetHomeLayout(context.Context, core.HomeLayout) (core.HomeLayout, error)
	PreviewLayout(context.Context, *core.User, core.HubContext, string, int, core.HomeLayout) (*core.HomePreview, error)
	LayoutDefaults() map[string]int
}

func (h *Handler) writeLayout(w http.ResponseWriter, r *http.Request, rules []core.LayoutRule) {
	avail, err := h.deps.Personalization.AvailableRows(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if rules == nil {
		rules = []core.LayoutRule{}
	}
	if avail == nil {
		avail = []core.RowDescriptor{}
	}
	result := map[string]any{"rules": rules, "availableRows": avail}
	if e, ok := h.deps.Personalization.(LayeredPersonalization); ok {
		d, err := e.HomeLayout(r.Context())
		if err != nil {
			writeEngineError(w, err)
			return
		}
		result["layout"] = d
		result["defaults"] = map[string]any{"maxRows": e.LayoutDefaults()}
	}
	writeJSON(w, http.StatusOK, result)
}
func (h *Handler) handleGetLayout(w http.ResponseWriter, r *http.Request) {
	if !h.personalizationReady(w, false) {
		return
	}
	rules, err := h.deps.Personalization.Layout(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	h.writeLayout(w, r, rules)
}
func (h *Handler) handlePutLayout(w http.ResponseWriter, r *http.Request) {
	if !h.personalizationReady(w, false) {
		return
	}
	var body struct {
		Rules  []core.LayoutRule `json:"rules"`
		Layout *core.HomeLayout  `json:"layout"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "bad_json", err.Error())
		return
	}
	if body.Layout != nil {
		e, ok := h.deps.Personalization.(LayeredPersonalization)
		if !ok {
			writeNotImplemented(w, "layered home layout")
			return
		}
		if _, err := e.SetHomeLayout(r.Context(), *body.Layout); err != nil {
			writeEngineError(w, err)
			return
		}
		rules, err := h.deps.Personalization.Layout(r.Context())
		if err != nil {
			writeEngineError(w, err)
			return
		}
		h.writeLayout(w, r, rules)
		return
	}
	rules, err := h.deps.Personalization.SetLayout(r.Context(), body.Rules)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	h.writeLayout(w, r, rules)
}

type layoutPreviewBody struct {
	ViewerID *int64           `json:"viewerId"`
	Layout   *core.HomeLayout `json:"layout"`
	Device   string           `json:"device"`
	At       string           `json:"at"`
	Section  string           `json:"section"`
	Count    int              `json:"count"`
}

func (h *Handler) decodeLayoutPreview(w http.ResponseWriter, r *http.Request) (layoutPreviewBody, core.HubContext, LayeredPersonalization, bool) {
	var b layoutPreviewBody
	var hc core.HubContext
	if !h.personalizationReady(w, true) {
		return b, hc, nil, false
	}
	e, ok := h.deps.Personalization.(LayeredPersonalization)
	if !ok {
		writeNotImplemented(w, "layered home layout")
		return b, hc, nil, false
	}
	if err := decodeJSON(r, &b); err != nil {
		writeError(w, 400, "bad_json", err.Error())
		return b, hc, e, false
	}
	if b.Device == "" {
		b.Device = core.DeviceTV
	}
	switch b.Device {
	case core.DeviceTV, core.DeviceMobile, core.DeviceWeb, core.DeviceDesktop:
	default:
		writeValidationError(w, []FieldError{{Field: "device", Message: "must be tv, mobile, web or desktop"}})
		return b, hc, e, false
	}
	at := time.Now()
	if b.At != "" {
		var err error
		at, err = time.Parse(time.RFC3339, b.At)
		if err != nil {
			writeValidationError(w, []FieldError{{Field: "at", Message: "must be an RFC 3339 time"}})
			return b, hc, e, false
		}
	}
	if b.Count == 0 {
		b.Count = 12
	}
	if b.Count < 1 || b.Count > 100 {
		writeValidationError(w, []FieldError{{Field: "count", Message: "must be 1-100"}})
		return b, hc, e, false
	}
	if b.Layout == nil {
		d, err := e.HomeLayout(r.Context())
		if err != nil {
			writeEngineError(w, err)
			return b, hc, e, false
		}
		b.Layout = &d
	}
	hc = core.NewHubContext(at.Local(), "", "", "")
	hc.Device = b.Device
	return b, hc, e, true
}
func (h *Handler) handleLayoutPreview(w http.ResponseWriter, r *http.Request) {
	b, hc, e, ok := h.decodeLayoutPreview(w, r)
	if !ok {
		return
	}
	u := &core.User{IsOwner: true}
	var viewer any
	if b.ViewerID != nil {
		users, err := h.deps.Users.Users(r.Context())
		if err != nil {
			writeEngineError(w, err)
			return
		}
		found := false
		for _, v := range users {
			if v.ID == *b.ViewerID {
				copy := v
				u = &copy
				found = true
				viewer = map[string]any{"id": v.ID, "title": toViewer(v, core.ViewerStats{}).Title}
				break
			}
		}
		if !found {
			writeError(w, 404, "not_found", "no such viewer")
			return
		}
	}
	p, err := e.PreviewLayout(r.Context(), u, hc, b.Section, b.Count, *b.Layout)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	h.writeHomePreview(w, p, viewer)
}
func overrideSettings(d core.HomeLayout, id int64) int {
	n := 0
	for _, v := range d.Viewers {
		if v.UserID == id {
			for _, p := range v.Pages {
				n += len(p.Rows) + len(p.DeviceMaxRows)
				if p.MaxRows > 0 {
					n++
				}
			}
		}
	}
	return n
}
func (h *Handler) handleLayoutImpact(w http.ResponseWriter, r *http.Request) {
	b, hc, e, ok := h.decodeLayoutPreview(w, r)
	if !ok {
		return
	}
	users, err := h.deps.Users.Users(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	saved, err := e.HomeLayout(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	results := []map[string]any{}
	overrides, changed := 0, 0
	for _, u := range users {
		old, err := e.PreviewLayout(r.Context(), &u, hc, b.Section, b.Count, saved)
		if err != nil {
			writeEngineError(w, err)
			return
		}
		next, err := e.PreviewLayout(r.Context(), &u, hc, b.Section, b.Count, *b.Layout)
		if err != nil {
			writeEngineError(w, err)
			return
		}
		before, after := map[string]int{}, map[string]int{}
		for _, row := range old.Rows {
			if !row.Hidden {
				before[row.HubID] = len(before) + 1
			}
		}
		for _, row := range next.Rows {
			if !row.Hidden {
				after[row.HubID] = len(after) + 1
			}
		}
		added, removed := []string{}, []string{}
		moved := 0
		for _, row := range next.Rows {
			if row.Hidden {
				continue
			}
			if pos := before[row.HubID]; pos == 0 {
				added = append(added, row.Title)
			} else if pos != after[row.HubID] {
				moved++
			}
		}
		for _, row := range old.Rows {
			if !row.Hidden && after[row.HubID] == 0 {
				removed = append(removed, row.Title)
			}
		}
		n := overrideSettings(*b.Layout, u.ID)
		if n > 0 {
			overrides++
		}
		diff := len(added)+len(removed)+moved > 0
		if diff {
			changed++
		}
		results = append(results, map[string]any{"viewerId": u.ID, "title": toViewer(u, core.ViewerStats{}).Title, "overrides": n, "changed": diff, "added": added, "removed": removed, "moved": moved})
	}
	writeJSON(w, 200, map[string]any{"viewers": results, "summary": map[string]int{"viewers": len(users), "withOverrides": overrides, "changed": changed}})
}
