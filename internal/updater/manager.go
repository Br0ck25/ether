package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

// Manager states reported by Status.
const (
	StateIdle          = "idle"
	StateDownloading   = "downloading"
	StateStaged        = "staged"
	StateWaitingWindow = "waiting-for-window"
	StateWaitingIdle   = "waiting-for-idle"
	StateApplying      = "applying"
	StateRolledBack    = "rolled-back"
)

// Capabilities reported by Status.
const (
	CapabilitySelfApply  = "self-apply"
	CapabilityNotifyOnly = "notify-only"
	CapabilityDisabled   = "disabled"
)

// Manager defaults.
const (
	DefaultPollInterval    = 24 * time.Hour
	DefaultFirstCheckDelay = 5 * time.Minute
	DefaultRetryInterval   = time.Hour
	DefaultQuietPeriod     = 10 * time.Minute
	DefaultHealthPeriod    = 10 * time.Minute
	DefaultTick            = 30 * time.Second
	DefaultWindowStart     = "03:00"
	DefaultWindowEnd       = "06:00"
)

var (
	// ErrUpdatesDisabled: this build cannot verify updates (no release key)
	// or the feed settings are unusable.
	ErrUpdatesDisabled = errors.New("updater: updates are disabled in this build")
	// ErrNotSelfApplicable: the install is notify-only (package manager,
	// container, macOS app bundle, read-only install dir, ...).
	ErrNotSelfApplicable = errors.New("updater: this installation cannot update itself")
)

// Checker is the slice of *Client the Manager needs.
type Checker interface {
	Check(ctx context.Context) (*Update, error)
	Download(ctx context.Context, upd *Update, dir string, progress ProgressFunc) (string, error)
}

// ManagerOptions configures a Manager. Only Dir and Config are required.
type ManagerOptions struct {
	// NewClient builds the feed client for the current settings; it is
	// rebuilt when channel/feed change. Nil or failing at construction:
	// update checks are disabled (startup confirmation still runs).
	NewClient func(config.Updates) (Checker, error)
	// Config returns the live Updates settings.
	Config func() config.Updates
	// CurrentVersion is the running version (default: Version).
	CurrentVersion string
	// Dir is <dataDir>/updates: downloads and marker files.
	Dir string
	// Target is the running server executable that a binary update
	// replaces (default: os.Executable()).
	Target string
	// SelfApply reports whether this install may replace its own binary;
	// NotifyReason explains why not. See SelfApplyCapability.
	SelfApply    bool
	NotifyReason string
	// ActiveSessions returns how many playback / transcode / live TV
	// sessions are in progress. Nil: always idle.
	ActiveSessions func() int
	// RequestRestart asks the app to shut down gracefully and exit so the
	// service manager starts the new binary. Called at most once.
	RequestRestart func(reason string)
	// Boot is the result of ConfirmStartup for this process.
	Boot StartupResult

	Now             func() time.Time
	Location        *time.Location // maintenance window zone (default time.Local)
	PollInterval    time.Duration
	FirstCheckDelay time.Duration
	RetryInterval   time.Duration
	QuietPeriod     time.Duration
	HealthPeriod    time.Duration
	Tick            time.Duration
	// ApplyBinary / CleanupOld default to the package functions.
	ApplyBinary func(newPath, target string) error
	CleanupOld  func(target string) error
	Logger      *slog.Logger
}

// Manager polls the signed feed and, when allowed, downloads a release
// early and installs it at a safe moment: inside the maintenance window
// (unless the owner pressed "Update now") and after the server has been
// idle for the quiet period (unless forced).
type Manager struct {
	opts ManagerOptions
	log  *slog.Logger
	kick chan struct{}

	mu             sync.Mutex
	client         Checker
	clientKey      string
	disabledReason string
	noKey          bool // build has no release key: never check
	state          string
	available      *Update
	skipped        string // available version ignored because it was rolled back
	failed         *FailedUpdate
	staged         string // verified download for available
	stagedVersion  string
	requested      bool
	force          bool
	lastCheck      time.Time
	nextCheck      time.Time
	downloadAfter  time.Time
	applyAfter     time.Time
	lastErr        string
	lastActive     int
	idleSince      time.Time
	startedAt      time.Time
	confirmed      bool
	restarting     bool
}

// NewManager builds a Manager; it does not start polling (see Run).
func NewManager(opts ManagerOptions) *Manager {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.CurrentVersion == "" {
		opts.CurrentVersion = Version
	}
	if opts.Target == "" {
		if exe, err := os.Executable(); err == nil {
			opts.Target = exe
		}
	}
	if opts.Config == nil {
		opts.Config = func() config.Updates { return config.Updates{Channel: ChannelStable} }
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opts.PollInterval, DefaultPollInterval)
	def(&opts.FirstCheckDelay, DefaultFirstCheckDelay)
	def(&opts.RetryInterval, DefaultRetryInterval)
	def(&opts.QuietPeriod, DefaultQuietPeriod)
	def(&opts.HealthPeriod, DefaultHealthPeriod)
	def(&opts.Tick, DefaultTick)
	if opts.ApplyBinary == nil {
		opts.ApplyBinary = ApplyBinary
	}
	if opts.CleanupOld == nil {
		opts.CleanupOld = CleanupOld
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.SelfApply && opts.Target == "" {
		opts.SelfApply, opts.NotifyReason = false, "cannot locate the running executable"
	}
	m := &Manager{opts: opts, log: opts.Logger, kick: make(chan struct{}, 1), state: StateIdle}
	if opts.Boot.Confirming == nil {
		m.confirmed = true
	}
	var f FailedUpdate
	if ok, _ := readJSON(FailedPath(opts.Dir), &f); ok {
		m.failed = &f
	}
	if opts.Boot.RolledBack != nil {
		m.failed = opts.Boot.RolledBack
	}
	if opts.NewClient == nil {
		m.disabledReason = ErrUpdatesDisabled.Error()
	} else {
		cfg := opts.Config()
		if c, err := opts.NewClient(cfg); err != nil {
			m.disabledReason = err.Error()
			m.noKey = errors.Is(err, ErrNoPublicKey)
		} else {
			m.client, m.clientKey = c, clientKey(cfg)
		}
	}
	return m
}

func clientKey(u config.Updates) string { return u.Channel + "\x00" + u.FeedURL }

// Enabled reports whether update checks run.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client != nil
}

// Run drives the manager until ctx ends or a restart has been requested.
func (m *Manager) Run(ctx context.Context) {
	m.begin()
	t := time.NewTicker(m.opts.Tick)
	defer t.Stop()
	for {
		m.step(ctx)
		if m.isRestarting() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
		}
	}
}

func (m *Manager) begin() {
	now := m.opts.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startedAt = now
	m.idleSince = now
	if !m.requested { // "Update now" before Run keeps its immediate check
		m.nextCheck = now.Add(m.opts.FirstCheckDelay)
	}
}

func (m *Manager) isRestarting() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restarting
}

func (m *Manager) wake() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// step is one evaluation of the state machine at the injected clock.
func (m *Manager) step(ctx context.Context) {
	now := m.opts.Now()
	m.confirmIfHealthy(now)
	feedKey := clientKey(m.opts.Config())
	active := 0
	if m.opts.ActiveSessions != nil {
		active = m.opts.ActiveSessions()
	}
	m.mu.Lock()
	m.lastActive = active
	if active > 0 {
		m.idleSince = time.Time{}
	} else if m.idleSince.IsZero() {
		m.idleSince = now
	}
	enabled := m.opts.NewClient != nil && !m.noKey
	// A channel/feed change in settings re-checks right away.
	due := !now.Before(m.nextCheck) || (m.client != nil && feedKey != m.clientKey)
	stop := m.restarting
	m.mu.Unlock()
	if stop || !enabled {
		return
	}
	if due {
		m.check(ctx, now)
	}
	m.advance(ctx, now)
}

// confirmIfHealthy finishes a self-update once the new version has run for
// the health period: drop the marker, the rollback copy and old downloads.
func (m *Manager) confirmIfHealthy(now time.Time) {
	m.mu.Lock()
	if m.confirmed || m.startedAt.IsZero() || now.Sub(m.startedAt) < m.opts.HealthPeriod {
		m.mu.Unlock()
		return
	}
	m.confirmed = true
	p := m.opts.Boot.Confirming
	m.mu.Unlock()

	if err := removeIfExists(PendingPath(m.opts.Dir)); err != nil {
		m.log.Warn("updater: remove update marker", "err", err)
	}
	target := p.Target
	if target == "" {
		target = m.opts.Target
	}
	cleanupLeftovers(target, m.opts.CleanupOld, m.log)
	m.cleanDownloads("")
	forgetFailedIfSuperseded(m.opts.Dir, m.opts.CurrentVersion, m.log)
	m.mu.Lock()
	if m.failed != nil && !Newer(m.failed.Version, m.opts.CurrentVersion) {
		m.failed = nil
	}
	m.mu.Unlock()
	m.log.Info("updater: update confirmed healthy", "from", p.From, "version", p.To)
}

func (m *Manager) check(ctx context.Context, now time.Time) {
	cfg := m.opts.Config()
	m.mu.Lock()
	if key := clientKey(cfg); m.client == nil || key != m.clientKey {
		c, err := m.opts.NewClient(cfg)
		if err != nil {
			m.lastErr = "update feed settings: " + err.Error()
			m.nextCheck = now.Add(m.opts.RetryInterval)
			m.mu.Unlock()
			return
		}
		m.client, m.clientKey, m.disabledReason = c, key, ""
		m.resetAvailableLocked()
	}
	client := m.client
	m.mu.Unlock()
	if client == nil {
		return
	}

	upd, err := client.Check(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCheck = now
	if err != nil {
		if ctx.Err() == nil {
			m.lastErr = "check: " + err.Error()
			m.log.Warn("updater: check failed", "err", err)
		}
		m.nextCheck = now.Add(m.opts.RetryInterval)
		return
	}
	m.nextCheck = now.Add(m.opts.PollInterval)
	m.lastErr = ""
	m.skipped = ""
	if upd != nil && m.failed != nil && sameVersion(upd.Version(), m.failed.Version) {
		m.skipped = upd.Version()
		m.log.Info("updater: skipping previously rolled-back version", "version", upd.Version())
		upd = nil
	}
	if upd == nil {
		m.resetAvailableLocked()
		m.requested, m.force = false, false
		return
	}
	if m.available == nil || m.available.Version() != upd.Version() {
		m.resetAvailableLocked()
		m.log.Info("updater: update available", "current", m.opts.CurrentVersion, "available", upd.Version(), "kind", upd.Artifact.Kind)
	}
	m.available = upd
}

func (m *Manager) resetAvailableLocked() {
	m.available = nil
	m.staged, m.stagedVersion = "", ""
	m.downloadAfter, m.applyAfter = time.Time{}, time.Time{}
}

// selfApplicableLocked reports whether upd can be installed by this process.
func (m *Manager) selfApplicableLocked(upd *Update) (bool, string) {
	if !m.opts.SelfApply {
		return false, m.opts.NotifyReason
	}
	if upd != nil && upd.Artifact.Kind != KindBinary {
		return false, "this release is distributed as an installer for this platform"
	}
	return true, ""
}

func (m *Manager) advance(ctx context.Context, now time.Time) {
	cfg := m.opts.Config()
	m.mu.Lock()
	upd := m.available
	if upd == nil {
		m.state = StateIdle
		m.mu.Unlock()
		return
	}
	if ok, _ := m.selfApplicableLocked(upd); !ok || !(cfg.AutoUpdate || m.requested) {
		m.state = StateIdle
		m.mu.Unlock()
		return
	}
	if m.staged == "" || m.stagedVersion != upd.Version() {
		if now.Before(m.downloadAfter) {
			m.state = StateIdle
			m.mu.Unlock()
			return
		}
		m.state = StateDownloading
		m.mu.Unlock()
		path, err := m.client.Download(ctx, upd, m.opts.Dir, nil)
		m.mu.Lock()
		if m.available != upd { // feed moved on meanwhile
			m.mu.Unlock()
			if err == nil {
				_ = os.Remove(path)
			}
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				m.lastErr = "download: " + err.Error()
				m.log.Warn("updater: download failed", "version", upd.Version(), "err", err)
			}
			m.downloadAfter = now.Add(m.opts.RetryInterval)
			m.state = StateIdle
			m.mu.Unlock()
			return
		}
		m.staged, m.stagedVersion = path, upd.Version()
		m.lastErr = ""
		m.state = StateStaged
		m.mu.Unlock()
		m.cleanDownloads(path)
		m.log.Info("updater: update downloaded and verified; waiting for a safe moment", "version", upd.Version())
		m.mu.Lock()
	}

	switch {
	case !m.requested && !inWindow(now.In(m.opts.Location), cfg):
		m.state = StateWaitingWindow
	case !m.confirmed:
		// Never stack a second update on one still being confirmed.
		m.state = StateWaitingIdle
	case !(m.requested && m.force) && (m.lastActive > 0 || m.idleSince.IsZero() || now.Sub(m.idleSince) < m.opts.QuietPeriod):
		m.state = StateWaitingIdle
	case now.Before(m.applyAfter):
		m.state = StateStaged
	default:
		m.state = StateApplying
		staged := m.staged
		m.mu.Unlock()
		m.apply(now, upd, staged)
		return
	}
	m.mu.Unlock()
}

func (m *Manager) apply(now time.Time, upd *Update, staged string) {
	fail := func(msg string, err error, redownload bool) {
		m.log.Error("updater: "+msg, "version", upd.Version(), "err", err)
		m.mu.Lock()
		m.lastErr = msg + ": " + err.Error()
		m.applyAfter = now.Add(m.opts.RetryInterval)
		if redownload {
			m.staged, m.stagedVersion = "", ""
		}
		m.state = StateStaged
		m.mu.Unlock()
	}
	// The download sat on disk while we waited; verify it again.
	if err := VerifyFile(staged, upd.Artifact); err != nil {
		_ = os.Remove(staged)
		fail("staged update failed verification", err, true)
		return
	}
	target := m.opts.Target
	p := Pending{From: m.opts.CurrentVersion, To: upd.Version(), Target: target, AppliedAt: now}
	if err := writeJSONAtomic(PendingPath(m.opts.Dir), &p); err != nil {
		fail("write update marker", err, false)
		return
	}
	if err := m.opts.ApplyBinary(staged, target); err != nil {
		_ = removeIfExists(PendingPath(m.opts.Dir))
		fail("install update", err, false)
		return
	}
	m.log.Info("updater: update installed; restarting", "from", p.From, "to", p.To, "target", target)
	m.mu.Lock()
	m.restarting = true
	m.mu.Unlock()
	if m.opts.RequestRestart != nil {
		m.opts.RequestRestart(fmt.Sprintf("updated %s → %s", p.From, p.To))
	}
}

// cleanDownloads removes old downloaded artifacts, keeping keep and the
// marker files, so years of releases never pile up in the data dir.
func (m *Manager) cleanDownloads(keep string) {
	entries, err := os.ReadDir(m.opts.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == pendingFile || name == failedFile || strings.HasSuffix(name, ".json.tmp") {
			continue
		}
		p := filepath.Join(m.opts.Dir, name)
		if keep != "" && filepath.Clean(p) == filepath.Clean(keep) {
			continue
		}
		_ = os.Remove(p)
	}
}

// UpdateNow asks for the available update (checking the feed first if none
// is known) to be installed as soon as the server is idle, ignoring the
// maintenance window. force also skips the idle wait.
func (m *Manager) UpdateNow(force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		if m.disabledReason != "" {
			return fmt.Errorf("%w: %s", ErrUpdatesDisabled, m.disabledReason)
		}
		return ErrUpdatesDisabled
	}
	if ok, why := m.selfApplicableLocked(m.available); !ok {
		return fmt.Errorf("%w: %s", ErrNotSelfApplicable, why)
	}
	if m.restarting {
		return nil
	}
	m.requested = true
	m.force = m.force || force
	m.lastErr = ""
	m.downloadAfter, m.applyAfter = time.Time{}, time.Time{}
	if m.available == nil {
		m.nextCheck = time.Time{}
	}
	m.wake()
	return nil
}

// Window is the maintenance window as reported by Status.
type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Open  bool   `json:"open"`
}

// AvailableUpdate describes the newest release on the feed.
type AvailableUpdate struct {
	Version     string     `json:"version"`
	Kind        string     `json:"kind"`
	NotesURL    string     `json:"notesUrl,omitempty"`
	Notes       string     `json:"notes,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	Staged      bool       `json:"staged"`
}

// ManagerStatus is the admin-facing snapshot.
type ManagerStatus struct {
	Enabled            bool             `json:"enabled"`
	DisabledReason     string           `json:"disabledReason,omitempty"`
	Current            string           `json:"current"`
	Available          *AvailableUpdate `json:"available,omitempty"`
	Capability         string           `json:"capability"`
	CapabilityReason   string           `json:"capabilityReason,omitempty"`
	State              string           `json:"state"`
	AutoUpdate         bool             `json:"autoUpdate"`
	Requested          bool             `json:"requested"`
	Force              bool             `json:"force"`
	LastCheck          *time.Time       `json:"lastCheck,omitempty"`
	NextCheck          *time.Time       `json:"nextCheck,omitempty"`
	LastError          string           `json:"lastError,omitempty"`
	Window             Window           `json:"window"`
	ActiveSessions     int              `json:"activeSessions"`
	IdleSince          *time.Time       `json:"idleSince,omitempty"`
	QuietPeriodSeconds int              `json:"quietPeriodSeconds"`
	Confirming         bool             `json:"confirming"`
	FailedVersion      string           `json:"failedVersion,omitempty"`
	FailedReason       string           `json:"failedReason,omitempty"`
	Note               string           `json:"note,omitempty"`
}

// Status snapshots the manager.
func (m *Manager) Status() ManagerStatus {
	cfg := m.opts.Config()
	now := m.opts.Now()
	start, end := windowBounds(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	st := ManagerStatus{
		Enabled:            m.client != nil,
		DisabledReason:     m.disabledReason,
		Current:            m.opts.CurrentVersion,
		State:              m.state,
		AutoUpdate:         cfg.AutoUpdate,
		Requested:          m.requested,
		Force:              m.force,
		LastError:          m.lastErr,
		Window:             Window{Start: start, End: end, Open: inWindow(now.In(m.opts.Location), cfg)},
		ActiveSessions:     m.lastActive,
		QuietPeriodSeconds: int(m.opts.QuietPeriod / time.Second),
		Confirming:         !m.confirmed,
		Note:               m.opts.Boot.Note,
	}
	tp := func(t time.Time) *time.Time {
		if t.IsZero() {
			return nil
		}
		return &t
	}
	st.LastCheck = tp(m.lastCheck)
	if st.Enabled {
		st.NextCheck = tp(m.nextCheck)
	}
	st.IdleSince = tp(m.idleSince)
	if m.available != nil {
		mf := m.available.Manifest
		st.Available = &AvailableUpdate{
			Version:  mf.Version,
			Kind:     m.available.Artifact.Kind,
			NotesURL: mf.NotesURL,
			Notes:    mf.Notes,
			Staged:   m.staged != "" && m.stagedVersion == mf.Version,
		}
		if !mf.PublishedAt.IsZero() {
			pub := mf.PublishedAt
			st.Available.PublishedAt = &pub
		}
	}
	switch ok, why := m.selfApplicableLocked(m.available); {
	case m.client == nil:
		st.Capability = CapabilityDisabled
	case ok:
		st.Capability = CapabilitySelfApply
	default:
		st.Capability, st.CapabilityReason = CapabilityNotifyOnly, why
	}
	if m.failed != nil {
		st.FailedVersion, st.FailedReason = m.failed.Version, m.failed.Reason
		if st.State == StateIdle && m.available == nil {
			st.State = StateRolledBack
		}
	}
	return st
}

// parseClock parses "HH:MM" into minutes past midnight.
func parseClock(s string) (int, bool) {
	s = strings.TrimSpace(s)
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// windowBounds returns the effective window (defaults for empty/invalid).
func windowBounds(cfg config.Updates) (start, end string) {
	start, end = cfg.MaintenanceStart, cfg.MaintenanceEnd
	if _, ok := parseClock(start); !ok {
		start = DefaultWindowStart
	}
	if _, ok := parseClock(end); !ok {
		end = DefaultWindowEnd
	}
	return start, end
}

// inWindow reports whether local time t falls inside [start, end). The
// window wraps midnight when end <= start; start == end means always open.
func inWindow(t time.Time, cfg config.Updates) bool {
	s, e := windowBounds(cfg)
	start, _ := parseClock(s)
	end, _ := parseClock(e)
	now := t.Hour()*60 + t.Minute()
	switch {
	case start == end:
		return true
	case start < end:
		return now >= start && now < end
	default:
		return now >= start || now < end
	}
}

// Environment describes the install for SelfApplyCapability.
type Environment struct {
	GOOS        string
	Target      string // running executable
	Dev         bool   // IsDev()
	InContainer bool
	// Supervised: a service manager restarts the process after it exits.
	Supervised bool
	// DirWritable probes the executable's directory (default: DirWritable).
	DirWritable func(dir string) bool
}

// SelfApplyCapability decides whether this install may replace its own
// binary. Anything owned by something else — a package manager, a
// container image, a signed macOS app bundle — is notify-only.
func SelfApplyCapability(env Environment) (bool, string) {
	if env.DirWritable == nil {
		env.DirWritable = DirWritable
	}
	switch {
	case env.Dev:
		return false, "development build"
	case env.GOOS == "darwin":
		return false, "the macOS app is updated with its installer"
	case env.InContainer:
		return false, "running in a container; pull the new image instead"
	case env.Target == "":
		return false, "cannot locate the running executable"
	case env.GOOS == "linux" && packageManaged(env.Target):
		return false, "installed by the system package manager; update with it"
	case !env.Supervised:
		return false, "not running as a service, so nothing would restart Ether after an update"
	case !env.DirWritable(filepath.Dir(env.Target)):
		return false, "the install directory is not writable by the Ether service"
	}
	return true, ""
}

func packageManaged(target string) bool {
	for _, p := range []string{"/usr/bin/", "/usr/sbin/", "/usr/lib/", "/usr/libexec/", "/usr/share/", "/snap/"} {
		if strings.HasPrefix(target, p) {
			return true
		}
	}
	return false
}

// DirWritable reports whether this process can create files in dir.
func DirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".ether-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// InContainer reports whether we are running inside a container image,
// which must be updated by pulling a new image, never in place.
func InContainer() bool {
	if v := strings.TrimSpace(os.Getenv("ETHER_IN_CONTAINER")); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		return true
	}
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
