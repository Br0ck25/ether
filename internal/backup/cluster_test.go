package backup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/store"
)

// clusterNodes returns two backup services sharing one database, each
// with its own data directory, as two cluster nodes would have.
func clusterNodes(t *testing.T) (a, b *Service) {
	t.Helper()
	db := store.OpenTest(t) // PostgreSQL when ETHER_TEST_POSTGRES is set
	node := func(id string) *Service {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.json")
		if err := os.WriteFile(cfg, []byte(`{"server":{"friendlyName":"`+id+`"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		s := New(db, cfg, filepath.Join(dir, "backups"), 7)
		s.SetCluster(Node{ID: "id-" + id, Name: id})
		return s
	}
	return node("a"), node("b")
}

func byName(snaps []Snapshot, name string) (Snapshot, bool) {
	for _, s := range snaps {
		if s.Name == name {
			return s, true
		}
	}
	return Snapshot{}, false
}

func TestClusterBackupsListEveryNode(t *testing.T) {
	ctx := context.Background()
	a, b := clusterNodes(t)
	sa, err := a.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sb.Node != "b" || sb.NodeID != "id-b" || sb.Shared {
		t.Fatalf("created %+v", sb)
	}
	list, err := a.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list %+v", list)
	}
	if got, _ := byName(list, sa.Name); got.Node != "a" || got.Remote || got.Size == 0 {
		t.Fatalf("own bundle %+v", got)
	}
	if got, _ := byName(list, sb.Name); got.Node != "b" || !got.Remote {
		t.Fatalf("other node's bundle %+v", got)
	}

	// Downloading another node's bundle explains where it is.
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/export/"+sb.Name, nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `node \"b\"`) {
		t.Fatalf("export of b's bundle on a: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/export/"+sa.Name, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("export of a's own bundle: %d", w.Code)
	}

	// A bundle removed from b's disk drops out of the list once b looks.
	if err := os.Remove(filepath.Join(b.dir, sb.Name)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Snapshots(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ = a.Snapshots(ctx)
	if _, ok := byName(list, sb.Name); ok || len(list) != 1 {
		t.Fatalf("removed bundle still listed: %+v", list)
	}
}

func TestClusterBackupsSharedDirectory(t *testing.T) {
	ctx := context.Background()
	a, b := clusterNodes(t)
	shared := t.TempDir()
	// Settings are shared (one database): set on a, used by b.
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/settings", strings.NewReader(`{"intervalHours":24,"retain":2,"sharedDir":`+strconv.Quote(shared)+`}`)))
	if w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	// A client that does not know the field leaves it alone.
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/settings", strings.NewReader(`{"intervalHours":12,"retain":2}`)))
	if st, _ := b.Settings(ctx); w.Code != 200 || st.SharedDir != shared || st.IntervalHours != 12 {
		t.Fatalf("settings after partial update: %+v (%d)", st, w.Code)
	}
	var names []string
	for i := 0; i < 3; i++ {
		sn, err := b.Create(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !sn.Shared {
			t.Fatalf("not written to the shared directory: %+v", sn)
		}
		names = append(names, sn.Name)
	}
	if _, err := os.Stat(filepath.Join(shared, names[2])); err != nil {
		t.Fatal(err)
	}
	if own, _ := b.List(); len(own) != 0 {
		t.Fatalf("bundle written to b's own directory: %+v", own)
	}
	list, err := a.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Retention (2) applies to the shared directory.
	if len(list) != 2 {
		t.Fatalf("list %+v", list)
	}
	got, ok := byName(list, names[2])
	if !ok || !got.Shared || got.Remote || got.Node != "b" {
		t.Fatalf("shared bundle %+v", got)
	}
	// Any node can serve a shared bundle.
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/export/"+names[2], nil))
	if w.Code != http.StatusOK {
		t.Fatalf("export of a shared bundle on a: %d", w.Code)
	}
	var body struct {
		Settings Settings `json:"settings"`
	}
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Settings.SharedDir != shared {
		t.Fatalf("settings in listing: %+v %v", body.Settings, err)
	}
	if err := a.Configure(ctx, Settings{IntervalHours: 1, Retain: 1, SharedDir: "relative/dir"}); err == nil {
		t.Fatal("relative shared directory accepted")
	}
}

// A standalone server lists only its own directory, rejects a shared
// directory and returns the same JSON as before.
func TestStandaloneBackupsUnchanged(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"server":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(store.OpenTest(t), cfg, filepath.Join(dir, "backups"), 7)
	sn, err := s.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sn.Node != "" || sn.NodeID != "" {
		t.Fatalf("standalone snapshot tagged with a node: %+v", sn)
	}
	if err := s.Configure(ctx, Settings{IntervalHours: 1, Retain: 1, SharedDir: dir}); err == nil {
		t.Fatal("standalone server accepted a shared directory")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	for _, field := range []string{"node", "nodeId", "shared", "remote", "sharedDir"} {
		if strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Fatalf("standalone listing has %q: %s", field, w.Body.String())
		}
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_files`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("standalone bundle recorded: n=%d err=%v", n, err)
	}
}
