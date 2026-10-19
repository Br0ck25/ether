package plextv

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"os"
	"time"
)

// Cluster nodes share one plex.direct certificate. With
// CertManagerOptions.Shared set, the certificate, its key and the request
// state (back-off) live in the cluster database, sealed with the cluster's
// credential key; each node keeps a local file copy it serves from. Only
// the node allowed to renew (the leader, see SetRenewalAllowed) asks
// plex.tv for certificates and writes them back; the others pick renewals
// up through the event bus (SetEventBus) or by polling (RunSharedSync).
// Owner-supplied custom certificates are per node and never shared.

// SharedCertStore holds named, sealed blobs shared by the cluster
// (implemented by clusterconfig.Secrets). Version 0 means absent.
type SharedCertStore interface {
	Get(ctx context.Context, name string) ([]byte, int64, error)
	Put(ctx context.Context, name string, data []byte) (int64, error)
	Version(ctx context.Context, name string) (int64, error)
}

// EventBus is the cluster event bus as the certificate manager uses it.
type EventBus interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	Subscribe(topic string, fn func([]byte)) (unsubscribe func())
}

const (
	// SharedCertName is the shared blob holding the certificate.
	SharedCertName = "plextv/plexdirect"
	// CertTopic is published after a node stores a new certificate.
	CertTopic = "certs:changed"
	// DefaultCertSyncInterval is RunSharedSync's default poll interval.
	DefaultCertSyncInterval = time.Minute
)

// ErrRenewalDeferred: this node may not request certificates (another
// cluster node is the leader).
var ErrRenewalDeferred = errors.New("plextv: certificate renewal is handled by the cluster leader")

type sharedCertBlob struct {
	CertPEM []byte    `json:"cert,omitempty"`
	KeyPEM  []byte    `json:"key,omitempty"`
	State   certState `json:"state"`
}

// SetRenewalAllowed installs the predicate deciding whether this node may
// request certificates from plex.tv (cluster wiring passes the leader
// check). Nil, the default, always allows (a standalone server).
func (m *CertManager) SetRenewalAllowed(fn func() bool) {
	m.mu.Lock()
	m.allowRenew = fn
	m.mu.Unlock()
}

func (m *CertManager) renewalAllowed() bool {
	m.mu.RLock()
	fn := m.allowRenew
	m.mu.RUnlock()
	return fn == nil || fn()
}

// SetEventBus subscribes to certificate change announcements from other
// nodes (and the bus's resync notice) and announces this node's writes.
// Nil disconnects.
func (m *CertManager) SetEventBus(bus EventBus) {
	m.mu.Lock()
	old := m.unsubs
	m.unsubs, m.bus = nil, bus
	m.mu.Unlock()
	for _, u := range old {
		if u != nil {
			u()
		}
	}
	if bus == nil || m.o.Shared == nil {
		return
	}
	sync := func([]byte) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := m.SyncShared(ctx); err != nil {
				m.o.Logger.Warn("sync shared certificate", "err", err)
			}
		}()
	}
	unsubs := []func(){bus.Subscribe(CertTopic, sync), bus.Subscribe("bus:resync", sync)}
	m.mu.Lock()
	m.unsubs = append(m.unsubs, unsubs...)
	m.mu.Unlock()
}

// RunSharedSync polls the shared certificate every interval (default
// DefaultCertSyncInterval) until ctx ends. It returns at once when the
// manager has no shared store.
func (m *CertManager) RunSharedSync(ctx context.Context, every time.Duration) {
	if m.o.Shared == nil {
		return
	}
	if every <= 0 {
		every = DefaultCertSyncInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := m.SyncShared(sctx); err != nil && ctx.Err() == nil {
			m.o.Logger.Warn("sync shared certificate", "err", err)
		}
		cancel()
	}
}

// SyncShared installs the cluster's certificate when another node stored a
// newer one, and seeds the shared copy from this node's plex.direct
// certificate when the cluster has none. It reports whether the served
// certificate changed.
func (m *CertManager) SyncShared(ctx context.Context) (bool, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.syncSharedLocked(ctx)
}

func (m *CertManager) syncSharedLocked(ctx context.Context) (bool, error) {
	if m.o.Shared == nil || m.custom() {
		return false, nil
	}
	v, err := m.o.Shared.Version(ctx, SharedCertName)
	if err != nil {
		return false, err
	}
	m.mu.RLock()
	have := m.sharedVer
	m.mu.RUnlock()
	if v == have && v != 0 {
		return false, nil
	}
	if v == 0 {
		// Nothing shared yet: offer ours if it is a real plex.direct one.
		if cur := m.Info(); cur.Present && !cur.SelfSigned {
			return false, m.pushSharedLocked(ctx, m.currentState())
		}
		return false, nil
	}
	raw, v, err := m.o.Shared.Get(ctx, SharedCertName)
	if err != nil || v == 0 {
		return false, err
	}
	var blob sharedCertBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return false, err
	}
	changed := false
	if len(blob.CertPEM) > 0 {
		pair, err := tls.X509KeyPair(blob.CertPEM, blob.KeyPEM)
		if err != nil {
			return false, err
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return false, err
		}
		pair.Leaf = leaf
		cur := m.Info()
		// The stored certificate is the cluster's: a newer version replaces
		// whatever this node serves.
		if !cur.Present || cur.SelfSigned || Fingerprint(leaf) != cur.Fingerprint {
			if err := m.writeFiles(blob.CertPEM, blob.KeyPEM); err != nil {
				return false, err
			}
			m.install(&pair, leaf, m.certPath(), m.keyPath(), false)
			changed = true
			m.o.Logger.Info("installed the cluster's plex.direct certificate", "expires", leaf.NotAfter)
		}
	}
	m.saveState(blob.State)
	m.mu.Lock()
	m.info.LastAttempt, m.info.LastError, m.info.RetryAfter = blob.State.LastAttempt, blob.State.LastError, blob.State.RetryAfter
	m.sharedVer = v
	m.mu.Unlock()
	return changed, nil
}

func (m *CertManager) currentState() certState {
	info := m.Info()
	return certState{LastAttempt: info.LastAttempt, LastError: info.LastError, Hash: info.Hash, RetryAfter: info.RetryAfter}
}

// pushSharedLocked stores the served certificate (unless self-signed, which
// stays local) and st as the cluster's, and announces it.
func (m *CertManager) pushSharedLocked(ctx context.Context, st certState) error {
	if m.o.Shared == nil || m.custom() {
		return nil
	}
	blob := sharedCertBlob{State: st}
	if cur := m.Info(); cur.Present && !cur.SelfSigned {
		certPEM, err := os.ReadFile(m.certPath())
		if err != nil {
			return err
		}
		keyPEM, err := os.ReadFile(m.keyPath())
		if err != nil {
			return err
		}
		blob.CertPEM, blob.KeyPEM = certPEM, keyPEM
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		return err
	}
	v, err := m.o.Shared.Put(ctx, SharedCertName, raw)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.sharedVer = v
	bus := m.bus
	m.mu.Unlock()
	if bus != nil && len(blob.CertPEM) > 0 {
		if err := bus.Publish(ctx, CertTopic, []byte(time.Now().UTC().Format(time.RFC3339))); err != nil {
			m.o.Logger.Warn("announce certificate", "err", err)
		}
	}
	return nil
}

// writeFiles replaces the local certificate and key copies.
func (m *CertManager) writeFiles(certPEM, keyPEM []byte) error {
	if m.o.Dir == "" {
		return errors.New("plextv: no certificate directory")
	}
	if b, err := os.ReadFile(m.certPath()); err == nil && bytes.Equal(b, certPEM) {
		if k, err := os.ReadFile(m.keyPath()); err == nil && bytes.Equal(k, keyPEM) {
			return nil
		}
	}
	if err := writeFileAtomic(m.keyPath(), keyPEM, 0o600); err != nil {
		return err
	}
	return writeFileAtomic(m.certPath(), certPEM, 0o644)
}
