package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
)

// Owner status presents one row per viewer, including passthrough IPTV.
// Programme sources stay private to the playout service.
type ownerPlayback struct {
	core.Playback
	app *App
}

func (p ownerPlayback) Sessions() []*core.PlaySession {
	var live []*core.PlaySession
	if p.app.livetv != nil {
		live = p.app.livetv.ViewingSessions()
	}
	groups := map[string]bool{}
	for _, s := range live {
		groups[s.ID] = true
	}
	out := make([]*core.PlaySession, 0, len(live))
	for _, s := range p.Playback.Sessions() {
		if s.PlaybackGroup == "" || !groups[s.PlaybackGroup] {
			out = append(out, s)
		}
	}
	return append(out, live...)
}
func (p ownerPlayback) Session(id string) (*core.PlaySession, bool) {
	for _, s := range p.Sessions() {
		if s.ID == id {
			return s, true
		}
	}
	return nil, false
}
func (a *App) stopOwnerSession(id string) error {
	if a.livetv != nil {
		for _, s := range a.livetv.ViewingSessions() {
			if s.ID == id {
				return a.livetv.Stop(context.Background(), id, &core.User{IsOwner: true})
			}
		}
	}
	if a.plexServer != nil {
		a.plexServer.StopPlayback(id, "The server owner has ended this stream.")
	} else {
		a.playback.Stop(id)
	}
	return nil
}
