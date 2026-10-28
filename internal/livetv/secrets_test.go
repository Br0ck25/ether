package livetv

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func upgradedSealer(t *testing.T) *config.Store {
	t.Helper()
	s, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpgradeCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func rawSource(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT data FROM livetv_sources WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestXtreamSourceSealedAtRestAndStillAuthenticates(t *testing.T) {
	ctx := context.Background()
	up := provider(t, func(r *http.Request) bool {
		q := r.URL.Query()
		return q.Get("username") == "xtream-user" && q.Get("password") == "xtream-pass"
	})
	s := testService(t, Options{HTTPClient: up.Client(), Sealer: upgradedSealer(t)})
	if _, err := s.ImportSource(ctx, Source{ID: "x", Name: "Panel", Auth: &SourceAuth{Type: "xtream", Server: up.URL + "/", Username: "xtream-user", Password: "xtream-pass"}}); err != nil {
		t.Fatal(err)
	}
	raw := rawSource(t, s.o.DB, "x")
	if strings.Contains(raw, "xtream-pass") || strings.Contains(raw, "xtream-user") || strings.Contains(raw, "get.php") {
		t.Fatalf("source credentials at rest: %s", raw)
	}
	// Refresh and the scheduler read the sealed row.
	if _, err := s.RefreshSource(ctx, "x"); err != nil {
		t.Fatalf("refresh from sealed row: %v", err)
	}
	s.refreshDueSources(ctx) // reads the sealed URLs for scheduling only
	listed := sourceByID(t, s, "x")
	if listed.Auth.Username != "xtream-user" || listed.Auth.Password != redactedSecret || !listed.HasPlaylist {
		t.Fatalf("listed: %+v", listed.Auth)
	}
	// Re-saving with the placeholder for another panel is refused.
	body := `{"id":"x","name":"Panel","auth":{"type":"xtream","server":"https://evil.test/","username":"xtream-user","password":"` + redactedSecret + `"}}`
	var out Source
	if code := ownerJSON(t, s, "PUT", "/sources/x", body, &out); code == 200 {
		t.Fatal("placeholder carried to another server")
	}
	// The same panel keeps the stored password.
	body = `{"id":"x","name":"Renamed","auth":{"type":"xtream","server":"` + up.URL + `/","username":"xtream-user","password":"` + redactedSecret + `"}}`
	if code := ownerJSON(t, s, "PUT", "/sources/x", body, &out); code != 200 {
		t.Fatalf("placeholder for the same server: %d", code)
	}
}

func TestSourceHeadersSealedAndBoundToDestination(t *testing.T) {
	ctx := context.Background()
	s := testService(t, Options{Sealer: upgradedSealer(t)})
	src := Source{ID: "h", Name: "Keyed", Playlist: m3uFixture("h=One"), Auth: &SourceAuth{Type: "headers", Headers: map[string]string{"X-Api-Key": "header-secret-value"}}}
	if _, err := s.ImportSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	raw := rawSource(t, s.o.DB, "h")
	if strings.Contains(raw, "header-secret-value") {
		t.Fatal("header value at rest")
	}
	imported := StreamSource{ID: stableID("h", "playlist")}
	h, err := s.upstreamHeaders(ctx, Channel{ID: "h", SourceID: "h", StreamSources: []StreamSource{imported}}, imported)
	if err != nil || h.Get("X-Api-Key") != "header-secret-value" {
		t.Fatalf("headers from sealed row: %v %v", h, err)
	}
	// Repoint the stored source by hand: the bound header no longer opens.
	var stored map[string]any
	_ = json.Unmarshal([]byte(raw), &stored)
	stored["auth"].(map[string]any)["type"] = "bearer"
	b, _ := json.Marshal(stored)
	if _, err := s.o.DB.Exec(`UPDATE livetv_sources SET data=? WHERE id='h'`, string(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.storedSourceByID(ctx, "h"); err == nil {
		t.Fatal("repointed source opened")
	}
}

func TestSourceMigrationLeavesUnreadableRows(t *testing.T) {
	ctx := context.Background()
	plain := testService(t, Options{})
	if _, err := plain.ImportSource(ctx, Source{ID: "a", Name: "A", Playlist: m3uFixture("a=One"), Auth: &SourceAuth{Type: "bearer", Token: "bearer-secret"}}); err != nil {
		t.Fatal(err)
	}
	// A URL-backed source row as an older release stored it.
	if _, err := plain.o.DB.Exec(`INSERT INTO livetv_sources(id, data) VALUES('b', ?)`, `{"id":"b","name":"B","playlistUrl":"https://iptv.test/list.m3u?token=url-secret","updatedAt":"2026-01-01T00:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawSource(t, plain.o.DB, "a"), "bearer-secret") {
		t.Fatal("fixture should start in plaintext")
	}
	s := testService(t, Options{DB: plain.o.DB, Sealer: upgradedSealer(t)})
	n, err := s.MigrateSecrets(ctx)
	if err != nil || n != 2 {
		t.Fatalf("migration: %d %v", n, err)
	}
	if strings.Contains(rawSource(t, s.o.DB, "a"), "bearer-secret") || strings.Contains(rawSource(t, s.o.DB, "b"), "url-secret") {
		t.Fatal("migrated rows still plaintext")
	}
	if got, err := s.storedSourceByID(ctx, "b"); err != nil || got.PlaylistURL != "https://iptv.test/list.m3u?token=url-secret" {
		t.Fatalf("migrated source: %v", err)
	}
	if n, err := s.MigrateSecrets(ctx); err != nil || n != 0 {
		t.Fatalf("rerun: %d %v", n, err)
	}
	// Another key cannot open the rows; nothing is rewritten.
	before := rawSource(t, s.o.DB, "a")
	other := testService(t, Options{DB: plain.o.DB, Sealer: upgradedSealer(t)})
	if _, err := other.MigrateSecrets(ctx); err == nil {
		t.Fatal("wrong key migration reported success")
	}
	if _, err := other.Sources(ctx); err == nil {
		t.Fatal("wrong key listed sources")
	}
	if rawSource(t, s.o.DB, "a") != before {
		t.Fatal("unreadable row rewritten")
	}
	if _, err := plain.storedSourceByID(ctx, "a"); !errors.Is(err, config.ErrSealedWithoutKey) {
		t.Fatalf("nil sealer: %v", err)
	}
}
