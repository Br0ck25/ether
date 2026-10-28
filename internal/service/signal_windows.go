//go:build windows

package service

import (
	"context"
	"os"
	"os/signal"
)

func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt)
}
