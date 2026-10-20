package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Marker files inside the updates directory (<dataDir>/updates).
const (
	pendingFile = "pending.json"
	failedFile  = "failed.json"

	// DefaultMaxStartAttempts is how many starts of a freshly installed
	// version may happen without it confirming health before the previous
	// binary is restored.
	DefaultMaxStartAttempts = 3
)

// Pending is written immediately before a binary swap and removed once the
// new version has served healthily for the health period. Attempts counts
// starts of the new version; a crash loop pushes it past the limit and
// triggers a rollback.
type Pending struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Target    string    `json:"target"`
	AppliedAt time.Time `json:"appliedAt"`
	Attempts  int       `json:"attempts"`
}

// FailedUpdate records a version that was rolled back; the manager skips
// exactly this version until a newer one is published.
type FailedUpdate struct {
	Version string    `json:"version"`
	From    string    `json:"from"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
	// ReportPending is set by a rollback and cleared once a clustered node
	// has told the other nodes to skip Version. A successful rollback
	// restarts straight into the restored binary, so it is that process
	// (with no update to confirm) that reports.
	ReportPending bool `json:"reportPending,omitempty"`
}

// PendingPath / FailedPath locate the marker files inside dir.
func PendingPath(dir string) string { return filepath.Join(dir, pendingFile) }
func FailedPath(dir string) string  { return filepath.Join(dir, failedFile) }

// readJSON loads path into v. ok is false (with a nil error) when the file
// does not exist.
func readJSON(path string, v any) (ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("updater: parse %s: %w", path, err)
	}
	return true, nil
}

// writeJSONAtomic writes v to path via a synced temp file + rename, so a
// crash never leaves a half-written marker.
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// sameVersion compares versions semantically ("v1.2" == "1.2.0"), falling
// back to a trimmed string compare for unparseable values.
func sameVersion(a, b string) bool {
	if c, err := CompareVersions(a, b); err == nil {
		return c == 0
	}
	return strings.TrimPrefix(strings.TrimSpace(a), "v") == strings.TrimPrefix(strings.TrimSpace(b), "v")
}

// StartupOptions configures ConfirmStartup.
type StartupOptions struct {
	// Dir is the updates directory holding pending.json / failed.json.
	Dir string
	// Target is the running executable; used when the marker has none and
	// for leftover cleanup. Empty: os.Executable().
	Target string
	// CurrentVersion is the running build's version.
	CurrentVersion string
	// MaxAttempts before rollback. <= 0: DefaultMaxStartAttempts.
	MaxAttempts int
	Now         func() time.Time
	// Rollback / CleanupOld default to the package functions.
	Rollback   func(target string) error
	CleanupOld func(target string) error
	Logger     *slog.Logger
}

// StartupResult is what ConfirmStartup found and did.
type StartupResult struct {
	// Confirming is the marker of a just-installed update that this
	// process (the new version) must confirm by running healthily.
	Confirming *Pending
	// RolledBack is set when the previous binary was restored. When
	// RestartRequired is also set the process must exit so the service
	// manager starts the restored binary.
	RolledBack      *FailedUpdate
	RestartRequired bool
	// Note is a human-readable summary for logs and the admin status.
	Note string
}

// ConfirmStartup runs once at process start, before anything that a bad
// release could break (config parse, database migrations), and implements
// confirm-or-rollback for a self-applied update:
//
//   - no marker: remove leftover "<target>.old"/".bad" from a confirmed update;
//   - marker and Version == To: count this start; past MaxAttempts the
//     update is considered crash-looping, the previous binary is restored,
//     the version is recorded as failed and a restart is requested;
//   - marker and Version == From: the swap never took effect; clear it.
func ConfirmStartup(opts StartupOptions) (StartupResult, error) {
	var res StartupResult
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxStartAttempts
	}
	if opts.Rollback == nil {
		opts.Rollback = Rollback
	}
	if opts.CleanupOld == nil {
		opts.CleanupOld = CleanupOld
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if opts.Target == "" {
		if exe, err := os.Executable(); err == nil {
			opts.Target = exe
		}
	}
	if opts.Dir == "" {
		return res, errors.New("updater: no updates directory")
	}
	var p Pending
	ok, err := readJSON(PendingPath(opts.Dir), &p)
	if err != nil {
		// A corrupt marker can't be acted on safely; drop it.
		log.Warn("updater: discarding unreadable update marker", "err", err)
		_ = removeIfExists(PendingPath(opts.Dir))
		ok = false
	}
	if !ok {
		cleanupLeftovers(opts.Target, opts.CleanupOld, log)
		forgetFailedIfSuperseded(opts.Dir, opts.CurrentVersion, log)
		return res, nil
	}
	target := p.Target
	if target == "" {
		target = opts.Target
	}
	switch {
	case sameVersion(opts.CurrentVersion, p.To):
		p.Attempts++
		if p.Attempts <= opts.MaxAttempts {
			if err := writeJSONAtomic(PendingPath(opts.Dir), &p); err != nil {
				return res, fmt.Errorf("updater: record start attempt: %w", err)
			}
			res.Confirming = &p
			res.Note = fmt.Sprintf("Confirming update %s → %s (start %d of %d)", p.From, p.To, p.Attempts, opts.MaxAttempts)
			log.Info("updater: new version started; confirming health", "from", p.From, "to", p.To, "attempt", p.Attempts)
			return res, nil
		}
		failed := &FailedUpdate{
			Version:       p.To,
			From:          p.From,
			At:            opts.Now(),
			Reason:        fmt.Sprintf("version %s failed to run healthily after %d starts", p.To, p.Attempts-1),
			ReportPending: true,
		}
		res.RolledBack = failed
		if rbErr := opts.Rollback(target); rbErr != nil {
			failed.Reason += "; rollback failed: " + rbErr.Error()
			log.Error("updater: UPDATE FAILED AND ROLLBACK FAILED; staying on the new version",
				"version", p.To, "previous", p.From, "target", target, "err", rbErr)
			res.Note = failed.Reason
		} else {
			res.RestartRequired = true
			res.Note = fmt.Sprintf("Rolled back %s → %s: %s", p.To, p.From, failed.Reason)
			log.Error("updater: UPDATE ROLLED BACK; restarting into the previous version",
				"failed", p.To, "restored", p.From, "target", target, "starts", p.Attempts-1)
		}
		if err := writeJSONAtomic(FailedPath(opts.Dir), failed); err != nil {
			log.Error("updater: could not record failed version", "err", err)
		}
		if err := removeIfExists(PendingPath(opts.Dir)); err != nil {
			log.Error("updater: could not remove update marker", "err", err)
		}
		return res, nil
	case sameVersion(opts.CurrentVersion, p.From):
		res.Note = fmt.Sprintf("Update %s → %s did not take effect; still running %s", p.From, p.To, p.From)
		log.Warn("updater: update marker found but the previous version is running; the swap did not take effect",
			"from", p.From, "to", p.To)
	default:
		log.Warn("updater: update marker does not match this version; discarding",
			"from", p.From, "to", p.To, "running", opts.CurrentVersion)
	}
	if err := removeIfExists(PendingPath(opts.Dir)); err != nil {
		return res, err
	}
	return res, nil
}

// cleanupLeftovers removes the previous binary kept for rollback plus the
// ".bad" copy a Windows rollback leaves behind (it cannot delete the binary
// it is running from).
func cleanupLeftovers(target string, cleanupOld func(string) error, log *slog.Logger) {
	if target == "" {
		return
	}
	if err := cleanupOld(target); err != nil {
		log.Debug("updater: remove previous binary", "err", err)
	}
	_ = removeIfExists(target + ".bad")
}

// forgetFailedIfSuperseded drops the failed-version record once the running
// version is at least as new (the owner installed it by hand, or a newer
// release confirmed).
func forgetFailedIfSuperseded(dir, current string, log *slog.Logger) {
	var f FailedUpdate
	ok, err := readJSON(FailedPath(dir), &f)
	if err != nil {
		_ = removeIfExists(FailedPath(dir))
		return
	}
	if ok && (sameVersion(current, f.Version) || Newer(current, f.Version)) && current != "dev" && current != "" {
		if err := removeIfExists(FailedPath(dir)); err == nil {
			log.Debug("updater: cleared superseded failed-version record", "version", f.Version)
		}
	}
}
