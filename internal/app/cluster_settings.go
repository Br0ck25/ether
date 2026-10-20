package app

import (
	"context"
	"fmt"
	"net/http"

	"github.com/braedonsaunders/ether/internal/clusterconfig"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/plextv"
)

// Cluster wiring (HA phase 3b: shared settings and identity).
//
// Boot is two-step: config.Open reads this node's config.json (which names
// the database), the database is opened, then attachCluster overlays the
// cluster's shared settings when the database is PostgreSQL. A standalone
// (SQLite) server skips all of this and keeps everything in config.json.
//
// connectClusterHooks routes config and certificate change notifications
// over the cluster bus and lets only the leader renew plex.direct.

// attachCluster switches the config store to the shared settings in the
// cluster database, seeding them from config.json on the cluster's first
// node, and refuses to start when this node's credential key is not the
// cluster's.
func (a *App) attachCluster(ctx context.Context, databaseURL string) error {
	if err := clusterconfig.Attach(ctx, a.cfg, a.db, config.SharedOptions{
		Logger: a.log.With("component", "config"),
	}); err != nil {
		return fmt.Errorf("cluster settings: %w", err)
	}
	a.databaseURL = databaseURL
	a.closers = append(a.closers, a.cfg.Close)
	a.log.Info("cluster: using shared settings from PostgreSQL", "version", a.cfg.SharedVersion())
	return nil
}

// sharedCerts is the cluster's certificate store, nil on a standalone server.
func (a *App) sharedCerts() plextv.SharedCertStore {
	if !a.clustered() {
		return nil
	}
	return clusterconfig.NewSecrets(a.db, a.cfg)
}

// runClusterSync follows certificate renewals made by other nodes.
func (a *App) runClusterSync(ctx context.Context) {
	if a.clustered() {
		a.certs.RunSharedSync(ctx, plextv.DefaultCertSyncInterval)
	}
}

func (a *App) connectClusterHooks() {
	if !a.clustered() {
		return
	}
	a.cfg.SetNotifier(a.cluster)
	a.certs.SetEventBus(a.cluster)
	a.certs.SetRenewalAllowed(a.cluster.IsLeader)
	// In-memory caches follow writes made by other nodes.
	bus := a.clusterNode()
	a.users.SetBus(bus)
	a.safety.SetBus(bus)
	a.resolver.SetBus(bus)
	a.personalize.SetBus(bus)
	a.collections.SetBus(bus)
	a.episodeorder.SetBus(bus)
	a.closers = append(a.closers, func() {
		a.users.Close()
		a.safety.Close()
		a.resolver.SetBus(nil)
		a.personalize.SetBus(nil)
		a.collections.SetBus(nil)
		a.episodeorder.SetBus(nil)
	})
}

// clusterSettingsHandler is the owner API for shared settings and join
// tokens, mounted under /ether/api/cluster beside the membership status.
func (a *App) clusterSettingsHandler() http.Handler {
	return clusterconfig.Handler(clusterconfig.HandlerOptions{
		DB: a.db, Clustered: a.clustered(), Config: a.cfg,
		DatabaseURL: func() string { return a.databaseURL },
	})
}
