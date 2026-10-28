package app

import (
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/plextv"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type diagnostic struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}

func (a *App) diagnosticsHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		st := a.registrar.Status()
		cfg := a.cfg.Get()
		steps := []diagnostic{}
		add := func(name string, good bool, detail, action string) {
			state := "attention"
			if good {
				state = "ok"
			}
			steps = append(steps, diagnostic{name, state, detail, action})
		}
		add("Plex account", st.Claimed, "The server must be claimed by the owner account.", "Sign in and claim this server in Plex & remote access.")
		add("Server discovery", st.Listed && st.LastPublishError == "", "Plex must list Ether and its connection addresses.", "Check the published address and refresh registration.")
		add("Remote access enabled", cfg.Plex.RemoteAccess, "The configured public port must forward to Ether.", "Enable remote access and set the router port forward.")
		add("Port mapping", st.UPnP.LastError == "" && st.UPnP.Active, "Automatic mapping is optional when a manual port forward exists.", "Use a manual forward if your router does not support UPnP/NAT-PMP.")
		if st.PlexReachable == nil {
			steps = append(steps, diagnostic{"External reachability", "unknown", "Plex has not supplied an external connectivity result yet.", "Refresh registration and allow the connectivity check to complete."})
		} else {
			add("External reachability", *st.PlexReachable, "This result comes from plex.tv; local WAN loopback tests are not external proof.", "Check forwarding, double NAT/CGNAT, and the published public port.")
		}
		add("Secure connection", st.Cert.Present && !st.Cert.SelfSigned && st.CertError == "", "Official clients prefer a valid plex.direct or custom certificate.", "Check certificate renewal, local DNS rebinding protection, and the device clock.")
		key, err := os.Stat(filepath.Join(a.opts.DataDir, config.CredentialKeyFile))
		private := err == nil && key.Mode().Perm()&0077 == 0
		add("Credential storage", private, "Configuration secrets use authenticated encryption with a private local key.", "Keep the data directory and complete backup bundles private.")
		steps = append(steps, diagnostic{"Compatibility version", "ok", plextv.PMSVersion(cfg), "Ether reports the PMS version its responses were verified against unless you override it or opt into tracking the latest public release in Plex settings."})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"checkedAt": time.Now(), "steps": steps, "remote": st, "compatibilityVersion": plextv.PMSVersion(cfg), "latestPublicVersion": cfg.Plex.LatestCompatibilityVersion, "limits": "Account settings and paid app features follow Plex's account authorization and entitlements."})
	})
	m.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
		a.registrar.Trigger()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]bool{"scheduled": true})
	})
	return m
}
