package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Autostart manages "launch the tray at login":
//   - macOS:   ~/Library/LaunchAgents/io.github.braedonsaunders.ether-tray.plist
//   - Linux:   ~/.config/autostart/ether-tray.desktop
//   - Windows: HKCU\...\Run registry value "EtherTray" (via reg.exe)
type Autostart struct {
	GOOS     string
	HomeDir  string
	ExecPath string
	// Run executes a command (Windows registry path). Injectable for tests.
	Run func(name string, args ...string) error
	// QueryRunValue reports the Windows Run value contents, if any.
	QueryRunValue func() (string, bool)
}

// DefaultAutostart builds an Autostart for this process.
func DefaultAutostart() *Autostart {
	exe, _ := os.Executable()
	home, _ := os.UserHomeDir()
	return &Autostart{
		GOOS: runtime.GOOS, HomeDir: home, ExecPath: exe,
		Run: func(name string, args ...string) error { return exec.Command(name, args...).Run() },
		QueryRunValue: func() (string, bool) {
			out, err := exec.Command("reg", "query", windowsRunKey, "/v", "EtherTray").Output()
			if err != nil {
				return "", false
			}
			return string(out), true
		},
	}
}

const windowsRunKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

// launchAgentPath / desktopPath are the per-user autostart files.
func (a *Autostart) launchAgentPath() string {
	return filepath.Join(a.HomeDir, "Library", "LaunchAgents", "io.github.braedonsaunders.ether-tray.plist")
}

func (a *Autostart) desktopPath() string {
	return filepath.Join(a.HomeDir, ".config", "autostart", "ether-tray.desktop")
}

// LaunchAgentPlist renders the macOS login-item plist for the tray.
func (a *Autostart) LaunchAgentPlist() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>io.github.braedonsaunders.ether-tray</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, xmlEscape(a.ExecPath))
}

// DesktopEntry renders the XDG autostart entry.
func (a *Autostart) DesktopEntry() string {
	return fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Ether\nComment=Ether media server tray\nExec=%s\nIcon=ether\nTerminal=false\nX-GNOME-Autostart-enabled=true\n", a.ExecPath)
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// Enabled reports whether launch-at-login is currently set.
func (a *Autostart) Enabled() bool {
	switch a.GOOS {
	case "darwin":
		_, err := os.Stat(a.launchAgentPath())
		return err == nil
	case "linux":
		_, err := os.Stat(a.desktopPath())
		return err == nil
	case "windows":
		if a.QueryRunValue == nil {
			return false
		}
		_, ok := a.QueryRunValue()
		return ok
	default:
		return false
	}
}

// Enable installs launch-at-login.
func (a *Autostart) Enable() error {
	if a.ExecPath == "" {
		return fmt.Errorf("autostart: no executable path")
	}
	switch a.GOOS {
	case "darwin":
		return writeFile(a.launchAgentPath(), a.LaunchAgentPlist())
	case "linux":
		return writeFile(a.desktopPath(), a.DesktopEntry())
	case "windows":
		if a.Run == nil {
			return fmt.Errorf("autostart: no command runner")
		}
		return a.Run("reg", "add", windowsRunKey, "/v", "EtherTray", "/t", "REG_SZ",
			"/d", `"`+a.ExecPath+`"`, "/f")
	default:
		return fmt.Errorf("autostart: unsupported OS %q", a.GOOS)
	}
}

// Disable removes launch-at-login. Missing entries are not an error.
func (a *Autostart) Disable() error {
	switch a.GOOS {
	case "darwin":
		return removeIfExists(a.launchAgentPath())
	case "linux":
		return removeIfExists(a.desktopPath())
	case "windows":
		if a.Run == nil {
			return fmt.Errorf("autostart: no command runner")
		}
		return a.Run("reg", "delete", windowsRunKey, "/v", "EtherTray", "/f")
	default:
		return fmt.Errorf("autostart: unsupported OS %q", a.GOOS)
	}
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// openCommand picks the platform browser-opener. Pure for tests.
func openCommand(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	name, args := openCommand(runtime.GOOS, url)
	return exec.Command(name, args...).Start()
}
