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
// HA HOOKS for the cluster package (phase 3a) to connect once it exists:
//
//	a.cfg.SetNotifier(bus)                 // config:changed / bus:resync; nil = 30s polling only
//	a.certs.SetEventBus(bus)               // certs:changed / bus:resync; nil = 1 min polling only
//	a.certs.SetRenewalAllowed(isLeader)    // only the leader renews plex.direct; nil = always (solo)

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
	a.clustered, a.databaseURL = true, databaseURL
	a.closers = append(a.closers, a.cfg.Close)
	a.log.Info("cluster: using shared settings from PostgreSQL", "version", a.cfg.SharedVersion())
	return nil
}

// sharedCerts is the cluster's certificate store, nil on a standalone server.
func (a *App) sharedCerts() plextv.SharedCertStore {
	if !a.clustered {
		return nil
	}
	return clusterconfig.NewSecrets(a.db, a.cfg)
}

// runClusterSync follows certificate renewals made by other nodes.
func (a *App) runClusterSync(ctx context.Context) {
	if a.clustered {
		a.certs.RunSharedSync(ctx, plextv.DefaultCertSyncInterval)
	}
}

// clusterHandler is the owner API for join tokens (/ether/api/cluster).
func (a *App) clusterHandler() http.Handler {
	return clusterconfig.Handler(clusterconfig.HandlerOptions{
		DB: a.db, Clustered: a.clustered, Config: a.cfg,
		DatabaseURL: func() string { return a.databaseURL },
	})
}
