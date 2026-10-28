package app

import (
	"encoding/json"
	"net/http"

	"github.com/braedonsaunders/ether/internal/cluster"
	"github.com/braedonsaunders/ether/internal/config"
)

// Node-local controls deliberately operate on the node whose manager is open.
// Peers expose their own manager links rather than forwarding owner credentials.
func (a *App) clusterManageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fail := func(code int, msg string) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "cluster_node", "message": msg}})
		}
		if r.Method != http.MethodPatch {
			w.Header().Set("Allow", "PATCH")
			fail(405, "Use PATCH to update this node.")
			return
		}
		if !a.clustered() || !a.cfg.Get().SetupComplete {
			fail(409, "This server must be a configured cluster member.")
			return
		}
		var body struct {
			Weight   *int  `json:"weight"`
			Draining *bool `json:"draining"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			fail(400, "Invalid node settings.")
			return
		}
		if body.Weight != nil && (*body.Weight < 1 || *body.Weight > 100) {
			fail(400, "Node weight must be between 1 and 100.")
			return
		}
		// A pending rolling update owns the drain state until it finishes.
		if a.updates != nil && a.updates.mgr != nil {
			st := a.updates.mgr.Status()
			if st.Cluster != nil && st.Cluster.Draining {
				fail(409, "A rolling update controls this node's maintenance state.")
				return
			}
		}
		if body.Weight != nil {
			if err := a.cfg.Update(func(c *config.Config) error { c.Server.ClusterWeight = *body.Weight; return nil }); err != nil {
				fail(500, "Could not save this node's weight.")
				return
			}
		}
		if body.Draining != nil {
			(&clusterUpdates{a: a}).SetDraining(*body.Draining)
		}
		if pg, ok := a.cluster.(*cluster.Postgres); ok {
			pg.Beat()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"weight": a.cfg.Get().Server.ClusterWeight, "draining": a.nodeDraining()})
	})
}
