package archive

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
)

func cbcFixture(t *testing.T, plaintext []byte, cuts ...int) ([]Volume, Member) {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	iv := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, (len(plaintext)+15)/16*16)
	copy(ciphertext, plaintext)
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ciphertext, ciphertext)
	m := Member{Name: "test.mkv", Size: int64(len(plaintext)), Stored: true, Encrypted: true, Encryption: &Encryption{Key: key, IV: iv, Size: int64(len(plaintext))}}
	var volumes []Volume
	from := 0
	for _, end := range append(cuts, len(ciphertext)) {
		if end < from || end > len(ciphertext) {
			t.Fatal("invalid fixture cut")
		}
		// A volume prefix must not be mistaken for part of the CBC stream.
		prefix := bytes.Repeat([]byte{0xa5}, len(volumes)+7)
		data := append(prefix, ciphertext[from:end]...)
		m.Extents = append(m.Extents, Extent{Volume: len(volumes), Offset: int64(len(prefix)), Size: int64(end - from)})
		volumes = append(volumes, Volume{R: bytes.NewReader(data), Size: int64(len(data))})
		from = end
	}
	return volumes, m
}

func TestMemberCBCUnalignedCrossVolumeAndFolder(t *testing.T) {
	plaintext := make([]byte, 3*cbcReadChunk+123)
	rand.New(rand.NewSource(7)).Read(plaintext)
	vols, m := cbcFixture(t, plaintext, 1, 15, 17, 65549, 131075)
	m.Encryption.Offset = 13
	m.Size = int64(len(plaintext) - 13 - 19)
	expected := plaintext[13 : len(plaintext)-19]
	r, err := NewMemberReader(m, vols)
	if err != nil {
		t.Fatal(err)
	}
	// Mutation of parser metadata after construction must not alter a reader.
	m.Encryption.Key[0] ^= 1
	m.Encryption.IV[0] ^= 1
	m.Extents[0].Offset++
	vols[0].R = nil
	random := rand.New(rand.NewSource(19))
	for i := 0; i < 100; i++ {
		off := random.Intn(len(expected))
		length := 1 + random.Intn(len(expected))
		got := make([]byte, length)
		n, err := r.ReadAt(got, int64(off))
		want := expected[off:]
		if len(want) > length {
			want = want[:length]
		}
		if n != len(want) || !bytes.Equal(got[:n], want) {
			t.Fatalf("range %d+%d: got %d bytes, want %d", off, length, n, len(want))
		}
		if n < length && !errors.Is(err, io.EOF) || n == length && err != nil {
			t.Fatalf("range %d+%d: err=%v", off, length, err)
		}
	}
}

func TestMemberCBCConcurrentReads(t *testing.T) {
	plaintext := bytes.Repeat([]byte("concurrent seeks through encrypted archive volumes"), 3000)
	vols, m := cbcFixture(t, plaintext, 29, 65543)
	r, err := NewMemberReader(m, vols)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				off := (i*997 + j*1237) % (len(plaintext) - 4096)
				buf := make([]byte, 4096)
				n, err := r.ReadAt(buf, int64(off))
				if err != nil || n != len(buf) || !bytes.Equal(buf, plaintext[off:off+len(buf)]) {
					t.Errorf("concurrent read: n=%d err=%v", n, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

type cbcCountingReader struct {
	r       io.ReaderAt
	bytes   atomic.Int64
	largest atomic.Int64
}

func (r *cbcCountingReader) ReadAt(p []byte, off int64) (int, error) {
	r.bytes.Add(int64(len(p)))
	for old := r.largest.Load(); int64(len(p)) > old; old = r.largest.Load() {
		if r.largest.CompareAndSwap(old, int64(len(p))) {
			break
		}
	}
	return r.r.ReadAt(p, off)
}

func TestMemberCBCSeekReadsOnlyNeededBlocks(t *testing.T) {
	plaintext := bytes.Repeat([]byte{42}, 4*1024*1024+3)
	vols, m := cbcFixture(t, plaintext)
	counter := &cbcCountingReader{r: vols[0].R}
	vols[0].R = counter
	r, err := NewMemberReader(m, vols)
	if err != nil {
		t.Fatal(err)
	}
	if counter.bytes.Load() != 0 {
		t.Fatal("constructor read payload")
	}
	buf := make([]byte, 3)
	off := int64(len(plaintext) - 5)
	if n, err := r.ReadAt(buf, off); err != nil || n != 3 || !bytes.Equal(buf, plaintext[off:off+3]) {
		t.Fatalf("seek: %d %v", n, err)
	}
	if got := counter.bytes.Load(); got > 48 {
		t.Fatalf("tiny seek fetched %d bytes", got)
	}
	buf = make([]byte, len(plaintext))
	if _, err := r.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if got := counter.largest.Load(); got > cbcReadChunk {
		t.Fatalf("unbounded read %d", got)
	}
}

func TestMemberReaderInvalidMetadata(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func([]Volume, *Member)
		sentinel error
	}{
		{"negative size", func(v []Volume, m *Member) { m.Size = -1 }, ErrCorrupt},
		{"negative offset", func(v []Volume, m *Member) { m.Encryption.Offset = -1 }, ErrCorrupt},
		{"folder too small", func(v []Volume, m *Member) { m.Encryption.Size = 3 }, ErrCorrupt},
		{"member past folder", func(v []Volume, m *Member) { m.Encryption.Offset = 1 }, ErrCorrupt},
		{"padding overflow", func(v []Volume, m *Member) { m.Encryption.Size = math.MaxInt64 }, ErrCorrupt},
		{"bad key", func(v []Volume, m *Member) { m.Encryption.Key = []byte("bad") }, ErrCorrupt},
		{"missing volume", func(v []Volume, m *Member) { m.Extents[0].Volume = 2 }, ErrMissingVolume},
		{"negative volume", func(v []Volume, m *Member) { m.Extents[0].Volume = -1 }, ErrMissingVolume},
		{"extent overflow", func(v []Volume, m *Member) { m.Extents[0].Offset = math.MaxInt64 }, ErrCorrupt},
		{"negative extent", func(v []Volume, m *Member) { m.Extents[0].Size = -1 }, ErrCorrupt},
		{"short ciphertext", func(v []Volume, m *Member) { m.Extents[0].Size-- }, ErrCorrupt},
		{"nil reader", func(v []Volume, m *Member) { v[0].R = nil }, ErrCorrupt},
		{"unsupported encryption", func(v []Volume, m *Member) { m.Encryption = nil }, ErrEncrypted},
		{"solid", func(v []Volume, m *Member) { m.Solid = true }, ErrSolid},
		{"compressed", func(v []Volume, m *Member) { m.Stored = false }, ErrCompressed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vols, m := cbcFixture(t, []byte("hello world"))
			tc.mutate(vols, &m)
			if _, err := NewMemberReader(m, vols); !errors.Is(err, tc.sentinel) {
				t.Fatalf("got %v want %v", err, tc.sentinel)
			}
		})
	}
	if !errors.Is(ErrPasswordRequired, ErrEncrypted) || !errors.Is(ErrBadPassword, ErrEncrypted) {
		t.Fatal("password errors must retain encrypted classification")
	}
}

type cbcErrorReader struct {
	err   error
	short bool
}

func (r cbcErrorReader) ReadAt(p []byte, off int64) (int, error) {
	if r.short && len(p) > 0 {
		return len(p) - 1, r.err
	}
	return 0, r.err
}

func TestMemberCBCTransportAndShortReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    io.ReaderAt
		want error
	}{
		{"cancelled", cbcErrorReader{err: context.Canceled}, context.Canceled},
		{"deadline", cbcErrorReader{err: context.DeadlineExceeded}, context.DeadlineExceeded},
		{"nil short", cbcErrorReader{short: true}, ErrCorrupt},
		{"EOF short", cbcErrorReader{short: true, err: io.EOF}, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vols, m := cbcFixture(t, bytes.Repeat([]byte{1}, 100))
			vols[0].R = tc.r
			r, err := NewMemberReader(m, vols)
			if err != nil {
				t.Fatal(err)
			}
			for _, off := range []int64{0, 33} {
				buf := bytes.Repeat([]byte{77}, 5)
				n, err := r.ReadAt(buf, off)
				if n != 0 || !errors.Is(err, tc.want) || !bytes.Equal(buf, bytes.Repeat([]byte{77}, 5)) {
					t.Fatalf("ReadAt(%d): n=%d err=%v buf=%x", off, n, err, buf)
				}
			}
		})
	}
}

func TestMemberReaderPlainAndEOFSemantics(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		vols, m := cbcFixture(t, []byte("abc"))
		if !encrypted {
			vols = []Volume{{R: bytes.NewReader([]byte("xabcy")), Size: 5}}
			m = Member{Size: 3, Stored: true, Extents: []Extent{{Offset: 1, Size: 3}}}
		}
		r, err := NewMemberReader(m, vols)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 5)
		n, err := r.ReadAt(buf, 1)
		if n != 2 || err != io.EOF || string(buf[:n]) != "bc" {
			t.Fatalf("partial EOF: %d %v %q", n, err, buf)
		}
		if n, err := r.ReadAt(buf, 3); n != 0 || err != io.EOF {
			t.Fatalf("EOF: %d %v", n, err)
		}
		if n, err := r.ReadAt(nil, 3); n != 0 || err != nil {
			t.Fatalf("empty: %d %v", n, err)
		}
		if _, err := r.ReadAt(buf, -1); err == nil {
			t.Fatalf("negative: %v", err)
		}
	}
}

type cbcFailAfterReader struct {
	r      io.ReaderAt
	failAt int64
}

func (r cbcFailAfterReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.failAt {
		return 0, context.Canceled
	}
	return r.r.ReadAt(p, off)
}

func TestMemberCBCPartialProgressBeforeTransportFailure(t *testing.T) {
	plaintext := bytes.Repeat([]byte{19}, cbcReadChunk*3)
	vols, m := cbcFixture(t, plaintext)
	vols[0].R = cbcFailAfterReader{r: vols[0].R, failAt: m.Extents[0].Offset + cbcReadChunk}
	r, err := NewMemberReader(m, vols)
	if err != nil {
		t.Fatal(err)
	}
	buf := bytes.Repeat([]byte{77}, len(plaintext))
	n, err := r.ReadAt(buf, 0)
	if n != cbcReadChunk || !errors.Is(err, context.Canceled) || !bytes.Equal(buf[:n], plaintext[:n]) {
		t.Fatalf("partial progress: n=%d err=%v", n, err)
	}
	if !bytes.Equal(buf[n:], bytes.Repeat([]byte{77}, len(buf)-n)) {
		t.Fatal("failed CBC chunk modified caller bytes")
	}
}
