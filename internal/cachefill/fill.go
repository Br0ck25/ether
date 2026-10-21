// Package cachefill maintains an opt-in, bounded local shelf of complete media
// sources. It fills sequentially at background priority and never writes watch
// history. The manifest and segment pins are node-local.
package cachefill

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

const pinReason = "preloaded"
const chunkBytes int64 = 16 << 20

type Cache interface {
	core.SegmentCache
	ReleaseBytes(string) int64
	Pins() map[string][]string
	Describe(string, string, int64)
}
type Catalog interface {
	Browse(context.Context, core.BrowseQuery) (core.Page, error)
}
type Engine interface {
	core.Engine
	DurablePrefetch(context.Context, string, []byte, int64, int64) error
}

type Deps struct {
	Catalog   Catalog
	Resolver  core.Resolver
	Engine    Engine
	Cache     Cache
	Owner     func(context.Context) (*core.User, error)
	Config    func() config.Cache
	Active    func() bool
	FreeBytes func() (int64, error)
	Remember  func(context.Context, *core.User, core.RatingKey, core.ClientCaps, *core.Resolution) error
	Logger    *slog.Logger
}
type Item struct {
	Key         core.RatingKey `json:"key"`
	Title       string         `json:"title"`
	ReleaseID   string         `json:"releaseId"`
	Size        int64          `json:"size"`
	Complete    bool           `json:"complete"`
	StoredBytes int64          `json:"storedBytes"`
	Updated     time.Time      `json:"updated"`
}
type Status struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	Title     string `json:"title"`
	Fetched   int64  `json:"fetched"`
	Size      int64  `json:"size"`
	Allocated int64  `json:"allocated"`
	Ready     int    `json:"ready"`
	Failed    int    `json:"failed"`
	Paused    bool   `json:"paused"`
	Items     []Item `json:"items"`
}
type Manager struct {
	deps   Deps
	path   string
	mu     sync.Mutex
	saveMu sync.Mutex
	status Status
	items  map[core.RatingKey]Item
	cancel context.CancelFunc
	wake   chan struct{}
}

func New(dir string, d Deps) (*Manager, error) {
	if d.Catalog == nil || d.Resolver == nil || d.Engine == nil || d.Cache == nil || d.Config == nil || d.Owner == nil || d.FreeBytes == nil {
		return nil, errors.New("cachefill: missing dependency")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	m := &Manager{deps: d, path: filepath.Join(dir, "preload.json"), wake: make(chan struct{}, 1), items: map[core.RatingKey]Item{}, status: Status{State: "idle"}}
	b, err := os.ReadFile(m.path)
	if err == nil {
		var doc struct {
			Items  []Item `json:"items"`
			Paused bool   `json:"paused"`
		}
		if err = json.Unmarshal(b, &doc); err != nil {
			d.Logger.Warn("ignoring invalid preload manifest", "err", err)
			doc.Items = nil
			doc.Paused = true
		}
		m.status.Paused = doc.Paused
		for _, it := range doc.Items {
			if it.ReleaseID != "" && it.Size > 0 {
				m.items[it.Key] = it
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return m, nil
}
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status
	s.Items = make([]Item, 0, len(m.items))
	s.Ready = 0
	s.Allocated = 0
	for _, it := range m.items {
		it.Complete = it.Complete && it.StoredBytes > 0 && m.deps.Cache.ReleaseBytes(it.ReleaseID) >= it.StoredBytes
		s.Items = append(s.Items, it)
		s.Allocated += max(it.Size, m.deps.Cache.ReleaseBytes(it.ReleaseID))
		if it.Complete {
			s.Ready++
		}
	}
	sort.Slice(s.Items, func(i, j int) bool { return s.Items[i].Title < s.Items[j].Title })
	return s
}

// Wake cancels an old-policy job before scheduling a pass with current settings.
func (m *Manager) Wake() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
func (m *Manager) Pause(v bool) error {
	m.mu.Lock()
	m.status.Paused = v
	m.mu.Unlock()
	m.Wake()
	return m.save()
}
func (m *Manager) state(state, message string) {
	m.mu.Lock()
	m.status.State, m.status.Message = state, message
	m.mu.Unlock()
}
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		run, cancel := context.WithCancel(ctx)
		m.mu.Lock()
		m.cancel = cancel
		m.mu.Unlock()
		err := m.pass(run)
		cancel()
		m.mu.Lock()
		m.cancel = nil
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			m.state("waiting", err.Error())
			m.deps.Logger.Warn("cache preload pass", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
	}
}
func enabled(c config.Cache) bool {
	return c.PreloadEnabled && c.Mode != "off" && c.PreloadBudgetBytes > 0
}
func (m *Manager) allowed() error {
	c := m.deps.Config()
	m.mu.Lock()
	paused := m.status.Paused
	m.mu.Unlock()
	if paused {
		return errors.New("Preloading is paused")
	}
	if !enabled(c) {
		return errors.New("Managed preloading is disabled")
	}
	if !c.PreloadDuringPlayback && m.deps.Active != nil && m.deps.Active() {
		return errors.New("Waiting for live playback to finish")
	}
	free, err := m.deps.FreeBytes()
	if err != nil {
		return errors.New("Could not measure free disk space")
	}
	reserve := c.PreloadMinFreeBytes
	if reserve <= 0 {
		reserve = 5 << 30
	}
	if free < reserve+chunkBytes*2 {
		return errors.New("Waiting for free space above the disk reserve")
	}
	return nil
}
func (m *Manager) save() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	items := make([]Item, 0, len(m.items))
	for _, it := range m.items {
		items = append(items, it)
	}
	paused := m.status.Paused
	m.mu.Unlock()
	b, err := json.Marshal(struct {
		Items  []Item `json:"items"`
		Paused bool   `json:"paused"`
	}{items, paused})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), "preload-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, m.path)
}
func (m *Manager) forget(key core.RatingKey) error {
	m.mu.Lock()
	it := m.items[key]
	if it.ReleaseID == "" {
		m.mu.Unlock()
		return nil
	}
	delete(m.items, key)
	m.mu.Unlock()
	if err := m.deps.Cache.Unpin(it.ReleaseID, pinReason); err != nil {
		return err
	}
	return m.save()
}
func (m *Manager) pass(ctx context.Context) error {
	cfg := m.deps.Config()
	limit := cfg.PreloadBudgetBytes
	if cfg.BudgetBytes > 0 {
		limit = min(limit, cfg.BudgetBytes)
	}
	maxTitles := cfg.PreloadMaxTitles
	if maxTitles <= 0 {
		maxTitles = 200
	}
	// Retire excess reservations first, even while disabled/paused. Cached bytes
	// become ordinary retention candidates, never a forced deletion.
	snapshot := m.Status()
	sort.Slice(snapshot.Items, func(i, j int) bool { return snapshot.Items[i].Updated.Before(snapshot.Items[j].Updated) })
	allocated, count := snapshot.Allocated, len(snapshot.Items)
	for _, it := range snapshot.Items {
		if !enabled(cfg) || allocated > limit || count > maxTitles || m.deps.Cache.ReleaseBytes(it.ReleaseID) == 0 {
			allocated -= max(it.Size, m.deps.Cache.ReleaseBytes(it.ReleaseID))
			count--
			if err := m.forget(it.Key); err != nil {
				return err
			}
		}
	}
	// Clear orphan pins left between pinning and a manifest write/crash.
	wanted := map[string]bool{}
	for _, it := range m.Status().Items {
		wanted[it.ReleaseID] = true
	}
	for id, reasons := range m.deps.Cache.Pins() {
		for _, reason := range reasons {
			if reason == pinReason && !wanted[id] {
				if err := m.deps.Cache.Unpin(id, pinReason); err != nil {
					return err
				}
			}
		}
	}
	if !enabled(cfg) {
		m.state("disabled", "Enable a preload allocation to build a local shelf")
		return nil
	}
	if err := m.allowed(); err != nil {
		return err
	}
	owner, err := m.deps.Owner(ctx)
	if err != nil || owner == nil {
		return errors.New("Waiting for a signed-in server owner")
	}
	order := core.SortPopularity
	if cfg.PreloadSource == "recent" {
		order = core.SortReleased
	}
	// Browse only the local, released catalogue. Never crawl all of TMDB.
	m.state("selecting", "Choosing from the local catalogue")
	var candidates []core.Title
	for offset := 0; offset < maxTitles*3; offset += 50 {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := m.deps.Catalog.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie}, Sort: order, Desc: true, Offset: offset, Limit: min(50, maxTitles*3-offset)})
		if err != nil {
			return errors.New("Could not browse the local catalogue")
		}
		for _, title := range page.Items {
			if title.Type == core.TypeMovie && !title.Released.IsZero() && !title.Released.After(time.Now()) {
				candidates = append(candidates, title)
			}
		}
		if len(page.Items) == 0 || offset+len(page.Items) >= page.Total {
			break
		}
	}
	ranks := map[core.RatingKey]int{}
	for i, title := range candidates {
		ranks[title.Key] = i
	}
	attempts := 0
	for _, title := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		existing, ok := m.items[title.Key]
		m.mu.Unlock()
		if ok && existing.Complete && existing.StoredBytes > 0 && m.deps.Cache.ReleaseBytes(existing.ReleaseID) >= existing.StoredBytes {
			continue
		}
		if err := m.allowed(); err != nil {
			return err
		}
		attempts++
		if attempts > maxTitles {
			break
		}
		job, cancel := context.WithTimeout(ctx, 6*time.Hour)
		err = m.fill(job, owner, &title, limit, maxTitles, ranks)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			m.mu.Lock()
			m.status.Failed++
			m.mu.Unlock()
			m.deps.Logger.Debug("preload skipped", "key", title.Key, "err", err)
			if dropErr := m.forget(title.Key); dropErr != nil {
				return dropErr
			}
		}
	}
	m.state("ready", "Catalogue pass finished; checking again in 15 minutes")
	return nil
}
func (m *Manager) fill(ctx context.Context, owner *core.User, title *core.Title, limit int64, maxTitles int, ranks map[core.RatingKey]int) error {
	m.mu.Lock()
	previous := m.items[title.Key]
	m.status.Title = title.Title
	m.status.Fetched = 0
	m.status.Size = 0
	m.mu.Unlock()
	m.state("resolving", "Selecting a release using the owner's quality rules")
	resolveCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	caps := core.ClientCaps{ClientID: "ether-managed-cache", Product: "Ether Managed Cache"}
	res, err := m.deps.Resolver.Resolve(resolveCtx, core.ResolveRequest{Title: title, User: owner, Client: caps, Prefer: previous.ReleaseID})
	cancel()
	if err != nil {
		return err
	}
	if res == nil {
		return errors.New("no release")
	}
	// Open determines playable size (release size can include archives/extras).
	src, err := m.deps.Engine.Open(ctx, res.Release.ID, res.NZB)
	if err != nil {
		return err
	}
	size := src.Size()
	defer src.Close()
	if size <= 0 || size > limit {
		return errors.New("source size unknown or larger than the preload allocation")
	}
	if previous.ReleaseID != "" && previous.ReleaseID != res.Release.ID {
		if err := m.forget(title.Key); err != nil {
			return err
		}
		previous = Item{}
	}
	// Reserve source overhead and make room only after a real source opens.
	reservation := size + max(size/20, 32<<20)
	if reservation < 0 || reservation > limit {
		return errors.New("source does not fit preload allocation")
	}
	if err := m.makeRoom(title.Key, reservation, limit, maxTitles, ranks); err != nil {
		return err
	}
	if err := m.deps.Cache.Pin(res.Release.ID, pinReason); err != nil {
		return err
	}
	it := Item{Key: title.Key, Title: title.Title, ReleaseID: res.Release.ID, Size: reservation, Updated: time.Now()}
	m.mu.Lock()
	m.items[title.Key] = it
	m.status.Size = size
	m.mu.Unlock()
	if err := m.save(); err != nil {
		_ = m.deps.Cache.Unpin(res.Release.ID, pinReason)
		return err
	}
	m.deps.Cache.Describe(res.Release.ID, title.Title, size)
	m.state("filling", "Fetching complete source at background priority")
	for off := int64(0); off < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.allowed(); err != nil {
			return err
		}
		n := min(chunkBytes, size-off)
		started := time.Now()
		if err := m.deps.Engine.DurablePrefetch(ctx, res.Release.ID, res.NZB, off, n); err != nil {
			return err
		}
		off += n
		m.mu.Lock()
		m.status.Fetched = off
		m.mu.Unlock()
		if m.deps.Cache.ReleaseBytes(res.Release.ID) > reservation {
			return errors.New("source overhead exceeded its reservation")
		}
		if rate := m.deps.Config().PreloadRateMiB; rate > 0 {
			delay := time.Duration(float64(n)/float64(int64(rate)<<20)*float64(time.Second)) - time.Since(started)
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	// Prefetch failure is not a complete item. Successful coverage is protected
	// by its preload pin; changing profiles may still select a different release.
	it.Complete = true
	it.StoredBytes = m.deps.Cache.ReleaseBytes(it.ReleaseID)
	it.Updated = time.Now()
	m.mu.Lock()
	m.items[title.Key] = it
	m.mu.Unlock()
	if err := m.save(); err != nil {
		return err
	}
	if m.deps.Remember != nil {
		if err := m.deps.Remember(ctx, owner, title.Key, caps, res); err != nil {
			m.deps.Logger.Warn("remember preloaded source", "err", err)
		}
	}
	return nil
}

// makeRoom rotates out lower-ranked shelf entries, never better ones. Removing
// a preload reservation leaves its bytes available to ordinary cache retention.
func (m *Manager) makeRoom(key core.RatingKey, reservation, limit int64, maxTitles int, ranks map[core.RatingKey]int) error {
	s := m.Status()
	allocated, count := s.Allocated, len(s.Items)
	rank := func(k core.RatingKey) int {
		if n, ok := ranks[k]; ok {
			return n
		}
		return len(ranks) + 1
	}
	var victims []Item
	for _, it := range s.Items {
		if it.Key == key {
			allocated -= max(it.Size, m.deps.Cache.ReleaseBytes(it.ReleaseID))
			count--
			continue
		}
		if rank(it.Key) > rank(key) {
			victims = append(victims, it)
		}
	}
	sort.Slice(victims, func(i, j int) bool { return rank(victims[i].Key) > rank(victims[j].Key) })
	need := 0
	for reservation > limit-allocated || count >= maxTitles {
		if need >= len(victims) {
			return errors.New("higher-ranked sources already fill the allocation")
		}
		it := victims[need]
		allocated -= max(it.Size, m.deps.Cache.ReleaseBytes(it.ReleaseID))
		count--
		need++
	}
	for _, it := range victims[:need] {
		if err := m.forget(it.Key); err != nil {
			return err
		}
	}
	return nil
}
