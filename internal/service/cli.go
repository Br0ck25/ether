package service

import (
	"fmt"
	"io"
	"strings"

	ksvc "github.com/kardianos/service"
)

// Commands accepted after the "service" subcommand.
var Commands = []string{"install", "uninstall", "start", "stop", "restart", "status"}

// ErrNotInstalled reports a status query against an unregistered service.
var ErrNotInstalled = ksvc.ErrNotInstalled

// HandleCommand implements `ether service <cmd>` for cmd/ether. args is
// os.Args[1:]. It returns handled=false (and nil error) when args do not
// start with "service", so the caller can fall through to serving.
// Usage/help text goes to out; a bare `ether service` prints usage and is
// handled without error.
func HandleCommand(args []string, svc *Service, out io.Writer) (handled bool, err error) {
	if len(args) == 0 || args[0] != "service" {
		return false, nil
	}
	if out == nil {
		out = io.Discard
	}
	if len(args) < 2 {
		fmt.Fprintf(out, "usage: ether service <%s>\n", strings.Join(Commands, "|"))
		return true, nil
	}
	cmd := args[1]
	switch cmd {
	case "install":
		updated, err := svc.InstallOrUpdate()
		if err != nil {
			return true, err
		}
		if updated {
			fmt.Fprintf(out, "service %q updated in place (%s); restart it to run this binary\n", svc.cfg.Name, svc.Platform())
		} else {
			fmt.Fprintf(out, "service %q installed (%s)\n", svc.cfg.Name, svc.Platform())
		}
	case "uninstall":
		// Best effort stop first so uninstalling a running service works.
		_ = svc.Stop()
		if err := svc.Uninstall(); err != nil {
			return true, err
		}
		fmt.Fprintf(out, "service %q uninstalled\n", svc.cfg.Name)
	case "start":
		if err := svc.Start(); err != nil {
			return true, err
		}
		fmt.Fprintln(out, "ether started")
	case "stop":
		if err := svc.Stop(); err != nil {
			return true, err
		}
		fmt.Fprintln(out, "ether stopped")
	case "restart":
		if err := svc.Restart(); err != nil {
			return true, err
		}
		fmt.Fprintln(out, "ether restarted")
	case "status":
		st, err := svc.Status()
		if err != nil {
			fmt.Fprintf(out, "service %q: not installed or status unavailable (%v)\n", svc.cfg.Name, err)
			return true, nil
		}
		fmt.Fprintf(out, "service %q: %s (%s)\n", svc.cfg.Name, st, svc.Platform())
	case "help", "-h", "--help":
		fmt.Fprintf(out, "usage: ether service <%s>\n", strings.Join(Commands, "|"))
	default:
		fmt.Fprintf(out, "usage: ether service <%s>\n", strings.Join(Commands, "|"))
		return true, fmt.Errorf("service: unknown command %q", cmd)
	}
	return true, nil
}
