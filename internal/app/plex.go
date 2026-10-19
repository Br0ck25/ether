package app

import (
	"net/http"

	"github.com/ether-media/ether/internal/config"
)

// buildPlex returns the PMS-compatible front end. Placeholder until
// internal/plex lands.
func (a *App) buildPlex(cfg config.Config) (http.Handler, error) {
	return http.NotFoundHandler(), nil
}
