package cardigann

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// privateSite fakes a tracker with a login form and server-side sessions.
type privateSite struct {
	mu       sync.Mutex
	logins   int
	next     int
	valid    map[string]bool
	delay    time.Duration
	srv      *httptest.Server
	torrent  []byte
	hash     string
	badForms []string
}

func newPrivateSite(t *testing.T) *privateSite {
	t.Helper()
	p := &privateSite{valid: map[string]bool{}}
	p.torrent, p.hash = torrentFile("Private.Film.2020")
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *privateSite) authed(r *http.Request) bool {
	c, err := r.Cookie("session")
	p.mu.Lock()
	defer p.mu.Unlock()
	return err == nil && p.valid[c.Value]
}

func (p *privateSite) expire() {
	p.mu.Lock()
	p.valid = map[string]bool{}
	p.mu.Unlock()
}

func (p *privateSite) loginCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logins
}

func (p *privateSite) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/login.php":
		fmt.Fprint(w, `<html><head><meta name="csrf" content="tok123"></head><body>
<form id="other"><input name="q"></form>
<form id="loginform" action="takelogin.php" method="post">
<input type="hidden" name="remember" value="1"><input type="checkbox" name="unchecked" value="x">
<input name="username"><input type="password" name="password"><input type="submit" name="go" value="Login">
</form></body></html>`)
		return
	case "/takelogin.php":
		_ = r.ParseForm()
		f := r.PostForm
		if f.Get("csrf") != "tok123" || f.Get("remember") != "1" || f.Has("unchecked") || f.Has("go") || f.Has("q") {
			p.mu.Lock()
			p.badForms = append(p.badForms, f.Encode())
			p.mu.Unlock()
			http.Error(w, "bad form", 400)
			return
		}
		if f.Get("username") != "alice" || f.Get("password") != "secret" {
			fmt.Fprint(w, `<div class="login-error">Wrong username or password</div>`)
			return
		}
		time.Sleep(p.delay)
		p.mu.Lock()
		p.logins++
		p.next++
		tok := fmt.Sprintf("s%d", p.next)
		p.valid[tok] = true
		p.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "session", Value: tok, Path: "/"})
		http.Redirect(w, r, "/index.php", http.StatusFound)
		return
	}
	if !p.authed(r) {
		http.Redirect(w, r, "/login.php", http.StatusFound)
		return
	}
	switch r.URL.Path {
	case "/index.php":
		fmt.Fprint(w, `<a href="logout.php">Logout</a>`)
	case "/torrents.php":
		fmt.Fprintf(w, `<a href="logout.php">Logout</a>
<div class="torrent"><span class="cat">10</span><h3>Private.Film.2020.1080p</h3><a class="details" href="details.php?id=5">open</a><span class="size">4 GB</span><span class="seed">8</span></div>
<div class="torrent"><span class="cat">20</span><h3>Private.Show.S01E01</h3><a class="details" href="details.php?id=6">open</a><span class="size">1 GB</span><span class="seed">2</span></div>`)
	case "/details.php":
		fmt.Fprint(w, `<a href="logout.php">Logout</a><a class="dl-torrent" href="/dl/5.torrent">Download</a>`)
	case "/dl/5.torrent":
		w.Write(p.torrent)
	default:
		http.NotFound(w, r)
	}
}

var aliceSettings = map[string]string{"username": "alice", "password": "secret"}

func TestFormLoginSearchAndDownload(t *testing.T) {
	site := newPrivateSite(t)
	def := loadDef(t, "private.yml")
	sessions := &sessionStore{now: func() time.Time { return testNow }}
	r := testRunner(t, def, site.srv.URL, runnerSetup{settings: aliceSettings, sessions: sessions})
	ctx := context.Background()
	rels, err := r.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Private Film", Year: 2020}, nil, nil, nil)
	if err != nil {
		t.Fatalf("search: %v (bad forms %v)", err, site.badForms)
	}
	if site.loginCount() != 1 || len(rels) != 1 || rels[0].Name != "Private.Film.2020.1080p" {
		t.Fatalf("logins=%d rels=%+v", site.loginCount(), rels)
	}
	if rels[0].NZBURL != site.srv.URL+"/details.php?id=5" {
		t.Fatalf("download link = %q", rels[0].NZBURL)
	}
	var b budget
	got, err := r.ResolveTorrent(ctx, rels[0], b.reserve)
	if err != nil {
		t.Fatal(err)
	}
	if got.InfoHash != site.hash || b.grabs != 1 {
		t.Fatalf("resolved %+v grabs=%d", got, b.grabs)
	}
	row, ok, _ := sessions.load(ctx, r.id)
	if !ok || row.Version != 1 || len(row.Cookies) == 0 || row.Cookies[0].Name != "session" {
		t.Fatalf("stored session = %+v", row)
	}

	// Another node (or a restart) reuses the stored session.
	r2 := testRunner(t, def, site.srv.URL, runnerSetup{settings: aliceSettings, sessions: sessions})
	if _, err := r2.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Private Film"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if site.loginCount() != 1 {
		t.Fatalf("second node logged in again: %d", site.loginCount())
	}

	// The site drops the session: the first node signs in again; the
	// second, seeing its stale session fail, adopts that new login.
	site.expire()
	if rels, err := r.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Private Film"}, nil, nil, nil); err != nil || len(rels) != 1 {
		t.Fatalf("after expiry: %v %v", rels, err)
	}
	if site.loginCount() != 2 {
		t.Fatalf("logins after expiry = %d", site.loginCount())
	}
	if _, err := r2.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Private Film"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if site.loginCount() != 2 {
		t.Fatalf("stale node logged in instead of adopting: %d", site.loginCount())
	}
	// A download with an expired session signs in once and retries.
	site.expire()
	if _, err := r.ResolveTorrent(ctx, rels[0], nil); err != nil {
		t.Fatal(err)
	}
	if site.loginCount() != 3 {
		t.Fatalf("logins after download expiry = %d", site.loginCount())
	}
}

func TestConcurrentLoginsAreSingleFlight(t *testing.T) {
	site := newPrivateSite(t)
	site.delay = 50 * time.Millisecond
	def := loadDef(t, "private.yml")
	sessions := &sessionStore{now: time.Now}
	km := &keyedMutex{}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		r := testRunner(t, def, site.srv.URL, runnerSetup{settings: aliceSettings, sessions: sessions, lock: km.lock})
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Private Film"}, nil, nil, nil)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if site.loginCount() != 1 {
		t.Fatalf("logins = %d, want 1", site.loginCount())
	}
}

func TestFormLoginWrongPassword(t *testing.T) {
	site := newPrivateSite(t)
	r := testRunner(t, loadDef(t, "private.yml"), site.srv.URL, runnerSetup{settings: map[string]string{"username": "alice", "password": "nope"}})
	res, err := r.Test(context.Background())
	if !errors.Is(err, ErrLogin) || !strings.Contains(res.Error, "Wrong username or password") || res.OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestPostLoginErrorMessageAndSuccess(t *testing.T) {
	var keep string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/takelogin.php":
			_ = r.ParseForm()
			keep = r.PostForm.Get("keeplogged")
			if r.PostForm.Get("password") != "right" {
				fmt.Fprint(w, `<div class="error"><b>Error:</b> <span class="msg">Invalid password</span></div>`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "ok", Path: "/"})
			fmt.Fprint(w, `<a class="logout">out</a>`)
		case "/index.php", "/s.php":
			if c, err := r.Cookie("sid"); err != nil || c.Value != "ok" {
				http.Redirect(w, r, "/takelogin.php", http.StatusFound)
				return
			}
			fmt.Fprint(w, `<a class="logout">out</a><ul><li class="r"><b>Film 2020</b><a href="magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff">m</a></li></ul>`)
		}
	}))
	defer srv.Close()
	def := loadDef(t, "post.yml")
	bad := testRunner(t, def, srv.URL, runnerSetup{settings: map[string]string{"username": "u", "password": "wrong"}})
	if _, err := bad.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "Invalid password") {
		t.Fatalf("err = %v", err)
	}
	good := testRunner(t, def, srv.URL, runnerSetup{settings: map[string]string{"username": "u", "password": "right"}})
	res, err := good.Test(context.Background())
	if err != nil || !res.OK || res.Results != 1 || res.Titles[0] != "Film 2020" || keep != "1" {
		t.Fatalf("res=%+v err=%v keeplogged=%q", res, err, keep)
	}
}

func TestCookieLoginAndMergedRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Cookie("uid")
		pass, _ := r.Cookie("pass")
		if uid == nil || pass == nil || uid.Value != "1" || pass.Value != "abc" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/index.php":
			fmt.Fprint(w, `<a class="logout">out</a>`)
		case "/search":
			fmt.Fprint(w, `<a class="logout">out</a><table class="list">
<tr class="top"><td class="name">Show.S01E01.720p</td></tr><tr class="bottom"><td class="hash">1111111111111111111111111111111111111111</td><td class="size">1 GB</td></tr>
<tr class="top"><td class="name">Show.S01E02.720p</td></tr><tr class="bottom"><td class="hash">2222222222222222222222222222222222222222</td><td class="size">2 GB</td></tr>
</table>`)
		}
	}))
	defer srv.Close()
	def := loadDef(t, "cookie.yml")
	r := testRunner(t, def, srv.URL, runnerSetup{settings: map[string]string{"cookie": "uid=1; pass=abc"}})
	rels, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeEpisode, Title: "Show", Season: 1, Episode: 2}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 || rels[1].InfoHash != strings.Repeat("2", 40) || rels[1].Size != 2<<30 {
		t.Fatalf("rels = %+v", rels)
	}
	empty := testRunner(t, def, srv.URL, runnerSetup{})
	if _, err := empty.Test(context.Background()); !errors.Is(err, ErrLogin) {
		t.Fatalf("missing cookie err = %v", err)
	}
}

func TestCaptchaLoginIsUnsupported(t *testing.T) {
	def, warnings, err := Parse(readFixture(t, "captcha.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "captcha") {
		t.Fatalf("warnings = %v", warnings)
	}
	r := testRunner(t, def, "https://fakecaptcha.invalid/", runnerSetup{})
	if _, err := r.Test(context.Background()); !errors.Is(err, ErrCaptcha) {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionStoreVersionsInDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &sessionStore{db: db, now: func() time.Time { return testNow }}
	if _, ok, err := s.load(ctx, "ix"); ok || err != nil {
		t.Fatalf("empty load ok=%v err=%v", ok, err)
	}
	v1, err := s.save(ctx, "ix", []savedCookie{{Name: "a", Value: "1"}}, "UA/1")
	if err != nil || v1 != 1 {
		t.Fatalf("save v1 = %d %v", v1, err)
	}
	v2, err := s.save(ctx, "ix", []savedCookie{{Name: "a", Value: "2"}}, "UA/2")
	if err != nil || v2 != 2 {
		t.Fatalf("save v2 = %d %v", v2, err)
	}
	row, ok, err := s.load(ctx, "ix")
	if err != nil || !ok || row.Version != 2 || row.Cookies[0].Value != "2" || row.UserAgent != "UA/2" {
		t.Fatalf("row = %+v ok=%v err=%v", row, ok, err)
	}
	if err := s.clear(ctx, "ix"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.load(ctx, "ix"); ok {
		t.Fatal("clear left the row")
	}
}

func TestLoginLockHonoursContext(t *testing.T) {
	km := &keyedMutex{}
	unlock, err := km.lock(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := km.lock(ctx, "k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	unlock()
	if u, err := km.lock(context.Background(), "k"); err != nil {
		t.Fatal(err)
	} else {
		u()
	}
}
