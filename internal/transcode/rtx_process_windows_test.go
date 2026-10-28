//go:build windows

package transcode

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestRTXJobHelper(t *testing.T) {
	n := len(os.Args)
	if n < 3 || os.Args[n-2] != "rtx-job-helper" {
		t.Skip("subprocess helper")
	}
	if os.Args[n-1] == "child" {
		time.Sleep(time.Minute)
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestRTXJobHelper$", "--", "rtx-job-helper", "child")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("CHILD=%d\n", child.Process.Pid)
	time.Sleep(time.Minute)
}
func TestRTXJobStopsOwnedDescendants(t *testing.T) {
	var out boundedRTXOutput
	p, err := (ExecRunner{}).StartRTX(Command{Path: os.Args[0], Args: []string{"-test.run=^TestRTXJobHelper$", "--", "rtx-job-helper", "parent"}, Dir: t.TempDir(), Stdout: &out, Stderr: &out})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	pid := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, "CHILD=") {
				pid, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "CHILD=")))
			}
		}
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("child not launched: %s", out.String())
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	p.Stop()
	status, err := windows.WaitForSingleObject(child, 1000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned child survived Stop: status=%d err=%v", status, err)
	}
}
