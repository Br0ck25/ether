package transcode

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMatroskaCollectorWaitsForClosedChunk(t *testing.T) {
	dir := t.TempDir()
	chunks := filepath.Join(dir, "chunks")
	if err := os.Mkdir(chunks, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := &blockedProc{done: make(chan struct{})}
	t.Cleanup(p.Stop)
	r := &run{proc: p, maxDone: -1, stderr: newTailBuffer(10), spec: runSpec{Dir: dir, Playlist: "run.m3u8", InitName: "header.mkv"}}
	r.chunks = &chunkRun{dir: chunks, container: ContainerMatroska, initPath: filepath.Join(dir, "header.mkv"), sl: newSlotter(0, 4*time.Second, 2, 0)}
	s := &Session{m: &Manager{}, dir: dir, container: ContainerMatroska, cur: r,
		done: []map[int]bool{{}}, changed: make(chan struct{}), segDur: 4 * time.Second, total: 2, endSeg: -1, log: testLogger(t)}
	chunk := filepath.Join(chunks, "00000.mkv")
	write(chunk, nil)
	write(filepath.Join(dir, "run.m3u8"), []byte("#EXTM3U\n#EXTINF:4,\n00000.mkv\n"))
	s.collectChunks(r, false)
	if r.chunks.next != 0 || s.done[0][0] {
		t.Fatal("consumed a listed chunk before its writer closed it")
	}
	// Even a parseable prefix is not a closed chunk. The following file
	// is opened only after ssegment has closed the previous AVIO buffer.
	write(chunk, emptyCluster(matroskaOrigin))
	s.collectChunks(r, false)
	if r.chunks.next != 0 || s.done[0][0] {
		t.Fatal("published a parseable chunk while it was still open")
	}
	write(filepath.Join(chunks, "00001.mkv"), nil)
	s.collectChunks(r, false)
	if r.chunks.next != 1 || !s.done[0][0] {
		t.Fatal("did not publish the closed chunk")
	}
}
