package archive

// 7z AES property decoding and key derivation adapted from bodgit/sevenzip,
// internal/aes7z at bc7ebbd2043c386e3bab1bd04a64a62062890fc4 (BSD-3-Clause).
// Copyright (c) 2020, Matt Dainty.
// See SEVENZIP_CRYPTO_LICENSE. Ether adds cancellation, bounded per-parse
// derivation reuse and a cumulative work budget; no process-global secrets.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

const sz7MaxKDFPower = 24
const sz7MaxKDFWork = 1 << 25

type sz7Crypto struct {
	ctx         context.Context
	password    string
	enabled     bool
	keys        map[string][]byte
	work        uint64
	hashedBytes uint64
}

func (c *sz7Crypto) encryption(props []byte) (*Encryption, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if !c.enabled || c.password == "" {
		return nil, ErrPasswordRequired
	}
	if len(props) < 1 {
		return nil, fmt.Errorf("archive: 7z AES properties: %w", ErrCorrupt)
	}
	power := props[0] & 0x3f
	var salt []byte
	var iv [aes.BlockSize]byte
	if props[0]&0xc0 != 0 {
		if len(props) < 2 {
			return nil, fmt.Errorf("archive: 7z AES properties: %w", ErrCorrupt)
		}
		ns := int((props[0]>>7)&1) + int(props[1]>>4)
		ni := int((props[0]>>6)&1) + int(props[1]&15)
		if len(props) != 2+ns+ni {
			return nil, fmt.Errorf("archive: 7z AES properties length: %w", ErrCorrupt)
		}
		salt = props[2 : 2+ns]
		copy(iv[:], props[2+ns:])
	} else if len(props) != 1 {
		return nil, fmt.Errorf("archive: 7z AES properties length: %w", ErrCorrupt)
	}
	if power != 63 && power > sz7MaxKDFPower {
		return nil, fmt.Errorf("archive: 7z AES work factor exceeds limit: %w", ErrUnsupported)
	}
	cacheID := string(append([]byte{power}, salt...))
	key := c.keys[cacheID]
	if key == nil {
		if len(c.password) > 4096 {
			return nil, fmt.Errorf("archive: 7z password too long: %w", ErrUnsupported)
		}
		u := utf16.Encode([]rune(c.password))
		input := make([]byte, len(salt)+2*len(u))
		copy(input, salt)
		for i, v := range u {
			binary.LittleEndian.PutUint16(input[len(salt)+i*2:], v)
		}
		key = make([]byte, sha256.Size)
		if power == 63 {
			copy(key, input)
		} else {
			rounds := uint64(1) << power
			if c.work+rounds > sz7MaxKDFWork {
				return nil, fmt.Errorf("archive: 7z cumulative AES work exceeds limit: %w", ErrUnsupported)
			}
			// Bound bytes hashed as well as rounds: long passwords must not
			// multiply the permitted CPU budget into gigabytes of work.
			bytesToHash := rounds * uint64(len(input)+8)
			if bytesToHash > 512<<20 || c.hashedBytes > (512<<20)-bytesToHash {
				return nil, fmt.Errorf("archive: 7z AES byte-work exceeds limit: %w", ErrUnsupported)
			}
			c.hashedBytes += bytesToHash
			c.work += rounds
			h := sha256.New()
			var counter [8]byte
			for i := uint64(0); i < rounds; i++ {
				if i&1023 == 0 {
					if err := c.ctx.Err(); err != nil {
						return nil, err
					}
				}
				_, _ = h.Write(input)
				binary.LittleEndian.PutUint64(counter[:], i)
				_, _ = h.Write(counter[:])
			}
			copy(key, h.Sum(nil))
		}
		if len(c.keys) < 32 {
			c.keys[cacheID] = key
		}
	}
	return &Encryption{Key: key, IV: iv}, nil
}

func (c *sz7Crypto) decrypt(props, packed []byte, unpack uint64) ([]byte, error) {
	enc, err := c.encryption(props)
	if err != nil {
		return nil, err
	}
	if len(packed)%aes.BlockSize != 0 || unpack > uint64(len(packed)) || uint64(len(packed))-unpack >= aes.BlockSize {
		return nil, fmt.Errorf("archive: 7z AES header size: %w", ErrCorrupt)
	}
	block, err := aes.NewCipher(enc.Key)
	if err != nil {
		return nil, err
	}
	cipher.NewCBCDecrypter(block, enc.IV[:]).CryptBlocks(packed, packed)
	return packed[:unpack], nil
}

// chain returns a single-input/output coder chain in data-flow order. Coder
// declaration order is not necessarily execution order. Reject cycles,
// duplicate binds and disconnected coders before touching packed data.
func (f *sz7Fold) chain() ([]int, error) {
	if f.numPacked != 1 || len(f.coders) == 0 || len(f.bindIn) != len(f.coders)-1 || len(f.bindIn) != len(f.bindOut) {
		return nil, fmt.Errorf("archive: 7z coder graph: %w", ErrUnsupported)
	}
	n := len(f.coders)
	next := make([]int, n)
	bound := make([]bool, n)
	for i, c := range f.coders {
		if c.numIn != 1 || c.numOut != 1 {
			return nil, fmt.Errorf("archive: 7z coder streams: %w", ErrUnsupported)
		}
		next[i] = -1
	}
	for i, in := range f.bindIn {
		out := f.bindOut[i]
		if in >= uint64(n) || out >= uint64(n) || bound[in] || next[out] != -1 {
			return nil, fmt.Errorf("archive: 7z duplicate/out-of-range bind: %w", ErrCorrupt)
		}
		bound[in] = true
		next[out] = int(in)
	}
	start := -1
	for i, b := range bound {
		if !b {
			if start != -1 {
				return nil, fmt.Errorf("archive: 7z disconnected coders: %w", ErrCorrupt)
			}
			start = i
		}
	}
	order := make([]int, 0, n)
	seen := make([]bool, n)
	for i := start; i != -1; i = next[i] {
		if seen[i] {
			return nil, fmt.Errorf("archive: 7z coder cycle: %w", ErrCorrupt)
		}
		seen[i] = true
		order = append(order, i)
	}
	if len(order) != n {
		return nil, fmt.Errorf("archive: 7z disconnected coders: %w", ErrCorrupt)
	}
	return order, nil
}
