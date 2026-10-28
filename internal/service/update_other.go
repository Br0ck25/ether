//go:build !windows

package service

import ksvc "github.com/kardianos/service"

// updateInstalledPlatform is nil outside Windows: systemd units and launchd
// plists are files that kardianos/service renders on a fresh install, and
// re-registering them is left to uninstall + install.
var updateInstalledPlatform func(cfg *ksvc.Config, keepDataDir bool) error
