package config

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Secrets stored outside config.json.
//
// Components that keep credentials in the database (Trakt tokens,
// notification channels, Live TV sources, indexer login cookies, ratings
// keys) seal them with SealField before writing and open them with
// OpenField after reading. A field is bound to the context its owner names
// (table, column, row identity and, for a credential sent somewhere, that
// destination), so a sealed value copied to another row, column or
// destination fails to open instead of authenticating there.
//
// Upgrade. Releases before v2 read these columns as plaintext and would
// send a sealed value to the provider (Trakt then drops the link as
// revoked). Until UpgradeCredentials is called (or, on a cluster node,
// shared settings another node upgraded are loaded) SealField therefore
// returns its input unchanged and config.json keeps the v1 format; the app calls
// it once no rollback to an older release can happen and, in a cluster,
// every live node runs a release that reads v2. It then rewrites
// config.json and the cluster's shared settings in v2, and the components
// re-seal the plaintext rows they find (their MigrateSecrets).

// FieldSealer protects secrets stored outside config.json (*Store
// implements it).
type FieldSealer interface {
	SealField(plain string, context ...string) (string, error)
	OpenField(stored string, context ...string) (plain string, legacy bool, err error)
}

// ErrSealedWithoutKey is returned when a sealed field is read without a
// sealer.
var ErrSealedWithoutKey = errors.New("config: sealed value but no credential key")

// SealField protects plain for storage, bound to context. Empty values
// stay empty. Until the store seals fields (SealsFields) it returns plain
// unchanged.
func (s *Store) SealField(plain string, context ...string) (string, error) {
	if plain == "" || !s.bound.Load() {
		return plain, nil
	}
	aead, err := newAEAD(s.credentialKey)
	if err != nil {
		return "", err
	}
	return sealWithAAD(aead, sealedPrefixV2, plain, fieldAAD(context))
}

// OpenField reverses SealField for the same context. A value stored
// unsealed (written before the upgrade) is returned as is with legacy set,
// so the caller can re-seal it. A sealed value that does not open — wrong
// key, damaged, or moved to another context — is an error.
func (s *Store) OpenField(stored string, context ...string) (string, bool, error) {
	if !strings.HasPrefix(stored, sealedMarker) {
		return stored, stored != "", nil
	}
	if !strings.HasPrefix(stored, sealedPrefixV2) {
		return "", false, fmt.Errorf("config: open %s: unsupported sealed format", fieldName(context))
	}
	aead, err := newAEAD(s.credentialKey)
	if err != nil {
		return "", false, err
	}
	plain, err := openWithAAD(aead, sealedPrefixV2, stored, fieldAAD(context))
	if err != nil {
		return "", false, fmt.Errorf("config: open %s: key mismatch, damaged value, or the value was moved to another record or destination", fieldName(context))
	}
	return plain, false, nil
}

// SealWith is s.SealField, or plain unchanged when s is nil (components
// built without a credential key, such as in tests).
func SealWith(s FieldSealer, plain string, context ...string) (string, error) {
	if s == nil {
		return plain, nil
	}
	return s.SealField(plain, context...)
}

// OpenWith is s.OpenField; with a nil s, a sealed value is
// ErrSealedWithoutKey and anything else is returned as is.
func OpenWith(s FieldSealer, stored string, context ...string) (string, bool, error) {
	if s == nil {
		if strings.HasPrefix(stored, sealedMarker) {
			return "", false, ErrSealedWithoutKey
		}
		return stored, stored != "", nil
	}
	return s.OpenField(stored, context...)
}

// IsSealed reports whether a stored value is sealed (any format).
func IsSealed(stored string) bool { return strings.HasPrefix(stored, sealedMarker) }

// fieldAAD is the additional data of a sealed field.
func fieldAAD(context []string) []byte { return contextAAD("ether-field:v2", context) }

// fieldName names a field in errors: its first two context parts (table
// and column by convention), never row identities or destinations.
func fieldName(context []string) string {
	if len(context) > 2 {
		context = context[:2]
	}
	return strings.Join(context, ".")
}

// FieldContext joins an int64 row identity for a field context.
func FieldContext(id int64) string { return strconv.FormatInt(id, 10) }

// CredentialsUpgraded reports whether this node completed
// UpgradeCredentials (config.json is written in v2).
func (s *Store) CredentialsUpgraded() bool { return s.localBound.Load() }

// SealsFields reports whether SealField seals (database fields and shared
// settings are written in v2), which can precede CredentialsUpgraded on a
// cluster node that loaded settings another node upgraded.
func (s *Store) SealsFields() bool { return s.bound.Load() }

// KeyCreated reports whether Open generated this data directory's
// credential key, i.e. nothing stored before this start was sealed with
// it. Database rows sealed under another key mean the key file was lost.
func (s *Store) KeyCreated() bool { return s.keyCreated }

// UpgradeCredentials switches the store to v2 credentials (see above):
// config.json is rewritten in v2 and, on a cluster node, the shared
// settings are re-sealed in v2 unless another node already did. It is
// idempotent and resumable: config.json is rewritten atomically when the
// store switches (a configuration opened in v2 already has), and the
// shared settings are checked on every call, so a call interrupted
// between the two steps completes on the next.
func (s *Store) UpgradeCredentials(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	var err error
	s.bound.Store(true)
	if !s.localBound.Swap(true) {
		if err = s.save(); err != nil {
			s.localBound.Store(false)
		}
	}
	b, o := s.shared, s.sharedOpts
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("config: rewrite credentials: %w", err)
	}
	if b == nil {
		return nil
	}
	for attempt := 1; ; attempt++ {
		lctx, cancel := context.WithTimeout(ctx, o.Timeout)
		v, err := s.upgradeShared(lctx, b)
		cancel()
		if err == nil {
			if v > 0 {
				s.mu.Lock()
				if v > s.sharedVer {
					s.sharedVer = v
				}
				n := s.notifier
				s.mu.Unlock()
				if n != nil {
					_ = n.Publish(ctx, SharedTopic, []byte(strconv.FormatInt(v, 10)))
				}
			}
			return nil
		}
		if !errors.Is(err, ErrSharedConflict) || attempt >= maxSharedAttempts {
			return fmt.Errorf("config: upgrade shared credentials: %w", err)
		}
	}
}

// upgradeShared re-seals the stored shared document in v2 without
// changing its settings and returns the new version (0: nothing to do).
func (s *Store) upgradeShared(ctx context.Context, b SharedBackend) (int64, error) {
	doc, found, err := b.LoadShared(ctx)
	if err != nil || !found {
		return 0, err
	}
	if fp := s.KeyFingerprint(); doc.KeyFingerprint != fp {
		return 0, ErrKeyMismatch
	}
	plain, stats, err := openDocument([]byte(doc.Doc), s.credentialKey)
	if err != nil {
		return 0, err
	}
	if stats.strict && stats.plaintext == 0 {
		return 0, nil
	}
	sealed, err := sealDocument(plain, s.credentialKey, true)
	if err != nil {
		return 0, err
	}
	return b.SaveShared(ctx, SharedDoc{Doc: string(sealed), KeyFingerprint: doc.KeyFingerprint}, doc.Version)
}
