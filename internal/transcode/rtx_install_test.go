package transcode

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func fakeRTXPackage(dir string) error {
	for _, name := range []string{"NVEncC64.exe", "nvngx_vsr.dll", "NVEncNVSDKNGX.dll"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func TestRTXInstallVerifiedAtomicAndReusable(t *testing.T) {
	payload := "pinned package fixture"
	var requests, extracts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, payload) }))
	defer server.Close()
	root := t.TempDir()
	i := rtxInstaller{root: root, url: server.URL, digest: fmt.Sprintf("%x", sha256.Sum256([]byte(payload))), size: int64(len(payload)), client: server.Client(), extract: func(ctx context.Context, archive, dir string) error {
		extracts.Add(1)
		if _, err := os.Stat(filepath.Join(root, "nvenc-9.38-windows-amd64")); !os.IsNotExist(err) {
			t.Error("published before extraction")
		}
		b, err := os.ReadFile(archive)
		if err != nil || string(b) != payload {
			t.Fatal("incorrect verified input")
		}
		return fakeRTXPackage(dir)
	}}
	path, err := i.ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "NVEncC64.exe") {
		t.Fatal(path)
	}
	again, err := i.ensure(context.Background())
	if err != nil || again != path {
		t.Fatal(again, err)
	}
	if requests.Load() != 1 || extracts.Load() != 1 {
		t.Fatal("re-downloaded installed runtime")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("staging files retained")
	}
}

func TestRTXInstallRejectsBadDownloadsBeforeExtraction(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"checksum", "evil", 200}, {"short", "ok", 200}, {"oversize", "oversized", 200}, {"status", "good", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			root := t.TempDir()
			i := rtxInstaller{root: root, url: server.URL, digest: fmt.Sprintf("%x", sha256.Sum256([]byte("good"))), size: 4, client: server.Client(), extract: func(context.Context, string, string) error { t.Fatal("unverified bytes extracted"); return nil }}
			if _, err := i.ensure(context.Background()); err == nil {
				t.Fatal("accepted invalid package")
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatal("failed install left files")
			}
		})
	}
}

func TestRTXInstallCancellationAndMissingDLLCleanUp(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "good") }))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := t.TempDir()
			i := rtxInstaller{root: root, url: server.URL, digest: fmt.Sprintf("%x", sha256.Sum256([]byte("good"))), size: 4, client: server.Client(), extract: func(_ context.Context, _, dir string) error {
				if cancelled {
					if err := fakeRTXPackage(dir); err != nil {
						return err
					}
					cancel()
				}
				return nil
			}}
			if _, err := i.ensure(ctx); err == nil {
				t.Fatal("published incomplete/cancelled runtime")
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatal("temporary files remain")
			}
		})
	}
}

func TestRTXSetupEligibilityAndManualPath(t *testing.T) {
	for _, tc := range []struct {
		name, mode, os, arch, path string
		gpuOK, installed           bool
		wantDownloads              int
	}{
		{name: "off", mode: "off", os: "windows", arch: "amd64", gpuOK: true},
		{name: "default off", os: "windows", arch: "amd64", gpuOK: true},
		{name: "mac", mode: "on", os: "darwin", arch: "amd64", gpuOK: true},
		{name: "arm", mode: "on", os: "windows", arch: "arm64", gpuOK: true},
		{name: "no RTX", mode: "on", os: "windows", arch: "amd64"},
		{name: "manual", mode: "on", os: "windows", arch: "amd64", path: "custom.exe", gpuOK: true},
		{name: "existing PATH", mode: "on", os: "windows", arch: "amd64", gpuOK: true, installed: true},
		{name: "automatic", mode: "auto", os: "windows", arch: "amd64", gpuOK: true, wantDownloads: 1},
		{name: "on", mode: "on", os: "windows", arch: "amd64", gpuOK: true, wantDownloads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			downloads, probes, gpus := 0, 0, 0
			r := rtxSetupResolver{goos: tc.os, goarch: tc.arch, probe: func(_ context.Context, path string) (string, core.VideoEnhancementSupport) {
				probes++
				return path, core.VideoEnhancementSupport{Available: path == "managed.exe" || tc.installed, Reason: "missing"}
			}, gpu: func(context.Context) error {
				gpus++
				if !tc.gpuOK {
					return fmt.Errorf("no RTX")
				}
				return nil
			}, install: func(context.Context) (string, error) { downloads++; return "managed.exe", nil }}
			_, s := r.resolve(context.Background(), config.RTXVideo{Mode: tc.mode, ExecutablePath: tc.path}, func(string, string) {})
			if downloads != tc.wantDownloads {
				t.Fatalf("downloads=%d", downloads)
			}
			if tc.mode == "off" || tc.mode == "" || tc.os != "windows" || tc.arch != "amd64" {
				if probes != 0 || gpus != 0 {
					t.Fatal("disabled/inapplicable setup touched machine")
				}
			}
			if tc.wantDownloads == 1 && !s.Available {
				t.Fatal("did not validate installed runtime")
			}
		})
	}
}

func TestRTXSetupFailureLeavesOrdinaryTranscodingAvailable(t *testing.T) {
	r := rtxSetupResolver{goos: "windows", goarch: "amd64", probe: func(context.Context, string) (string, core.VideoEnhancementSupport) {
		return "", core.VideoEnhancementSupport{}
	}, gpu: func(context.Context) error { return nil }, install: func(context.Context) (string, error) { return "", fmt.Errorf("offline") }}
	_, s := r.resolve(context.Background(), config.RTXVideo{Mode: "on"}, func(string, string) {})
	if s.Available || !strings.Contains(s.Reason, "ordinary transcoding remains available") || !strings.Contains(s.Reason, "retry") {
		t.Fatal(s)
	}
}

func TestRTXSetupOffCancelsAndCloseJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered, exited, off := make(chan struct{}, 2), make(chan struct{}, 2), make(chan struct{}, 1)
	w := &rtxSetupWorker{ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	w.resolve = func(ctx context.Context, cfg config.RTXVideo, _ func(string, string)) (string, core.VideoEnhancementSupport) {
		if cfg.Mode == "off" {
			off <- struct{}{}
			return "", core.VideoEnhancementSupport{Reason: "disabled"}
		}
		entered <- struct{}{}
		<-ctx.Done()
		exited <- struct{}{}
		// A stale successful result must not re-enable the backend after Off.
		return "stale.exe", core.VideoEnhancementSupport{Available: true}
	}
	m := &Manager{cfg: config.Transcode{RTXVideo: config.RTXVideo{Mode: "on"}}, rtxSetup: w, stop: make(chan struct{}), sessions: map[string]*Session{}}
	m.wg.Add(1)
	go m.runRTXSetup(w)
	defer m.Close()
	m.mu.Lock()
	w.changedLocked()
	m.mu.Unlock()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not progress")
		}
	}
	wait(entered)
	m.UpdateConfig(config.Transcode{RTXVideo: config.RTXVideo{Mode: "off"}})
	wait(exited)
	wait(off)
	m.UpdateConfig(config.Transcode{RTXVideo: config.RTXVideo{Mode: "on"}})
	wait(entered)
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	wait(done)
	wait(exited)
	if m.EnhancementStatus().Available {
		t.Fatal("cancelled install published success")
	}
}

// Opt-in native extraction proof. Uses a local copy of the pinned public
// archive, a loopback HTTP fixture and t.TempDir; never changes service config.
func TestRTXRuntimePackageNative(t *testing.T) {
	archive := os.Getenv("ETHER_RTX_INSTALL_TEST_ARCHIVE")
	if runtime.GOOS != "windows" || archive == "" {
		t.Skip("Windows plus local pinned runtime archive required")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, archive) }))
	defer server.Close()
	i := newRTXInstaller(t.TempDir(), ExecRunner{})
	i.url = server.URL
	i.client = server.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	path, err := i.ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, support := probeRTX(ctx, ExecRunner{}, "windows", path)
	if !support.Available {
		t.Fatalf("installed runtime did not validate: %+v", support)
	}
	t.Log(support.Version)
	if again, err := i.ensure(ctx); err != nil || again != path {
		t.Fatal(again, err)
	}
}
