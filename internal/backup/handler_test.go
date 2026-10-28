package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

const ownerToken = "owner-token"

// snapshotBytes is a real bundle whose config holds the secret "private".
func snapshotBytes(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	conf := filepath.Join(dir, "config.json")
	if err := os.WriteFile(conf, []byte(`{"server":{},"plex":{"token":"private"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(store.OpenTest(t), conf, filepath.Join(dir, "backups"), 2)
	sn, err := s.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, sn.Name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// backupAPI serves s.Handler() behind the real admin API, as the server
// mounts it, with a config whose owner token is ownerToken.
func backupAPI(t *testing.T, s *Service, setupComplete bool) (http.Handler, *config.Store) {
	t.Helper()
	cfg, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Plex.Token, c.SetupComplete = ownerToken, setupComplete
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return admin.New(admin.Deps{
		Config:       cfg,
		IsOwnerToken: func(_ context.Context, tok string) bool { return tok == ownerToken },
		Extensions:   map[string]http.Handler{"adoption/backups": s.Handler()},
	}), cfg
}

type call struct {
	method, path string
	body         []byte
	owner        bool
	contentType  string
	header       map[string]string
}

func (c call) do(h http.Handler) *httptest.ResponseRecorder {
	r := httptest.NewRequest(c.method, "/api/adoption/backups"+c.path, bytes.NewReader(c.body))
	r.RemoteAddr, r.Host = "192.168.1.50:50000", "192.168.1.10:32400"
	r.Header.Set("X-Ether-Request", "1")
	if len(c.body) > 0 {
		ct := c.contentType
		if ct == "" {
			ct = "application/json"
		}
		r.Header.Set("Content-Type", ct)
	}
	if c.owner {
		r.Header.Set("X-Plex-Token", ownerToken)
	}
	for k, v := range c.header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func exportBody(password string) []byte {
	b, _ := json.Marshal(map[string]string{"password": password})
	return b
}

func passwordHeader(p string) map[string]string {
	return map[string]string{"X-Ether-Backup-Password": base64.StdEncoding.EncodeToString([]byte(p))}
}

func errCode(w *httptest.ResponseRecorder) string {
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &e)
	return e.Error.Code
}

func standalone(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	conf := filepath.Join(dir, "config.json")
	if err := os.WriteFile(conf, []byte(`{"server":{},"plex":{"token":"private"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.CredentialKeyFile), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	return New(store.OpenTest(t), conf, filepath.Join(dir, "backups"), 7)
}

func TestBackupAPICreateAndExportNeedOwnerAfterSetup(t *testing.T) {
	s := standalone(t)
	sn, err := s.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	export := "/export/" + sn.Name
	// Before setup completes the LAN gets tokenless admin access, but
	// not the server's secrets.
	pre, _ := backupAPI(t, s, false)
	for _, c := range []call{
		{method: "POST", path: "/"},
		{method: "POST", path: export, body: exportBody(testPassword)},
		{method: "POST", path: "/settings", body: []byte(`{"intervalHours":1,"retain":2}`)},
		{method: "POST", path: export, body: exportBody(testPassword), owner: true},
	} {
		if w := c.do(pre); w.Code != http.StatusForbidden || errCode(w) != "owner_required" {
			t.Fatalf("pre-setup %s %s owner=%v: %d %s", c.method, c.path, c.owner, w.Code, w.Body)
		}
	}
	h, _ := backupAPI(t, s, true)
	if w := (call{method: "POST", path: export, body: exportBody(testPassword)}).do(h); w.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless export after setup: %d", w.Code)
	}
	// No plaintext download path remains.
	if w := (call{method: "GET", path: export, owner: true}).do(h); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET export: %d %s", w.Code, w.Body)
	}
	if w := (call{method: "POST", path: export, body: exportBody("short"), owner: true}).do(h); w.Code != 400 || errCode(w) != "weak_password" {
		t.Fatalf("weak password: %d %s", w.Code, w.Body)
	}
	if w := (call{method: "POST", path: "/", owner: true}).do(h); w.Code != 200 {
		t.Fatalf("owner create: %d %s", w.Code, w.Body)
	}
	w := (call{method: "POST", path: export, body: exportBody(testPassword), owner: true}).do(h)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), strings.TrimSuffix(sn.Name, ".tar.gz")+EncryptedExtension) {
		t.Fatalf("owner export: %d %v", w.Code, w.Header())
	}
	enc := w.Body.Bytes()
	plain, err := os.ReadFile(filepath.Join(s.dir, sn.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(enc) || bytes.Contains(enc, bytes.Repeat([]byte{7}, 32)) {
		t.Fatal("export is not encrypted")
	}
	if got, err := decrypt(enc, testPassword); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("export does not decrypt to the bundle: %v", err)
	}
}

func TestBackupAPIRestore(t *testing.T) {
	bundle := snapshotBytes(t)
	enc := encrypt(t, bundle, testPassword)
	restored := func(s *Service) []string {
		entries, _ := os.ReadDir(filepath.Dir(s.configPath))
		var out []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "restored-") || strings.HasPrefix(e.Name(), ".restore-") {
				out = append(out, e.Name())
			}
		}
		return out
	}
	upload := func(body []byte, owner bool, header map[string]string) call {
		return call{method: "POST", path: "/restore", body: body, owner: owner, contentType: "application/octet-stream", header: header}
	}

	t.Run("fresh server stages an encrypted backup", func(t *testing.T) {
		s := standalone(t)
		before, _ := os.ReadFile(s.configPath)
		h, cfg := backupAPI(t, s, false)
		if w := upload(enc, false, nil).do(h); w.Code != 400 || errCode(w) != "password_required" {
			t.Fatalf("no password: %d %s", w.Code, w.Body)
		}
		if w := upload(enc, false, passwordHeader("wrong horse battery")).do(h); w.Code != 400 || errCode(w) != "bad_password" || strings.Contains(w.Body.String(), "wrong horse") {
			t.Fatalf("wrong password: %d %s", w.Code, w.Body)
		}
		if got := restored(s); len(got) != 0 {
			t.Fatalf("failed restore left %v", got)
		}
		w := upload(enc, false, passwordHeader(testPassword)).do(h)
		var res struct{ Path string }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Path == "" {
			t.Fatalf("restore: %d %s", w.Code, w.Body)
		}
		// Staged only: the running server's settings are untouched.
		if after, _ := os.ReadFile(s.configPath); !bytes.Equal(before, after) || cfg.Get().SetupComplete {
			t.Fatal("restore changed the running server")
		}
		if b, err := os.ReadFile(filepath.Join(res.Path, "config.json")); err != nil || !strings.Contains(string(b), "private") {
			t.Fatalf("staged config %s %v", b, err)
		}
	})

	t.Run("unencrypted backup needs the owner and a confirmation", func(t *testing.T) {
		s := standalone(t)
		pre, _ := backupAPI(t, s, false)
		confirm := map[string]string{"X-Ether-Backup-Legacy": "plaintext"}
		if w := upload(bundle, false, confirm).do(pre); w.Code != http.StatusForbidden || errCode(w) != "owner_required" {
			t.Fatalf("tokenless legacy restore: %d %s", w.Code, w.Body)
		}
		h, _ := backupAPI(t, s, true)
		if w := upload(bundle, true, nil).do(h); w.Code != 400 || errCode(w) != "legacy_confirm" {
			t.Fatalf("unconfirmed legacy restore: %d %s", w.Code, w.Body)
		}
		if got := restored(s); len(got) != 0 {
			t.Fatalf("refused restore left %v", got)
		}
		if w := upload(bundle, true, confirm).do(h); w.Code != 200 {
			t.Fatalf("owner legacy restore: %d %s", w.Code, w.Body)
		}
	})

	t.Run("upload content type and origin", func(t *testing.T) {
		s := standalone(t)
		h, _ := backupAPI(t, s, true)
		c := upload(enc, true, passwordHeader(testPassword))
		c.contentType = "text/plain"
		if w := c.do(h); w.Code != http.StatusForbidden {
			t.Fatalf("form-encodable restore body: %d", w.Code)
		}
		c = upload(enc, true, map[string]string{"Origin": "https://evil.example.com", "X-Ether-Backup-Password": passwordHeader(testPassword)["X-Ether-Backup-Password"]})
		if w := c.do(h); w.Code != http.StatusForbidden {
			t.Fatalf("cross-origin restore: %d", w.Code)
		}
		// Raw bodies are only for the restore upload.
		c = call{method: "POST", path: "/settings", body: []byte(`{"intervalHours":1,"retain":2}`), owner: true, contentType: "application/octet-stream"}
		if w := c.do(h); w.Code != http.StatusForbidden {
			t.Fatalf("octet-stream settings: %d", w.Code)
		}
		c = upload(enc, true, passwordHeader(testPassword))
		c.header["Origin"] = "http://192.168.1.10:32400"
		if w := c.do(h); w.Code != 200 {
			t.Fatalf("same-origin restore: %d %s", w.Code, w.Body)
		}
	})
}

// A bundle without the credential key cannot carry sealed values.
func TestRestoreRefusesSealedDataWithoutKey(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ name, config, row string }{
		{"config", `{"server":{},"plex":{"token":"ether-sealed:v2:abc"}}`, "plain"},
		{"database", `{"server":{}}`, "ether-sealed:v2:abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			conf := filepath.Join(dir, "config.json")
			db := store.OpenTest(t)
			if _, err := db.ExecContext(ctx, `CREATE TABLE sealed_probe(v TEXT)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO sealed_probe(v) VALUES(?)`, tc.row); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(conf, []byte(`{"server":{}}`), 0600); err != nil {
				t.Fatal(err)
			}
			s := New(db, conf, filepath.Join(dir, "backups"), 2)
			sn, err := s.Create(ctx)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(s.dir, sn.Name))
			if err != nil {
				t.Fatal(err)
			}
			b = rewriteEntry(t, b, "config.json", []byte(tc.config))
			dest := filepath.Join(dir, "restored")
			if err := RestoreBundle(ctx, bytes.NewReader(b), dest); err == nil || !strings.Contains(err.Error(), "no credential key") {
				t.Fatalf("restored sealed %s without key: %v", tc.name, err)
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatal("destination published")
			}
		})
	}
}

func TestFileContainsAcrossReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	b := bytes.Repeat([]byte{'x'}, 1<<20-5)
	b = append(b, "ether-sealed:"...)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if ok, err := fileContains(path, "ether-sealed:"); !ok || err != nil {
		t.Fatalf("match across read boundary: %v %v", ok, err)
	}
	if ok, _ := fileContains(path, "ether-sealed:v9"); ok {
		t.Fatal("false match")
	}
}

// rewriteEntry replaces one bundle entry and its manifest checksum.
func rewriteEntry(t *testing.T, bundle []byte, name string, content []byte) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		names, files[h.Name] = append(names, h.Name), b
	}
	files[name] = content
	var m Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	m.SHA256[name] = hex.EncodeToString(sum[:])
	files["manifest.json"], _ = json.Marshal(m)
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	for _, n := range names {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0600, Size: int64(len(files[n]))})
		tw.Write(files[n])
	}
	tw.Close()
	gw.Close()
	return out.Bytes()
}
