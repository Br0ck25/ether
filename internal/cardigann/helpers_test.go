package cardigann

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func loadDef(t *testing.T, name string) *Definition {
	t.Helper()
	def, warnings, err := Parse(readFixture(t, name))
	if err != nil {
		t.Fatalf("parse %s: %v (warnings %v)", name, err, warnings)
	}
	return def
}

type runnerSetup struct {
	settings map[string]string
	sessions *sessionStore
	lock     LoginLock
	flare    string
	id       string
}

func testRunner(t *testing.T, def *Definition, site string, s runnerSetup) *Runner {
	t.Helper()
	if s.id == "" {
		s.id = def.ID + "-ix"
	}
	r, err := newRunner(def, runnerOptions{
		IndexerID: s.id, SiteLink: site, Settings: s.settings, Sessions: s.sessions, Lock: s.lock,
		FlareURL: func() string { return s.flare }, Logger: quietLog(), Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// torrentFile builds a minimal single-file .torrent and its info hash.
func torrentFile(name string) ([]byte, string) {
	info := fmt.Sprintf("d6:lengthi1048576e4:name%d:%s12:piece lengthi262144e6:pieces20:aaaaaaaaaaaaaaaaaaaae", len(name), name)
	sum := sha1.Sum([]byte(info))
	return []byte("d8:announce22:http://tracker.test/an4:info" + info + "e"), hex.EncodeToString(sum[:])
}

// budget records reservations.
type budget struct {
	mu          sync.Mutex
	hits, grabs int
}

func (b *budget) reserve(_ context.Context, kind string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if kind == "hit" {
		b.hits++
	} else {
		b.grabs++
	}
	return nil
}
