package main

import (
	"context"
	"fmt"
	"runtime"

	"github.com/braedonsaunders/ether/internal/service"
)

// A Mac app has no installer to register its server. Bootstrap a per-user
// service on launch, while leaving an already-running development server alone.
func (a *app) ensureMacServer(ctx context.Context) error {
	goos := a.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "darwin" || a.svc == nil {
		return nil
	}
	if a.status != nil {
		if st, err := a.status.Poll(ctx); err == nil && st.Running {
			return nil
		}
	}
	st, err := a.svc.Status()
	if err == nil && st == service.StatusRunning {
		return nil
	}
	if err != nil || st == service.StatusUnknown {
		installer, ok := a.svc.(interface{ Install() error })
		if !ok {
			return fmt.Errorf("server service cannot be installed")
		}
		if err := installer.Install(); err != nil {
			return fmt.Errorf("install server service: %w", err)
		}
	}
	return a.svc.Start()
}
