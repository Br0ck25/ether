package debrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

const testKey = "test-key-123"

var testHash = strings.Repeat("ab", 20)

func fastOpts(base string) ProviderOptions {
	return ProviderOptions{BaseURL: base, PollInterval: time.Millisecond, ReadyPolls: 3}
}

func acquireReq(cachedOnly bool) AcquireRequest {
	return AcquireRequest{InfoHash: testHash, Magnet: "magnet:?xt=urn:btih:" + testHash,
		Choose: func(fs []File) (File, error) { return ChooseFile(fs, nil) }, CachedOnly: cachedOnly, RemoveUncached: true}
}

// --- Real-Debrid ------------------------------------------------------------

type fakeRD struct {
	mu       sync.Mutex
	cached   bool
	selected string
	adds     int
	deleted  []string
	existing bool // a downloaded torrent with the hash is already on the account
}

func (f *fakeRD) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":"bad_token","error_code":8}`)
				return
			}
			h(w, r)
		}
	}
	files := func(sel string) string {
		s1, s2 := 0, 0
		if sel == "1" || sel == "all" {
			s1 = 1
		}
		if sel == "2" {
			s2 = 1
		}
		return fmt.Sprintf(`[{"id":1,"path":"/Movie.2024/Movie.2024.1080p.mkv","bytes":4000000000,"selected":%d},{"id":2,"path":"/Movie.2024/Sample/sample.mkv","bytes":30000000,"selected":%d}]`, s1, s2)
	}
	mux.HandleFunc("POST /torrents/addMagnet", auth(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !strings.Contains(r.PostForm.Get("magnet"), testHash) {
			t.Errorf("magnet %q", r.PostForm.Get("magnet"))
		}
		f.mu.Lock()
		f.adds++
		f.mu.Unlock()
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"T1","uri":"x"}`)
	}))
	mux.HandleFunc("GET /torrents", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.existing {
			fmt.Fprintf(w, `[{"id":"OLD","hash":"%s","status":"downloaded"}]`, strings.ToUpper(testHash))
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	mux.HandleFunc("GET /torrents/info/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		switch {
		case id == "OLD":
			fmt.Fprintf(w, `{"id":"OLD","hash":"%s","status":"downloaded","files":%s,"links":["https://rd.example/d/OLD1"]}`, testHash, files("1"))
		case f.selected == "":
			fmt.Fprintf(w, `{"id":"T1","hash":"%s","status":"waiting_files_selection","files":%s,"links":[]}`, testHash, files(""))
		case f.cached:
			fmt.Fprintf(w, `{"id":"T1","hash":"%s","status":"downloaded","files":%s,"links":["https://rd.example/d/L1"]}`, testHash, files(f.selected))
		default:
			fmt.Fprintf(w, `{"id":"T1","hash":"%s","status":"downloading","files":%s,"links":[]}`, testHash, files(f.selected))
		}
	}))
	mux.HandleFunc("POST /torrents/selectFiles/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.selected = r.PostForm.Get("files")
		f.mu.Unlock()
		w.WriteHeader(204)
	}))
	mux.HandleFunc("DELETE /torrents/delete/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deleted = append(f.deleted, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /unrestrict/link", auth(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		link := r.PostForm.Get("link")
		if strings.HasSuffix(link, "GONE") {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"unavailable_file","error_code":24}`)
			return
		}
		fmt.Fprintf(w, `{"download":"https://cdn.example/%s/Movie.mkv","filesize":4000000000,"filename":"Movie.mkv"}`, link[strings.LastIndex(link, "/")+1:])
	}))
	mux.HandleFunc("GET /user", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"username":"owner","type":"premium","expiration":"2030-01-01T00:00:00.000Z"}`)
	}))
	return mux
}

func TestRealDebridCachedFlow(t *testing.T) {
	f := &fakeRD{cached: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p := newRealDebrid(testKey, fastOpts(srv.URL))
	ctx := context.Background()
	if id, err := p.Find(ctx, testHash); err != nil || id != "" {
		t.Fatalf("find on empty account: %q %v", id, err)
	}
	acq, err := p.Acquire(ctx, acquireReq(true))
	if err != nil {
		t.Fatal(err)
	}
	if acq.TorrentID != "T1" || acq.File.ID != "1" || acq.File.Link != "https://rd.example/d/L1" || !acq.Added || f.selected != "1" {
		t.Fatalf("acquired %+v selected %q", acq, f.selected)
	}
	dl, err := p.Link(ctx, Handle{TorrentID: acq.TorrentID, FileID: acq.File.ID, Link: acq.File.Link})
	if err != nil || dl.URL != "https://cdn.example/L1/Movie.mkv" || dl.Size != 4000000000 || dl.Expires.IsZero() {
		t.Fatalf("link %+v %v", dl, err)
	}
	// Without the stored hoster link, the link is looked up from the torrent.
	if dl, err = p.Link(ctx, Handle{TorrentID: "T1", FileID: "1"}); err != nil || !strings.Contains(dl.URL, "L1") {
		t.Fatalf("lookup link %+v %v", dl, err)
	}
	if _, err := p.Link(ctx, Handle{TorrentID: "T1", FileID: "1", Link: "https://rd.example/d/GONE"}); !errors.Is(err, ErrGone) {
		t.Fatalf("gone link: %v", err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("a cached torrent was deleted: %v", f.deleted)
	}
	a, err := p.Account(ctx)
	if err != nil || !a.Premium || a.User != "owner" {
		t.Fatalf("account %+v %v", a, err)
	}
}

func TestRealDebridUncachedIsRemoved(t *testing.T) {
	f := &fakeRD{cached: false}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p := newRealDebrid(testKey, fastOpts(srv.URL))
	_, err := p.Acquire(context.Background(), acquireReq(true))
	if !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("want not cached, got %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "T1" {
		t.Fatalf("uncached torrent not removed: %v", f.deleted)
	}
	// Not cached-only: the torrent is left to download.
	f.selected, f.deleted = "", nil
	if _, err = p.Acquire(context.Background(), acquireReq(false)); !errors.Is(err, core.ErrNotCached) || len(f.deleted) != 0 {
		t.Fatalf("download mode: %v deleted %v", err, f.deleted)
	}
}

// Real-Debrid's filename filter answers unrestrict with infringing_file for
// a torrent that reached "downloaded". The release must count as not cached
// on that account, leave no torrent behind and not be promoted or retried.
func TestRealDebridInfringingFileIsNotCached(t *testing.T) {
	f := &fakeRD{cached: true}
	base := f.handler(t)
	var unrestricts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unrestrict/link" {
			f.mu.Lock()
			unrestricts++
			f.mu.Unlock()
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"infringing_file","error_code":35}`)
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := newRealDebrid(testKey, fastOpts(srv.URL))
	cfg := config.Debrid{CachedOnly: true, RemoveUncached: true, Providers: []config.DebridProvider{{ID: "rd", Kind: "realdebrid", APIKey: testKey, Enabled: true}}}
	svc := NewService(cfg, Options{NewProvider: func(config.DebridProvider, ProviderOptions) (Provider, error) { return p, nil }})
	rel := core.Release{ID: "r1", Name: "Movie.2024.1080p", Protocol: core.ProtocolTorrent, InfoHash: testHash}

	_, err := svc.Acquire(t.Context(), rel)
	if !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("want not cached, got %v", err)
	}
	if svc.KnownCached(testHash) {
		t.Fatal("a blocked file is still promoted as known cached")
	}
	f.mu.Lock()
	deleted := append([]string(nil), f.deleted...)
	f.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "T1" {
		t.Fatalf("blocked torrent left on the account: deleted %v", deleted)
	}
	st := svc.Stats()[0]
	if st.Blocked != 1 || st.Failures != 0 || st.State != "ok" {
		t.Fatalf("stats %+v", st)
	}

	// The next Play skips the release on this account without API calls.
	if _, err := svc.Acquire(t.Context(), rel); !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("second play: %v", err)
	}
	f.mu.Lock()
	adds, calls := f.adds, unrestricts
	f.mu.Unlock()
	if adds != 1 || calls != 1 {
		t.Fatalf("blocked release retried: adds=%d unrestricts=%d", adds, calls)
	}
}

func TestRealDebridReusesExistingTorrentAndRejectsBadKey(t *testing.T) {
	f := &fakeRD{existing: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p := newRealDebrid(testKey, fastOpts(srv.URL))
	ctx := context.Background()
	id, err := p.Find(ctx, testHash)
	if err != nil || id != "OLD" {
		t.Fatalf("find %q %v", id, err)
	}
	req := acquireReq(true)
	req.TorrentID = id
	acq, err := p.Acquire(ctx, req)
	if err != nil || acq.Added || acq.File.Link != "https://rd.example/d/OLD1" || f.adds != 0 {
		t.Fatalf("reuse %+v %v adds %d", acq, err, f.adds)
	}
	bad := newRealDebrid("wrong", fastOpts(srv.URL))
	if _, err := bad.Account(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("bad key: %v", err)
	}
	if strings.Contains(fmt.Sprint(err), "wrong") {
		t.Fatal("error leaks the key")
	}
}

// --- TorBox ----------------------------------------------------------------

type fakeTorBox struct {
	mu      sync.Mutex
	cached  bool
	created int
	deleted []string
	cachedQ string
}

func (f *fakeTorBox) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, data string) {
		fmt.Fprintf(w, `{"success":true,"error":null,"detail":"ok","data":%s}`, data)
	}
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"success":false,"error":"BAD_TOKEN","detail":"invalid token","data":null}`)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /torrents/checkcached", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cachedQ = r.URL.Query().Get("hash")
		cached := f.cached
		f.mu.Unlock()
		if cached {
			ok(w, fmt.Sprintf(`{"%s":{"name":"Show.S01","size":9000000000,"hash":"%s"}}`, testHash, testHash))
			return
		}
		ok(w, `{}`)
	}))
	mux.HandleFunc("POST /torrents/createtorrent", auth(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil || !strings.Contains(r.FormValue("magnet"), testHash) {
			t.Errorf("createtorrent form: %v", err)
		}
		f.mu.Lock()
		if r.FormValue("add_only_if_cached") == "true" && !f.cached {
			f.mu.Unlock()
			fmt.Fprint(w, `{"success":false,"error":"TORRENT_NOT_CACHED","detail":"not cached","data":null}`)
			return
		}
		f.created++
		f.mu.Unlock()
		ok(w, `{"torrent_id":77,"hash":"x","auth_id":"y"}`)
	}))
	mux.HandleFunc("GET /torrents/mylist", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		cached := f.cached
		f.mu.Unlock()
		files := `[{"id":0,"name":"Show.S01/Show.S01E01.mkv","size":1500000000},{"id":1,"name":"Show.S01/Show.S01E02.mkv","size":1400000000},{"id":2,"name":"Show.S01/Show.S01E02.nfo","size":100}]`
		if r.URL.Query().Get("id") == "" {
			ok(w, fmt.Sprintf(`[{"id":77,"hash":"%s","download_present":%v,"files":%s}]`, testHash, cached, files))
			return
		}
		ok(w, fmt.Sprintf(`{"id":77,"hash":"%s","download_present":%v,"download_finished":%v,"files":%s}`, testHash, cached, cached, files))
	}))
	mux.HandleFunc("GET /torrents/requestdl", auth(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("token") != testKey || q.Get("torrent_id") != "77" {
			t.Errorf("requestdl query %v", q)
		}
		ok(w, fmt.Sprintf(`"https://cdn.torbox.example/%s"`, q.Get("file_id")))
	}))
	mux.HandleFunc("POST /torrents/controltorrent", auth(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.deleted = append(f.deleted, fmt.Sprint(body["torrent_id"], ":", body["operation"]))
		f.mu.Unlock()
		ok(w, `null`)
	}))
	mux.HandleFunc("GET /user/me", auth(func(w http.ResponseWriter, r *http.Request) {
		ok(w, `{"email":"o@example.com","plan":2}`)
	}))
	return mux
}

func TestTorBoxFlow(t *testing.T) {
	f := &fakeTorBox{cached: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p := newTorBox(testKey, fastOpts(srv.URL))
	ctx := context.Background()
	other := strings.Repeat("cd", 20)
	got, err := p.CheckCached(ctx, []string{testHash, other})
	if err != nil || !got[testHash] || got[other] {
		t.Fatalf("checkcached %v %v", got, err)
	}
	req := acquireReq(true)
	req.Choose = func(fs []File) (File, error) { return ChooseFile(fs, &core.ReleaseTarget{Season: 1, Episode: 2}) }
	acq, err := p.Acquire(ctx, req)
	if err != nil || acq.TorrentID != "77" || acq.File.ID != "1" || acq.File.Size != 1400000000 {
		t.Fatalf("acquire %+v %v", acq, err)
	}
	if f.cachedQ != testHash+","+other {
		t.Fatal("Acquire repeated the bulk cache check on the Play path")
	}
	dl, err := p.Link(ctx, Handle{TorrentID: "77", FileID: "1", Size: 1400000000})
	if err != nil || dl.URL != "https://cdn.torbox.example/1" {
		t.Fatalf("link %+v %v", dl, err)
	}
	if id, err := p.Find(ctx, testHash); err != nil || id != "77" {
		t.Fatalf("find %q %v", id, err)
	}
	if a, err := p.Account(ctx); err != nil || !a.Premium {
		t.Fatalf("account %+v %v", a, err)
	}
}

func TestTorBoxCacheExpiryBeforeCreateCannotStartDownload(t *testing.T) {
	f := &fakeTorBox{cached: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p := newTorBox(testKey, fastOpts(srv.URL))
	cached, err := p.CheckCached(t.Context(), []string{testHash})
	if err != nil || !cached[testHash] {
		t.Fatalf("initial cache check: %v %v", cached, err)
	}
	f.mu.Lock()
	f.cached = false
	f.mu.Unlock()
	if _, err := p.Acquire(t.Context(), acquireReq(true)); !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("expired cache returned %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created != 0 {
		t.Fatal("an uncached torrent was created after its cached result expired")
	}
}

func TestTorBoxUncachedSkipsWithoutAdding(t *testing.T) {
	f := &fakeTorBox{cached: false}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p := newTorBox(testKey, fastOpts(srv.URL))
	if _, err := p.Acquire(context.Background(), acquireReq(true)); !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("got %v", err)
	}
	if f.created != 0 {
		t.Fatal("an uncached torrent was added in cached-only mode")
	}
	if err := p.Remove(context.Background(), "77"); err != nil || len(f.deleted) != 1 || f.deleted[0] != "77:delete" {
		t.Fatalf("remove %v %v", err, f.deleted)
	}
	if _, err := newTorBox("nope", fastOpts(srv.URL)).Account(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("bad key: %v", err)
	}
}

// --- AllDebrid ---------------------------------------------------------------

type fakeAD struct {
	mu      sync.Mutex
	ready   bool
	deleted []string
}

func (f *fakeAD) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, data string) { fmt.Fprintf(w, `{"status":"success","data":%s}`, data) }
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("agent") != "ether" {
				t.Errorf("missing agent on %s", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				fmt.Fprint(w, `{"status":"error","error":{"code":"AUTH_BAD_APIKEY","message":"The auth apikey is invalid"}}`)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /v4/magnet/upload", auth(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !strings.Contains(r.PostForm.Get("magnets[]"), testHash) {
			t.Errorf("upload form %v", r.PostForm)
		}
		ok(w, fmt.Sprintf(`{"magnets":[{"magnet":"m","hash":"%s","name":"Movie","size":1,"ready":%v,"id":555}]}`, testHash, f.ready))
	}))
	mux.HandleFunc("GET /v4.1/magnet/status", auth(func(w http.ResponseWriter, r *http.Request) {
		code := 1
		if f.ready {
			code = 4
		}
		if r.URL.Query().Get("id") != "" {
			ok(w, fmt.Sprintf(`{"magnets":{"id":555,"hash":"%s","statusCode":%d}}`, testHash, code))
			return
		}
		ok(w, fmt.Sprintf(`{"magnets":[{"id":555,"hash":"%s","statusCode":%d}]}`, testHash, code))
	}))
	mux.HandleFunc("POST /v4/magnet/files", auth(func(w http.ResponseWriter, r *http.Request) {
		ok(w, `{"magnets":[{"id":"555","files":[{"n":"Movie.2024","e":[{"n":"Movie.2024.mkv","s":3000000000,"l":"https://alldebrid.com/f/abc"},{"n":"Sample","e":[{"n":"sample.mkv","s":2000,"l":"https://alldebrid.com/f/s"}]}]}]}]}`)
	}))
	mux.HandleFunc("POST /v4/link/unlock", auth(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("link") == "https://alldebrid.com/f/dead" {
			fmt.Fprint(w, `{"status":"error","error":{"code":"LINK_DOWN","message":"down"}}`)
			return
		}
		ok(w, `{"link":"https://cdn.alldebrid.example/abc/Movie.2024.mkv","filesize":3000000000}`)
	}))
	mux.HandleFunc("POST /v4/magnet/delete", auth(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.deleted = append(f.deleted, r.PostForm.Get("id"))
		f.mu.Unlock()
		ok(w, `{"message":"deleted"}`)
	}))
	mux.HandleFunc("GET /v4/user", auth(func(w http.ResponseWriter, r *http.Request) {
		ok(w, `{"user":{"username":"o","isPremium":true,"premiumUntil":1900000000}}`)
	}))
	return mux
}

func TestAllDebridFlow(t *testing.T) {
	f := &fakeAD{ready: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p := newAllDebrid(testKey, fastOpts(srv.URL))
	ctx := context.Background()
	acq, err := p.Acquire(ctx, acquireReq(true))
	if err != nil || acq.TorrentID != "555" || acq.File.Path != "Movie.2024/Movie.2024.mkv" || acq.File.Link != "https://alldebrid.com/f/abc" {
		t.Fatalf("acquire %+v %v", acq, err)
	}
	dl, err := p.Link(ctx, Handle{TorrentID: "555", Link: acq.File.Link})
	if err != nil || dl.Size != 3000000000 {
		t.Fatalf("link %+v %v", dl, err)
	}
	if dl, err = p.Link(ctx, Handle{TorrentID: "555", Path: "Movie.2024/Movie.2024.mkv", Size: 3000000000}); err != nil || dl.URL == "" {
		t.Fatalf("relink by path %+v %v", dl, err)
	}
	if _, err = p.Link(ctx, Handle{Link: "https://alldebrid.com/f/dead"}); !errors.Is(err, ErrGone) {
		t.Fatalf("dead link: %v", err)
	}
	if id, _ := p.Find(ctx, testHash); id != "555" {
		t.Fatalf("find %q", id)
	}
	if _, err := newAllDebrid("x", fastOpts(srv.URL)).Account(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("auth: %v", err)
	}
}

func TestAllDebridNotReadyIsRemoved(t *testing.T) {
	f := &fakeAD{ready: false}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p := newAllDebrid(testKey, fastOpts(srv.URL))
	if _, err := p.Acquire(context.Background(), acquireReq(true)); !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("got %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "555" {
		t.Fatalf("deleted %v", f.deleted)
	}
}

// --- Premiumize --------------------------------------------------------------

func premiumizeServer(t *testing.T, cached bool, creates *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("apikey")
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			key = r.PostForm.Get("apikey")
		}
		if key != testKey {
			fmt.Fprint(w, `{"status":"error","message":"Not logged in."}`)
			return
		}
		switch r.URL.Path {
		case "/cache/check":
			items := r.URL.Query()["items[]"]
			resp := make([]bool, len(items))
			for i, h := range items {
				resp[i] = cached && h == testHash
			}
			b, _ := json.Marshal(resp)
			fmt.Fprintf(w, `{"status":"success","response":%s}`, b)
		case "/transfer/directdl":
			fmt.Fprint(w, `{"status":"success","content":[{"path":"Movie/Movie.mkv","size":2500000000,"link":"https://pm.example/dl/Movie.mkv","stream_link":"x"},{"path":"Movie/movie.nfo","size":10,"link":"https://pm.example/dl/nfo"}]}`)
		case "/transfer/create":
			*creates++
			fmt.Fprint(w, `{"status":"success","id":"x"}`)
		case "/account/info":
			fmt.Fprint(w, `{"status":"success","customer_id":123,"premium_until":4102444800}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
}

func TestPremiumizeFlow(t *testing.T) {
	creates := 0
	srv := premiumizeServer(t, true, &creates)
	defer srv.Close()
	p := newPremiumize(testKey, fastOpts(srv.URL))
	ctx := context.Background()
	acq, err := p.Acquire(ctx, acquireReq(true))
	if err != nil || acq.TorrentID != "" || acq.File.Size != 2500000000 {
		t.Fatalf("acquire %+v %v", acq, err)
	}
	dl, err := p.Link(ctx, Handle{Magnet: "magnet:?xt=urn:btih:" + testHash, Path: acq.File.Path, Size: acq.File.Size})
	if err != nil || dl.URL != "https://pm.example/dl/Movie.mkv" {
		t.Fatalf("link %+v %v", dl, err)
	}
	if _, err := p.Find(ctx, testHash); !errors.Is(err, ErrUnsupported) {
		t.Fatal("premiumize has no torrent list")
	}
	if a, err := p.Account(ctx); err != nil || !a.Premium {
		t.Fatalf("account %+v %v", a, err)
	}
	if _, err := newPremiumize("bad", fastOpts(srv.URL)).Account(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("auth %v", err)
	}
}

func TestPremiumizeUncached(t *testing.T) {
	creates := 0
	srv := premiumizeServer(t, false, &creates)
	defer srv.Close()
	p := newPremiumize(testKey, fastOpts(srv.URL))
	if _, err := p.Acquire(context.Background(), acquireReq(true)); !errors.Is(err, core.ErrNotCached) || creates != 0 {
		t.Fatalf("cached-only: %v creates %d", err, creates)
	}
	if _, err := p.Acquire(context.Background(), acquireReq(false)); !errors.Is(err, core.ErrNotCached) || creates != 1 {
		t.Fatalf("download mode: %v creates %d", err, creates)
	}
}

// --- shared ------------------------------------------------------------------

func TestNewProviderAndErrorsNeverLeakKeys(t *testing.T) {
	for _, kind := range config.DebridKinds {
		p, err := NewProvider(config.DebridProvider{Kind: kind, APIKey: "k"}, ProviderOptions{})
		if err != nil || p.Kind() != kind {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if _, err := NewProvider(config.DebridProvider{Kind: "realdebrid"}, ProviderOptions{}); !errors.Is(err, ErrAuth) {
		t.Fatal("missing key accepted")
	}
	if _, err := NewProvider(config.DebridProvider{Kind: "nope", APIKey: "k"}, ProviderOptions{}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	// A transport error must not echo the URL (TorBox and Premiumize put
	// the key in the query string).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	}))
	defer srv.Close()
	p := newPremiumize("secret-key-xyz", fastOpts(srv.URL))
	_, err := p.CheckCached(context.Background(), []string{testHash})
	if err == nil || strings.Contains(err.Error(), "secret-key-xyz") {
		t.Fatalf("error %v", err)
	}

}

// Real-Debrid also answers addMagnet with infringing_file once an account
// has added a burst of torrents. A refusal skips that hash briefly without
// recording it as uncached; a second in a row pauses new adds (doubling)
// so Ether stops provoking the throttle; the hash is retried later.
func TestRealDebridAddRefusalsPauseAdds(t *testing.T) {
	var mu sync.Mutex
	adds := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/torrents/addMagnet":
			mu.Lock()
			adds++
			mu.Unlock()
			w.WriteHeader(451)
			fmt.Fprint(w, `{"error":"infringing_file","error_code":35}`)
		case "/torrents":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	now := time.Unix(1_800_000_000, 0)
	p := newRealDebrid(testKey, fastOpts(srv.URL))
	cfg := config.Debrid{CachedOnly: true, RemoveUncached: true, Providers: []config.DebridProvider{{ID: "rd", Kind: "realdebrid", APIKey: testKey, Enabled: true}}}
	svc := NewService(cfg, Options{Now: func() time.Time { return now }, NewProvider: func(config.DebridProvider, ProviderOptions) (Provider, error) { return p, nil }})
	rel := func(n int) core.Release {
		h := strings.Repeat(fmt.Sprintf("%x", n), 40)[:40]
		return core.Release{ID: h, Name: "Movie.2024.1080p", Protocol: core.ProtocolTorrent, InfoHash: h}
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return adds }
	play := func(n int) {
		t.Helper()
		if _, err := svc.Acquire(t.Context(), rel(n)); !errors.Is(err, core.ErrNotCached) {
			t.Fatalf("release %d: %v", n, err)
		}
	}

	play(1)
	play(2)
	if count() != 2 {
		t.Fatalf("adds %d", count())
	}
	play(3) // adds paused after two refusals in a row
	if count() != 2 {
		t.Fatalf("paused provider was asked to add: %d", count())
	}
	if st := svc.Stats()[0]; st.Failures != 0 || st.State != "ok" || st.Blocked != 2 {
		t.Fatalf("stats %+v", st)
	}
	now = now.Add(3 * time.Minute) // pause over; a third refusal doubles it
	play(3)
	if count() != 3 {
		t.Fatalf("adds %d after pause", count())
	}
	now = now.Add(3 * time.Minute)
	play(4)
	if count() != 3 {
		t.Fatalf("doubled pause not applied: %d", count())
	}
	now = now.Add(10 * time.Minute) // refused hashes are retried, not remembered as uncached
	play(1)
	if count() != 4 {
		t.Fatalf("refused hash was not retried: %d", count())
	}
}
