package overlays

import (
	"bytes"
	"container/list"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/lru"
)

const cacheEnvelope = "ETHEROV3"
const maxCachedPoster = 8 << 20
const cacheHeaderSize = len(cacheEnvelope) + 2*sha256.Size

// CacheStats is a point-in-time diagnostic snapshot. Counters last for this
// Service's lifetime; disk sizes include the checksum envelope.
type CacheStats struct {
	MemoryHits      uint64 `json:"memoryHits"`
	DiskHits        uint64 `json:"diskHits"`
	Renders         uint64 `json:"renders"`
	Coalesced       uint64 `json:"coalesced"`
	MemoryEvictions uint64 `json:"memoryEvictions"`
	DiskEvictions   uint64 `json:"diskEvictions"`
	DiskErrors      uint64 `json:"diskErrors"`
	MemoryBytes     int64  `json:"memoryBytes"`
	DiskBytes       int64  `json:"diskBytes"`
	MemoryEntries   int    `json:"memoryEntries"`
	DiskEntries     int    `json:"diskEntries"`
	InFlight        int    `json:"inFlight"`
}

type diskPoster struct {
	key     string
	size    int64
	touched time.Time
}
type posterFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	data    []byte
	err     error
}

type posterCache struct {
	mu                                         sync.Mutex
	memory                                     *lru.Cache[string, []byte]
	memoryLimit                                int64
	flights                                    map[string]*posterFlight
	slots                                      chan struct{}
	diskMu                                     sync.Mutex
	dir                                        string
	disk                                       map[string]*list.Element
	diskLRU                                    list.List
	diskBytes, diskLimit                       int64
	memoryHits, diskHits, renders, coalesced   atomic.Uint64
	memoryEvictions, diskEvictions, diskErrors atomic.Uint64
}

func newPosterCache(o Options) *posterCache {
	if o.MemoryCacheBytes == 0 {
		o.MemoryCacheBytes = 16 << 20
	}
	if o.DiskCacheBytes == 0 {
		o.DiskCacheBytes = 256 << 20
	}
	if o.RenderConcurrency <= 0 {
		o.RenderConcurrency = 2
	}
	c := &posterCache{flights: make(map[string]*posterFlight), memoryLimit: o.MemoryCacheBytes, diskLimit: o.DiskCacheBytes, slots: make(chan struct{}, min(o.RenderConcurrency, 16)), disk: make(map[string]*list.Element)}
	c.memory = lru.New(lru.Options[string, []byte]{
		MaxCost: c.memoryLimit, // putMemory admits nothing when the limit is not positive
		Cost:    func(_ string, data []byte) int64 { return int64(len(data)) },
		OnEvict: func(string, []byte) { c.memoryEvictions.Add(1) },
	})
	if o.CacheDir != "" && c.diskLimit > 0 {
		c.dir = o.CacheDir
		if err := os.MkdirAll(c.dir, 0700); err != nil {
			c.diskErrors.Add(1)
			c.dir = ""
			return c
		}
		root, err := c.openRoot()
		if err != nil {
			c.diskErrors.Add(1)
			c.dir = ""
			return c
		}
		defer root.Close()
		dir, err := root.Open(".")
		if err != nil {
			c.diskErrors.Add(1)
			c.dir = ""
			return c
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			c.diskErrors.Add(1)
			c.dir = ""
			return c
		}
		var existing []diskPoster
		for _, entry := range entries {
			if validTemporaryName(entry.Name()) {
				info, err := entry.Info()
				if err == nil && info.Mode().IsRegular() {
					if err := root.Remove(entry.Name()); err != nil {
						c.diskErrors.Add(1)
						c.diskLimit = 0
					}
				}
				continue
			}
			if !validCacheName(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			existing = append(existing, diskPoster{key: strings.TrimSuffix(entry.Name(), ".jpg"), size: info.Size(), touched: info.ModTime()})
		}
		sort.Slice(existing, func(i, j int) bool { return existing[i].touched.Before(existing[j].touched) })
		for _, e := range existing {
			c.disk[e.key] = c.diskLRU.PushFront(e)
			c.diskBytes += e.size
		}
		c.pruneDisk(root, 0)
	}
	return c
}

// Directory anchoring and file identity checks keep cache operations inside the
// dedicated directory even when unexpected symlinks appear there.
func (c *posterCache) openRoot() (*os.Root, error) {
	before, err := os.Lstat(c.dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("overlay cache directory is not a real directory")
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, errors.New("overlay cache directory changed")
	}
	return root, nil
}

func validCacheName(name string) bool {
	if len(name) != 64+4 || !strings.HasSuffix(name, ".jpg") {
		return false
	}
	_, err := hex.DecodeString(name[:64])
	return err == nil && strings.ToLower(name) == name
}

// Only our exclusive atomic-write filenames are eligible for crash cleanup.
func validTemporaryName(name string) bool {
	if len(name) != len(".overlay-")+24+len(".tmp") || !strings.HasPrefix(name, ".overlay-") || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	_, err := hex.DecodeString(name[len(".overlay-") : len(name)-len(".tmp")])
	return err == nil && strings.ToLower(name) == name
}

func (s *Service) CacheStats() CacheStats { return s.generated.stats() }
func (c *posterCache) stats() CacheStats {
	v := CacheStats{MemoryHits: c.memoryHits.Load(), DiskHits: c.diskHits.Load(), Renders: c.renders.Load(), Coalesced: c.coalesced.Load(), MemoryEvictions: c.memoryEvictions.Load(), DiskEvictions: c.diskEvictions.Load(), DiskErrors: c.diskErrors.Load()}
	c.mu.Lock()
	v.MemoryBytes, v.MemoryEntries, v.InFlight = c.memory.Cost(), c.memory.Len(), len(c.flights)
	c.mu.Unlock()
	c.diskMu.Lock()
	v.DiskBytes, v.DiskEntries = c.diskBytes, len(c.disk)
	c.diskMu.Unlock()
	return v
}

// get coalesces both disk reads and rendering. A canceled caller stops waiting
// immediately; shared work is canceled only after its last interested caller
// leaves. No workers or timers survive an idle cache.
func (c *posterCache) get(ctx context.Context, key string, render func(context.Context) ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if data, ok := c.memory.Get(key); ok {
		c.mu.Unlock()
		c.memoryHits.Add(1)
		return data, nil
	}
	flight := c.flights[key]
	if flight == nil {
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		flight = &posterFlight{done: make(chan struct{}), cancel: cancel}
		c.flights[key] = flight
		go c.run(workCtx, key, flight, render)
	} else {
		c.coalesced.Add(1)
	}
	flight.waiters++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		c.mu.Lock()
		flight.waiters--
		if flight.waiters == 0 {
			flight.cancel()
			if c.flights[key] == flight {
				delete(c.flights, key)
			}
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return flight.data, flight.err
	}
}

func (c *posterCache) run(ctx context.Context, key string, f *posterFlight, render func(context.Context) ([]byte, error)) {
	defer f.cancel()
	data := c.readDisk(key)
	var err error
	if data == nil {
		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err == nil {
			if err = ctx.Err(); err == nil {
				c.renders.Add(1)
				data, err = render(ctx)
			}
			<-c.slots
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			c.writeDisk(key, data)
		}
	}
	c.mu.Lock()
	if ctx.Err() != nil {
		data = nil
		err = ctx.Err()
	}
	if err == nil {
		c.putMemory(key, data)
	}
	f.data, f.err = data, err
	if c.flights[key] == f {
		delete(c.flights, key)
	}
	close(f.done)
	c.mu.Unlock()
}

// putMemory holds mu and evicts only the least recently used entries needed to
// admit this item. Oversize results are served without disturbing hot entries.
func (c *posterCache) putMemory(key string, data []byte) {
	if size := int64(len(data)); size > c.memoryLimit || size == 0 {
		return
	}
	c.memory.Add(key, data)
}

func (c *posterCache) forgetDisk(key string) {
	if e := c.disk[key]; e != nil {
		c.diskBytes -= e.Value.(diskPoster).size
		c.diskLRU.Remove(e)
		delete(c.disk, key)
	}
}
func (c *posterCache) pruneDisk(root *os.Root, incoming int64) {
	for c.diskBytes+incoming > c.diskLimit && c.diskLRU.Len() > 0 {
		e := c.diskLRU.Back()
		old := e.Value.(diskPoster)
		if err := root.Remove(old.key + ".jpg"); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.diskErrors.Add(1)
			return
		}
		c.forgetDisk(old.key)
		c.diskEvictions.Add(1)
	}
}

func (c *posterCache) readDisk(key string) []byte {
	if c.dir == "" || !validCacheName(key+".jpg") {
		return nil
	}
	c.diskMu.Lock()
	defer c.diskMu.Unlock()
	entry := c.disk[key]
	if entry == nil {
		return nil
	}
	root, err := c.openRoot()
	if err != nil {
		c.diskErrors.Add(1)
		return nil
	}
	defer root.Close()
	name := key + ".jpg"
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxCachedPoster || before.Size() < int64(cacheHeaderSize) {
		c.diskErrors.Add(1)
		c.forgetDisk(key)
		// Do not touch a symlink or other unexpected file type.
		if err == nil && before.Mode().IsRegular() {
			_ = root.Remove(name)
		}
		return nil
	}
	f, err := root.Open(name)
	if err != nil {
		c.diskErrors.Add(1)
		return nil
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		c.diskErrors.Add(1)
		return nil
	}
	raw := make([]byte, int(before.Size()))
	_, err = io.ReadFull(f, raw)
	var extra [1]byte
	if n, readErr := f.Read(extra[:]); n != 0 || readErr != io.EOF {
		err = errors.New("overlay cache file changed during read")
	}
	data, valid := decodeCachedPoster(raw, key)
	if err != nil || !valid {
		c.diskErrors.Add(1)
		f.Close()
		_ = root.Remove(name)
		c.forgetDisk(key)
		return nil
	}
	c.diskLRU.MoveToFront(entry)
	// Runtime LRU is exact; restart ordering falls back to file write times.
	c.diskHits.Add(1)
	return data
}

func decodeCachedPoster(raw []byte, key string) ([]byte, bool) {
	offset := cacheHeaderSize
	if len(raw) < offset || len(raw) > maxCachedPoster || string(raw[:len(cacheEnvelope)]) != cacheEnvelope {
		return nil, false
	}
	storedKey := raw[len(cacheEnvelope) : len(cacheEnvelope)+sha256.Size]
	expectedKey, err := hex.DecodeString(key)
	if err != nil || !bytes.Equal(storedKey, expectedKey) {
		return nil, false
	}
	data := raw[offset:]
	if len(data) < 2 || data[len(data)-2] != 0xff || data[len(data)-1] != 0xd9 {
		return nil, false
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(raw[len(cacheEnvelope)+sha256.Size:offset], digest[:]) {
		return nil, false
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	return data, err == nil && cfg.Width == 500 && cfg.Height > 0 && cfg.Height <= 1500
}

func (c *posterCache) writeDisk(key string, data []byte) {
	size := int64(cacheHeaderSize + len(data))
	if c.dir == "" || !validCacheName(key+".jpg") || size > c.diskLimit || size > maxCachedPoster || len(data) == 0 {
		return
	}
	c.diskMu.Lock()
	defer c.diskMu.Unlock()
	root, err := c.openRoot()
	if err != nil {
		c.diskErrors.Add(1)
		return
	}
	defer root.Close()
	// All disk filenames are generated hashes; refuse replacement of symlinks,
	// directories, and other unexpected entries at a target name.
	name := key + ".jpg"
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		c.diskErrors.Add(1)
		return
	}
	if c.disk[key] != nil {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.diskErrors.Add(1)
			return
		}
		c.forgetDisk(key)
	}
	c.pruneDisk(root, size)
	if c.diskBytes+size > c.diskLimit {
		return
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		c.diskErrors.Add(1)
		return
	}
	temporary := ".overlay-" + hex.EncodeToString(random[:]) + ".tmp"
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		c.diskErrors.Add(1)
		return
	}
	defer root.Remove(temporary)
	digest := sha256.Sum256(data)
	keyDigest, _ := hex.DecodeString(key)
	_, err = io.Copy(f, io.MultiReader(strings.NewReader(cacheEnvelope), bytes.NewReader(keyDigest), bytes.NewReader(digest[:]), bytes.NewReader(data)))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(temporary, name)
	}
	if err != nil {
		c.diskErrors.Add(1)
		return
	}
	c.disk[key] = c.diskLRU.PushFront(diskPoster{key: key, size: size, touched: time.Now()})
	c.diskBytes += size
}
