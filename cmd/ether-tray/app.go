// Command ether-tray is Ether's tray / menu-bar companion: it shows
// server status, opens the web UI, starts/stops the service, checks the
// signed update feed and can launch at login. The GUI lives in tray.go
// (requires CGO on macOS); everything testable lives here and in
// status.go / autostart.go so `go test` covers it headlessly.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/service"
	"github.com/braedonsaunders/ether/internal/updater"
)

// Tray icons (regenerate: go run scripts/generate-tray-icons.go cmd/ether-tray/assets).
//
//go:embed assets/icon-template.png
var iconTemplate []byte

//go:embed assets/icon.png
var iconPNG []byte

//go:embed assets/icon-off.png
var iconOffPNG []byte

//go:embed assets/icon.ico
var iconICO []byte

const (
	defaultBaseURL = "http://127.0.0.1:32400"
	plexWebURL     = "https://app.plex.tv/desktop"
)

// ServiceController is the slice of *service.Service the tray needs.
type ServiceController interface {
	Start() error
	Stop() error
	Restart() error
	Status() (service.Status, error)
}

// UpdateChecker is the slice of *updater.Client the tray needs.
type UpdateChecker interface {
	Check(ctx context.Context) (*updater.Update, error)
	Apply(ctx context.Context, upd *updater.Update, opts updater.ApplyOptions) (string, error)
}

type app struct {
	baseURL   string
	status    *StatusClient
	svc       ServiceController // nil when the service manager is unavailable
	updates   UpdateChecker     // nil when no release key is compiled in (dev builds)
	cfg       config.Updates
	dataDir   string
	autostart *Autostart
	openURL   func(string) error
	log       *slog.Logger
}

func newApp(log *slog.Logger) *app {
	base := os.Getenv("ETHER_URL")
	if base == "" {
		base = defaultBaseURL
	}
	dataDir := config.DefaultDataDir()
	cfg := config.Updates{Channel: "stable"}
	if store, err := config.Open(dataDir); err == nil {
		c := store.Get()
		cfg = c.Updates
		if c.Server.Port != 0 && os.Getenv("ETHER_URL") == "" {
			base = fmt.Sprintf("http://127.0.0.1:%d", c.Server.Port)
		}
	} else {
		log.Debug("tray: config not loaded; using defaults", "err", err)
	}
	a := &app{
		baseURL:   base,
		status:    NewStatusClient(base, &http.Client{Timeout: 3 * time.Second}),
		cfg:       cfg,
		dataDir:   dataDir,
		autostart: DefaultAutostart(),
		log:       log,
	}
	a.openURL = func(u string) error { return OpenBrowser(u) }

	// Control-only service handle (no RunFunc): install/start/stop/status.
	opts := service.DefaultOptions()
	opts.DataDir = dataDir
	if exe := serverExecutable(); exe != "" {
		opts.Executable = exe
	}
	if svc, err := service.New(nil, opts); err == nil {
		a.svc = svc
	} else {
		log.Warn("tray: service control unavailable", "err", err)
	}
	if uc, err := updater.NewClientFromConfig(cfg, log); err == nil {
		a.updates = uc
	} else {
		log.Debug("tray: update checks disabled", "err", err)
	}
	return a
}

// serverExecutable locates the ether server binary relative to the tray:
// next to it (Windows/Linux install dir) or in Contents/Resources /
// ../MacOS on macOS. Falls back to "" (service package uses os.Executable,
// which is wrong for the tray — callers should prefer an explicit hit).
func serverExecutable() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "ether"
	if runtime.GOOS == "windows" {
		name = "ether.exe"
	}
	dir := filepath.Dir(self)
	for _, cand := range []string{
		filepath.Join(dir, name),
		filepath.Join(dir, "..", "Resources", name), // Ether.app/Contents/MacOS -> Resources
		filepath.Join(dir, "..", "MacOS", name),
	} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			if abs, err := filepath.Abs(cand); err == nil {
				return abs
			}
			return cand
		}
	}
	if p := os.Getenv("ETHER_SERVER_BIN"); p != "" {
		return p
	}
	return ""
}

func (a *app) webUIURL() string { return a.baseURL + "/ether/" }

func (a *app) openEther() {
	if err := a.openURL(a.webUIURL()); err != nil {
		a.log.Warn("tray: open Ether UI", "err", err)
	}
}

func (a *app) openPlex() {
	if err := a.openURL(plexWebURL); err != nil {
		a.log.Warn("tray: open Plex Web", "err", err)
	}
}

// pollOnce fetches status, tolerating an absent server (Running=false).
func (a *app) pollOnce(ctx context.Context) Status {
	st, _ := a.status.Poll(ctx)
	return st
}

// checkUpdates returns a pending update, applying it immediately when the
// owner chose auto-update. appliedTo is the downloaded/replaced path.
func (a *app) checkUpdates(ctx context.Context, forceApply bool) (upd *updater.Update, appliedTo string, err error) {
	if a.updates == nil {
		return nil, "", fmt.Errorf("update checks unavailable in this build")
	}
	upd, err = a.updates.Check(ctx)
	if err != nil || upd == nil {
		return upd, "", err
	}
	if forceApply || updater.ShouldAutoApply(a.cfg) {
		appliedTo, err = a.updates.Apply(ctx, upd, updater.ApplyOptions{
			DownloadDir: filepath.Join(a.dataDir, "updates"),
		})
		if err != nil {
			return upd, "", fmt.Errorf("apply update %s: %w", upd.Version(), err)
		}
		// Binary self-replace needs a service restart to take effect.
		if upd.Artifact.Kind == updater.KindBinary && a.svc != nil {
			if rerr := a.svc.Restart(); rerr != nil {
				a.log.Warn("tray: restart after update failed", "err", rerr)
			}
		}
	}
	return upd, appliedTo, nil
}

func (a *app) serviceAction(action string) error {
	if a.svc == nil {
		return fmt.Errorf("service control unavailable on this system")
	}
	switch action {
	case "start":
		return a.svc.Start()
	case "stop":
		return a.svc.Stop()
	case "restart":
		return a.svc.Restart()
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
}

// statusLine renders the one-line tray status.
func statusLine(st Status) string {
	if !st.Running {
		return "Ether: not running"
	}
	line := "Ether: running"
	if st.FriendlyName != "" && st.FriendlyName != "Ether" {
		line += " (" + st.FriendlyName + ")"
	}
	if st.Version != "" {
		line += " · v" + st.Version
	}
	if st.SetupComplete != nil && !*st.SetupComplete {
		line += " · setup needed"
	}
	return line
}

// runHeadless is the no-GUI fallback (CGO-less builds): poll + log status,
// periodically check for updates. It keeps behaviour observable in CI and
// over SSH, and it's what `go run ./cmd/ether-tray` does without CGO.
func (a *app) runHeadless(ctx context.Context) error {
	a.log.Info("ether-tray running headless (no GUI in this build)", "server", a.baseURL)
	st := a.pollOnce(ctx)
	a.log.Info(statusLine(st))
	if a.updates != nil {
		uctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		upd, _, err := a.checkUpdates(uctx, false)
		cancel()
		switch {
		case err != nil:
			a.log.Debug("tray: update check", "err", err)
		case upd != nil:
			a.log.Info("update available", "version", upd.Version())
		}
	}
	tk := time.NewTicker(15 * time.Second)
	defer tk.Stop()
	last := statusLine(st)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			st := a.pollOnce(ctx)
			if line := statusLine(st); line != last {
				a.log.Info(line)
				last = line
			}
		}
	}
}
