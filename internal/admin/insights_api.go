package admin

import (
	"net/http"

	"github.com/braedonsaunders/ether/internal/core"
)

// Insights: which rows lead to plays, and server-wide viewing.

func (h *Handler) handleInsightRows(w http.ResponseWriter, r *http.Request) {
	rows, err := h.deps.Personalization.RowInsights(r.Context(), 7)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if rows == nil {
		rows = []core.RowInsight{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (h *Handler) handleInsightRanker(w http.ResponseWriter, r *http.Request) {
	in, err := h.deps.Personalization.RankerInsight(r.Context())
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if in.Features == nil {
		in.Features = []core.RankerFeature{}
	}
	writeJSON(w, http.StatusOK, in)
}

func (h *Handler) handleInsightActivity(w http.ResponseWriter, r *http.Request) {
	days, err := h.deps.Personalization.Activity(r.Context(), 30)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	if days == nil {
		days = []core.ActivityDay{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}
