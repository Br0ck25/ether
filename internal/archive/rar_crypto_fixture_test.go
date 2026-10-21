package archive

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	at "github.com/braedonsaunders/ether/internal/archive/archivetest"
)

const fixtureRARPassword = "pāss🔐word"

func fixtureRARPayload() []byte {
	b := make([]byte, 8197)
	for i := range b {
		b[i] = byte(i*31 + i/13)
	}
	copy(b, []byte{0x1a, 0x45, 0xdf, 0xa3})
	return b
}
func fixtureCBC(t *testing.T, key, iv, p []byte) []byte {
	t.Helper()
	b, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, (len(p)+15)&^15)
	copy(out, p)
	cipher.NewCBCEncrypter(b, iv).CryptBlocks(out, out)
	return out
}
func fixtureCat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
func fixtureLE32(v uint32) []byte { var b [4]byte; binary.LittleEndian.PutUint32(b[:], v); return b[:] }

// These deterministic files are independently checked with 7-Zip; fixed salts
// and IVs are ONLY for offline fixtures, never archive creation in production.
func buildEncryptedRARFixture(t *testing.T, version int, headers, multi bool) [][]byte {
	t.Helper()
	plain := fixtureRARPayload()
	salt := bytes.Repeat([]byte{0x52}, 16)
	iv := bytes.Repeat([]byte{0x73}, 16)
	c := newRARCrypto(RAROptions{Password: fixtureRARPassword})
	var keys rarKeys
	var err error
	if version == 4 {
		salt = salt[:8]
		keys, err = c.rar4(salt)
		iv = keys.iv[:]
	} else {
		keys, err = c.rar5(15, salt, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	encrypted := fixtureCBC(t, keys.key, iv, plain)
	parts := [][]byte{encrypted}
	if multi {
		parts = [][]byte{encrypted[:4093], encrypted[4093:]}
	}
	var vols [][]byte
	for index, part := range parts {
		before, after := index > 0, index < len(parts)-1
		checksum := crc32.ChecksumIEEE(plain)
		if after {
			checksum = crc32.ChecksumIEEE(part)
		}
		var out []byte
		if version == 4 {
			flags := uint16(0)
			if headers {
				flags |= rar4MainPassword
			}
			if multi {
				flags |= rar4MainVolume | rar4MainNewNumbering
				if !before {
					flags |= rar4MainFirstVolume
				}
			}
			out = fixtureCat(at.RAR4Signature, at.RAR4MainHeader(flags))
			fh := at.RAR4FileHeader(at.Header{Name: "fixture.mkv", PackSize: int64(len(part)), UnpackSize: int64(len(plain)), Encrypted: true, CRC: checksum, SplitBefore: before, SplitAfter: after})
			fh = append(fh, salt...)
			binary.LittleEndian.PutUint16(fh[3:], binary.LittleEndian.Uint16(fh[3:])|0x400)
			binary.LittleEndian.PutUint16(fh[5:], uint16(len(fh)))
			binary.LittleEndian.PutUint16(fh, uint16(crc32.ChecksumIEEE(fh[2:])))
			end := at.RAR4EndHeader(after, index)
			if headers {
				fh = fixtureCat(salt, fixtureCBC(t, keys.key, iv, fh))
				end = fixtureCat(salt, fixtureCBC(t, keys.key, iv, end))
			}
			out = fixtureCat(out, fh, part, end)
		} else {
			out = append(out, at.RAR5Signature...)
			if headers {
				out = append(out, at.RAR5Block(4, 0, fixtureCat(at.Vint(0), at.Vint(1), []byte{15}, salt, keys.check), nil, false, 0)...)
			}
			main := at.RAR5MainHeader(multi, index, false)
			fields := fixtureCat(at.Vint(4), at.Vint(uint64(len(plain))), at.Vint(0x20), fixtureLE32(checksum), at.Vint(0), at.Vint(1), at.Vint(11), []byte("fixture.mkv"))
			rec := at.RAR5Record(1, fixtureCat(at.Vint(0), at.Vint(1), []byte{15}, salt, iv, keys.check))
			flags := uint64(0)
			if before {
				flags |= rar5HdrSplitBefore
			}
			if after {
				flags |= rar5HdrSplitAfter
			}
			fh := at.RAR5Block(2, flags, fields, rec, true, int64(len(part)))
			end := at.RAR5EndHeader(after)
			if headers {
				main = fixtureCat(iv, fixtureCBC(t, keys.key, iv, main))
				fh = fixtureCat(iv, fixtureCBC(t, keys.key, iv, fh))
				end = fixtureCat(iv, fixtureCBC(t, keys.key, iv, end))
			}
			out = fixtureCat(out, main, fh, part, end)
		}
		vols = append(vols, out)
	}
	return vols
}

func TestRARGeneratedEncryptedSeek(t *testing.T) {
	for _, version := range []int{4, 5} {
		for _, headers := range []bool{false, true} {
			for _, multi := range []bool{false, true} {
				name := fmt.Sprintf("rar%d-headers%t-multi%t", version, headers, multi)
				t.Run(name, func(t *testing.T) {
					data := buildEncryptedRARFixture(t, version, headers, multi)
					if headers || !multi {
						for i, b := range data {
							filename := fmt.Sprintf("testdata/rar-encrypted/%s.part%d.rar", name, i+1)
							golden, err := os.ReadFile(filename)
							if err != nil {
								t.Fatal(err)
							}
							if !bytes.Equal(b, golden) {
								t.Fatalf("generated fixture changed from independently verified golden: %s", filename)
							}
						}
					}
					vols := memVols(data...)
					parsed := make([]*RARVolume, len(vols))
					cache := &RARPasswordCache{}
					for i, v := range vols {
						var err error
						parsed[i], err = ReadRARVolume(v.R, v.Size, &RAROptions{Password: fixtureRARPassword, PasswordCache: cache})
						if err != nil {
							t.Fatal(err)
						}
					}
					members, err := ComposeRAR(parsed)
					if err != nil {
						t.Fatal(err)
					}
					if len(members) != 1 {
						t.Fatal(len(members))
					}
					m := members[0]
					r, err := NewMemberReader(m, vols)
					if err != nil {
						t.Fatal(err)
					}
					checkRandomReads(t, r, fixtureRARPayload(), extentCuts(m), 42)
					if directory := os.Getenv("ETHER_REGENERATE_RAR_FIXTURES"); directory != "" {
						for i, b := range data {
							filename := fmt.Sprintf("%s.part%d.rar", name, i+1)
							if err := os.WriteFile(filepath.Join(directory, filename), b, 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
				})
			}
		}
	}
}
