package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestRARRealEncryptedFixtures(t *testing.T) {
	for _, name := range []string{"rar5-psw.rar", "rar5-hpsw.rar", "rar3-comment-hpsw.rar"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/rar-encrypted/" + name)
			if err != nil {
				t.Fatal(err)
			}
			v, err := ReadRARVolume(bytes.NewReader(data), int64(len(data)), &RAROptions{Password: "password"})
			if err != nil {
				t.Fatal(err)
			}
			members, err := ComposeRAR([]*RARVolume{v})
			if err != nil {
				t.Fatal(err)
			}
			decrypted := 0
			for _, m := range members {
				if m.Encrypted && m.Encryption == nil {
					t.Fatal("missing derived key")
				}
				if !m.Stored {
					continue
				}
				decrypted++
				r, err := NewMemberReader(m, []Volume{{R: bytes.NewReader(data), Size: int64(len(data))}})
				if err != nil {
					t.Fatal(err)
				}
				out, err := io.ReadAll(io.NewSectionReader(r, 0, m.Size))
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(out)
				if hex.EncodeToString(sum[:]) != "2eaebb4c18cdef7f20089f8a2fa3475bc59c2a193f66e2f1513609a4bef13e22" {
					t.Fatalf("unexpected decrypted output %x", sum)
				}
			}
			if name == "rar3-comment-hpsw.rar" {
				if len(members) != 2 || members[0].Name != "file1.txt" || members[1].Name != "file2.txt" {
					t.Fatal("RAR4 encrypted header names not decoded")
				}
			} else if decrypted != 1 {
				t.Fatalf("decrypted %d stored members, want1", decrypted)
			}
			_, err = ReadRARVolume(bytes.NewReader(data), int64(len(data)), &RAROptions{Password: "wrong"})
			if !errors.Is(err, ErrBadPassword) {
				t.Fatalf("wrong password: %v", err)
			}
			if name != "rar5-psw.rar" {
				_, err = ReadRARVolume(bytes.NewReader(data), int64(len(data)), nil)
				if !errors.Is(err, ErrPasswordRequired) {
					t.Fatalf("missing password: %v", err)
				}
			}
		})
	}
}

func TestRARPasswordWorkBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newRARCrypto(RAROptions{Context: ctx, Password: "password"})
	if _, err := c.rar4(make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.rar5(15, make([]byte, 16), nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c = newRARCrypto(RAROptions{Password: "password"})
	if _, err := c.rar5(25, make([]byte, 16), nil); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	c.rounds = 1 << 26
	if _, err := c.rar4(make([]byte, 8)); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestRARPasswordCacheCoalescesAndCancels(t *testing.T) {
	cache := &RARPasswordCache{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c := newRARCrypto(RAROptions{Password: "password", PasswordCache: cache})
			if _, err := c.rar4(make([]byte, 8)); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if cache.derivations != 1 {
		t.Fatalf("derived %d times, expected one derivation", cache.derivations)
	}
	if len(cache.entries) != 1 || len(cache.pending) != 0 {
		t.Fatal("incorrect completed cache state")
	}
	// Waiting on another operation must honor this caller's cancellation.
	blocked := make(chan struct{})
	entered := make(chan struct{})
	finished := make(chan struct{})
	leader := newRARCrypto(RAROptions{Password: "password", PasswordCache: cache})
	go func() {
		defer close(finished)
		_, err := leader.derive("blocked", func() (rarKeys, error) { close(entered); <-blocked; return rarKeys{}, context.Canceled })
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	follower := newRARCrypto(RAROptions{Password: "password", Context: ctx, PasswordCache: cache})
	cancel()
	if _, err := follower.derive("blocked", func() (rarKeys, error) { t.Error("follower derived canceled work"); return rarKeys{}, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(blocked)
	<-finished
	if _, ok := cache.entries["blocked"]; ok {
		t.Fatal("canceled result cached")
	}
	if len(cache.pending) != 0 {
		t.Fatal("inflight entry leaked")
	}
}

func TestRARPasswordNormalizationAndArchiveBudget(t *testing.T) {
	for _, version := range []int{4, 5} {
		cache := &RARPasswordCache{}
		prefix := strings.Repeat("界", 128)
		first := newRARCrypto(RAROptions{Password: prefix, PasswordCache: cache})
		second := newRARCrypto(RAROptions{Password: prefix + "ignored", PasswordCache: cache})
		var a, b rarKeys
		var err error
		if version == 4 {
			a, err = first.rar4(make([]byte, 8))
			if err == nil {
				b, err = second.rar4(make([]byte, 8))
			}
		} else {
			a, err = first.rar5(15, make([]byte, 16), nil)
			if err == nil {
				b, err = second.rar5(15, make([]byte, 16), nil)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.key, b.key) || len(cache.entries) != 1 {
			t.Fatal("password normalization differs from upstream")
		}
		first.rounds = 1 << 26
		third := newRARCrypto(RAROptions{Password: prefix, PasswordCache: cache})
		if _, err = third.rar4(bytes.Repeat([]byte{1}, 8)); err != nil {
			t.Fatal("budget leaked between volumes", err)
		}
	}
}

// A long RAR4 set needs fresh salts for every encrypted header; the bounded
// work allowance belongs to each parse, not the lifetime of its shared cache.
func TestRARLargeVolumeSetBudget(t *testing.T) {
	cache := &RARPasswordCache{}
	for volume := 0; volume < 200; volume++ {
		c := newRARCrypto(RAROptions{Password: "password", PasswordCache: cache})
		for header := 0; header < 3; header++ {
			if err := c.budget(1<<18, 32*(1<<18)); err != nil {
				t.Fatalf("volume%d header%d: %v", volume, header, err)
			}
		}
	}
}

func TestRARCryptoRejectsDamagedMetadata(t *testing.T) {
	for _, version := range []int{4, 5} {
		data := buildEncryptedRARFixture(t, version, true, true)
		parsed := make([]*RARVolume, len(data))
		cache := &RARPasswordCache{}
		for i, b := range data {
			v, err := ReadRARVolume(bytes.NewReader(b), int64(len(b)), &RAROptions{Password: fixtureRARPassword, PasswordCache: cache})
			if err != nil {
				t.Fatal(err)
			}
			parsed[i] = v
		}
		if _, err := ComposeRAR(parsed[:1]); !errors.Is(err, ErrMissingVolume) {
			t.Fatal("missing volume", err)
		}
		original := parsed[1].Files[0].Encryption
		changed := *original
		changed.IV[0] ^= 1
		parsed[1].Files[0].Encryption = &changed
		if _, err := ComposeRAR(parsed); !errors.Is(err, ErrCorrupt) {
			t.Fatal("changed split IV", err)
		}
		parsed[1].Files[0].Encryption = original
		parsed[1].Files[0].UnpackedSize++
		if _, err := ComposeRAR(parsed); !errors.Is(err, ErrCorrupt) {
			t.Fatal("changed split size", err)
		}
		parsed[1].Files[0].UnpackedSize--
		parsed[1].Files[0].PackedSize--
		if _, err := ComposeRAR(parsed); !errors.Is(err, ErrCorrupt) {
			t.Fatal("truncated ciphertext", err)
		}
		damaged := bytes.Clone(data[0])
		offset := parsed[0].Files[0].HeaderOffset
		// RAR4 uses an8byte salt, RAR5 a16byte IV before encrypted header.
		if version == 4 {
			offset += 8
		} else {
			offset += 16
		}
		damaged[offset] ^= 1
		if _, err := ReadRARVolume(bytes.NewReader(damaged), int64(len(damaged)), &RAROptions{Password: fixtureRARPassword}); err == nil {
			t.Fatal("corrupt encrypted header accepted")
		}
	}
}
