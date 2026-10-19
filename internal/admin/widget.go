package admin

import "net/http"

func (h *Handler) handleWidget(w http.ResponseWriter, r *http.Request) {
	if f := h.deps.ExtraStatus["widget"]; f != nil {
		writeJSON(w, 200, f(r.Context()))
		return
	}
	h.handleDashboard(w, r)
}
