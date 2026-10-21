// Package cachefill maintains an opt-in, bounded local shelf of complete media
// sources. It fills sequentially at background priority and never writes watch
// history. The manifest and segment pins are node-local.
package cachefill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	CachedTitle(context.Context, core.RatingKey) (*core.Title, error)
	Children(context.Context, core.RatingKey) ([]core.Title, error)
}
type Engine interface {
	core.Engine
	DurablePrefetch(context.Context, string, []byte, int64, int64) error
}

type Deps struct {
	Shared *SharedStore // nil for a node-local shelf
	// Household returns active members of the configured household scope.
	Household func(context.Context, string) ([]core.User, error)
	State     WatchReader
	Now       func() time.Time
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
	Reason      string         `json:"reason,omitempty"`
	Score       float64        `json:"score,omitempty"`
	StoredBytes int64          `json:"storedBytes"`
	Updated     time.Time      `json:"updated"`
}
type Status struct {
	State     string      `json:"state"`
	Message   string      `json:"message"`
	Title     string      `json:"title"`
	Fetched   int64       `json:"fetched"`
	Size      int64       `json:"size"`
	Allocated int64       `json:"allocated"`
	Ready     int         `json:"ready"`
	Failed    int         `json:"failed"`
	Paused    bool        `json:"paused"`
	Selection []Selection `json:"selection"`
	Viewers   int         `json:"viewers"`
	Items     []Item      `json:"items"`
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
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	m := &Manager{deps: d, path: filepath.Join(dir, "preload.json"), wake: make(chan struct{}, 1), items: map[core.RatingKey]Item{}, status: Status{State: "idle"}}
	var b []byte
	var err error
	if d.Shared != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		b, m.status, err = d.Shared.load(ctx)
		cancel()
	} else {
		b, err = os.ReadFile(m.path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err = m.loadDocument(b); err != nil {
			if d.Shared != nil {
				return nil, err
			}
			d.Logger.Warn("ignoring invalid preload manifest", "err", err)
			m.status.Paused = true
		}
	}

	return m, nil
}
func (m *Manager) Status() Status {
	if m.deps.Shared != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b, st, err := m.deps.Shared.load(ctx)
		if err != nil {
			return Status{State: "waiting", Message: "Shared preload database is unavailable"}
		}
		var doc struct {
			Items []Item `json:"items"`
		}
		if json.Unmarshal(b, &doc) == nil {
			st.Items = doc.Items
		}
		st.Ready, st.Allocated = 0, 0
		sizes := m.releaseSizes()
		for i := range st.Items {
			it := &st.Items[i]
			n := sizes(it.ReleaseID)
			it.Complete = it.Complete && it.StoredBytes > 0 && n >= it.StoredBytes
			st.Allocated += max(it.Size, n)
			if it.Complete {
				st.Ready++
			}
		}
		return st
	}
	return m.localStatus()
}
func (m *Manager) localStatus() Status {
	sizes := m.releaseSizes()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status
	s.Selection = append([]Selection(nil), s.Selection...)
	s.Items = make([]Item, 0, len(m.items))
	s.Ready = 0
	s.Allocated = 0
	for _, it := range m.items {
		it.Complete = it.Complete && it.StoredBytes > 0 && sizes(it.ReleaseID) >= it.StoredBytes
		s.Items = append(s.Items, it)
		s.Allocated += max(it.Size, sizes(it.ReleaseID))
		if it.Complete {
			s.Ready++
		}
	}
	sort.Slice(s.Items, func(i, j int) bool { return s.Items[i].Title < s.Items[j].Title })
	return s
}

// Wake cancels an old-policy job before scheduling a pass with current settings.
func (m *Manager) Wake() {
	if m.deps.Shared != nil {
		m.deps.Shared.wake()
	}
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
	if m.deps.Shared != nil {
		err := m.deps.Shared.pause(v)
		m.Wake()
		return err
	}
	m.mu.Lock()
	m.status.Paused = v
	m.mu.Unlock()
	m.Wake()
	return m.save()
}
func (m *Manager) state(state, message string) {
	m.mu.Lock()
	m.status.State, m.status.Message = state, message
	st := m.status
	m.mu.Unlock()
	if m.deps.Shared != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.deps.Shared.save(ctx, nil, st); err != nil {
			m.deps.Logger.Debug("shared preload status", "err", err)
		}
	}
}
func (m *Manager) Run(ctx context.Context) {
	every := 15 * time.Minute
	if m.deps.Shared != nil {
		every = 30 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		run, cancel := context.WithCancel(ctx)
		m.mu.Lock()
		m.cancel = cancel
		m.mu.Unlock()
		err := m.runPass(run)
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
	if m.deps.Shared != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, st, err := m.deps.Shared.load(ctx)
		cancel()
		if err != nil {
			return err
		}
		paused = st.Paused
	}
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
	if m.deps.Shared != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return m.deps.Shared.save(ctx, b, m.localStatus())
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
	if err := m.pin(it.ReleaseID, false); err != nil {
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
	snapshot := m.localStatus()
	sort.Slice(snapshot.Items, func(i, j int) bool { return snapshot.Items[i].Updated.Before(snapshot.Items[j].Updated) })
	allocated, count := snapshot.Allocated, len(snapshot.Items)
	for _, it := range snapshot.Items {
		if !enabled(cfg) || !contentAllowed(cfg, it.Key) || allocated > limit || count > maxTitles || m.deps.Cache.ReleaseBytes(it.ReleaseID) == 0 {
			allocated -= max(it.Size, m.deps.Cache.ReleaseBytes(it.ReleaseID))
			count--
			if err := m.forget(it.Key); err != nil {
				return err
			}
		}
	}
	// Clear orphan pins left between pinning and a manifest write/crash.
	wanted := map[string]bool{}
	for _, it := range m.localStatus().Items {
		wanted[it.ReleaseID] = true
		if m.deps.Shared != nil {
			if err := m.pin(it.ReleaseID, true); err != nil {
				return err
			}
		}
	}
	for id, reasons := range m.deps.Cache.Pins() {
		for _, reason := range reasons {
			if reason == pinReason && !wanted[id] {
				if err := m.pin(id, false); err != nil {
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
	m.state("selecting", "Blending catalogue demand with household viewing")
	plan, err := m.selectCandidates(ctx, cfg, maxTitles)
	if err != nil {
		return err
	}
	candidates := plan.Candidates
	wantedKeys := map[core.RatingKey]bool{}
	for _, c := range candidates {
		wantedKeys[c.Title.Key] = true
	}
	// Slide each successfully planned show's shelf forward. Old episodes
	// return to ordinary retention; transient metadata failures preserve pins.
	for _, it := range m.localStatus().Items {
		if it.Key.Kind() == core.KindEpisode && plan.Shows[rootKey(it.Key)] && !wantedKeys[it.Key] {
			if err := m.forget(it.Key); err != nil {
				return err
			}
		}
	}
	ranks := map[core.RatingKey]int{}
	for i, title := range candidates {
		ranks[title.Title.Key] = i
	}
	for _, it := range m.localStatus().Items {
		if plan.UnavailableShows[rootKey(it.Key)] {
			ranks[it.Key] = -1
		}
	}
	attempts := 0
	for _, candidate := range candidates {
		title := candidate.Title
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
		if err == nil {
			m.mu.Lock()
			it := m.items[title.Key]
			it.Reason = candidate.Reason
			it.Score = candidate.Score
			m.items[title.Key] = it
			m.mu.Unlock()
			err = m.save()
		}
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
	m.status.Title = displayTitle(*title)
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
	if err := m.pin(res.Release.ID, true); err != nil {
		return err
	}
	it := Item{Key: title.Key, Title: displayTitle(*title), ReleaseID: res.Release.ID, Size: reservation, Updated: time.Now()}
	m.mu.Lock()
	m.items[title.Key] = it
	m.status.Size = size
	m.mu.Unlock()
	if err := m.save(); err != nil {
		_ = m.pin(res.Release.ID, false)
		return err
	}
	m.deps.Cache.Describe(res.Release.ID, displayTitle(*title), size)
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
	s := m.localStatus()
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

func (m *Manager) loadDocument(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	var doc struct {
		Items  []Item `json:"items"`
		Paused bool   `json:"paused"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("invalid preload manifest: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = map[core.RatingKey]Item{}
	for _, it := range doc.Items {
		if it.ReleaseID != "" && it.Size > 0 {
			m.items[it.Key] = it
		}
	}
	if m.deps.Shared == nil {
		m.status.Paused = doc.Paused
	}
	return nil
}

func (m *Manager) releaseSizes() func(string) int64 {
	if c, ok := m.deps.Cache.(core.ReleaseSizeSource); ok {
		sizes := c.ReleaseSizes()
		return func(id string) int64 { return sizes[id] }
	}
	return m.deps.Cache.ReleaseBytes
}
