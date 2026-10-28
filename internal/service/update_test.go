package service

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	ksvc "github.com/kardianos/service"
)

func TestMergeServiceEnv(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing []string
		want     map[string]string
		keep     bool
		out      []string
	}{
		{
			name:     "upgrade keeps an owner's custom data dir",
			existing: []string{`ETHER_DATA_DIR=D:\Media\Ether`},
			want:     map[string]string{"ETHER_DATA_DIR": `C:\ProgramData\Ether`},
			keep:     true,
			out:      []string{`ETHER_DATA_DIR=D:\Media\Ether`},
		},
		{
			name:     "explicit data dir replaces the registered one",
			existing: []string{`ETHER_DATA_DIR=D:\Media\Ether`},
			want:     map[string]string{"ETHER_DATA_DIR": `E:\Ether`},
			out:      []string{`ETHER_DATA_DIR=E:\Ether`},
		},
		{
			name: "keep does not invent a data dir the service never had",
			want: map[string]string{"ETHER_DATA_DIR": `C:\ProgramData\Ether`},
			keep: true,
			out:  []string{},
		},
		{
			name:     "owner additions survive and other vars update",
			existing: []string{"HTTPS_PROXY=http://proxy:3128", "ETHER_LOG=debug", "garbage", "=x"},
			want:     map[string]string{"ETHER_LOG": "info"},
			keep:     true,
			out:      []string{"ETHER_LOG=info", "HTTPS_PROXY=http://proxy:3128"},
		},
		{
			name:     "names match case-insensitively",
			existing: []string{`ether_data_dir=D:\Old`},
			want:     map[string]string{"ETHER_DATA_DIR": `E:\New`},
			out:      []string{`ETHER_DATA_DIR=E:\New`},
		},
		{
			name:     "value containing equals signs",
			existing: []string{"OPTS=a=b=c"},
			out:      []string{"OPTS=a=b=c"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeServiceEnv(tc.existing, tc.want, tc.keep)
			if !reflect.DeepEqual(got, tc.out) {
				t.Fatalf("got %q, want %q", got, tc.out)
			}
		})
	}
}

func TestWindowsBinaryPath(t *testing.T) {
	for _, tc := range []struct {
		exe  string
		args []string
		want string
	}{
		{`C:\Program Files\Ether\ether.exe`, nil, `"C:\Program Files\Ether\ether.exe"`},
		{`C:\Ether\ether.exe`, []string{"serve", "--data-dir", `D:\My Data\`}, `"C:\Ether\ether.exe" serve --data-dir "D:\My Data\\"`},
		{`C:\e.exe`, []string{"", `say "hi"`}, `"C:\e.exe" "" "say \"hi\""`},
	} {
		if got := windowsBinaryPath(tc.exe, tc.args); got != tc.want {
			t.Errorf("windowsBinaryPath(%q, %q) = %s, want %s", tc.exe, tc.args, got, tc.want)
		}
	}
}

func TestInstallUpdatesExistingRegistrationInPlace(t *testing.T) {
	be := &fakeBackend{status: ksvc.StatusRunning}
	svc, err := newWithBackend(nil, Options{Executable: `C:\Program Files\Ether\ether.exe`, DataDir: `C:\ProgramData\Ether`, KeepInstalledDataDir: true}, be)
	if err != nil {
		t.Fatal(err)
	}
	var gotCfg *ksvc.Config
	var gotKeep bool
	svc.updateInstalled = func(cfg *ksvc.Config, keep bool) error { gotCfg, gotKeep = cfg, keep; return nil }
	svc.secureData = func(string) error { t.Fatal("an update must not re-ACL the data dir"); return nil }
	recovery := 0
	svc.postInstall = func(string) error { recovery++; return nil }

	var out bytes.Buffer
	if _, err := HandleCommand([]string{"service", "install"}, svc, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(be.calls, ",") != "status" {
		t.Fatalf("backend calls = %v; an existing service must not be deleted or re-created", be.calls)
	}
	if gotCfg == nil || gotCfg.Executable != `C:\Program Files\Ether\ether.exe` || !gotKeep {
		t.Fatalf("update cfg=%+v keep=%v", gotCfg, gotKeep)
	}
	if recovery != 1 {
		t.Fatalf("restart policy applied %d times, want 1", recovery)
	}
	if !strings.Contains(out.String(), "updated in place") {
		t.Fatalf("output %q", out.String())
	}
}

func TestInstallUpdateWithExplicitDataDir(t *testing.T) {
	be := &fakeBackend{status: ksvc.StatusStopped}
	svc := testService(t, nil, be) // KeepInstalledDataDir false: --data-dir given
	var secured string
	svc.secureData = func(dir string) error { secured = dir; return nil }
	var gotKeep = true
	svc.updateInstalled = func(cfg *ksvc.Config, keep bool) error {
		gotKeep = keep
		if cfg.EnvVars["ETHER_DATA_DIR"] != "/var/lib/ether" {
			t.Errorf("env = %v", cfg.EnvVars)
		}
		return nil
	}
	if updated, err := svc.InstallOrUpdate(); !updated || err != nil {
		t.Fatalf("updated=%v err=%v", updated, err)
	}
	if gotKeep || secured != "/var/lib/ether" {
		t.Fatalf("keep=%v secured=%q", gotKeep, secured)
	}
}

func TestInstallFreshWhenNotRegistered(t *testing.T) {
	be := &fakeBackend{err: map[string]error{"status": ksvc.ErrNotInstalled}}
	svc := testService(t, nil, be)
	svc.updateInstalled = func(*ksvc.Config, bool) error { t.Fatal("update called for a missing service"); return nil }
	updated, err := svc.InstallOrUpdate()
	if err != nil || updated {
		t.Fatalf("updated=%v err=%v", updated, err)
	}
	if strings.Join(be.calls, ",") != "status,install" {
		t.Fatalf("backend calls = %v", be.calls)
	}
}

func TestInstallUpdateFailureIsReported(t *testing.T) {
	be := &fakeBackend{status: ksvc.StatusStopped}
	svc := testService(t, nil, be)
	svc.updateInstalled = func(*ksvc.Config, bool) error {
		return errors.New("the specified service has been marked for deletion")
	}
	if _, err := svc.InstallOrUpdate(); err == nil || !strings.Contains(err.Error(), "marked for deletion") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(be.calls, ",") != "status" {
		t.Fatalf("backend calls = %v", be.calls)
	}
}
