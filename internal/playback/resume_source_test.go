package playback

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestSourcePreferenceInvalidatesRememberedRelease(t *testing.T) {
	for _, tc := range []struct{ name, mode, oldProtocol, newProtocol string }{
		{"debrid", "debrid", "", core.ProtocolTorrent},
		{"usenet", "usenet", core.ProtocolTorrent, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []*fakeRelease{mkv("old", 10000, time.Minute), mkv("new", 10000, time.Minute)}, []string{"new"})
			ctx := context.Background()
			caps := defaultCaps("client")
			old := core.Release{ID: "old", Protocol: tc.oldProtocol}
			fresh := core.Release{ID: "new", Protocol: tc.newProtocol}
			if err := h.m.Remember(ctx, owner, movieKey, caps, &core.Resolution{Release: old, NZB: []byte("nzb-old")}); err != nil {
				t.Fatal(err)
			}
			h.cfg.Resolver.SourcePreference = tc.mode
			h.m.SetConfig(h.cfg)
			if rp, err := h.m.ResumeFor(ctx, owner, movieKey, caps); err != nil || rp != nil {
				t.Fatalf("stale source still reusable: %+v %v", rp, err)
			}
			ps := h.prepare(owner, movieKey, "client")
			if ps.Resolution.Release.ID != "new" || h.resolver.callCount() != 1 {
				t.Fatalf("Prepare bypassed resolver: %+v", ps.Resolution.Release)
			}
			h.m.Stop(ps.ID)
			if err := h.m.Remember(ctx, owner, movieKey, caps, &core.Resolution{Release: fresh, NZB: []byte("nzb-new")}); err != nil {
				t.Fatal(err)
			}
			rp, err := h.m.ResumeFor(ctx, owner, movieKey, caps)
			if err != nil || rp == nil || rp.ReleaseID() != "new" {
				t.Fatalf("preferred source not remembered: %+v %v", rp, err)
			}
		})
	}
}

func TestAutoSourceKeepsRememberedRelease(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("old", 10000, time.Minute)}, nil)
	ctx := context.Background()
	caps := defaultCaps("client")
	if err := h.m.Remember(ctx, owner, movieKey, caps, &core.Resolution{Release: core.Release{ID: "old"}, NZB: []byte("nzb-old")}); err != nil {
		t.Fatal(err)
	}
	h.cfg.Resolver.SourcePreference = "auto"
	h.m.SetConfig(h.cfg)
	ps := h.prepare(owner, movieKey, "client")
	if ps.Resolution.Release.ID != "old" || h.resolver.callCount() != 0 {
		t.Fatal("auto must reuse the remembered source")
	}
}

func TestRememberedRebootCannotBypassSeriesIdentity(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("old", 10000, time.Minute), mkv("correct", 10000, time.Minute)}, []string{"correct"})
	h.catalog.titles[epKey].ShowYear = 2005
	caps := defaultCaps("series-client")
	old := core.Release{ID: "old", Info: core.ReleaseInfo{Year: 2023}}
	if err := h.m.Remember(t.Context(), owner, epKey, caps, &core.Resolution{Release: old, NZB: []byte("nzb-old")}); err != nil {
		t.Fatal(err)
	}
	ps := h.prepare(owner, epKey, "series-client")
	if ps.Resolution.Release.ID != "correct" || h.resolver.callCount() != 1 {
		t.Fatalf("stored reboot bypassed identity: %+v", ps.Resolution.Release)
	}
}
