package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
)

func (a *App) extraHomeRows(ctx context.Context, u *core.User, hc core.HubContext, section string, limit int) []core.Hub {
	var out []core.Hub
	if a.watchlist != nil {
		rows, _ := a.watchlist.Hubs(ctx, u, limit)
		out = append(out, rows...)
	}
	if a.discoveryPersonal != nil {
		rows, _ := a.discoveryPersonal.Hubs(ctx, u, limit)
		out = append(out, rows...)
	}
	if section == "home" && a.livetv != nil && a.registrar != nil && a.registrar.Status().PlexPass {
		row, err := a.livetv.HomeHub(ctx, u, limit)
		if err == nil && len(row.Items) > 0 {
			out = append(out, row)
		}
	}
	if section == "movies" || section == "shows" {
		typ := core.TypeMovie
		if section == "shows" {
			typ = core.TypeShow
		}
		filtered := out[:0]
		for _, h := range out {
			if h.Type == typ {
				filtered = append(filtered, h)
			}
		}
		out = filtered
	}
	return out
}
