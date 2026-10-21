package stream

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"html"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/archive"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/segcache"
)

func passwordNZB(doc []byte, password string) []byte {
	return bytes.Replace(doc, []byte("<head>"), []byte("<head><meta type=\"password\">"+html.EscapeString(password)+"</meta>"), 1)
}

func TestEncrypted7zEngine(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		split          bool
	}{
		{"content", "Secret", false}, {"header", "Secret", false}, {"unicode-multi", "Sëcret🔐日本", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packed := readFixture(t, "7z-crypto/"+tc.name+".7z")
			want := readFixture(t, "7z-crypto/video.mkv")
			vols := [][]byte{packed}
			if tc.split { // Odd boundaries deliberately split AES blocks and articles.
				vols = nil
				for len(packed) > 0 {
					cut := 17003
					if len(vols) == 1 {
						cut += 17
					}
					if len(vols) == 2 {
						cut -= 17
					}
					n := min(cut, len(packed))
					vols = append(vols, packed[:n])
					packed = packed[n:]
				}
			}
			f := newFakeFetcher()
			doc := postVolumes(t, f, 1009, vols, func(i int) string { return fmt.Sprintf("video.7z.%03d", i+1) })
			doc = passwordNZB(doc, tc.password)
			e := newTestEngine(t, f, nil, Options{ReadAheadMin: 1, ReadAheadMax: 1})
			if ok, err := e.Check(context.Background(), doc); err != nil || !ok {
				t.Fatalf("Check: %v %v", ok, err)
			}
			src := openSource(t, e, tc.name, doc)
			if src.Size() != int64(len(want)) {
				t.Fatalf("plaintext size %d want%d", src.Size(), len(want))
			}
			checkRandomReads(t, src, want, 71, 100)
			if got := readAll(t, src, 997); !bytes.Equal(got, want) {
				t.Fatal("plaintext mismatch")
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					off := int64(13 + i*257)
					buf := make([]byte, 711)
					n, err := src.ReadAt(buf, off)
					if err != nil || n != len(buf) || !bytes.Equal(buf, want[off:off+711]) {
						t.Errorf("concurrent read %d: %d %v", i, n, err)
					}
				}(i)
			}
			wg.Wait()
		})
	}
}

func TestEncryptedArchivePasswordsAndPlanIsolation(t *testing.T) {
	for _, name := range []string{"content", "header"} {
		t.Run(name, func(t *testing.T) {
			f := newFakeFetcher()
			doc := post(t, f, 1031, postFile{name: "video.7z", data: readFixture(t, "7z-crypto/"+name+".7z")})
			e := newTestEngine(t, f, nil, Options{})
			for _, pw := range []string{"", "not-the-password"} {
				bad := doc
				if pw != "" {
					bad = passwordNZB(doc, pw)
				}
				ok, err := e.Check(context.Background(), bad)
				if ok || err == nil || !errors.Is(err, core.ErrReleaseRejected) {
					t.Fatalf("password %q accepted: %v %v", pw, ok, err)
				}
				if bytes.Contains([]byte(err.Error()), []byte("not-the-password")) {
					t.Fatal("password leaked in error")
				}
			}
			// Same release ID and ciphertext, corrected NZB metadata: no sticky wrong
			// password verdict or wrong plaintext is reused from the previous plan.
			src := openSource(t, e, "same-release", passwordNZB(doc, "Secret"))
			head := make([]byte, 4)
			if _, err := src.ReadAt(head, 0); err != nil || !bytes.Equal(head, []byte{0x1a, 0x45, 0xdf, 0xa3}) {
				t.Fatalf("corrected password: %x %v", head, err)
			}
		})
	}
}

func TestEncryptedArchiveCancellationMissingAndPriority(t *testing.T) {
	f := newFakeFetcher()
	raw := readFixture(t, "7z-crypto/content.7z")
	doc := passwordNZB(post(t, f, 1009, postFile{name: "video.7z", data: raw}), "Secret")
	e := newTestEngine(t, f, nil, Options{ReadAheadMin: 1, ReadAheadMax: 1})
	src := openSource(t, e, "faults", doc).(*source)
	want := readFixture(t, "7z-crypto/video.mkv")
	if len(want) < 65536 {
		t.Fatal("fixture too small to test cold seeks")
	}
	f.mu.Lock()
	f.latency = time.Second
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	buf := make([]byte, 2000)
	if _, err := src.ReadAtContext(ctx, buf, 32001); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation lost: %v", err)
	}
	f.mu.Lock()
	f.latency = 0
	f.mu.Unlock()
	if n, err := src.ReadAtPrefetchContext(context.Background(), buf, 45001); err != nil || n != len(buf) || !bytes.Equal(buf, want[45001:47001]) {
		t.Fatalf("prefetch: %d %v", n, err)
	}
	if f.prio(core.PriorityPrefetch) == 0 {
		t.Fatal("encrypted prefetch lost background priority")
	}
	// Select an article at an unread offset from the verified plaintext layout.
	pc := src.plan.layout().pieces(80001, 1000)[0]
	pf := src.plan.vols[pc.vol]
	slot := pf.slotsFor(pc.off, pc.n)[0]
	f.setMissing(pf.msgID(slot))
	src.SetDamageHandler(func(int64, int64) bool { return true })
	if _, err := src.ReadMetadataAtContext(context.Background(), buf, 80001); !errors.Is(err, core.ErrArticleMissing) {
		t.Fatalf("encrypted missing article concealed: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ReadAt(buf, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed source: %v", err)
	}
}

func TestEncryptedDurablePrefetchCipherDependencies(t *testing.T) {
	raw := readFixture(t, "7z-crypto/content.7z")
	want := readFixture(t, "7z-crypto/video.mkv")
	f := newFakeFetcher()
	doc := passwordNZB(post(t, f, 1009, postFile{name: "video.7z", data: raw}), "Secret")
	cache, err := segcache.Open(t.TempDir(), 16<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	e := newTestEngine(t, f, cache, Options{ReadAheadMin: 1, ReadAheadMax: 1})
	const off, n = 30001, 15001
	if err := e.DurablePrefetch(context.Background(), "cached", doc, off, n); err != nil {
		t.Fatal(err)
	}
	// Force a new engine to reload headers and ciphertext from disk, then take
	// the provider offline before serving the fully warmed plaintext span.
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2 := newTestEngine(t, f, cache, Options{ReadAheadMin: 1, ReadAheadMax: 1})
	src := openSource(t, e2, "cached", doc).(*source)
	f.mu.Lock()
	f.failAll = true
	f.mu.Unlock()
	buf := make([]byte, n)
	if got, err := src.ReadMetadataAtContext(context.Background(), buf, off); err != nil || got != n || !bytes.Equal(buf, want[off:off+n]) {
		t.Fatalf("disk range: %d %v", got, err)
	}
	tail := make([]byte, 17)
	got, err := src.ReadAt(tail, src.Size())
	if got != 0 || err != io.EOF {
		t.Fatalf("EOF: %d %v", got, err)
	}
}

func TestEncryptedRARThroughEngine(t *testing.T) {
	for _, version := range []int{4, 5} {
		for _, mode := range []string{"headersfalse-multifalse", "headerstrue-multifalse", "headerstrue-multitrue"} {
			t.Run(fmt.Sprintf("rar%d-%s", version, mode), func(t *testing.T) {
				prefix := fmt.Sprintf("rar-encrypted/rar%d-%s", version, mode)
				vols := [][]byte{readFixture(t, prefix+".part1.rar")}
				if mode == "headerstrue-multitrue" {
					vols = append(vols, readFixture(t, prefix+".part2.rar"))
				}
				f := newFakeFetcher()
				rawDoc := postVolumes(t, f, 997, vols, newStyle("video"))
				doc := passwordNZB(rawDoc, "pāss🔐word")
				e := newTestEngine(t, f, nil, Options{ReadAheadMin: 1, ReadAheadMax: 1})
				if ok, err := e.Check(context.Background(), doc); err != nil || !ok {
					t.Fatalf("Check: %v %v", ok, err)
				}
				if _, err := e.Open(context.Background(), prefix, rawDoc); !errors.Is(err, archive.ErrPasswordRequired) {
					t.Fatalf("missing RAR password: %v", err)
				}
				if _, err := e.Open(context.Background(), prefix, passwordNZB(rawDoc, "wrong")); !errors.Is(err, core.ErrReleaseRejected) {
					t.Fatalf("wrong RAR password: %v", err)
				}
				src := openSource(t, e, prefix, doc)
				want := make([]byte, 8197)
				for i := range want {
					want[i] = byte(i*31 + i/13)
				}
				copy(want, []byte{0x1a, 0x45, 0xdf, 0xa3})
				checkRandomReads(t, src, want, 91, 100)
				if got := readAll(t, src, 1003); !bytes.Equal(got, want) {
					t.Fatal("RAR decrypted stream differs")
				}
			})
		}
	}
}

func TestEncryptedMediaSignatureCoverage(t *testing.T) {
	for _, tc := range []struct {
		name string
		head []byte
	}{
		{"flv", []byte{'F', 'L', 'V', 1, 5, 0, 0, 0, 9}},
		{"ogm", append([]byte{'O', 'g', 'g', 'S', 0, 2}, make([]byte, 21)...)},
		{"mpeg", []byte{0, 0, 1, 0xb3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !looksLikeVideo(tc.head) {
				t.Fatal("supported encrypted media signature rejected")
			}
		})
	}
	if looksLikeVideo([]byte("not decrypted media")) {
		t.Fatal("invalid signature accepted")
	}
}

func TestEncryptedDemandReadsRemainParallel(t *testing.T) {
	plain := mkvBytes(88, 4<<20)
	key := bytes.Repeat([]byte{0x71}, 32)
	var iv [16]byte
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ciphertext, plain)
	f := newFakeFetcher()
	doc := post(t, f, 128<<10, postFile{name: "cipher.bin", data: ciphertext})
	e := newTestEngine(t, f, nil, Options{ReadParallel: 4, ReadAheadMin: 1, ReadAheadMax: 1})
	rel, err := e.acquire(doc)
	if err != nil {
		t.Fatal(err)
	}
	b := &builder{e: e, rel: rel, prio: core.PriorityLive}
	if err := b.heads(context.Background(), rel.files); err != nil {
		e.release(rel)
		t.Fatal(err)
	}
	member := archive.Member{Name: "video.mkv", Size: int64(len(plain)), Stored: true, Encrypted: true,
		Encryption: &archive.Encryption{Key: key, IV: iv, Size: int64(len(plain))},
		Extents:    []archive.Extent{{Size: int64(len(ciphertext))}}}
	p, err := b.encryptedPlan(context.Background(), kindRAR, &member, rel.files, []volInfo{{size: int64(len(ciphertext))}})
	if err != nil {
		e.release(rel)
		t.Fatal(err)
	}
	src := newSource(e, rel, p, "parallel")
	defer src.Close()
	f.mu.Lock()
	f.latency = 10 * time.Millisecond
	f.mu.Unlock()
	got := make([]byte, len(plain))
	if n, err := src.ReadMetadataAtContext(context.Background(), got, 0); err != nil || n != len(got) || !bytes.Equal(got, plain) {
		t.Fatalf("parallel decrypt %d %v", n, err)
	}
	if f.maxInflight.Load() < 4 {
		t.Fatalf("encrypted range serialized article fetches: max inflight %d", f.maxInflight.Load())
	}
	if f.maxInflight.Load() > 4 {
		t.Fatalf("encrypted range exceeded demand worker bound: %d", f.maxInflight.Load())
	}
	if f.prio(core.PriorityReadahead) != 0 {
		t.Fatal("metadata read started speculative work")
	}
}

type blockedArchiveFetcher struct {
	*fakeFetcher
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (f *blockedArchiveFetcher) BodyPart(ctx context.Context, id string, groups []string, p core.Priority) ([]byte, core.ArticlePart, error) {
	f.once.Do(func() { close(f.started) })
	select {
	case <-ctx.Done():
		return nil, core.ArticlePart{}, ctx.Err()
	case <-f.unblock:
	}
	return f.fakeFetcher.BodyPart(ctx, id, groups, p)
}
func (f *blockedArchiveFetcher) Body(ctx context.Context, id string, groups []string, p core.Priority) ([]byte, error) {
	b, _, err := f.BodyPart(ctx, id, groups, p)
	return b, err
}

func TestEncryptedConcurrentOpenWaiterCanCancel(t *testing.T) {
	f := &blockedArchiveFetcher{fakeFetcher: newFakeFetcher(), started: make(chan struct{}), unblock: make(chan struct{})}
	doc := passwordNZB(post(t, f.fakeFetcher, 4093, postFile{name: "video.7z", data: readFixture(t, "7z-crypto/header.7z")}), "Secret")
	e := newTestEngine(t, f, nil, Options{})
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(f.unblock) }) }
	first := make(chan error, 1)
	go func() {
		src, err := e.Open(context.Background(), "waiter", doc)
		if src != nil {
			_ = src.Close()
		}
		first <- err
	}()
	defer func() {
		unblock()
		if err := <-first; err != nil {
			t.Errorf("first opener affected by follower cancellation: %v", err)
		}
	}()
	select {
	case <-f.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first open did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() {
		src, err := e.Open(ctx, "waiter", doc)
		if src != nil {
			_ = src.Close()
		}
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting opener: %v", err)
		}
	case <-time.After(time.Second):
		// The deferred release unblocks the first opener, so even a failing test
		// cannot leave either request running after the engine cleanup.
		t.Error("canceled waiter remained blocked on archive planning")
		unblock()
		<-second
	}
}
