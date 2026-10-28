package backup

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/admin"
)

// Handler is the owner's backup API, mounted behind the admin API's
// requireAuth (which also enforces the CSRF header):
//
//	GET  /               bundles, schedule and a filesystem warning
//	POST /settings       change the schedule              owner, setup complete
//	POST /               create a bundle now              owner, setup complete
//	POST /export/{name}  {"password"} → .etherbackup      owner, setup complete
//	POST /restore        stage an uploaded bundle (below)
//
// A bundle holds config.json, the database and the credential key that
// unseals them, so it leaves the server only encrypted with a password the
// owner chooses (encrypted.go). Bundles kept in the data directory stay
// owner-only (0600) files.
//
// Restore takes the raw file (application/octet-stream). An encrypted
// bundle needs its password in X-Ether-Backup-Password (base64 of the
// UTF-8 password) and is accepted wherever the admin API is: from the
// owner, or before setup completes from the local network (a fresh server
// being restored from backup). An unencrypted bundle from before
// encryption needs the signed-in owner and X-Ether-Backup-Legacy:
// plaintext. Either way the bundle is only validated and staged in a new
// directory; nothing replaces the running server until the operator
// restarts Ether from that directory.
func (s *Service) Handler() http.Handler {
	m := http.NewServeMux()
	fail := func(w http.ResponseWriter, status int, code, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	}
	reply := func(w http.ResponseWriter, v any, e error) {
		if e != nil {
			fail(w, http.StatusBadRequest, "invalid_request", e.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	// ownerReady admits only the signed-in owner of a set-up server:
	// tokenless setup access must not take the server's secrets away.
	ownerReady := func(w http.ResponseWriter, r *http.Request) bool {
		if a, ok := admin.RequestAccess(r.Context()); ok && a.Owner && a.SetupComplete {
			return true
		}
		fail(w, http.StatusForbidden, "owner_required", "finish setup and sign in as the owner to create or download backups")
		return false
	}
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		snaps, e := s.Snapshots(r.Context())
		v, se := s.Settings(r.Context())
		if e == nil {
			e = se
		}
		reply(w, map[string]any{"snapshots": snaps, "settings": v, "warning": FilesystemWarning(r.Context(), filepath.Dir(s.configPath))}, e)
	})
	m.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		if !ownerReady(w, r) {
			return
		}
		var q struct {
			Settings
			SharedDir *string `json:"sharedDir"` // absent: unchanged
		}
		e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&q)
		if e == nil {
			if q.SharedDir != nil {
				q.Settings.SharedDir = *q.SharedDir
			} else if cur, ce := s.Settings(r.Context()); ce == nil {
				q.Settings.SharedDir = cur.SharedDir
			}
			e = s.Configure(r.Context(), q.Settings)
		}
		reply(w, map[string]bool{"ok": e == nil}, e)
	})
	m.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !ownerReady(w, r) {
			return
		}
		v, e := s.Create(r.Context())
		reply(w, v, e)
	})
	m.HandleFunc("POST /export/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !ownerReady(w, r) {
			return
		}
		name := r.PathValue("name")
		if filepath.Base(name) != name || !strings.HasPrefix(name, "ether-") || !strings.HasSuffix(name, ".tar.gz") {
			fail(w, http.StatusNotFound, "not_found", "no such backup")
			return
		}
		var q struct {
			Password string `json:"password"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&q) != nil {
			fail(w, http.StatusBadRequest, "bad_json", "send the export password as JSON")
			return
		}
		if e := checkPassword(q.Password, true); e != nil {
			fail(w, http.StatusBadRequest, "weak_password", e.Error())
			return
		}
		path, e := s.locate(r.Context(), name)
		var elsewhere errElsewhere
		if errors.As(e, &elsewhere) {
			fail(w, http.StatusNotFound, "on_other_node", elsewhere.Error())
			return
		}
		if e != nil {
			fail(w, http.StatusNotFound, "not_found", "no such backup")
			return
		}
		f, e := os.Open(path)
		if e != nil {
			fail(w, http.StatusNotFound, "not_found", "no such backup")
			return
		}
		defer f.Close()
		if st, e := f.Stat(); e != nil || !st.Mode().IsRegular() {
			fail(w, http.StatusNotFound, "not_found", "no such backup")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.TrimSuffix(name, ".tar.gz")+EncryptedExtension+`"`)
		ew, e := NewEncryptWriter(w, q.Password)
		if e == nil {
			if _, e = io.Copy(ew, f); e == nil {
				e = ew.Close()
			}
		}
		if e != nil {
			// The body may have started: drop the connection so the
			// client sees a failed download, never a short "complete"
			// file (which would not restore anyway: its final chunk is
			// missing).
			panic(http.ErrAbortHandler)
		}
	})
	m.HandleFunc("POST /restore", func(w http.ResponseWriter, r *http.Request) {
		access, ok := admin.RequestAccess(r.Context())
		if !ok {
			fail(w, http.StatusForbidden, "owner_required", "sign in as the owner to restore a backup")
			return
		}
		// One restore at a time: each can take a few GB of disk and a
		// deliberately slow password check.
		if !s.restoring.TryLock() {
			fail(w, http.StatusConflict, "busy", "another restore is in progress")
			return
		}
		defer s.restoring.Unlock()
		body := bufio.NewReaderSize(http.MaxBytesReader(w, r.Body, maxBundleFile+32<<20), 64<<10)
		head, _ := body.Peek(len(encMagic))
		dest := filepath.Join(filepath.Dir(s.configPath), "restored-"+time.Now().UTC().Format("20060102T150405.000000000"))
		var e error
		if IsEncrypted(head) {
			password, ok := backupPassword(r)
			if !ok {
				fail(w, http.StatusBadRequest, "password_required", "enter the password the backup was downloaded with")
				return
			}
			e = RestoreEncrypted(r.Context(), body, password, dest)
			if errors.Is(e, ErrBadPassword) {
				fail(w, http.StatusBadRequest, "bad_password", e.Error())
				return
			}
		} else {
			if !access.Owner {
				fail(w, http.StatusForbidden, "owner_required", "an unencrypted backup can only be restored by the signed-in owner")
				return
			}
			if r.Header.Get("X-Ether-Backup-Legacy") != "plaintext" {
				fail(w, http.StatusBadRequest, "legacy_confirm", "this is an unencrypted backup from an older version; confirm to restore it")
				return
			}
			e = RestoreBundle(r.Context(), body, dest)
		}
		msg := "Validated restore staged. Stop Ether and start it with ETHER_DATA_DIR pointing to this directory."
		if _, se := os.Stat(filepath.Join(dest, LogicalDumpFile)); se == nil {
			msg = "Validated restore staged. Stop Ether and start it with ETHER_DATA_DIR pointing to this directory and ETHER_DATABASE_URL pointing to an empty PostgreSQL database (or none, for a standalone server); the data is imported on first start."
		}
		reply(w, map[string]string{"path": dest, "message": msg}, e)
	})
	return m
}

// backupPassword reads the restore password header (base64 of UTF-8, as
// header values cannot carry arbitrary text).
func backupPassword(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get("X-Ether-Backup-Password"))
	if raw == "" {
		return "", false
	}
	b, e := base64.StdEncoding.DecodeString(raw)
	if e != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}
