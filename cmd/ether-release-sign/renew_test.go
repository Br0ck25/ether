package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/updater"
)

func TestRenewAuthenticatesAndPreservesRelease(t *testing.T) {
	pub, priv, err := updater.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ETHER_UPDATE_PUBLIC_KEY", updater.EncodeKey(pub))
	t.Setenv("ETHER_UPDATE_PRIVATE_KEY", updater.EncodeKey(priv))
	published := time.Now().UTC().Add(-60 * 24 * time.Hour)
	m := updater.Manifest{Version: "0.2.4", Channel: "stable", PublishedAt: published,
		Artifacts: []updater.Artifact{{OS: "darwin", Arch: "universal", Kind: "installer", URL: "https://example.invalid/Ether.dmg", SHA256: strings.Repeat("a", 64), Size: 123}}}
	// Legacy embedded signature deliberately lacks freshness. Renewal must verify
	// it, then publish a detached signature that old clients can still verify.
	if err := m.Sign(priv); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "old.json"), filepath.Join(dir, "new.json")
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(in, raw, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-in", in, "-out", out, "-version", "0.2.4", "-channel", "stable"}
	if err := cmdRenew(args); err != nil {
		t.Fatal(err)
	}
	got, body, err := readManifest(out)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(out + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.VerifyDetached(pub, body, string(sig)); err != nil {
		t.Fatal(err)
	}
	if err := got.ValidateFreshness(time.Now()); err != nil {
		t.Fatal(err)
	}
	if got.Signature != "" || !got.PublishedAt.Equal(published) || got.Artifacts[0] != m.Artifacts[0] {
		t.Fatal("renewal changed release content or left embedded signature")
	}
	if err := cmdVerify([]string{"-in", out, "-sig", out + ".sig"}); err != nil {
		t.Fatal(err)
	}
	// Renewal must not bless tampered content or a mismatched release.
	m.Artifacts[0].Size++
	raw, _ = json.Marshal(m)
	_ = os.WriteFile(in, raw, 0600)
	if err := cmdRenew(args); err == nil {
		t.Fatal("tampered source renewed")
	}
	if err := cmdRenew([]string{"-in", out, "-out", filepath.Join(dir, "wrong.json"), "-version", "9.0.0", "-channel", "stable"}); err == nil {
		t.Fatal("wrong release renewed")
	}
}
