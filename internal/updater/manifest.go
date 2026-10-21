// Package updater implements Ether's signed self-update feed.
//
// A release publishes one JSON manifest per channel ("stable" / "beta").
// The manifest lists per-platform artifacts (a bare ether binary and/or a
// platform installer) with SHA-256 checksums, and carries an ed25519
// signature — embedded in the "signature" field, or detached in a sibling
// "<manifest>.sig" file. The embedded signature covers the canonical JSON
// encoding of the manifest with the signature field emptied; a detached
// signature covers the exact published manifest bytes. The release public
// key is compiled in (PublicKeyB64, set via -ldflags); cmd/ether-release-sign
// generates keys and signs manifests in CI.
//
// Nothing unsigned or mis-checksummed is ever applied: Check verifies the
// signature before comparing versions, and Download verifies size and
// SHA-256 before Apply touches the installed binary.
package updater

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Channels.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// Artifact kinds.
const (
	KindBinary    = "binary"    // bare ether executable; atomic self-replace
	KindInstaller = "installer" // .dmg/.pkg/.exe/.msi/.deb/.rpm; downloaded + launched
	KindArchive   = "archive"   // .tar.gz/.zip bundle; downloaded, not applied automatically
)

// Artifact is one downloadable file for one platform.
type Artifact struct {
	OS       string `json:"os"`   // runtime.GOOS
	Arch     string `json:"arch"` // runtime.GOARCH, or "universal" (macOS)
	Kind     string `json:"kind"` // KindBinary | KindInstaller | KindArchive
	URL      string `json:"url"`
	SHA256   string `json:"sha256"` // hex, lowercase
	Size     int64  `json:"size"`
	Filename string `json:"filename,omitempty"`
}

// Manifest is the signed release document for one channel.
type Manifest struct {
	Version     string    `json:"version"`
	Channel     string    `json:"channel"`
	PublishedAt time.Time `json:"publishedAt,omitempty"`
	// IssuedAt and ExpiresAt bound replay of a signed feed. Pointers preserve
	// the canonical encoding of legacy manifests during signature checks.
	IssuedAt  *time.Time `json:"issuedAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	NotesURL  string     `json:"notesUrl,omitempty"`
	Notes     string     `json:"notes,omitempty"`
	// MinVersion, when set, is the oldest installed version that may
	// update directly to this release (older installs must reinstall).
	MinVersion string     `json:"minVersion,omitempty"`
	Artifacts  []Artifact `json:"artifacts"`
	// Signature is base64 ed25519 over SigningBytes(). Empty when the
	// signature is detached (served from "<manifest URL>.sig").
	Signature string `json:"signature,omitempty"`
}

var (
	ErrBadSignature   = errors.New("updater: manifest signature verification failed")
	ErrNoPublicKey    = errors.New("updater: no release public key configured")
	ErrNoArtifact     = errors.New("updater: no artifact for this platform")
	ErrChecksum       = errors.New("updater: artifact checksum mismatch")
	ErrTooOldToUpdate = errors.New("updater: installed version is too old to update directly; reinstall")
	ErrStaleManifest  = errors.New("updater: update feed is expired or lacks signed freshness; check the server clock and contact the feed publisher")
)

// ManifestLifetime bounds replay to 30 days. The publisher refreshes the
// signature weekly without changing the release, artifacts, or publication date.
const ManifestLifetime = 30 * 24 * time.Hour

// RenewFreshness updates the timestamps before signing. Never call this on
// unverified downloaded content: renewal is a release-publisher operation.
func (m *Manifest) RenewFreshness(now time.Time) {
	now = now.UTC()
	expires := now.Add(ManifestLifetime)
	m.IssuedAt, m.ExpiresAt = &now, &expires
}

// ValidateFreshness must run only after authenticating the manifest.
func (m *Manifest) ValidateFreshness(now time.Time) error {
	if m.IssuedAt == nil || m.ExpiresAt == nil || m.IssuedAt.IsZero() ||
		!m.ExpiresAt.After(*m.IssuedAt) || m.ExpiresAt.Sub(*m.IssuedAt) > ManifestLifetime ||
		m.IssuedAt.After(now.Add(5*time.Minute)) || !now.Before(*m.ExpiresAt) {
		return ErrStaleManifest
	}
	return nil
}

// SigningBytes is the canonical payload an embedded signature covers: the
// manifest's JSON encoding (struct field order, no indentation) with the
// Signature field emptied.
func (m *Manifest) SigningBytes() ([]byte, error) {
	cp := *m
	cp.Signature = ""
	return json.Marshal(&cp)
}

// Sign sets m.Signature using priv.
func (m *Manifest) Sign(priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("updater: bad private key size %d", len(priv))
	}
	payload, err := m.SigningBytes()
	if err != nil {
		return err
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	return nil
}

// SignDetached returns a base64 signature over the exact manifest file
// bytes, for publishing next to (not inside) the manifest.
func SignDetached(priv ed25519.PrivateKey, manifestBytes []byte) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("updater: bad private key size %d", len(priv))
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifestBytes)), nil
}

// Verify checks the embedded signature against pub.
func (m *Manifest) Verify(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrNoPublicKey
	}
	if m.Signature == "" {
		return ErrBadSignature
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(m.Signature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	payload, err := m.SigningBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return ErrBadSignature
	}
	return nil
}

// VerifyDetached checks a detached base64 (or raw) signature over the exact
// bytes the manifest was served as.
func VerifyDetached(pub ed25519.PublicKey, manifestBytes []byte, sigB64 string) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrNoPublicKey
	}
	sig, err := decodeSig(sigB64)
	if err != nil {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, manifestBytes, sig) {
		return ErrBadSignature
	}
	return nil
}

func decodeSig(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil && len(raw) == ed25519.SignatureSize {
		return raw, nil
	}
	if raw, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(raw) == ed25519.SignatureSize {
		return raw, nil
	}
	if len(s) == ed25519.SignatureSize {
		return []byte(s), nil
	}
	return nil, ErrBadSignature
}

// Validate checks the manifest is well-formed (not authentic — see Verify).
func (m *Manifest) Validate() error {
	if _, err := ParseVersion(m.Version); err != nil {
		return fmt.Errorf("updater: manifest version: %w", err)
	}
	switch m.Channel {
	case ChannelStable, ChannelBeta:
	default:
		return fmt.Errorf("updater: manifest channel %q (want stable|beta)", m.Channel)
	}
	if m.MinVersion != "" {
		if _, err := ParseVersion(m.MinVersion); err != nil {
			return fmt.Errorf("updater: manifest minVersion: %w", err)
		}
	}
	if len(m.Artifacts) == 0 {
		return errors.New("updater: manifest has no artifacts")
	}
	for i, a := range m.Artifacts {
		if a.OS == "" || a.Arch == "" || a.URL == "" {
			return fmt.Errorf("updater: artifact %d missing os/arch/url", i)
		}
		switch a.Kind {
		case KindBinary, KindInstaller, KindArchive:
		default:
			return fmt.Errorf("updater: artifact %d kind %q", i, a.Kind)
		}
		if len(a.SHA256) != 64 || strings.ToLower(a.SHA256) != a.SHA256 {
			return fmt.Errorf("updater: artifact %d sha256 must be 64 lowercase hex chars", i)
		}
		if _, err := parseHexSHA(a.SHA256); err != nil {
			return fmt.Errorf("updater: artifact %d: %w", i, err)
		}
		if a.Size < 0 {
			return fmt.Errorf("updater: artifact %d negative size", i)
		}
	}
	return nil
}

// ArtifactFor picks the artifact for goos/goarch, preferring a bare binary,
// then an installer, then an archive. "universal" arch matches any arch on
// the same OS (macOS universal binaries). prefKinds overrides the default
// preference order when non-empty.
func (m *Manifest) ArtifactFor(goos, goarch string, prefKinds ...string) (*Artifact, error) {
	kinds := prefKinds
	if len(kinds) == 0 {
		kinds = []string{KindBinary, KindInstaller, KindArchive}
	}
	for _, kind := range kinds {
		for _, arch := range []string{goarch, "universal"} {
			for i := range m.Artifacts {
				a := &m.Artifacts[i]
				if a.OS == goos && a.Arch == arch && a.Kind == kind {
					return a, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("%w: %s/%s", ErrNoArtifact, goos, goarch)
}

// GenerateKeyPair creates a fresh ed25519 release-signing key pair.
func GenerateKeyPair() (pub ed25519.PublicKey, priv ed25519.PrivateKey, err error) {
	return ed25519.GenerateKey(rand.Reader)
}

// PublicKeyFromB64 parses a base64 public key (as baked in via -ldflags).
func PublicKeyFromB64(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(s)); err != nil {
			return nil, fmt.Errorf("updater: decode public key: %w", err)
		}
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("updater: public key size %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// DefaultPublicKey returns the compiled-in release public key, if any.
func DefaultPublicKey() (ed25519.PublicKey, error) {
	if PublicKeyB64 == "" {
		return nil, ErrNoPublicKey
	}
	return PublicKeyFromB64(PublicKeyB64)
}

// EncodeKey renders a key as base64 for storage/-ldflags.
func EncodeKey(k []byte) string { return base64.StdEncoding.EncodeToString(k) }

// PrivateKeyFromB64 parses a base64 private key (64-byte ed25519 form, or a
// 32-byte seed which is expanded).
func PrivateKeyFromB64(s string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("updater: decode private key: %w", err)
	}
	switch len(raw) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	default:
		return nil, fmt.Errorf("updater: private key size %d", len(raw))
	}
}
