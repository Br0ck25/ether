//go:build windows || linux || (darwin && cgo)

package main

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"sync"
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
	mu         sync.Mutex
	pendingUpd *updater.Update
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

// updateLoop checks the feed at startup (after a short settle) and daily,
// surfacing a clickable "Update available" item. The tray never installs
// the server binary; the server's update manager does that at a safe moment.
func (t *tray) updateLoop() {
	if t.a.updates == nil {
		return
	}
	if uc, ok := t.a.updates.(*updater.Client); ok {
		uc.Poll(t.ctx, 24*time.Hour, time.Minute, func(_ context.Context, upd *updater.Update) {
			t.showUpdate(upd)
		})
		return
	}
	// Injected checker (tests): single startup check.
	if upd, err := t.a.checkUpdates(t.ctx); err == nil && upd != nil {
		t.showUpdate(upd)
	}
}

func (t *tray) showUpdate(upd *updater.Update) {
	t.mu.Lock()
	t.pendingUpd = upd
	t.mu.Unlock()
	t.updateItem.SetTitle("Update available: v" + upd.Version() + "…")
	t.updateItem.Enable()
}

func (t *tray) onUpdateClicked() {
	ctx, cancel := context.WithTimeout(t.ctx, 10*time.Minute)
	defer cancel()
	t.mu.Lock()
	upd := t.pendingUpd
	t.mu.Unlock()
	if upd != nil {
		// Installer download (macOS) or the web UI's update page.
		launched, err := t.a.openUpdate(ctx, upd)
		switch {
		case err != nil:
			t.a.log.Warn("tray: open update", "err", err)
			t.updateItem.SetTitle("Update v" + upd.Version() + " — open Ether to install")
		case launched:
			t.updateItem.SetTitle("Installer for v" + upd.Version() + " opened")
		}
		return
	}
	upd, err := t.a.checkUpdates(ctx)
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
