package app

import (
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/plextv"
	"github.com/braedonsaunders/ether/internal/resolver"
	"github.com/braedonsaunders/ether/internal/transcode"
	"net/http"
)

func (a *App) playbackPolicyHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /policy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"policies": transcode.ClientPolicies()})
	})
	m.HandleFunc("POST /prowlarr", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Config config.Prowlarr `json:"config"`
			Import bool            `json:"import"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			http.Error(w, "invalid Prowlarr settings", 400)
			return
		}
		indexers, err := newznab.DiscoverProwlarr(r.Context(), in.Config, newznab.Options{})
		if err != nil {
			http.Error(w, "Prowlarr discovery failed", 502)
			return
		}
		if in.Import {
			err = a.cfg.Update(func(c *config.Config) error {
				byID := map[string]int{}
				for i, x := range c.Indexers {
					byID[x.ID] = i
				}
				for _, x := range indexers {
					if i, ok := byID[x.ID]; ok {
						c.Indexers[i] = x
					} else {
						byID[x.ID] = len(c.Indexers)
						c.Indexers = append(c.Indexers, x)
					}
				}
				return nil
			})
			if err != nil {
				http.Error(w, "could not save indexers", 500)
				return
			}
		}
		for i := range indexers {
			indexers[i].APIKey = admin.RedactedSecret
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"indexers": indexers, "imported": in.Import})
	})
	return m
}
func validatePlaybackConfig(c config.Config) []admin.FieldError {
	var errs []admin.FieldError
	if _, err := resolver.NewFormatScorer(c.Resolver.CustomFormats, c.Resolver.CustomFormatScores); err != nil {
		errs = append(errs, admin.FieldError{Field: "resolver.customFormats", Message: err.Error()})
	}
	if c.Resolver.HealthRace < 0 || c.Resolver.HealthRace > 4 {
		errs = append(errs, admin.FieldError{Field: "resolver.healthRace", Message: "must be 0 through 4"})
	}
	if c.Plex.CompatibilityVersion != "" && !plextv.ValidPMSVersion(c.Plex.CompatibilityVersion) {
		errs = append(errs, admin.FieldError{Field: "plex.compatibilityVersion", Message: "must be a valid public PMS version"})
	}
	return errs
}
