package jellyfin

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebClient(t *testing.T) {
	f := newFixture(t)

	// Without a web client the root is the landing page and /web is not a file.
	if rr := f.call("GET", "/", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("landing: %d", rr.Code)
	}

	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "strings"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>jf</body></html>"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "strings", "en-US.json"), []byte(`{"a":1}`), 0o644))
	f.srv.d.WebDir = dir

	if rr := f.call("GET", "/", "", nil); rr.Code != http.StatusFound || rr.Header().Get("Location") != "/web/" {
		t.Fatalf("root redirect: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if rr := f.call("GET", "/web/", "", nil); rr.Code != http.StatusOK || rr.Body.String() != `<html>jf<script src="/ether/home.js" defer></script></body></html>` {
		t.Fatalf("web index: %d %q", rr.Code, rr.Body.String())
	}
	if rr := f.call("GET", "/web/index.html", "", nil); rr.Code != http.StatusMovedPermanently && rr.Code != http.StatusOK {
		t.Fatalf("web index.html: %d", rr.Code)
	}
	// File names keep their case; the API stays case-insensitive.
	if rr := f.call("GET", "/web/strings/en-US.json", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("case-sensitive asset: %d", rr.Code)
	}
	if rr := f.call("GET", "/web/strings/missing.json", "", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("missing asset: %d", rr.Code)
	}
	if rr := f.call("GET", "/System/Info/Public", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("api still served: %d", rr.Code)
	}
}

// The Jellyfin web client hides Play on items whose LocationType is Virtual
// (Jellyfin's marker for missing episodes), so catalogue titles must not be.
func TestTitlesAreNotVirtual(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	w := f.call("GET", "/Items/"+itemID(fightClub.Key)+"?userId="+userID(ownerUser.ID), tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("item: %d %s", w.Code, w.Body)
	}
	if got := decode[map[string]any](t, w)["LocationType"]; got != "FileSystem" {
		t.Fatalf("LocationType = %v", got)
	}
}

// Playback requests are logged with their outcome, without the token.
func TestPlaybackRequestsAreLogged(t *testing.T) {
	f := newFixture(t)
	var buf strings.Builder
	f.srv.log = slog.New(slog.NewTextHandler(&buf, nil))
	tok := f.login(ownerUser)
	f.call("POST", "/Items/"+itemID(fightClub.Key)+"/PlaybackInfo?api_key="+tok, tok, map[string]any{"DeviceProfile": webProfile()})
	f.call("GET", "/Items", tok, nil) // not playback
	out := buf.String()
	if !strings.Contains(out, "jellyfin: playback request") || !strings.Contains(out, "status=200") || !strings.Contains(out, "PlaybackInfo") {
		t.Fatalf("playback request not logged: %q", out)
	}
	if strings.Contains(out, tok) || strings.Count(out, "playback request") != 1 {
		t.Fatalf("token leaked or extra lines: %q", out)
	}
}

// A refused request is logged with why, and never with the token it sent.
func TestRefusalsAreLogged(t *testing.T) {
	f := newFixture(t)
	var buf strings.Builder
	f.srv.log = slog.New(slog.NewTextHandler(&buf, nil))
	for _, tok := range []string{"", "secret-token-value"} {
		r := httptest.NewRequest("POST", "/Items/"+itemID(fightClub.Key)+"/PlaybackInfo", nil)
		r.RemoteAddr = "192.168.1.20:5555"
		if tok != "" {
			r.Header.Set("Authorization", `MediaBrowser Client="App", DeviceId="d", Token="`+tok+`"`)
		}
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", tok, w.Code)
		}
	}
	out := buf.String()
	if !strings.Contains(out, "no token found") || !strings.Contains(out, "token not recognised") {
		t.Fatalf("reasons not logged: %q", out)
	}
	if strings.Contains(out, "secret-token-value") {
		t.Fatalf("token leaked: %q", out)
	}
}
