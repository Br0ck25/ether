package logfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Crash log file names inside the logs directory.
const (
	CrashLogName    = "crash.log"
	crashMarkerName = ".last-start"
	// DefaultCrashMaxSize caps crash.log; larger logs are moved to
	// crash.log.1 at startup.
	DefaultCrashMaxSize int64 = 5 << 20
)

// CrashLog is the result of PrepareCrashLog.
type CrashLog struct {
	// File is open for appending; hand it to debug.SetCrashOutput (which
	// duplicates the descriptor) and then close it.
	File *os.File
	// Path is the crash log path.
	Path string
	// Changed reports that the crash log was written since the previous
	// start, i.e. the previous run died with a fatal error.
	Changed bool
}

// PrepareCrashLog opens <dir>/crash.log for appending, rotating it to
// crash.log.1 first when it exceeds maxSize (<= 0: DefaultCrashMaxSize),
// and reports whether it changed since the previous call (tracked by a
// marker file touched on every call).
func PrepareCrashLog(dir string, maxSize int64) (*CrashLog, error) {
	if maxSize <= 0 {
		maxSize = DefaultCrashMaxSize
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("logfile: %w", err)
	}
	path := filepath.Join(dir, CrashLogName)
	marker := filepath.Join(dir, crashMarkerName)
	changed := false
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		mst, merr := os.Stat(marker)
		changed = merr != nil || st.ModTime().After(mst.ModTime())
		if st.Size() > maxSize {
			if err := os.Rename(path, path+".1"); err != nil {
				_ = os.Truncate(path, 0)
			}
		}
	}
	if err := touch(marker); err != nil {
		return nil, fmt.Errorf("logfile: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, fileMode)
	if err != nil {
		return nil, fmt.Errorf("logfile: %w", err)
	}
	return &CrashLog{File: f, Path: path, Changed: changed}, nil
}

func touch(path string) error {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, nil, fileMode)
}
