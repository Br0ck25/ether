//go:build windows || linux || (darwin && cgo)

package main

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/braedonsaunders/ether/internal/updater"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a := newApp(log)
	if len(os.Args) > 1 && os.Args[1] == "--headless" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := a.runHeadless(ctx); err != nil {
			log.Error("ether-tray", "err", err)
			os.Exit(1)
		}
		return
	}
	newTray(a).run()
}

type tray struct {
	a      *app
	ctx    context.Context
	cancel context.CancelFunc

	statusItem *systray.MenuItem
	updateItem *systray.MenuItem
	loginItem  *systray.MenuItem
	pendingUpd *updater.Update
	pendingVer string
}

func newTray(a *app) *tray { return &tray{a: a} }

func (t *tray) run() {
	t.ctx, t.cancel = context.WithCancel(context.Background())
	systray.Run(t.onReady, func() {
		t.cancel()
		t.a.log.Info("ether-tray exiting")
	})
}

func (t *tray) onReady() {
	if runtime.GOOS == "darwin" {
		systray.SetTemplateIcon(iconTemplate, iconTemplate)
	} else if runtime.GOOS == "windows" {
		systray.SetIcon(iconICO)
	} else {
		systray.SetIcon(iconPNG)
	}
	systray.SetTitle("")
	systray.SetTooltip("Ether")

	t.statusItem = systray.AddMenuItem("Ether: checking…", "Server status")
	t.statusItem.Disable()
	systray.AddSeparator()

	openEther := systray.AddMenuItem("Open Ether", "Open the Ether web UI")
	openPlex := systray.AddMenuItem("Open Plex", "Open Plex Web (app.plex.tv)")
	systray.AddSeparator()

	startItem := systray.AddMenuItem("Start server", "Start the Ether service")
	stopItem := systray.AddMenuItem("Stop server", "Stop the Ether service")
	restartItem := systray.AddMenuItem("Restart server", "Restart the Ether service")
	if t.a.svc == nil {
		startItem.Disable()
		stopItem.Disable()
		restartItem.Disable()
	}
	systray.AddSeparator()

	t.updateItem = systray.AddMenuItem("Check for updates", "Check the signed release feed")
	if t.a.updates == nil {
		t.updateItem.SetTitle("Updates unavailable (dev build)")
		t.updateItem.Disable()
	}
	t.loginItem = systray.AddMenuItemCheckbox("Launch at login", "Start the tray when you log in", t.a.autostart.Enabled())
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Quit the Ether tray (the server keeps running)")

	go t.pollLoop()
	go t.updateLoop()

	go func() {
		for {
			select {
			case <-t.ctx.Done():
				return
			case <-openEther.ClickedCh:
				t.a.openEther()
			case <-openPlex.ClickedCh:
				t.a.openPlex()
			case <-startItem.ClickedCh:
				t.doService("start")
			case <-stopItem.ClickedCh:
				t.doService("stop")
			case <-restartItem.ClickedCh:
				t.doService("restart")
			case <-t.updateItem.ClickedCh:
				t.onUpdateClicked()
			case <-t.loginItem.ClickedCh:
				t.toggleLogin()
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func (t *tray) pollLoop() {
	t.refreshStatus()
	tk := time.NewTicker(5 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-tk.C:
			t.refreshStatus()
		}
	}
}

func (t *tray) refreshStatus() {
	ctx, cancel := context.WithTimeout(t.ctx, 4*time.Second)
	defer cancel()
	st := t.a.pollOnce(ctx)
	t.statusItem.SetTitle(statusLine(st))
	if runtime.GOOS != "darwin" {
		if st.Running {
			systray.SetIcon(iconPNG)
		} else {
			systray.SetIcon(iconOffPNG)
		}
	}
	systray.SetTooltip(statusLine(st))
}

// updateLoop checks the feed at startup (after a short settle) and daily.
// Prompt vs auto follows the owner's config: auto applies immediately,
// prompt surfaces a clickable "Update to vX" menu item.
func (t *tray) updateLoop() {
	if t.a.updates == nil {
		return
	}
	if uc, ok := t.a.updates.(*updater.Client); ok {
		uc.Poll(t.ctx, 24*time.Hour, time.Minute, func(ctx context.Context, upd *updater.Update) {
			t.offerUpdate(ctx, upd)
		})
		return
	}
	// Injected checker (tests): single startup check.
	upd, _, err := t.a.checkUpdates(t.ctx, false)
	if err == nil && upd != nil {
		t.showUpdate(upd)
	}
}

func (t *tray) offerUpdate(ctx context.Context, upd *updater.Update) {
	if updater.ShouldAutoApply(t.a.cfg) {
		_, _, err := t.a.checkUpdates(ctx, true)
		if err != nil {
			t.a.log.Warn("tray: auto-update failed", "err", err)
			t.showUpdate(upd)
			return
		}
		t.updateItem.SetTitle("Updated to v" + upd.Version() + " — restarting…")
		return
	}
	t.showUpdate(upd)
}

func (t *tray) showUpdate(upd *updater.Update) {
	t.pendingUpd = upd
	t.pendingVer = upd.Version()
	t.updateItem.SetTitle("Update to v" + upd.Version() + "…")
	t.updateItem.Enable()
}

func (t *tray) onUpdateClicked() {
	ctx, cancel := context.WithTimeout(t.ctx, 10*time.Minute)
	defer cancel()
	if t.pendingUpd != nil {
		// Owner clicked the offer: that's the prompt's "yes".
		t.updateItem.SetTitle("Updating to v" + t.pendingVer + "…")
		t.updateItem.Disable()
		_, applied, err := t.a.checkUpdates(ctx, true)
		t.pendingUpd = nil
		if err != nil {
			t.a.log.Warn("tray: update failed", "err", err)
			t.updateItem.SetTitle("Update failed — check for updates")
		} else {
			_ = applied
			t.updateItem.SetTitle("Check for updates")
		}
		t.updateItem.Enable()
		t.refreshStatus()
		return
	}
	upd, _, err := t.a.checkUpdates(ctx, false)
	switch {
	case err != nil:
		t.a.log.Warn("tray: update check failed", "err", err)
		t.updateItem.SetTitle("Update check failed — retry")
	case upd == nil:
		t.updateItem.SetTitle("Ether is up to date")
	default:
		t.showUpdate(upd)
	}
}

func (t *tray) toggleLogin() {
	var err error
	if t.loginItem.Checked() {
		err = t.a.autostart.Disable()
		if err == nil {
			t.loginItem.Uncheck()
		}
	} else {
		err = t.a.autostart.Enable()
		if err == nil {
			t.loginItem.Check()
		}
	}
	if err != nil {
		t.a.log.Warn("tray: launch-at-login toggle failed", "err", err)
	}
}

func (t *tray) doService(action string) {
	if err := t.a.serviceAction(action); err != nil {
		t.a.log.Warn("tray: service action failed", "action", action, "err", err)
		t.statusItem.SetTitle("Ether: " + action + " failed")
		return
	}
	// Give the service a moment, then refresh.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		t.refreshStatus()
	}()
}
