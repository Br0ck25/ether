package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/notify"
	"github.com/braedonsaunders/ether/internal/stats"
)

func (a *App) buildInsights() {
	a.notifications = notify.New(a.db, notify.Options{Providers: a.fetcher.Stats, CacheUsage: a.cache.Usage, Logger: a.log.With("component", "notifications")})
	a.stats = stats.New(a.db, stats.Options{Playback: a.playback, SessionExists: func(id string) bool { _, ok := (ownerPlayback{Playback: a.playback, app: a}).Session(id); return ok }, Providers: a.fetcher.Stats, Indexers: a.indexers.Remaining, Logger: a.log.With("component", "stats"), StopSession: a.stopOwnerSession, Newsletter: func(ctx context.Context, html string) error {
		return a.notifications.Emit(ctx, notify.Event{Kind: "newsletter.weekly", Title: "Your week on Ether", Message: "Weekly viewing summary", HTML: html})
	}})
	a.closers = append(a.closers, a.playback.Subscribe(a.stats.ObservePlayback), a.playback.Subscribe(a.notifications.ObservePlayback), a.state.Subscribe(a.stats.ObserveState), a.indexers.Subscribe(a.stats.ObserveIndexer), a.indexers.Subscribe(a.notifications.ObserveIndexer))
}

func (a *App) widgetStatus(context.Context) any {
	groups := map[string]bool{}
	for _, s := range (ownerPlayback{Playback: a.playback, app: a}).Sessions() {
		id := s.PlaybackGroup
		if id == "" {
			id = s.ID
		}
		groups[id] = true
	}
	providers := a.fetcher.Stats()
	healthy := 0
	for _, p := range providers {
		if p.State == "ok" {
			healthy++
		}
	}
	used, budget := a.cache.Usage()
	return map[string]any{"server": a.cfg.Get().Server.FriendlyName, "version": a.opts.Version, "streams": len(groups), "providers": len(providers), "healthyProviders": healthy, "cacheUsed": used, "cacheBudget": budget}
}
