package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestDefaultDataDir(t *testing.T) {
	home := func() (string, error) { return "/home/u", nil }
	noHome := func() (string, error) { return "", errors.New("no home") }
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	user := func() int { return 1000 }
	root := func() int { return 0 }
	for _, tc := range []struct {
		name string
		goos string
		env  map[string]string
		home func() (string, error)
		euid func() int
		want string
	}{
		{"override wins", "windows", map[string]string{"ETHER_DATA_DIR": `D:\Data`, "PROGRAMDATA": `C:\ProgramData`}, home, user, `D:\Data`},
		{"windows programdata", "windows", map[string]string{"PROGRAMDATA": `E:\PD`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`}, home, user, `E:\PD\Ether`},
		{"windows trailing slash", "windows", map[string]string{"PROGRAMDATA": `E:\PD\`}, home, user, `E:\PD\Ether`},
		{"windows fallback", "windows", nil, noHome, user, `C:\ProgramData\Ether`},
		{"darwin", "darwin", nil, home, user, filepath.Join("/home/u", "Library", "Application Support", "Ether")},
		{"linux root", "linux", nil, home, root, "/var/lib/ether"},
		{"linux xdg", "linux", map[string]string{"XDG_DATA_HOME": "/xdg"}, home, user, filepath.Join("/xdg", "ether")},
		{"linux user", "linux", nil, home, user, filepath.Join("/home/u", ".local", "share", "ether")},
	} {
		if got := defaultDataDir(tc.goos, env(tc.env), tc.home, tc.euid); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
