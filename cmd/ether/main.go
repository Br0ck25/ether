// Command ether runs the Ether server: a Plex Media Server-compatible front
// end over a TMDB catalogue, streaming from Usenet on demand.
//
//	ether                         run (in the foreground, or under the OS service manager)
//	ether --spike movie.mp4 ...   run the spike: serve local files as a library
//	ether service install|uninstall|start|stop|restart|status
//	ether version
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ether-media/ether/internal/app"
	"github.com/ether-media/ether/internal/config"
	"github.com/ether-media/ether/internal/service"
	"github.com/ether-media/ether/internal/updater"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("ether %s (%s, %s)\n", updater.Version, updater.Commit, updater.Date)
		return
	}

	fs := flag.NewFlagSet("ether", flag.ExitOnError)
	dataDir := fs.String("data-dir", config.DefaultDataDir(), "data directory (config, database, cache)")
	spike := fs.Bool("spike", false, "spike mode: serve the given local video files as a library")
	logLevel := fs.String("log-level", "", "debug, info, warn or error (default from config)")

	args := os.Args[1:]
	if len(args) > 0 && args[0] == "service" {
		svc, err := service.New(nil, service.Options{DataDir: *dataDir})
		if err != nil {
			fatal(err)
		}
		if _, err := service.HandleCommand(args, svc, os.Stdout); err != nil {
			fatal(err)
		}
		return
	}
	_ = fs.Parse(args)

	run := func(ctx context.Context) error {
		level := slog.LevelInfo
		if lv := *logLevel; lv != "" {
			_ = level.UnmarshalText([]byte(strings.ToUpper(lv)))
		} else if cfg, err := config.Open(*dataDir); err == nil {
			_ = level.UnmarshalText([]byte(strings.ToUpper(cfg.Get().Server.LogLevel)))
		}
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
		a, err := app.New(ctx, app.Options{
			DataDir:    *dataDir,
			Logger:     logger,
			Spike:      *spike,
			SpikeFiles: fs.Args(),
		})
		if err != nil {
			return err
		}
		defer a.Close()
		return a.Run(ctx)
	}

	svc, err := service.New(run, service.Options{DataDir: *dataDir})
	if err != nil {
		fatal(err)
	}
	if err := svc.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ether:", err)
	os.Exit(1)
}
