package resolver

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestPasswordedUsenetRequiresHealthValidation(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "invalid"}[healthy], func(t *testing.T) {
			candidate := rel("encrypted", "The.Signal.1999.1080p.BluRay.x264-GRP", 8000)
			candidate.Password = true
			var checks atomic.Int64
			health := fakeHealth{checks: &checks, unhealthy: map[string]bool{}}
			if !healthy {
				health.unhealthy[candidate.ID] = true
			}
			svc := New(&fakeIndexer{releases: []core.Release{candidate}}, health, nil, testCfg(), Options{})
			result, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
			if checks.Load() == 0 {
				t.Fatal("password-flagged NZB never reached health validation")
			}
			if healthy && (err != nil || result.Release.ID != candidate.ID) {
				t.Fatalf("healthy encrypted release rejected: %v", err)
			}
			if !healthy && err == nil {
				t.Fatal("unhealthy encrypted release accepted")
			}
		})
	}
}

func TestPasswordedPinProtocolGate(t *testing.T) {
	svc := New(&fakeIndexer{}, fakeHealth{}, nil, testCfg(), Options{})
	candidate := rel("encrypted", "The.Signal.1999.1080p.BluRay.x264-GRP", 8000)
	candidate.Password = true
	req := core.SearchRequest{Type: core.TypeMovie}
	if reason := svc.pinRejectReason(candidate, req, webCaps(), nil); reason != "" {
		t.Fatalf("Usenet pin rejected before NZB validation: %s", reason)
	}
	candidate.Protocol = core.ProtocolTorrent
	if reason := svc.pinRejectReason(candidate, req, webCaps(), nil); reason != "passworded" {
		t.Fatalf("passworded torrent gate lost: %q", reason)
	}
}
