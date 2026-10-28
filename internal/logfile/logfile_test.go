package logfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriterRotatesAndKeeps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "ether.log")
	w, err := Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	line := strings.Repeat("x", 39) + "\n" // 40 bytes: two per file
	for i := 0; i < 10; i++ {
		if _, err := fmt.Fprint(w, line); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, path + ".1", path + ".2"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if st.Size() > 100 {
			t.Errorf("%s is %d bytes, over the limit", p, st.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("kept more than 2 rotated files: %v", err)
	}
}

func TestWriterAppendsToExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ether.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(w, "new\n")
	w.Close()
	if got := readFile(t, path); got != "old\nnew\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestWriterReopensAfterRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	path := filepath.Join(dir, "ether.log")
	w, err := Open(path, 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// Simulate a failure: close the file and remove the whole directory.
	w.Close()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(w, "after\n"); err != nil {
		t.Fatalf("write after removal: %v", err)
	}
	if got := readFile(t, path); got != "after\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestWriterConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ether.log")
	w, err := Open(path, 4096, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				fmt.Fprintf(w, "goroutine %d line %03d\n", g, i)
			}
		}(g)
	}
	wg.Wait()
	// Every line in the surviving files must be intact.
	for _, p := range []string{path, path + ".1", path + ".2", path + ".3"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			if !strings.HasPrefix(l, "goroutine ") || len(l) < len("goroutine 0 line 000") {
				t.Fatalf("%s: torn line %q", p, l)
			}
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("invalid handle") }

func TestTeeIgnoresFailingWriter(t *testing.T) {
	var buf bytes.Buffer
	n, err := Tee(failWriter{}, &buf).Write([]byte("hi\n"))
	if err != nil || n != 3 || buf.String() != "hi\n" {
		t.Fatalf("n=%d err=%v buf=%q", n, err, buf.String())
	}
	if _, err := Tee(failWriter{}).Write([]byte("x")); err == nil {
		t.Fatal("all writers failing should report an error")
	}
}

func TestPrepareCrashLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	cl, err := PrepareCrashLog(dir, 64)
	if err != nil {
		t.Fatal(err)
	}
	if cl.Changed {
		t.Fatal("fresh crash log reported as changed")
	}
	cl.File.Close()

	// Clean restart: nothing written, so nothing to report.
	cl, err = PrepareCrashLog(dir, 64)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a fatal trace written during this run, after startup.
	if _, err := cl.File.WriteString("fatal error: boom\n"); err != nil {
		t.Fatal(err)
	}
	cl.File.Close()
	// Coarse filesystem clocks: make the startup marker clearly older.
	earlier := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, crashMarkerName), earlier, earlier); err != nil {
		t.Fatal(err)
	}
	cl, err = PrepareCrashLog(dir, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !cl.Changed {
		t.Fatal("crash during the previous run not reported")
	}
	cl.File.Close()
	if got := readFile(t, cl.Path); got != "fatal error: boom\n" {
		t.Fatalf("crash log content = %q", got)
	}

	// Next start: the marker is now newer than the crash log.
	cl, err = PrepareCrashLog(dir, 64)
	if err != nil {
		t.Fatal(err)
	}
	cl.File.Close()
	if cl.Changed {
		t.Fatal("old crash reported again")
	}
}

func TestPrepareCrashLogRotatesLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CrashLogName)
	big := strings.Repeat("y", 100)
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	cl, err := PrepareCrashLog(dir, 64)
	if err != nil {
		t.Fatal(err)
	}
	cl.File.Close()
	if !cl.Changed {
		t.Error("pre-existing crash log without a marker should be reported")
	}
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Errorf("crash.log not rotated: %d bytes", st.Size())
	}
	if got := readFile(t, path+".1"); got != big {
		t.Errorf("rotated content lost")
	}
}
