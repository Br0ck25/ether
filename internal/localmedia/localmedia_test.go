package localmedia

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type catalogFake struct{ core.Catalog }

func (catalogFake) Search(context.Context, string, []core.MediaType, int) ([]core.Title, error) {
	return []core.Title{{Key: core.MovieKey(11), IDs: core.IDs{TMDB: 11}, Title: "Example", Year: 2024, Type: core.TypeMovie}}, nil
}
func (catalogFake) Get(_ context.Context, k core.RatingKey) (*core.Title, error) {
	return &core.Title{Key: k, Type: core.TypeMovie}, nil
}
func TestScanPreferredRangesAndRemoval(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	root := t.TempDir()
	path := filepath.Join(root, "Example.2024.1080p.mp4")
	if e = os.WriteFile(path, []byte("0123456789"), 0600); e != nil {
		t.Fatal(e)
	}
	s := New(db, catalogFake{}, nil)
	if e = s.AddFolder(ctx, root); e != nil {
		t.Fatal(e)
	}
	if e = s.Scan(ctx); e != nil {
		t.Fatal(e)
	}
	fs, e := s.Files(ctx)
	if e != nil || len(fs) != 1 || fs[0].Key != core.MovieKey(11) {
		t.Fatalf("files=%+v err=%v", fs, e)
	}
	src, e := s.Preferred(ctx, core.MovieKey(11))
	if e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 3)
	if _, e = src.ReadAt(b, 4); e != nil || string(b) != "456" {
		t.Fatalf("read=%q %v", b, e)
	}
	src.Close()
	mux := http.NewServeMux()
	mux.Handle("GET /files/{key}", s.SourceHandler())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/files/"+core.MovieKey(11).String(), nil)
	req.Header.Set("Range", "bytes=2-5")
	mux.ServeHTTP(rr, req)
	if rr.Code != 206 || rr.Body.String() != "2345" {
		t.Fatalf("range %d %q", rr.Code, rr.Body.String())
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if s.Available(ctx, core.MovieKey(11)) {
		t.Fatal("missing file available")
	}
	if e = s.Scan(ctx); e != nil {
		t.Fatal(e)
	}
	fs, e = s.Files(ctx)
	if e != nil || len(fs) != 0 {
		t.Fatalf("stale files: %+v %v", fs, e)
	}
}
func TestSymlinkEscapeAndCancelledWatcher(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "Example.2024.mp4")
	os.WriteFile(outside, []byte("secret"), 0600)
	if e = os.Symlink(outside, filepath.Join(root, "Example.2024.mp4")); e != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", e)
		}
		t.Fatal(e)
	}
	s := New(db, catalogFake{}, nil)
	s.AddFolder(ctx, root)
	if e = s.Scan(ctx); e != nil {
		t.Fatal(e)
	}
	fs, _ := s.Files(ctx)
	if len(fs) != 0 {
		t.Fatal("followed symlink")
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan struct{})
	go func() { s.Watch(c, time.Hour, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher ignored cancellation")
	}
}
