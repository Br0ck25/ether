package archive

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"testing"

	at "github.com/braedonsaunders/ether/internal/archive/archivetest"
)

func TestSevenZipEncryptedRealFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		files          []string
	}{
		{"content.7z", "Secret", []string{"video.mkv"}},
		{"header.7z", "Secret", []string{"video.mkv"}},
		{"unicode-multi.7z", "Sëcret🔐日本", []string{"sidecar.txt", "video.mkv"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := readFixture(t, "7z-crypto", tc.name)
			members, err := List7zWithPassword(context.Background(), bytes.NewReader(b), int64(len(b)), tc.password)
			if err != nil {
				t.Fatal(err)
			}
			if len(members) != len(tc.files) {
				t.Fatalf("got %d members", len(members))
			}
			for i, m := range members {
				if m.Name != tc.files[i] || !m.Encrypted || !m.Stored || m.Encryption == nil || m.Streamable() != nil {
					t.Fatalf("invalid member %s", m.Name)
				}
				want := readFixture(t, "7z-crypto", m.Name)
				mr, err := NewMemberReader(m, memVols(b))
				if err != nil {
					t.Fatal(err)
				}
				got := readAll(t, mr, m.Size)
				if !bytes.Equal(got, want) {
					t.Fatal("decrypted payload mismatch")
				}
				checkRandomReads(t, mr, want, nil, 71)
				// An intentionally unaligned volume boundary cuts through a CBC block.
				vols := memVols(b[:49], b[49:])
				m.Extents, err = SplitExtents(vols, m.Extents)
				if err != nil {
					t.Fatal(err)
				}
				mr, err = NewMemberReader(m, vols)
				if err != nil {
					t.Fatal(err)
				}
				checkRandomReads(t, mr, want, []int64{1, 15, 16, 17, 4090}, 73)
			}
		})
	}
}

func TestSevenZipExistingEncryptedFixtures(t *testing.T) {
	for _, name := range []string{"enc_content.7z", "enc_header.7z"} {
		b := readFixture(t, "7z", name)
		members, err := List7zWithPassword(context.Background(), bytes.NewReader(b), int64(len(b)), "Secret")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		mr, err := NewMemberReader(members[0], memVols(b))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readAll(t, mr, members[0].Size), readFixture(t, "7z", "rand_b.bin")) {
			t.Fatal("payload mismatch")
		}
	}
}

func TestSevenZipEncryptedPasswordAndDamage(t *testing.T) {
	for _, name := range []string{"content.7z", "header.7z"} {
		b := readFixture(t, "7z-crypto", name)
		if _, err := List7zWithPassword(context.Background(), bytes.NewReader(b), int64(len(b)), ""); !errors.Is(err, ErrPasswordRequired) {
			t.Fatalf("%s missing password: %v", name, err)
		}
	}
	b := readFixture(t, "7z-crypto", "header.7z")
	if _, err := List7zWithPassword(context.Background(), bytes.NewReader(b), int64(len(b)), "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	for _, cut := range []int{0, 7, 31, len(b) - 1} {
		if _, err := List7zWithPassword(context.Background(), bytes.NewReader(b[:cut]), int64(cut), "Secret"); err == nil {
			t.Fatalf("accepted truncation at %d", cut)
		}
	}
	damaged := bytes.Clone(b)
	damaged[len(damaged)-3] ^= 0x40
	if _, err := List7zWithPassword(context.Background(), bytes.NewReader(damaged), int64(len(damaged)), "Secret"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt header: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := List7zWithPassword(ctx, bytes.NewReader(b), int64(len(b)), "Secret"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestSevenZipKDFLimitsAndReuse(t *testing.T) {
	c := &sz7Crypto{ctx: context.Background(), password: "secret", enabled: true, keys: make(map[string][]byte)}
	for _, p := range [][]byte{nil, {0x40}, {0x40, 0xff}, {25}, {62}} {
		if _, err := c.encryption(p); err == nil {
			t.Fatalf("accepted invalid/expensive properties %x", p)
		}
	}
	a, err := c.encryption([]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.encryption([]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Key, b.Key) || c.work != 4 || len(c.keys) != 1 {
		t.Fatal("key derivation not reused")
	}
	// Raw-key mode is permitted by the format and does not perform hashing.
	raw, err := c.encryption([]byte{63})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw.Key[:12], []byte{'s', 0, 'e', 0, 'c', 0, 'r', 0, 'e', 0, 't', 0}) {
		t.Fatal("raw UTF16 key")
	}
	c.work = sz7MaxKDFWork
	if _, err := c.encryption([]byte{3}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("cumulative budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.ctx = ctx
	if _, err := c.encryption([]byte{2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cache ignored cancellation: %v", err)
	}
}

func TestSevenZipCoderGraph(t *testing.T) {
	f := sz7Fold{coders: []sz7Coder{{id: sz7MethodCopy, numIn: 1, numOut: 1}, {id: sz7MethodAES, numIn: 1, numOut: 1}}, numPacked: 1, bindIn: []uint64{0}, bindOut: []uint64{1}}
	order, err := f.chain()
	if err != nil || len(order) != 2 || order[0] != 1 {
		t.Fatalf("reordered chain %v %v", order, err)
	}
	f.bindIn = []uint64{1}
	f.bindOut = []uint64{1}
	if _, err := f.chain(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("cycle: %v", err)
	}
}

// Build a Copy folder containing several members, then wrap the folder's
// ciphertext in a 7z AES coder. 7zz itself emits a separate Copy folder per
// file, so this tests the legal shared-folder offset case independently.
func TestSevenZipEncryptedSharedFolderOffsets(t *testing.T) {
	files := []at.SevenZipFile{{Name: "a.mkv", Data: randBytes(81, 101)}, {Name: "b.mkv", Data: randBytes(82, 203)}}
	plainArchive := at.Build7zCopy(files, true)
	off := 32 + int(binary.LittleEndian.Uint64(plainArchive[12:]))
	header := bytes.Clone(plainArchive[off:])
	// Synthetic builder emits single Copy coder: numCoders=1, flags=1,id=0.
	needle := []byte{sz7Folder, 1, 0, 1, 1, 0}
	atPos := bytes.Index(header, needle)
	if atPos < 0 {
		t.Fatal("fixture coder missing")
	}
	header = append(append(append([]byte{}, header[:atPos+3]...), 1, 0x24, 0x06, 0xf1, 0x07, 0x01, 1, 63), header[atPos+6:]...)
	packedPlain := append(bytes.Clone(files[0].Data), files[1].Data...)
	padded := append(bytes.Clone(packedPlain), make([]byte, (16-len(packedPlain)%16)%16)...)
	c := &sz7Crypto{ctx: context.Background(), password: "shared", enabled: true, keys: make(map[string][]byte)}
	enc, err := c.encryption([]byte{63})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(enc.Key)
	cipher.NewCBCEncrypter(block, enc.IV[:]).CryptBlocks(padded, padded)
	// Pack size is the first two-byte NUMBER following kPackInfo, position=0,
	// count=1,kSize. Both 304 and 304-rounded are two-byte NUMBERs.
	p := bytes.Index(header, []byte{sz7PackInfo, 0, 1, sz7Size})
	if p < 0 {
		t.Fatal("fixture pack missing")
	}
	header[p+4] = 0x80 | byte(len(padded)>>8)
	header[p+5] = byte(len(padded))
	sh := bytes.Clone(plainArchive[:32])
	binary.LittleEndian.PutUint64(sh[12:], uint64(len(padded)))
	binary.LittleEndian.PutUint64(sh[20:], uint64(len(header)))
	binary.LittleEndian.PutUint32(sh[28:], crc32.ChecksumIEEE(header))
	binary.LittleEndian.PutUint32(sh[8:], crc32.ChecksumIEEE(sh[12:]))
	archiveBytes := append(append(sh, padded...), header...)
	members, err := List7zWithPassword(context.Background(), bytes.NewReader(archiveBytes), int64(len(archiveBytes)), "shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[1].Encryption.Offset != 101 {
		t.Fatal("wrong folder-relative member offset")
	}
	for i, m := range members {
		mr, err := NewMemberReader(m, memVols(archiveBytes))
		if err != nil {
			t.Fatal(err)
		}
		checkRandomReads(t, mr, files[i].Data, nil, 84)
	}
}

func TestSevenZipDecryptTruncatedBlock(t *testing.T) {
	c := &sz7Crypto{ctx: context.Background(), password: "x", enabled: true, keys: make(map[string][]byte)}
	if _, err := c.decrypt([]byte{63}, make([]byte, 15), 15); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("partial block: %v", err)
	}
}

// Reader errors must remain distinguishable from password/corruption failures.
func TestSevenZipEncryptedIOError(t *testing.T) {
	b := readFixture(t, "7z-crypto", "header.7z")
	_, err := List7zWithPassword(context.Background(), sz7FailReader{}, int64(len(b)), "Secret")
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("I/O error: %v", err)
	}
}

type sz7FailReader struct{}

func (sz7FailReader) ReadAt([]byte, int64) (int, error) { return 0, os.ErrPermission }

var _ io.ReaderAt = sz7FailReader{}

func TestSevenZipEncryptedCompressedHeader(t *testing.T) {
	b := readFixture(t, "7z-crypto", "compressed-header.7z")
	// Prove this fixture exercises AES followed by a compressed header coder,
	// rather than accidentally testing another AES-only header.
	off := 32 + int(binary.LittleEndian.Uint64(b[12:]))
	encoded := sz7Buf{b: b[off+1:]}
	st := encoded.streamsInfo()
	if encoded.err != nil || len(st.folders) != 1 || !st.folders[0].hasMethod(sz7MethodAES) || !st.folders[0].hasMethod(sz7MethodLZMA) {
		t.Fatal("fixture lacks AES + LZMA header")
	}
	members, err := List7zWithPassword(context.Background(), bytes.NewReader(b), int64(len(b)), "Secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 80 {
		t.Fatalf("got%d members", len(members))
	}
	for i, m := range members {
		want := []byte(fmt.Sprintf("Fixture %d\n", i))
		mr, err := NewMemberReader(m, memVols(b))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readAll(t, mr, m.Size), want) {
			t.Fatalf("member%d payload mismatch", i)
		}
	}
	if _, err := List7zWithPassword(context.Background(), bytes.NewReader(b), int64(len(b)), "incorrect"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("wrong compressed header password:%v", err)
	}
}

func TestSevenZipEncryptedCompressedMediaRejected(t *testing.T) {
	b := readFixture(t, "7z-crypto", "compressed-content.7z")
	members, err := List7zWithPassword(context.Background(), bytes.NewReader(b), int64(len(b)), "Secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Stored || members[0].Streamable() == nil {
		t.Fatal("compressed encrypted media accepted")
	}
}

// Deterministic cancellation in the inner KDF loop avoids a timing-dependent
// test and proves cancellation is checked after derivation has begun.
type sz7CancelDuringKDF struct {
	context.Context
	calls int
}

func (c *sz7CancelDuringKDF) Err() error {
	c.calls++
	if c.calls >= 3 {
		return context.Canceled
	}
	return nil
}
func TestSevenZipKDFCancellationDuringWork(t *testing.T) {
	ctx := &sz7CancelDuringKDF{Context: context.Background()}
	c := &sz7Crypto{ctx: ctx, password: "secret", enabled: true, keys: make(map[string][]byte)}
	if _, err := c.encryption([]byte{19}); !errors.Is(err, context.Canceled) {
		t.Fatalf("KDF cancellation:%v", err)
	}
	if len(c.keys) != 0 {
		t.Fatal("canceled key entered cache")
	}
}

func TestSevenZipKDFLongPasswordBudget(t *testing.T) {
	c := &sz7Crypto{ctx: context.Background(), password: strings.Repeat("x", 4096), enabled: true, keys: make(map[string][]byte)}
	if _, err := c.encryption([]byte{19}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("password-amplified CPU budget:%v", err)
	}
}
