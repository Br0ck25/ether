package transcode

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestMuxOutputWindowsSharingRetry(t *testing.T) {
	for _, release := range []bool{true, false} {
		name := "bounded failure"
		if release {
			name = "transient lock"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "chunk.m4s")
			if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
				t.Fatal(err)
			}
			p, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			closed := make(chan struct{})
			if release {
				go func() {
					time.Sleep(30 * time.Millisecond)
					windows.CloseHandle(h)
					close(closed)
				}()
				defer func() { <-closed }()
			} else {
				defer windows.CloseHandle(h)
			}
			start := time.Now()
			b, err := readMuxOutput(path)
			if release {
				if err != nil || string(b) != "media" {
					t.Fatalf("transient lock was not retried: %q, %v", b, err)
				}
			} else if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				t.Fatalf("permanent lock error = %v", err)
			}
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("sharing retry was not bounded: %s", elapsed)
			}
		})
	}
}
