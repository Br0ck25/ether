package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Keep key derivation cheap in tests (the minimum a restore accepts).
func init() { exportIterations = minIterations }

const testPassword = "correct horse battery"

func encrypt(t *testing.T, plain []byte, password string) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewEncryptWriter(&out, password)
	if err != nil {
		t.Fatal(err)
	}
	// Uneven writes exercise chunk boundaries.
	for p := plain; len(p) > 0; {
		n := min(len(p), 7777)
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func decrypt(enc []byte, password string) ([]byte, error) {
	r, err := NewDecryptReader(bytes.NewReader(enc), password)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestEncryptedRoundTripSizes(t *testing.T) {
	for _, size := range []int{0, 1, encChunk - 1, encChunk, encChunk + 1, 3 * encChunk, 3*encChunk + 5} {
		plain := make([]byte, size)
		rand.Read(plain)
		enc := encrypt(t, plain, testPassword)
		if !IsEncrypted(enc) || (size > 16 && bytes.Contains(enc, plain[:16])) {
			t.Fatalf("size %d: not encrypted", size)
		}
		got, err := decrypt(enc, testPassword)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d: round trip %v (got %d bytes)", size, err, len(got))
		}
	}
}

func TestEncryptedRejectsWrongPasswordAndTampering(t *testing.T) {
	plain := make([]byte, 3*encChunk+100)
	rand.Read(plain)
	enc := encrypt(t, plain, testPassword)
	chunk := encChunk + encTagLen
	mutate := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), enc...)) }
	cases := map[string][]byte{
		"wrong password": nil,
		"empty":          {},
		"header only":    enc[:encHeaderLen],
		"salt flipped":   mutate(func(b []byte) []byte { b[encHeaderLen-10] ^= 1; return b }),
		"prefix flipped": mutate(func(b []byte) []byte { b[encHeaderLen-1] ^= 1; return b }),
		"body flipped":   mutate(func(b []byte) []byte { b[encHeaderLen+chunk+3] ^= 1; return b }),
		"tag flipped":    mutate(func(b []byte) []byte { b[len(b)-1] ^= 1; return b }),
		"final dropped":  enc[:encHeaderLen+3*chunk],
		"cut mid chunk":  enc[:encHeaderLen+chunk+100],
		"appended byte":  append(append([]byte(nil), enc...), 0),
		"final repeated": append(append([]byte(nil), enc...), enc[encHeaderLen+3*chunk:]...),
		"chunks swapped": mutate(func(b []byte) []byte {
			first := append([]byte(nil), b[encHeaderLen:encHeaderLen+chunk]...)
			copy(b[encHeaderLen:], b[encHeaderLen+chunk:encHeaderLen+2*chunk])
			copy(b[encHeaderLen+chunk:], first)
			return b
		}),
		"chunk dropped": append(append([]byte(nil), enc[:encHeaderLen+chunk]...), enc[encHeaderLen+2*chunk:]...),
	}
	for name, b := range cases {
		password := testPassword
		if b == nil {
			b, password = enc, "wrong horse battery"
		}
		got, err := decrypt(b, password)
		if err == nil {
			t.Errorf("%s: accepted (%d bytes)", name, len(got))
			continue
		}
		if strings.Contains(err.Error(), password) {
			t.Errorf("%s: error mentions the password: %v", name, err)
		}
	}
	if _, err := decrypt(enc, "wrong horse battery"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("wrong password: %v", err)
	}
}

// Header parameters are checked before any key derivation, so a hostile
// file cannot ask for unbounded work.
func TestEncryptedHeaderLimits(t *testing.T) {
	enc := encrypt(t, []byte("bundle"), testPassword)
	set := func(off int, v uint32) []byte {
		b := append([]byte(nil), enc...)
		binary.BigEndian.PutUint32(b[len(encMagic)+off:], v)
		return b
	}
	kdf := append([]byte(nil), enc...)
	kdf[len(encMagic)] = 2
	for name, b := range map[string][]byte{
		"iterations above cap": set(1, maxIterations+1),
		"iterations huge":      set(1, 1<<31),
		"iterations below min": set(1, minIterations-1),
		"chunk size":           set(5, 1<<30),
		"kdf":                  kdf,
		"magic":                append([]byte("ETHERBK2"), enc[len(encMagic):]...),
	} {
		start := time.Now()
		if _, err := decrypt(b, testPassword); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: took %v", name, d)
		}
	}
	if _, err := NewEncryptWriter(io.Discard, "short"); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("short export password: %v", err)
	}
}

// An encrypted restore installs nothing unless every byte authenticates.
func TestRestoreEncryptedAllOrNothing(t *testing.T) {
	ctx := context.Background()
	bundle := snapshotBytes(t)
	enc := encrypt(t, bundle, testPassword)
	parent := t.TempDir()
	chunk := encChunk + encTagLen
	bad := map[string][]byte{
		"truncated":   enc[:len(enc)-1],
		"no final":    enc[:encHeaderLen+(len(enc)-encHeaderLen-1)/chunk*chunk],
		"appended":    append(append([]byte(nil), enc...), "trailing"...),
		"tail flip":   func() []byte { b := append([]byte(nil), enc...); b[len(b)-20] ^= 0x80; return b }(),
		"wrong magic": bundle,
	}
	for name, b := range bad {
		dest := filepath.Join(parent, name)
		if err := RestoreEncrypted(ctx, bytes.NewReader(b), testPassword, dest); err == nil {
			t.Errorf("%s: restored", name)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Errorf("%s: destination published", name)
		}
	}
	if err := RestoreEncrypted(ctx, bytes.NewReader(enc), "wrong horse battery", filepath.Join(parent, "pw")); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("failed restores left files behind: %v", entries)
	}
	dest := filepath.Join(parent, "ok")
	if err := RestoreEncrypted(ctx, bytes.NewReader(enc), testPassword, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "config.json")); err != nil || !strings.Contains(string(b), "private") {
		t.Fatalf("restored config %s %v", b, err)
	}
}
