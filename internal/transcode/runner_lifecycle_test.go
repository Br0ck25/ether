package transcode

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestEncoderLifecycleHelper(t *testing.T) {
	mode := os.Getenv("ETHER_TEST_ENCODER_MODE")
	switch mode {
	case "parent":
		os.Setenv("ETHER_TEST_ENCODER_MODE", "child")
		p, err := (ExecRunner{}).Start(Command{Path: os.Args[0], Args: []string{"-test.run=^TestEncoderLifecycleHelper$"}})
		if err != nil {
			os.Exit(10)
		}
		// Establish that the child is actually running before the parent exits.
		ready := false
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			if b, err := os.ReadFile(os.Getenv("ETHER_TEST_ENCODER_HEARTBEAT")); err == nil && len(b) > 0 {
				ready = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !ready {
			p.Stop()
			os.Exit(11)
		}
		// Deliberately bypass defer/shutdown, as an abrupt Ether exit does.
		os.WriteFile(os.Getenv("ETHER_TEST_ENCODER_PID"), []byte(strconv.Itoa(p.(*execProcess).PID())), 0600)
		os.Exit(0)
	case "child":
		for i := 0; i < 300; i++ {
			os.WriteFile(os.Getenv("ETHER_TEST_ENCODER_HEARTBEAT"), []byte(strconv.Itoa(i)), 0600)
			time.Sleep(20 * time.Millisecond)
		}
		os.Exit(0)
	case "output":
		os.Stdout.Write([]byte("encoder output"))
		os.Exit(0)
	case "wait":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		t.Skip("subprocess helper")
	}
}

func TestEncoderGuardianOutputAndCancellation(t *testing.T) {
	t.Setenv("ETHER_TEST_ENCODER_MODE", "output")
	var out bytes.Buffer
	if err := (ExecRunner{}).Run(context.Background(), Command{Path: os.Args[0], Args: []string{"-test.run=^TestEncoderLifecycleHelper$"}, Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "encoder output" {
		t.Fatalf("handshake leaked into output: %q", out.String())
	}
	t.Setenv("ETHER_TEST_ENCODER_MODE", "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	at := time.Now()
	if err := (ExecRunner{}).Run(ctx, Command{Path: os.Args[0], Args: []string{"-test.run=^TestEncoderLifecycleHelper$"}}); err != context.DeadlineExceeded {
		t.Fatalf("cancel: %v", err)
	}
	if time.Since(at) > 5*time.Second {
		t.Fatal("encoder cancellation stalled")
	}
}

func TestEncoderDiesAfterParentExit(t *testing.T) {
	dir := t.TempDir()
	heartbeat, pidFile := filepath.Join(dir, "heartbeat"), filepath.Join(dir, "pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEncoderLifecycleHelper$")
	cmd.Env = append(os.Environ(), "ETHER_TEST_ENCODER_MODE=parent", "ETHER_TEST_ENCODER_PID="+pidFile, "ETHER_TEST_ENCODER_HEARTBEAT="+heartbeat)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup owns only the child created by this test if a regression keeps it alive.
	defer func() {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
			p.Release()
		}
	}()
	time.Sleep(150 * time.Millisecond)
	first, _ := os.ReadFile(heartbeat)
	time.Sleep(150 * time.Millisecond)
	second, _ := os.ReadFile(heartbeat)
	if len(first) == 0 || !bytes.Equal(first, second) {
		t.Fatalf("encoder survived parent exit: %q -> %q", first, second)
	}
}
