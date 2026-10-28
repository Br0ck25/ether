package service

import "errors"

// ErrRestartRequested is returned by a RunFunc that wants the service
// manager to start a fresh process (e.g. after a self-update). Under a
// service manager the process exits with ExitCodeRestart; interactively
// Run returns it to the caller.
var ErrRestartRequested = errors.New("service: restart requested")

// ExitCodeRestart is the process exit code used for ErrRestartRequested.
const ExitCodeRestart = 3
