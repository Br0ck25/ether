package admin

import (
	"net/http"

	"github.com/braedonsaunders/ether/internal/core"
)

// Home Designer: the owner's layout rules (pin, hide, boost, bury rows,
// with schedules, per user or server-wide).

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
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "availableRows": avail})
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
		Rules []core.LayoutRule `json:"rules"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	rules, err := h.deps.Personalization.SetLayout(r.Context(), body.Rules)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	h.writeLayout(w, r, rules)
}
