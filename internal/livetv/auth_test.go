package livetv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// provider serves a playlist and guide only to requests that pass check.
func provider(t *testing.T, check func(r *http.Request) bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !check(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ".m3u"), r.URL.Path == "/get.php":
			w.Write([]byte(m3uFixture("news.us=News Network")))
		default:
			w.Write([]byte(xmltvFixture(6, "news.us=News Network")))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBasicAuthSourceKeepsItsSecretRedacted(t *testing.T) {
	ctx := context.Background()
	up := provider(t, func(r *http.Request) bool {
		u, p, ok := r.BasicAuth()
		return ok && u == "owner" && p == "s3cret" && r.UserAgent() == "VLC/3.0"
	})
	s := testService(t, Options{HTTPClient: up.Client()})
	src := Source{ID: "p", Name: "Provider", PlaylistURL: up.URL + "/list.m3u", GuideURL: up.URL + "/guide.xml",
		Auth: &SourceAuth{Type: "basic", Username: "owner", Password: "s3cret", UserAgent: "VLC/3.0"}}
	got, err := s.ImportSource(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Auth.Password != redactedSecret || got.ChannelCount != 1 {
		t.Fatalf("import response: %+v", got)
	}
	listed := sourceByID(t, s, "p")
	if listed.Auth == nil || listed.Auth.Password != redactedSecret || listed.Auth.Username != "owner" {
		t.Fatalf("listed auth: %+v", listed.Auth)
	}
	// The owner API sends the placeholder back: the stored secret is kept.
	var resaved Source
	body := `{"id":"p","name":"Provider","playlistUrl":"` + up.URL + `/list.m3u","auth":{"type":"basic","username":"owner","password":"` + redactedSecret + `","userAgent":"VLC/3.0"}}`
	if code := ownerJSON(t, s, "PUT", "/sources/p", body, &resaved); code != 200 {
		t.Fatalf("re-save with placeholder: %d", code)
	}
	if _, err := s.RefreshSource(ctx, "p"); err != nil {
		t.Fatalf("refresh with stored secret: %v", err)
	}
	src.Auth.Password = "wrong"
	if _, err := s.ImportSource(ctx, src); err == nil || !strings.Contains(err.Error(), "refused the credentials") {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestXtreamSourceDerivesAndHidesItsURLs(t *testing.T) {
	ctx := context.Background()
	var paths []string
	up := provider(t, func(r *http.Request) bool {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		q := r.URL.Query()
		return q.Get("username") == "u" && q.Get("password") == "p"
	})
	s := testService(t, Options{HTTPClient: up.Client()})
	got, err := s.ImportSource(ctx, Source{ID: "x", Name: "Panel", Auth: &SourceAuth{Type: "xtream", Server: up.URL + "/", Username: "u", Password: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.PlaylistURL != "" || got.GuideURL != "" || got.Auth.Password != redactedSecret || !got.HasPlaylist || !got.HasGuide {
		t.Fatalf("xtream source leaked or lost URLs: %+v", got)
	}
	if len(paths) != 2 || !strings.HasPrefix(paths[0], "/get.php?") || !strings.Contains(paths[0], "type=m3u_plus") || !strings.Contains(paths[0], "output=ts") || !strings.HasPrefix(paths[1], "/xmltv.php?") {
		t.Fatalf("derived requests: %v", paths)
	}
	if got.GuideChannelCount != 1 || got.ChannelCount != 1 {
		t.Fatalf("xtream import: %+v", got)
	}
	if _, err := s.RefreshSource(ctx, "x"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := (&SourceAuth{Type: "xtream", Server: "ftp://x", Username: "u", Password: "p"}).validate(); err == nil {
		t.Fatal("non-HTTP panel accepted")
	}
}

func TestCustomHeadersAreValidated(t *testing.T) {
	ctx := context.Background()
	up := provider(t, func(r *http.Request) bool { return r.Header.Get("X-Api-Key") == "k" })
	s := testService(t, Options{HTTPClient: up.Client()})
	if _, err := s.ImportSource(ctx, Source{ID: "h", Name: "Keyed", PlaylistURL: up.URL + "/a.m3u", Auth: &SourceAuth{Type: "headers", Headers: map[string]string{"X-Api-Key": "k"}}}); err != nil {
		t.Fatal(err)
	}
	if h := sourceByID(t, s, "h").Auth.Headers["X-Api-Key"]; h != redactedSecret {
		t.Fatalf("header value not redacted: %q", h)
	}
	for _, bad := range []map[string]string{{"Host": "x"}, {"X-Plex-Token": "x"}, {"Bad Name": "x"}, {"X-A": "a\r\nb"}} {
		if err := (&SourceAuth{Type: "headers", Headers: bad}).validate(); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}

func TestUpstreamHeadersUseOnlyTheInputsProvider(t *testing.T) {
	ctx := context.Background()
	s := testService(t, Options{})
	for _, src := range []Source{
		{ID: "a", Name: "A", Playlist: m3uFixture("a=One"), Auth: &SourceAuth{Type: "bearer", Token: "token-a", UserAgent: "UA-A"}},
		{ID: "b", Name: "B", Playlist: m3uFixture("b=Two"), Auth: &SourceAuth{Type: "basic", Username: "bob", Password: "pw"}},
		{ID: "c", Name: "C", Playlist: m3uFixture("c=Three")},
	} {
		if _, err := s.ImportSource(ctx, src); err != nil {
			t.Fatal(err)
		}
	}
	imported := StreamSource{ID: stableID("x", "playlist")}
	ch := Channel{ID: "x", SourceID: "a", StreamSources: []StreamSource{imported, {ID: "added"}}}
	own, _ := s.upstreamHeaders(ctx, ch, imported)
	if own.Get("Authorization") != "Bearer token-a" || own.Get("User-Agent") != "UA-A" {
		t.Fatalf("own provider headers: %v", own)
	}
	backup, _ := s.upstreamHeaders(ctx, ch, StreamSource{ID: "2", AuthSourceID: "b"})
	if !strings.HasPrefix(backup.Get("Authorization"), "Basic ") || backup.Get("User-Agent") != "" {
		t.Fatalf("backup provider headers: %v", backup)
	}
	plain, _ := s.upstreamHeaders(ctx, ch, StreamSource{ID: "3", AuthSourceID: "c"})
	if len(plain) != 0 {
		t.Fatalf("unauthenticated provider received credentials: %v", plain)
	}
	added, _ := s.upstreamHeaders(ctx, ch, StreamSource{ID: "added"})
	if len(added) != 0 {
		t.Fatalf("owner-added backup inherited the playlist login: %v", added)
	}
	legacy, _ := s.upstreamHeaders(ctx, Channel{ID: "y", SourceID: "a"}, StreamSource{ID: "only"})
	if legacy.Get("Authorization") != "Bearer token-a" {
		t.Fatalf("legacy single input lost its login: %v", legacy)
	}
	manual, _ := s.upstreamHeaders(ctx, Channel{ID: "v"}, StreamSource{ID: "4"})
	if len(manual) != 0 {
		t.Fatalf("manual channel received credentials: %v", manual)
	}
}
