//go:build windows

package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// dataDirSDDL grants full control to LocalSystem (SY) and Administrators
// (BA) only, inherited by every file and folder below, and blocks the
// parent's inherited entries (P). It matches the icacls call in ether.nsi.
const dataDirSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// secureDataDir restricts a LocalSystem service's data directory, which
// holds config.json, the credential key and ether.db, to SYSTEM and
// Administrators: %ProgramData% otherwise lets every local user read it.
// A directory this creates and the default %ProgramData%\Ether get the
// ACL, and existing files below inherit it; any other existing directory
// the owner chose keeps its own ACL.
func secureDataDir(dir string) error {
	def := defaultWindowsDataDir()
	if dir == "" {
		dir = def
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return err
		}
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", abs)
	case !strings.EqualFold(filepath.Clean(abs), filepath.Clean(def)):
		return nil
	}
	return applyDataDirACL(abs)
}

func applyDataDirACL(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(dataDirSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// SetNamedSecurityInfo propagates the inheritable entries to existing
	// children, so a database written under the old ACL is covered too.
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// defaultWindowsDataDir is config.DefaultDataDir's Windows answer without
// the ETHER_DATA_DIR override: the directory ether.nsi locks down.
func defaultWindowsDataDir() string {
	base := os.Getenv("PROGRAMDATA")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "Ether")
}
