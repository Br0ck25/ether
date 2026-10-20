package resolver

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestClientBitrateFitBeatsVerifiedLargeSource(t *testing.T) {
	for _, remote := range []bool{false, true} {
		big := rel("big", "The.Signal.1999.1080p.WEB-DL.H.264-GRP", 8000)
		small := rel("small", "The.Signal.1999.1080p.WEB-DL.H.264-GRP", 1200)
		svc := New(&fakeIndexer{releases: []core.Release{big, small}}, fakeHealth{}, nil, testCfg(), Options{})
		svc.markVerified(context.Background(), big.ID)
		caps := webCaps()
		caps.Remote = remote
		caps.MaxBitrateKbps = 2000
		res, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: caps})
		if err != nil || res.Release.ID != "small" {
			t.Fatalf("remote=%v selected oversized verified source: %v %v", remote, res, err)
		}
	}
}
