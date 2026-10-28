//go:build windows

package service

import (
	"errors"
	"fmt"

	ksvc "github.com/kardianos/service"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

// updateInstalledPlatform rewrites an existing SCM registration in place:
// binary path, names, delayed auto-start and environment. The service is
// never deleted, so an open handle elsewhere (services.msc, Process
// Explorer) cannot leave it "marked for deletion" with nothing to replace
// it. The account the service runs as is left alone. With keepDataDir the
// registered ETHER_DATA_DIR survives, so an owner's custom data directory
// is not swapped for the default on upgrade.
func updateInstalledPlatform(cfg *ksvc.Config, keepDataDir bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(cfg.Name)
	if err != nil {
		return fmt.Errorf("open service %q: %w", cfg.Name, err)
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return fmt.Errorf("read service config: %w", err)
	}
	c.BinaryPathName = windowsBinaryPath(cfg.Executable, cfg.Arguments)
	c.DisplayName = cfg.DisplayName
	c.Description = cfg.Description
	c.StartType = mgr.StartAutomatic
	c.DelayedAutoStart = true
	// Empty account and password mean "leave unchanged" to
	// ChangeServiceConfig; resending the account without its password
	// would fail for a custom service account.
	c.ServiceStartName = ""
	c.Password = ""
	if err := s.UpdateConfig(c); err != nil {
		return fmt.Errorf("update service config: %w", err)
	}
	return updateServiceEnvironment(cfg.Name, cfg.EnvVars, keepDataDir)
}

// updateServiceEnvironment merges env into the service's Environment
// registry value (the same value kardianos/service writes on install).
func updateServiceEnvironment(name string, env map[string]string, keepDataDir bool) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+name,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open service registry key: %w", err)
	}
	defer k.Close()
	existing, _, err := k.GetStringsValue("Environment")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("read service environment: %w", err)
	}
	merged := mergeServiceEnv(existing, env, keepDataDir)
	if len(merged) == 0 {
		return nil
	}
	if err := k.SetStringsValue("Environment", merged); err != nil {
		return fmt.Errorf("write service environment: %w", err)
	}
	return nil
}
