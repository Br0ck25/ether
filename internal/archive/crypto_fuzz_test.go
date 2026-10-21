package archive

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

// These targets enable password processing: the older parser fuzz targets
// intentionally exercise only the no-password API. Keep each input small and
// cancellation checked by both KDFs and backing reads, so mutated work factors
// cannot monopolize a fuzz worker. Cancellation is a valid parse outcome.
const cryptoFuzzTimeout = 100 * time.Millisecond
const cryptoFuzzMaxInput = 1 << 20

type cryptoFuzzReader struct {
	ctx context.Context
	r   *bytes.Reader
}

func (r cryptoFuzzReader) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.ReadAt(p, off)
}

func fuzzEncryptedMembers(t *testing.T, ctx context.Context, members []Member, b []byte) {
	t.Helper()
	vols := []Volume{{R: cryptoFuzzReader{ctx: ctx, r: bytes.NewReader(b)}, Size: int64(len(b))}}
	var buf [65]byte
	for i, m := range members {
		if i >= 64 || ctx.Err() != nil {
			return
		}
		for _, e := range m.Extents {
			if e.Volume != 0 || e.Offset < 0 || e.Size < 0 || e.Offset > int64(len(b)) || e.Size > int64(len(b))-e.Offset {
				t.Fatal("parser produced an extent outside the input")
			}
		}
		r, err := NewMemberReader(m, vols)
		if err != nil {
			continue // Unsupported media and malformed member bounds may reject.
		}
		for _, off := range []int64{0, 1, 15, 16, 17, r.Size() / 2, r.Size() - 1, r.Size()} {
			if off < 0 {
				continue
			}
			n, err := r.ReadAt(buf[:], off)
			if n < 0 || n > len(buf) || int64(n) > max(int64(0), r.Size()-off) {
				t.Fatal("member read returned an invalid byte count")
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil && err != io.EOF {
				t.Fatalf("accepted member range failed: %v", err)
			}
			if n < len(buf) && err == nil {
				t.Fatal("short member read without an error")
			}
		}
	}
}

func FuzzEncryptedSevenZip(f *testing.F) {
	for _, seed := range []struct{ name, password string }{
		{"content.7z", "Secret"},
		{"header.7z", "Secret"},
		{"compressed-header.7z", "Secret"},
		{"unicode-multi.7z", "Sëcret🔐日本"},
		{"header.7z", "wrong"},
	} {
		f.Add(readFixture(f, "7z-crypto", seed.name), seed.password)
	}
	f.Fuzz(func(t *testing.T, b []byte, password string) {
		if len(b) > cryptoFuzzMaxInput || len(password) > 256 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), cryptoFuzzTimeout)
		defer cancel()
		r := cryptoFuzzReader{ctx: ctx, r: bytes.NewReader(b)}
		members, err := List7zWithPassword(ctx, r, int64(len(b)), password)
		if err != nil {
			return
		}
		fuzzEncryptedMembers(t, ctx, members, b)
	})
}

func FuzzEncryptedRAR(f *testing.F) {
	for _, name := range []string{
		"rar4-headersfalse-multifalse.part1.rar",
		"rar4-headerstrue-multifalse.part1.rar",
		"rar5-headersfalse-multifalse.part1.rar",
		"rar5-headerstrue-multifalse.part1.rar",
	} {
		f.Add(readFixture(f, "rar-encrypted", name), fixtureRARPassword)
	}
	f.Add(readFixture(f, "rar-encrypted", "rar5-headerstrue-multifalse.part1.rar"), "wrong")
	f.Fuzz(func(t *testing.T, b []byte, password string) {
		if len(b) > cryptoFuzzMaxInput || len(password) > 256 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), cryptoFuzzTimeout)
		defer cancel()
		r := cryptoFuzzReader{ctx: ctx, r: bytes.NewReader(b)}
		v, err := ReadRARVolume(r, int64(len(b)), &RAROptions{Context: ctx, Password: password, MaxFiles: 64})
		if err != nil {
			return
		}
		for _, file := range v.Files {
			if file.DataOffset < 0 || file.PackedSize < 0 || file.DataOffset > int64(len(b)) || file.PackedSize > int64(len(b))-file.DataOffset {
				t.Fatal("RAR parser produced a file outside the input")
			}
		}
		members, err := ComposeRAR([]*RARVolume{v})
		if err != nil {
			return
		}
		fuzzEncryptedMembers(t, ctx, members, b)
	})
}
