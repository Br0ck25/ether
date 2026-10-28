//go:build windows

package service

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// aclSIDs returns the SIDs (as strings) granted by path's DACL and whether
// the DACL is protected from the parent.
func aclSIDs(t *testing.T, path string) (map[string]bool, bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	ctl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	sids := map[string]bool{}
	for i := 0; i < int(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		sids[sid.String()] = true
	}
	return sids, ctl&windows.SE_DACL_PROTECTED != 0
}

const (
	sidSystem = "S-1-5-18"
	sidAdmins = "S-1-5-32-544"
	sidUsers  = "S-1-5-32-545"
)

func assertLocked(t *testing.T, path string, wantProtected bool) {
	t.Helper()
	sids, protected := aclSIDs(t, path)
	if wantProtected && !protected {
		t.Errorf("%s: DACL not protected from parent", path)
	}
	if len(sids) != 2 || !sids[sidSystem] || !sids[sidAdmins] {
		t.Errorf("%s: DACL grants %v, want only SYSTEM and Administrators", path, sids)
	}
}

// setDACL replaces dir's DACL (propagating to children). The test owns the
// tree, and owners keep WRITE_DAC, so it can always undo the lockdown.
func setDACL(t *testing.T, dir, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func currentUserSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// testRoot is a temp tree the test can always clean up, even after its
// subdirectories were restricted to SYSTEM and Administrators.
func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	me := currentUserSID(t)
	t.Cleanup(func() { setDACL(t, root, "D:P(A;OICI;FA;;;"+me+")") })
	// Start from a ProgramData-like ACL: the owner, SYSTEM, Administrators,
	// plus Users read, inherited by everything created below.
	setDACL(t, root, "D:P(A;OICI;FA;;;"+me+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;BU)")
	return root
}

func TestSecureDataDirLocksDefaultDirAndExistingFiles(t *testing.T) {
	root := testRoot(t)
	t.Setenv("PROGRAMDATA", root)
	dir := filepath.Join(root, "Ether")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{"config.json", ".credentials-key", "ether.db", "ether.db-wal", "ether.db-shm"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if sids, _ := aclSIDs(t, filepath.Join(dir, "ether.db-wal")); !sids[sidUsers] {
		t.Fatalf("precondition: Users should read ether.db-wal before lockdown, got %v", sids)
	}
	if err := secureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	assertLocked(t, dir, true)
	for _, f := range files {
		assertLocked(t, filepath.Join(dir, f), false)
	}
	// New files written later inherit the lockdown.
	later := filepath.Join(dir, "ether.db-journal")
	if err := os.WriteFile(later, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	assertLocked(t, later, false)
}

func TestSecureDataDirEmptyMeansDefault(t *testing.T) {
	root := testRoot(t)
	t.Setenv("PROGRAMDATA", root)
	if err := secureDataDir(""); err != nil {
		t.Fatal(err)
	}
	assertLocked(t, filepath.Join(root, "Ether"), true)
}

func TestSecureDataDirLocksDirectoryItCreates(t *testing.T) {
	root := testRoot(t)
	t.Setenv("PROGRAMDATA", filepath.Join(root, "elsewhere"))
	dir := filepath.Join(root, "chosen", "Ether")
	if err := secureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	assertLocked(t, dir, true)
}

func TestSecureDataDirKeepsExistingOwnerChosenDir(t *testing.T) {
	root := testRoot(t)
	t.Setenv("PROGRAMDATA", filepath.Join(root, "elsewhere"))
	dir := filepath.Join(root, "chosen")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := secureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	sids, protected := aclSIDs(t, dir)
	if protected || !sids[sidUsers] {
		t.Fatalf("owner-chosen dir ACL changed: protected=%v sids=%v", protected, sids)
	}
}
