package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/braedonsaunders/ether/internal/service"
)

func TestServerIsStale(t *testing.T) {
	running := func(v string) Status { return Status{Running: true, Version: v} }
	for _, tc := range []struct {
		name string
		tray string
		st   Status
		want bool
	}{
		{"server older than installed bundle", "0.2.21", running("0.2.20"), true},
		{"older prerelease server", "0.3.0", running("0.3.0-beta.2"), true},
		{"same release", "0.2.21", running("0.2.21"), false},
		{"v prefix is not a difference", "v0.2.21", running("0.2.21"), false},
		{"server updated itself past the tray", "0.2.20", running("0.2.21"), false},
		{"dev tray", "dev", running("0.2.20"), false},
		{"unversioned tray", "", running("0.2.20"), false},
		{"dev server", "0.2.21", running("dev"), false},
		{"server down", "0.2.21", Status{Version: "0.2.20"}, false},
		{"only the PMS compatibility version known", "0.2.21", Status{Running: true, Version: "1.40.0", VersionIsPMS: true}, false},
		{"no version reported", "0.2.21", running(""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := serverIsStale(tc.tray, tc.st); got != tc.want {
				t.Fatalf("serverIsStale(%q, %+v) = %v, want %v", tc.tray, tc.st, got, tc.want)
			}
		})
	}
}

func TestRestartStaleServerOncePerTrayStart(t *testing.T) {
	svc := &bootstrapService{state: service.StatusRunning}
	a := &app{svc: svc, version: "0.2.21", log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// Polls that cannot name the server's release do not use up the check.
	for _, st := range []Status{{}, {Running: true, Version: "1.40.0", VersionIsPMS: true}} {
		if restarted, err := a.restartStaleServer(st); restarted || err != nil {
			t.Fatalf("inconclusive %+v: restarted=%v err=%v", st, restarted, err)
		}
	}
	old := Status{Running: true, Version: "0.2.20"}
	if restarted, err := a.restartStaleServer(old); !restarted || err != nil {
		t.Fatalf("stale server: restarted=%v err=%v", restarted, err)
	}
	// Still old after the restart (or the restart failed): never again.
	for range 3 {
		if restarted, _ := a.restartStaleServer(old); restarted {
			t.Fatal("restarted twice in one tray start")
		}
	}
	if !slices.Equal(svc.actions, []string{"restart"}) {
		t.Fatalf("actions = %v", svc.actions)
	}
}

func TestRestartStaleServerLeavesOthersAlone(t *testing.T) {
	old := Status{Running: true, Version: "0.2.20"}
	for _, tc := range []struct {
		name    string
		version string
		svc     *bootstrapService
	}{
		{"current server", "0.2.20", &bootstrapService{state: service.StatusRunning}},
		{"dev tray", "dev", &bootstrapService{state: service.StatusRunning}},
		{"server not run by the service manager", "0.2.21", &bootstrapService{state: service.StatusStopped}},
		{"service not installed", "0.2.21", &bootstrapService{state: service.StatusUnknown, statusErr: errors.New("not installed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{svc: tc.svc, version: tc.version}
			if restarted, err := a.restartStaleServer(old); restarted || err != nil || len(tc.svc.actions) != 0 {
				t.Fatalf("restarted=%v err=%v actions=%v", restarted, err, tc.svc.actions)
			}
		})
	}
	if restarted, err := (&app{version: "0.2.21"}).restartStaleServer(old); restarted || err != nil {
		t.Fatalf("no service control: restarted=%v err=%v", restarted, err)
	}
}

type failingRestart struct{ bootstrapService }

func (f *failingRestart) Restart() error { return errors.New("access denied") }

func TestRestartStaleServerReportsFailureOnce(t *testing.T) {
	a := &app{svc: &failingRestart{bootstrapService{state: service.StatusRunning}}, version: "0.2.21"}
	old := Status{Running: true, Version: "0.2.20"}
	if _, err := a.restartStaleServer(old); err == nil {
		t.Fatal("restart failure not reported")
	}
	if _, err := a.restartStaleServer(old); err != nil {
		t.Fatalf("failed restart retried: %v", err)
	}
}

func TestPollReadsReleaseFromPublicBootstrap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/identity", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<MediaContainer machineIdentifier="abc" friendlyName="Den" version="1.40.0"/>`)
	})
	mux.HandleFunc("/ether/api/status", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
	mux.HandleFunc("/ether/api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"version":"v0.2.21","setupComplete":true,"authRequired":true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	st, err := NewStatusClient(srv.URL, srv.Client()).Poll(context.Background())
	if err != nil || !st.Running || st.Version != "0.2.21" || st.VersionIsPMS || st.SetupComplete == nil || !*st.SetupComplete {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}

func TestPollOnceRestartsStaleServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/identity", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<MediaContainer machineIdentifier="abc" version="1.40.0"/>`)
	})
	mux.HandleFunc("/ether/api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"0.2.20"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	svc := &bootstrapService{state: service.StatusRunning}
	a := &app{svc: svc, version: "0.2.21", status: NewStatusClient(srv.URL, srv.Client()),
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.pollOnce(context.Background())
	a.pollOnce(context.Background())
	if !slices.Equal(svc.actions, []string{"restart"}) {
		t.Fatalf("actions = %v", svc.actions)
	}
}
