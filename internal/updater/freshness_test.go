package updater

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManifestFreshnessBounds(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name            string
		issued, expires *time.Time
		ok              bool
	}{
		{name: "missing"},
		{name: "valid", issued: ptrTime(now), expires: ptrTime(now.Add(ManifestLifetime)), ok: true},
		{name: "expired", issued: ptrTime(now.Add(-ManifestLifetime)), expires: ptrTime(now)},
		{name: "future", issued: ptrTime(now.Add(6 * time.Minute)), expires: ptrTime(now.Add(time.Hour))},
		{name: "unbounded", issued: ptrTime(now), expires: ptrTime(now.Add(ManifestLifetime + time.Second))},
		{name: "reversed", issued: ptrTime(now), expires: ptrTime(now.Add(-time.Second))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Manifest{IssuedAt: tc.issued, ExpiresAt: tc.expires}
			err := m.ValidateFreshness(now)
			if (err == nil) != tc.ok {
				t.Fatalf("freshness: %v", err)
			}
		})
	}
}
func ptrTime(t time.Time) *time.Time { return &t }

func TestClientRejectsAuthenticStaleFeed(t *testing.T) {
	for _, detached := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			f := newFeed(t, func(m *Manifest) {
				m.RenewFreshness(time.Now().Add(-2 * ManifestLifetime))
				if missing {
					m.IssuedAt = nil
					m.ExpiresAt = nil
				}
			}, detached)
			c, err := NewClient(Options{FeedURL: f.server.URL, CurrentVersion: "1.0.0", PublicKey: f.pub})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Check(context.Background()); !errors.Is(err, ErrStaleManifest) {
				t.Fatalf("detached=%v missing=%v: %v", detached, missing, err)
			}
		}
	}
}

func TestExpiryIsCoveredBySignature(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: "1.2.0", Channel: "stable"}
	m.RenewFreshness(time.Now())
	if err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
	newer := m.ExpiresAt.Add(time.Hour)
	m.ExpiresAt = &newer
	if err := m.Verify(pub); !errors.Is(err, ErrBadSignature) {
		t.Fatal("expiry modification was not rejected")
	}
}
