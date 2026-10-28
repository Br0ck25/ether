package transcode

import (
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestSoftwareThreadBudget(t *testing.T) {
	for _, tc := range []struct{ w, h, available, want int }{
		{640, 360, 32, 2}, {1920, 1080, 32, 4}, {3840, 2160, 32, 8},
		{3840, 2160, 2, 2}, {640, 360, 1, 1}, {0, 0, 32, 4}, {0, 0, 0, 1},
	} {
		if got := softwareThreadBudget(tc.w, tc.h, tc.available); got != tc.want {
			t.Errorf("%+v: got %d", tc, got)
		}
	}
}

func TestExitedProcessResourceMeasurements(t *testing.T) {
	proc, err := (ExecRunner{}).Start(Command{Path: os.Args[0], Args: []string{"-test.run=^$"}, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proc.Stop)
	select {
	case <-proc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not exit")
	}
	if err := proc.Err(); err != nil {
		t.Fatal(err)
	}
	p := proc.(*execProcess)
	if p.PID() <= 0 {
		t.Fatal("missing PID")
	}
	user, system := p.CPUTime()
	if user < 0 || system < 0 {
		t.Fatal("negative CPU usage")
	}
	peak, available := p.PeakRSSBytes()
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if !available || peak == 0 {
			t.Fatal("missing native peak RSS")
		}
	} else if available {
		t.Fatal("unsupported peak RSS reported as available")
	}
}
