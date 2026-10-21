package archive

// The password derivation routines are adapted from nwaples/rardecode.
// Copyright (c) 2015, Nicholas Waples. All rights reserved.
// See RAR_CRYPTO_LICENSE for the BSD-2-Clause license.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"unicode/utf16"
)

// rarCrypto is local to one volume parse; secrets never enter global caches.
type rarCrypto struct {
	ctx      context.Context
	password string
	cache    *RARPasswordCache
	budgetMu sync.Mutex
	rounds   uint64
	byteWork uint64
}

// RARPasswordCache shares at most 16 derived keys across volume parses.
// Its zero value is usable; discard it with the archive plan.
type RARPasswordCache struct {
	mu          sync.Mutex
	entries     map[string]rarKeys
	pending     map[string]chan struct{}
	gate        chan struct{}
	derivations uint64
}

// derive coalesces identical work and serializes different salts per archive.
// A waiting caller can cancel without canceling another caller's derivation.
func (c *rarCrypto) derive(id string, work func() (rarKeys, error)) (rarKeys, error) {
	for {
		if err := c.ctx.Err(); err != nil {
			return rarKeys{}, err
		}
		c.cache.mu.Lock()
		if k, ok := c.cache.entries[id]; ok {
			c.cache.mu.Unlock()
			return k, nil
		}
		if pending := c.cache.pending[id]; pending != nil {
			c.cache.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-c.ctx.Done():
				return rarKeys{}, c.ctx.Err()
			}
		}
		if len(c.cache.pending) >= 64 {
			c.cache.mu.Unlock()
			return rarKeys{}, fmt.Errorf("archive: too many pending RAR password derivations: %w", ErrCorrupt)
		}
		if c.cache.pending == nil {
			c.cache.pending = make(map[string]chan struct{})
		}
		done := make(chan struct{})
		c.cache.pending[id] = done
		if c.cache.gate == nil {
			c.cache.gate = make(chan struct{}, 1)
		}
		gate := c.cache.gate
		c.cache.mu.Unlock()
		var k rarKeys
		var err error
		select {
		case gate <- struct{}{}:
			k, err = work()
			c.cache.mu.Lock()
			c.cache.derivations++
			c.cache.mu.Unlock()
			<-gate
		case <-c.ctx.Done():
			err = c.ctx.Err()
		}
		c.cache.mu.Lock()
		if err == nil {
			if len(c.cache.entries) >= 16 {
				clear(c.cache.entries)
			}
			if c.cache.entries == nil {
				c.cache.entries = make(map[string]rarKeys)
			}
			c.cache.entries[id] = k
		}
		delete(c.cache.pending, id)
		close(done)
		c.cache.mu.Unlock()
		return k, err
	}
}

type rarKeys struct {
	key   []byte
	iv    [16]byte
	check []byte
}

func newRARCrypto(o RAROptions) *rarCrypto {
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	cache := o.PasswordCache
	if cache == nil {
		cache = &RARPasswordCache{}
	}
	// Match upstream rardecode's public password normalization. Both RAR
	// formats accept at most128 Unicode code points, then encode per format.
	password := o.Password
	if len(password) > 128 {
		n := 0
		for offset := range password {
			if n == 128 {
				password = password[:offset]
				break
			}
			n++
		}
	}
	return &rarCrypto{ctx: ctx, password: password, cache: cache}
}
func (c *rarCrypto) budget(n, byteWork uint64) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	if n > (1<<26)-c.rounds || byteWork > (1<<30)-c.byteWork {
		return fmt.Errorf("archive: RAR password derivation budget exceeded: %w", ErrCorrupt)
	}
	c.rounds += n
	c.byteWork += byteWork
	return nil
}
func (c *rarCrypto) cacheID(id string) string {
	sum := sha256.Sum256([]byte(c.password))
	return string(sum[:]) + id
}
func (c *rarCrypto) rar4(salt []byte) (rarKeys, error) {
	var k rarKeys
	if c.password == "" {
		return k, ErrPasswordRequired
	}
	id := c.cacheID("4" + string(salt))
	return c.derive(id, func() (rarKeys, error) {
		var k rarKeys
		if err := c.budget(1<<18, uint64(len(c.password)*2+len(salt)+3)*(1<<18)); err != nil {
			return k, err
		}
		p := make([]byte, 0, len(c.password)*2+len(salt))
		for _, v := range utf16.Encode([]rune(c.password)) {
			p = append(p, byte(v), byte(v>>8))
		}
		p = append(p, salt...)
		h := sha1.New()
		var sum [20]byte
		var counter [3]byte
		for i := 0; i < 1<<18; i++ {
			if i&1023 == 0 {
				if err := c.ctx.Err(); err != nil {
					return k, err
				}
			}
			h.Write(p)
			counter = [3]byte{byte(i), byte(i >> 8), byte(i >> 16)}
			h.Write(counter[:])
			if i%((1<<18)/16) == 0 {
				h.Sum(sum[:0])
				k.iv[i/((1<<18)/16)] = sum[19]
			}
		}
		h.Sum(sum[:0])
		k.key = append([]byte(nil), sum[:16]...)
		for i := 0; i < 16; i += 4 {
			k.key[i], k.key[i+1], k.key[i+2], k.key[i+3] = k.key[i+3], k.key[i+2], k.key[i+1], k.key[i]
		}
		return k, nil
	})
}
func (c *rarCrypto) rar5(exp byte, salt, check []byte) (rarKeys, error) {
	var k rarKeys
	if c.password == "" {
		return k, ErrPasswordRequired
	}
	if exp > 24 {
		return k, fmt.Errorf("archive: RAR5 password work factor exceeds limit: %w", ErrCorrupt)
	}
	if check != nil {
		if len(check) != 12 {
			return k, fmt.Errorf("archive: RAR5 password check truncated: %w", ErrCorrupt)
		}
		sum := sha256.Sum256(check[:8])
		if subtle.ConstantTimeCompare(sum[:4], check[8:]) != 1 {
			return k, fmt.Errorf("archive: RAR5 password check damaged: %w", ErrCorrupt)
		}
	}
	id := c.cacheID("5" + string([]byte{exp}) + string(salt))
	k, err := c.derive(id, func() (rarKeys, error) {
		var k rarKeys
		rounds := uint64(1) << exp
		if err := c.budget(rounds+32, 0); err != nil {
			return k, err
		}
		prf := hmac.New(sha256.New, []byte(c.password))
		prf.Write(salt)
		prf.Write([]byte{0, 0, 0, 1})
		u := prf.Sum(nil)
		t := append([]byte(nil), u...)
		for stage, iter := range []uint64{rounds - 1, 16, 16} {
			for j := uint64(0); j < iter; j++ {
				if j&1023 == 0 {
					if err := c.ctx.Err(); err != nil {
						return k, err
					}
				}
				prf.Reset()
				prf.Write(u)
				u = prf.Sum(u[:0])
				for n, v := range u {
					t[n] ^= v
				}
			}
			if stage == 0 {
				k.key = append([]byte(nil), t...)
			}
		}
		for i, v := range t[8:] {
			t[i&7] ^= v
		}
		sum := sha256.Sum256(t[:8])
		k.check = append(append([]byte(nil), t[:8]...), sum[:4]...)
		return k, nil
	})
	if err != nil {
		return k, err
	}
	if check != nil {
		if subtle.ConstantTimeCompare(k.check, check) != 1 {
			c.cache.mu.Lock()
			delete(c.cache.entries, id)
			c.cache.mu.Unlock()
			return k, ErrBadPassword
		}
	}
	return k, nil
}

// decryptRARHeader reads only the padded encrypted header, never the payload.
func decryptRARHeader(r io.ReaderAt, size, pos int64, key []byte, iv [16]byte, rar5 bool) ([]byte, int64, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, err
	}
	first := make([]byte, 16)
	if err = readAt(r, first, pos, size); err != nil {
		return nil, 0, err
	}
	encryptedFirst := append([]byte(nil), first...)
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(first, first)
	var length int
	if rar5 {
		h, n := uvarint(first[4:])
		if n == 0 || h == 0 || h > rar5MaxHeader {
			return nil, 0, fmt.Errorf("archive: encrypted RAR5 header invalid: %w", ErrBadPassword)
		}
		length = 4 + n + int(h)
	} else {
		length = int(binary.LittleEndian.Uint16(first[5:]))
		if length < 7 {
			return nil, 0, fmt.Errorf("archive: encrypted RAR4 header invalid: %w", ErrBadPassword)
		}
	}
	padded := (length + 15) &^ 15
	if int64(padded) > size-pos {
		return nil, 0, fmt.Errorf("archive: encrypted RAR header size invalid: %w: %w", ErrBadPassword, ErrCorrupt)
	}
	data := make([]byte, padded)
	copy(data, encryptedFirst)
	if err = readAt(r, data[16:], pos+16, size); err != nil {
		return nil, 0, err
	}
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(data, data)
	return data[:length], int64(padded), nil
}

func parseRAR5Encryption(b *rar5Buf, c *rarCrypto, file bool) (*Encryption, error) {
	if c.password == "" {
		return nil, ErrPasswordRequired
	}
	version, flags := b.vint(), b.vint()
	if b.err != nil {
		return nil, b.err
	}
	if version != 0 || flags & ^uint64(3) != 0 {
		return nil, fmt.Errorf("archive: unsupported RAR5 encryption: %w", ErrEncrypted)
	}
	exp := b.bytes(1)
	salt := b.bytes(16)
	var iv []byte
	if file {
		iv = b.bytes(16)
	}
	var check []byte
	if flags&1 != 0 {
		check = b.bytes(12)
	}
	if b.err != nil {
		return nil, b.err
	}
	k, err := c.rar5(exp[0], salt, check)
	if err != nil {
		return nil, err
	}
	e := &Encryption{Key: k.key}
	copy(e.IV[:], iv)
	return e, nil
}
