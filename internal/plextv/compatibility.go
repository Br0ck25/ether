package plextv

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/braedonsaunders/ether/internal/config"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var pmsVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+-[a-zA-Z0-9]+$`)

func ValidPMSVersion(v string) bool { return pmsVersionPattern.MatchString(v) }
func PMSVersion(c config.Config) string {
	if ValidPMSVersion(c.Plex.CompatibilityVersion) {
		return c.Plex.CompatibilityVersion
	}
	if !c.Plex.DisableVersionTracking && ValidPMSVersion(c.Plex.LatestCompatibilityVersion) {
		return c.Plex.LatestCompatibilityVersion
	}
	return DefaultServerVersion
}
func versionNumbers(v string) [4]int {
	var out [4]int
	p := strings.Split(strings.SplitN(v, "-", 2)[0], ".")
	for i := 0; i < len(p) && i < 4; i++ {
		out[i], _ = strconv.Atoi(p[i])
	}
	return out
}
func NewerPMSVersion(a, b string) bool {
	x, y := versionNumbers(a), versionNumbers(b)
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// LatestPublicVersion reads the public Plex release manifest. It only reads
// version metadata; it never downloads or executes a Plex server binary.
func LatestPublicVersion(ctx context.Context, hc *http.Client, endpoint string) (string, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	if endpoint == "" {
		endpoint = "https://plex.tv/api/downloads/5.json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	r, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return "", fmt.Errorf("release manifest: HTTP %d", r.StatusCode)
	}
	var manifest struct {
		Computer map[string]struct {
			Version string `json:"version"`
		} `json:"computer"`
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&manifest); err != nil {
		return "", err
	}
	best := ""
	for _, platform := range manifest.Computer {
		v := platform.Version
		if ValidPMSVersion(v) && (best == "" || NewerPMSVersion(v, best)) {
			best = v
		}
	}
	if best == "" {
		return "", fmt.Errorf("release manifest contains no valid PMS version")
	}
	return best, nil
}
