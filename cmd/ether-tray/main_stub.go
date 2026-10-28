//go:build !windows && !linux && !(darwin && cgo)

// Headless stub: this build has no GUI backend (e.g. CGO_ENABLED=0 on
// macOS, where fyne.io/systray requires CGO). It polls the server and
// logs status so `go build ./...` and CI stay green everywhere; release
// builds compile tray.go instead (macOS with CGO, Windows/Linux native).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a := newApp(log)
	if len(os.Args) > 1 && os.Args[1] == "--open" {
		os.Exit(a.openUI())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.runHeadless(ctx); err != nil {
		log.Error("ether-tray", "err", err)
		os.Exit(1)
	}
}
