package jellyfin

import (
	"net/http"
	"os"
	"path/filepath"
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
	must(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>jf</html>"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "strings", "en-US.json"), []byte(`{"a":1}`), 0o644))
	f.srv.d.WebDir = dir

	if rr := f.call("GET", "/", "", nil); rr.Code != http.StatusFound || rr.Header().Get("Location") != "/web/" {
		t.Fatalf("root redirect: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if rr := f.call("GET", "/web/", "", nil); rr.Code != http.StatusOK || rr.Body.String() != "<html>jf</html>" {
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
