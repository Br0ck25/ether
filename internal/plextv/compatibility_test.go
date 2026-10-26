package plextv

import (
	"context"
	"fmt"
	"github.com/braedonsaunders/ether/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicVersionValidation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"computer":{"MacOS":{"version":"1.43.4.10903-e5521bd8c"},"Linux":{"version":"1.44.0.11000-abcdef"},"Bad":{"version":"garbage"}}}`)
	}))
	defer srv.Close()
	v, err := LatestPublicVersion(context.Background(), srv.Client(), srv.URL)
	if err != nil || v != "1.44.0.11000-abcdef" {
		t.Fatalf("%q %v", v, err)
	}
	c := config.Config{}
	c.Plex.LatestCompatibilityVersion = v
	if PMSVersion(c) != DefaultServerVersion {
		t.Fatal("latest public version reported without opting into tracking")
	}
	c.Plex.TrackLatestVersion = true
	if PMSVersion(c) != v {
		t.Fatal("tracking ignored")
	}
	c.Plex.CompatibilityVersion = DefaultServerVersion
	if PMSVersion(c) != DefaultServerVersion {
		t.Fatal("override ignored")
	}
}
