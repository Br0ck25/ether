package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// ErrUnresponsive is returned by App.Run when the server stopped answering
// its own local health probe. The process then exits non-zero so the
// service manager restarts it.
var ErrUnresponsive = errors.New("app: server unresponsive to local health probes")

// Watchdog defaults: probe every minute, give up after five consecutive
// failures (about five minutes unresponsive).
const (
	watchdogInterval     = time.Minute
	watchdogThreshold    = 5
	watchdogProbeTimeout = 15 * time.Second
	// watchdogExitGrace bounds the graceful shutdown after the watchdog
	// fires; a wedged server may never finish it.
	watchdogExitGrace = 2 * time.Minute
)

// watchdog probes the server's own HTTP endpoint and reports when it has
// been unresponsive for threshold consecutive probes. It catches hangs
// that leave the process alive (deadlocked handler, exhausted listener)
// which no supervisor would otherwise notice.
type watchdog struct {
	interval  time.Duration
	threshold int
	// probe returns nil when the server answered at all.
	probe func(ctx context.Context) error
	log   *slog.Logger
	// forceExit runs if Run has not finished watchdogExitGrace after the
	// watchdog fired. nil: os.Exit(1).
	forceExit func()
	exitGrace time.Duration
}

func newWatchdog(url string, log *slog.Logger) *watchdog {
	return &watchdog{
		interval:  watchdogInterval,
		threshold: watchdogThreshold,
		probe:     httpProbe(url, watchdogProbeTimeout),
		log:       log,
		exitGrace: watchdogExitGrace,
	}
}

// httpProbe GETs url; any HTTP response (even an error status) counts as
// alive, only transport failures and timeouts count as down.
func httpProbe(url string, timeout time.Duration) func(context.Context) error {
	client := &http.Client{
		Timeout: timeout,
		// No proxy and no pooled connections: probe the listener itself.
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
	}
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-Plex-Product", "Ether Watchdog")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil
	}
}

// probeURL is the local /identity URL for a listener address: loopback
// when bound to all interfaces, otherwise the bound IP itself.
func probeURL(addr net.Addr) string {
	host, port := "127.0.0.1", ""
	if tcp, ok := addr.(*net.TCPAddr); ok {
		port = strconv.Itoa(tcp.Port)
		if tcp.IP != nil && !tcp.IP.IsUnspecified() {
			host = tcp.IP.String()
		}
	} else if h, p, err := net.SplitHostPort(addr.String()); err == nil {
		port = p
		if ip := net.ParseIP(h); ip != nil && !ip.IsUnspecified() {
			host = h
		}
	}
	return "http://" + net.JoinHostPort(host, port) + "/identity"
}

// run probes until ctx is done (returning nil) or threshold consecutive
// probes fail (returning ErrUnresponsive).
func (w *watchdog) run(ctx context.Context) error {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		err := w.probe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			if failures > 0 {
				w.log.Info("watchdog: server responsive again", "after_failures", failures)
			}
			failures = 0
			continue
		}
		failures++
		w.log.Warn("watchdog: local health probe failed", "failures", failures, "threshold", w.threshold, "err", err)
		if failures >= w.threshold {
			w.log.Error("watchdog: server unresponsive; exiting so the service manager restarts it",
				"failures", failures, "interval", w.interval)
			w.armForceExit()
			return ErrUnresponsive
		}
	}
}

// armForceExit guarantees the process ends even if the graceful shutdown
// that follows ErrUnresponsive hangs on the same wedge.
func (w *watchdog) armForceExit() {
	grace := w.exitGrace
	if grace <= 0 {
		grace = watchdogExitGrace
	}
	exit := w.forceExit
	if exit == nil {
		exit = func() { os.Exit(1) }
	}
	time.AfterFunc(grace, func() {
		w.log.Error("watchdog: shutdown did not finish; forcing exit", "grace", grace)
		exit()
	})
}
