package admin

import "net/http"

func (h *Handler) handleWidget(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.deps.ExtraStatus["widget"](r.Context()))
}
