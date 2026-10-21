package transcode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestDASHUnreadableChunkIsNotSkipped(t *testing.T) {
	for _, recover := range []bool{true, false} {
		name := "retry"
		if !recover {
			name = "fail final run"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			runDir := filepath.Join(dir, "run-1")
			if err := os.Mkdir(runDir, 0o700); err != nil {
				t.Fatal(err)
			}
			write := func(name string, b []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(runDir, name), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(dashInitFile(0), testVideoInit(1280, 720))
			write(dashChunkFile(0, 1), []byte("busy"))
			write(dashChunkFile(0, 2), testChunk(2, 90000, 3750, 24, []byte("video")))
			s := &Session{
				proto:  core.ProtocolDASH,
				m:      &Manager{opts: options{}},
				dir:    dir,
				segDur: 2 * time.Second,
				total:  1,
				log:    testLogger(t),
				sessionDelivery: sessionDelivery{
					done:    []map[int]bool{{}},
					changed: make(chan struct{}),
					dash:    newDASHState([]dashRep{{ID: 0, Kind: "video"}}, nil, "retry"),
				},
			}
			proc := &fakeProc{done: make(chan struct{}), kill: make(chan struct{})}
			close(proc.done)
			r := &run{seq: 1, proc: proc, stderr: newTailBuffer(10), maxDone: -1, started: time.Now(),
				dash: newDASHRun(runDir, []int{0}, 0, s.segDur, s.total, 0)}
			s.cur = r
			s.collectDASH(r, false)
			if got := r.dash.tracks[0].next; got != 1 {
				t.Fatalf("unreadable chunk skipped: next=%d, want 1", got)
			}
			if len(s.done[0]) != 0 {
				t.Fatal("published media past an unreadable chunk")
			}
			if recover {
				write(dashChunkFile(0, 1), testChunk(1, 0, 3750, 24, []byte("video")))
			}
			s.collectDASH(r, true)
			if !recover {
				if !r.dash.failed || r.proc.Err() == nil || len(s.done[0]) != 0 {
					t.Fatal("permanent unreadable media must fail the run without publication")
				}
				return
			}
			if r.dash.failed || r.dash.tracks[0].next != 3 || !s.done[0][0] {
				t.Fatalf("retry did not recover all media: next=%d, done=%v", r.dash.tracks[0].next, s.done[0])
			}
			info, err := parseInit(testVideoInit(1280, 720))
			if err != nil {
				t.Fatal(err)
			}
			chunk, err := scanChunk(s.trackSegPath(0, 0), info)
			if err != nil || chunk.start != 0 || chunk.end != 2*time.Second {
				t.Fatalf("recovered timeline = %s..%s, %v; want full 0..2s", chunk.start, chunk.end, err)
			}
		})
	}
}
