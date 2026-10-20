//go:build windows

package service

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

// applyPlatformRecovery gives the SCM entry Ether's full restart policy:
// restart forever with backoff (5s, 15s, then 60s for every later failure;
// the failure count resets after a clean day) and treat error exits (not
// only crashes) as failures. kardianos/service only configures a single
// recovery action, so this runs after Install. Matches ether.nsi.
func applyPlatformRecovery(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service %q: %w", name, err)
	}
	defer s.Close()
	actions := make([]mgr.RecoveryAction, 0, len(windowsRecoveryDelays))
	for _, d := range windowsRecoveryDelays {
		actions = append(actions, mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: d})
	}
	if err := s.SetRecoveryActions(actions, uint32(windowsRecoveryReset/time.Second)); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set failure flag: %w", err)
	}
	return nil
}
