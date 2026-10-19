package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Home Designer: the layered layout (a Global Home template, the engine's
// personalization within it, per-viewer overrides on top), server-side
// previews of unsaved drafts, and the impact of a draft across viewers.
// docs/owner-ui.md, "Home Designer".

func (h *Handler) writeLayout(w http.ResponseWriter, r *http.Request, doc *core.HomeLayout) {
	avail, err := h.deps.Personalization.AvailableRows(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if avail == nil {
		avail = []core.RowDescriptor{}
	}
	if doc == nil {
		doc = &core.HomeLayout{}
	}
	if doc.Global == nil {
		doc.Global = []core.LayoutPage{}
	}
	if doc.Viewers == nil {
		doc.Viewers = []core.ViewerLayout{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"layout":        doc,
		"availableRows": avail,
		"defaults":      map[string]any{"maxRows": h.deps.Personalization.LayoutDefaults()},
	})
}

func (h *Handler) handleGetLayout(w http.ResponseWriter, r *http.Request) {
	if !h.personalizationReady(w, false) {
		return
	}
	doc, err := h.deps.Personalization.HomeLayout(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	h.writeLayout(w, r, doc)
}

// handlePutLayout saves the layered document ({"layout": …}). The legacy
// body ({"rules": [...]}) is still accepted and converted; the response
// then carries the conversion notes.
func (h *Handler) handlePutLayout(w http.ResponseWriter, r *http.Request) {
	if !h.personalizationReady(w, false) {
		return
	}
	var body struct {
		Layout *core.HomeLayout   `json:"layout"`
		Rules  *[]core.LayoutRule `json:"rules"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	doc := body.Layout
	var notes []string
	if doc == nil {
		if body.Rules == nil {
			writeValidationError(w, []FieldError{{Field: "layout", Message: "is required"}})
			return
		}
		conv, err := h.deps.Personalization.ConvertLayoutRules(*body.Rules)
		if err != nil {
			writeEngineError(w, err)
			return
		}
		doc, notes = conv, conv.Notes
	}
	saved, err := h.deps.Personalization.SetHomeLayout(r.Context(), doc)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if len(notes) > 0 {
		saved.Notes = notes
	}
	h.writeLayout(w, r, saved)
}

// layoutContext is the context part of preview and impact requests.
type layoutContext struct {
	Device  string `json:"device"`
	At      string `json:"at"`
	Section string `json:"section"`
}

func (c layoutContext) hubContext() (core.HubContext, []FieldError) {
	device, fields := parseDevice(c.Device)
	at := time.Now()
	if c.At != "" {
		t, err := time.Parse(time.RFC3339, c.At)
		if err != nil {
			fields = append(fields, FieldError{Field: "at", Message: "must be an RFC 3339 time"})
		}
		at = t
	}
	hc := core.NewHubContext(at.Local(), "", "", "")
	hc.Device = device
	return hc, fields
}

// handleLayoutPreview assembles a page with a draft layout (or the saved
// one) for a viewer, or for the typical viewer when viewerId is null.
func (h *Handler) handleLayoutPreview(w http.ResponseWriter, r *http.Request) {
	if !h.personalizationReady(w, false) {
		return
	}
	var body struct {
		layoutContext
		Layout   *core.HomeLayout `json:"layout"`
		ViewerID *int64           `json:"viewerId"`
		Count    int              `json:"count"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	hc, fields := body.hubContext()
	if body.Count == 0 {
		body.Count = 12
	}
	if body.Count < 1 || body.Count > 100 {
		fields = append(fields, FieldError{Field: "count", Message: "must be 1-100"})
	}
	if len(fields) > 0 {
		writeValidationError(w, fields)
		return
	}
	var user *core.User
	if body.ViewerID != nil {
		if h.deps.Users == nil {
			writeNotImplemented(w, "user administration")
			return
		}
		u, ok := h.findViewer(w, r, *body.ViewerID)
		if !ok {
			return
		}
		user = u
	}
	p, err := h.deps.Personalization.PreviewLayout(r.Context(), user, hc, body.Section, body.Count, body.Layout)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	var who any
	if user != nil {
		who = map[string]any{"id": user.ID, "title": viewerTitle(*user)}
	}
	writeHome(w, p, map[string]any{"viewer": who})
}

// handleLayoutImpact compares every viewer's page under the saved layout
// with the draft, in one context.
func (h *Handler) handleLayoutImpact(w http.ResponseWriter, r *http.Request) {
	if !h.personalizationReady(w, true) {
		return
	}
	var body struct {
		layoutContext
		Layout *core.HomeLayout `json:"layout"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	hc, fields := body.hubContext()
	if body.Layout == nil {
		fields = append(fields, FieldError{Field: "layout", Message: "is required"})
	}
	if len(fields) > 0 {
		writeValidationError(w, fields)
		return
	}
	users, err := h.deps.Users.Users(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "users_failed", err.Error())
		return
	}
	for i := range users {
		users[i].Title = viewerTitle(users[i])
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	impact, err := h.deps.Personalization.LayoutImpact(ctx, body.Layout, hc, body.Section, users)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if impact == nil {
		impact = []core.LayoutImpact{}
	}
	withOverrides, changed := 0, 0
	for _, im := range impact {
		if im.Overrides > 0 {
			withOverrides++
		}
		if im.Changed {
			changed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"viewers": impact,
		"summary": map[string]int{"viewers": len(impact), "withOverrides": withOverrides, "changed": changed},
	})
}
