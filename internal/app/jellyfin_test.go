package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// The Jellyfin front end follows the settings: it listens when switched
// on, moves when the port changes, stops when switched off, and refuses the
// Plex port.
func TestJellyfinFrontFollowsSettings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Transcode.FFmpegPath = "/not-installed/ffmpeg"
		c.Plex.RemoteAccess = false
		c.Server.BindAddress = "127.0.0.1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	done := make(chan struct{})
	go func() { a.jellyfin.run(ctx, a.cfg.Get()); close(done) }()

	set := func(enabled bool, port int) {
		t.Helper()
		if err := a.cfg.Update(func(c *config.Config) error {
			c.Jellyfin = config.Jellyfin{Enabled: enabled, Port: port}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// Updates notify subscribers on the writer's goroutine; apply directly too.
		a.jellyfin.apply(a.cfg.Get())
	}
	get := func(port int, path string) (*http.Response, error) {
		return (&http.Client{Timeout: 2 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	}

	if _, err := get(config.DefaultJellyfinPort, "/System/Info/Public"); err == nil {
		t.Skip("port 8096 is in use on this machine")
	}
	p1 := freePort(t)
	set(true, p1)
	resp, err := get(p1, "/System/Info/Public")
	if err != nil {
		t.Fatalf("not listening after being switched on: %v", err)
	}
	var info map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info["ServerName"] != "Ether" || info["Id"] != a.cfg.Get().Server.MachineIdentifier || info["ProductName"] != "Jellyfin Server" {
		t.Fatalf("server identity: %v", info)
	}
	// Nothing is served without signing in.
	if resp, err = get(p1, "/Items"); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated library: %v %v", resp, err)
	}

	p2 := freePort(t)
	set(true, p2)
	if _, err := get(p1, "/System/Info/Public"); err == nil {
		t.Fatal("still listening on the old port")
	}
	if _, err := get(p2, "/System/Info/Public"); err != nil {
		t.Fatalf("not listening on the new port: %v", err)
	}

	set(false, p2)
	if _, err := get(p2, "/System/Info/Public"); err == nil {
		t.Fatal("still listening after being switched off")
	}
	// The Plex port is refused rather than fought over.
	set(true, a.cfg.Get().Server.Port)
	if a.jellyfin.http != nil {
		t.Fatal("listening on the Plex port")
	}

	// The owner's API manages access codes.
	r := httptest.NewRequest("GET", "http://localhost/ether/api/jellyfin/viewers", nil)
	r.RemoteAddr = "127.0.0.1:1024"
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	if w.Code == http.StatusNotFound || strings.Contains(w.Body.String(), "no such API endpoint") {
		t.Fatalf("access-code API is not mounted: %d %s", w.Code, w.Body)
	}

	cancel()
	<-done
}
