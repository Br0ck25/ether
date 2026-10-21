package overlays

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cacheTestKey(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func cacheTestImage(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 500, 750))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 34, G: 58, B: 81, A: 255}), image.Point{}, draw.Src)
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 88}); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}
func awaitCache(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !condition() {
		select {
		case <-deadline:
			t.Fatal("cache did not reach expected state")
		case <-tick.C:
		}
	}
}

func TestMemoryCacheEvictsLeastRecentlyUsedOnly(t *testing.T) {
	c := newPosterCache(Options{MemoryCacheBytes: 8})
	calls := map[string]int{}
	get := func(key string) {
		t.Helper()
		_, err := c.get(context.Background(), key, func(context.Context) ([]byte, error) { calls[key]++; return []byte("1234"), nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	get("a")
	get("b")
	get("a")
	get("c")
	get("a")
	if calls["a"] != 1 || c.stats().MemoryEvictions != 1 {
		t.Fatalf("hot entry was discarded: %v %+v", calls, c.stats())
	}
	get("b")
	if calls["b"] != 2 || c.stats().MemoryBytes > 8 {
		t.Fatalf("LRU budget failed: %v %+v", calls, c.stats())
	}
	_, err := c.get(context.Background(), "oversize", func(context.Context) ([]byte, error) { return make([]byte, 9), nil })
	if err != nil || c.stats().MemoryBytes > 8 {
		t.Fatal("oversize result entered cache")
	}
	before := c.stats().MemoryEvictions
	get("b")
	if calls["b"] != 2 || c.stats().MemoryEvictions != before {
		t.Fatal("oversize result flushed useful entries")
	}
}

func TestPersistentCacheRestartReuseAndCorruption(t *testing.T) {
	data := cacheTestImage(t)
	options := Options{CacheDir: t.TempDir(), MemoryCacheBytes: -1}
	key := cacheTestKey("source-and-style")
	first := newPosterCache(options)
	_, err := first.get(context.Background(), key, func(context.Context) ([]byte, error) { return data, nil })
	if err != nil {
		t.Fatal(err)
	}
	second := newPosterCache(options)
	got, err := second.get(context.Background(), key, func(context.Context) ([]byte, error) {
		t.Error("restart rendered a cached image")
		return nil, errors.New("unexpected render")
	})
	if err != nil || !bytes.Equal(data, got) || second.stats().DiskHits != 1 || second.stats().Renders != 0 {
		t.Fatalf("restart cache miss: %v %+v", err, second.stats())
	}
	file := filepath.Join(options.CacheDir, key+".jpg")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	third := newPosterCache(options)
	got, err = third.get(context.Background(), key, func(context.Context) ([]byte, error) { return data, nil })
	if err != nil || !bytes.Equal(got, data) || third.stats().Renders != 1 || third.stats().DiskErrors != 1 {
		t.Fatalf("corruption not repaired: %v %+v", err, third.stats())
	}
	fourth := newPosterCache(options)
	if _, err = fourth.get(context.Background(), key, func(context.Context) ([]byte, error) { return nil, errors.New("repair was not persisted") }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(options.CacheDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v %v", entries, err)
	}
}

func TestDiskBudgetLRUAndSmallerBudgetAfterRestart(t *testing.T) {
	data := cacheTestImage(t)
	entrySize := int64(len(data) + cacheHeaderSize)
	options := Options{CacheDir: t.TempDir(), MemoryCacheBytes: -1, DiskCacheBytes: 2 * entrySize}
	c := newPosterCache(options)
	get := func(key string) {
		t.Helper()
		if _, err := c.get(context.Background(), cacheTestKey(key), func(context.Context) ([]byte, error) { return data, nil }); err != nil {
			t.Fatal(err)
		}
	}
	get("a")
	get("b")
	get("a")
	get("c")
	if _, err := os.Stat(filepath.Join(options.CacheDir, cacheTestKey("b")+".jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disk LRU did not evict b")
	}
	if c.stats().DiskBytes > options.DiskCacheBytes || c.stats().DiskEntries != 2 {
		t.Fatalf("disk budget exceeded: %+v", c.stats())
	}
	options.DiskCacheBytes = entrySize
	smaller := newPosterCache(options)
	if smaller.stats().DiskBytes > entrySize || smaller.stats().DiskEntries != 1 {
		t.Fatalf("restart did not enforce smaller budget: %+v", smaller.stats())
	}
}

func TestCacheDoesNotFollowSymlinksOrTouchUnrelatedFiles(t *testing.T) {
	data := cacheTestImage(t)
	outside := filepath.Join(t.TempDir(), "original.jpg")
	if err := os.WriteFile(outside, data, 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key := cacheTestKey("symlink")
	if err := os.Symlink(outside, filepath.Join(dir, key+".jpg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	unrelated := filepath.Join(dir, "owner.txt")
	if err := os.WriteFile(unrelated, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	c := newPosterCache(Options{CacheDir: dir, MemoryCacheBytes: -1})
	if _, err := c.get(context.Background(), key, func(context.Context) ([]byte, error) { return data, nil }); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(original, data) {
		t.Fatal("source outside cache changed")
	}
	info, err := os.Lstat(filepath.Join(dir, key+".jpg"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("unexpected symlink replaced")
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "preserve" {
		t.Fatal("unrelated file touched")
	}
	if c.stats().DiskEntries != 0 || c.stats().DiskErrors == 0 {
		t.Fatalf("symlink accepted: %+v", c.stats())
	}
}

func TestSameKeyRenderCoalescingAndIndependentCancellation(t *testing.T) {
	c := newPosterCache(Options{})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	render := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return []byte("image"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	firstCtx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := c.get(firstCtx, "same", render); first <- err }()
	<-started
	second := make(chan error, 1)
	go func() {
		data, err := c.get(context.Background(), "same", render)
		if err == nil && string(data) != "image" {
			err = errors.New("wrong image")
		}
		second <- err
	}()
	awaitCache(t, func() bool { return c.stats().Coalesced == 1 })
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller: %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("one caller canceled shared work: %v", err)
	}
	if calls.Load() != 1 || c.stats().InFlight != 0 {
		t.Fatalf("duplicate/incomplete renders: %d %+v", calls.Load(), c.stats())
	}
}

func TestLastWaiterCancellationStopsQueuedRender(t *testing.T) {
	c := newPosterCache(Options{RenderConcurrency: 1})
	started := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := c.get(context.Background(), "busy", func(context.Context) ([]byte, error) { close(started); <-release; return []byte("busy"), nil })
		first <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	var queuedCalls atomic.Int32
	go func() {
		_, err := c.get(ctx, "queued", func(context.Context) ([]byte, error) { queuedCalls.Add(1); return []byte("queued"), nil })
		queued <- err
	}()
	awaitCache(t, func() bool { return c.stats().InFlight == 2 })
	cancel()
	if err := <-queued; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	awaitCache(t, func() bool { return c.stats().InFlight == 0 })
	if queuedCalls.Load() != 0 {
		t.Fatal("canceled queued work rendered")
	}
}

func TestLastWaiterCancellationStopsActiveRender(t *testing.T) {
	c := newPosterCache(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := c.get(ctx, "active", func(work context.Context) ([]byte, error) {
			close(started)
			<-work.Done()
			close(stopped)
			return nil, work.Err()
		})
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("abandoned render still running")
	}
	if c.stats().MemoryEntries != 0 {
		t.Fatal("canceled output cached")
	}
	if _, err := c.get(context.Background(), "active", func(context.Context) ([]byte, error) { return []byte("retry"), nil }); err != nil {
		t.Fatal(err)
	}
}

func TestColdRenderConcurrencyBound(t *testing.T) {
	c := newPosterCache(Options{RenderConcurrency: 2})
	var active, peak atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.get(context.Background(), fmt.Sprint(i), func(context.Context) ([]byte, error) {
				n := active.Add(1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				<-release
				active.Add(-1)
				return []byte("image"), nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	awaitCache(t, func() bool { return active.Load() == 2 && c.stats().InFlight == 8 })
	close(release)
	wg.Wait()
	if peak.Load() != 2 || c.stats().Renders != 8 {
		t.Fatalf("render bound: peak=%d stats=%+v", peak.Load(), c.stats())
	}
}

func TestCacheKeyIncludesPixelsOnly(t *testing.T) {
	s := New(nil, Options{})
	data := cacheTestImage(t)
	labels := []string{"NEW SEASON", "4K HDR"}
	cfg := normalizeSettings(Settings{Enabled: true, Status: true, Quality: true})
	first, _, err := s.composite(data, labels, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Localize = true
	cfg.Personalize = true
	cfg.Ratings = true
	cfg.Details = true
	cfg.Status = false
	second, _, err := s.composite(data, labels, cfg)
	if err != nil || !bytes.Equal(first, second) || s.CacheStats().Renders != 1 || s.CacheStats().MemoryHits != 1 {
		t.Fatalf("nonvisual flag duplicated output: %v %+v", err, s.CacheStats())
	}
	cfg.Accent = "#00aaff"
	third, _, err := s.composite(data, labels, cfg)
	if err != nil || bytes.Equal(first, third) || s.CacheStats().Renders != 2 {
		t.Fatalf("pixel change not rendered: %v %+v", err, s.CacheStats())
	}
}

func TestCacheRetriesErrorsAndIgnoresAlreadyCanceledRequests(t *testing.T) {
	c := newPosterCache(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	render := func(context.Context) ([]byte, error) { calls++; return nil, errors.New("decode failed") }
	if _, err := c.get(ctx, "failure", render); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled request rendered: %v calls=%d", err, calls)
	}
	for range 2 {
		if _, err := c.get(context.Background(), "failure", render); err == nil {
			t.Fatal("render error hidden")
		}
	}
	if calls != 2 || c.stats().MemoryEntries != 0 || c.stats().InFlight != 0 {
		t.Fatalf("errors were cached: calls=%d %+v", calls, c.stats())
	}
}

func TestDiskEnvelopeBindsImageToRequestedKey(t *testing.T) {
	data := cacheTestImage(t)
	dir := t.TempDir()
	options := Options{CacheDir: dir, MemoryCacheBytes: -1}
	first := newPosterCache(options)
	a, b := cacheTestKey("a"), cacheTestKey("b")
	if _, err := first.get(context.Background(), a, func(context.Context) ([]byte, error) { return data, nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, a+".jpg"), filepath.Join(dir, b+".jpg")); err != nil {
		t.Fatal(err)
	}
	restarted := newPosterCache(options)
	if _, err := restarted.get(context.Background(), b, func(context.Context) ([]byte, error) { return data, nil }); err != nil {
		t.Fatal(err)
	}
	if restarted.stats().DiskHits != 0 || restarted.stats().DiskErrors != 1 || restarted.stats().Renders != 1 {
		t.Fatalf("wrong poster accepted at renamed key: %+v", restarted.stats())
	}
}

func TestConcurrentRealComposites(t *testing.T) {
	data := cacheTestImage(t)
	s := New(nil, Options{RenderConcurrency: 2})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := normalizeSettings(Settings{Accent: fmt.Sprintf("#%06x", i*1024)})
			rendered, _, err := s.composite(data, []string{"4K HDR", "IMDb 8.6"}, cfg)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := jpeg.Decode(bytes.NewReader(rendered)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if s.CacheStats().Renders != 8 {
		t.Fatalf("missing distinct compositions: %+v", s.CacheStats())
	}
}

func TestRestartCleansOnlyOwnInterruptedWrites(t *testing.T) {
	dir := t.TempDir()
	temporary := ".overlay-0123456789abcdef01234567.tmp"
	for _, name := range []string{temporary, "owner.tmp", ".overlay-other.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("unfinished"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_ = newPosterCache(Options{CacheDir: dir})
	if _, err := os.Stat(filepath.Join(dir, temporary)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted cache write retained")
	}
	for _, name := range []string{"owner.tmp", ".overlay-other.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("unrelated temporary file removed", name)
		}
	}
}
