// Command ether runs the Ether server: a Plex Media Server-compatible front
// end over a TMDB catalogue, streaming from Usenet on demand.
//
//	ether                         run (in the foreground, or under the OS service manager)
//	ether --spike movie.mp4 ...   run the spike: serve local files as a library
//	ether service install|uninstall|start|stop|restart|status [--data-dir DIR]
//	ether version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/braedonsaunders/ether/internal/app"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/service"
	"github.com/braedonsaunders/ether/internal/updater"
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

	_ = fs.Parse(os.Args[1:])
	if fs.Arg(0) == "service" {
		// `ether [flags] service <cmd> [flags]`: flags may follow the
		// subcommand, e.g. `ether service install --data-dir X`.
		svcArgs := append([]string{"service"}, parseInterleaved(fs, fs.Args()[1:])...)
		dir := *dataDir
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		// The data dir is baked into the registration as ETHER_DATA_DIR.
		svc, err := service.New(nil, service.Options{DataDir: dir})
		if err != nil {
			fatal(err)
		}
		if _, err := service.HandleCommand(svcArgs, svc, os.Stdout); err != nil {
			fatal(err)
		}
		return
	}

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
		if errors.Is(err, service.ErrRestartRequested) {
			fmt.Fprintln(os.Stderr, "ether: restart requested")
			os.Exit(service.ExitCodeRestart)
		}
		fatal(err)
	}
}

// parseInterleaved parses flags that may appear anywhere in args and
// returns the positional arguments in order.
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		_ = fs.Parse(args)
		rest := fs.Args()
		if len(rest) == 0 {
			return positional
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ether:", err)
	os.Exit(1)
}
