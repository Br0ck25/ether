package backup

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf8"
)

// Password-encrypted backups (.etherbackup).
//
// A bundle holds config.json, the database and the credential key that
// unseals both, so a downloaded copy is the server's secrets. Exports are
// therefore encrypted with a password the owner chooses:
//
//	header  magic "ETHERBK1" | kdf (1 = PBKDF2-HMAC-SHA256) | iterations
//	        uint32 | chunk size uint32 | salt [16] | nonce prefix [7]
//	body    the .tar.gz bundle in chunks sealed with AES-256-GCM
//
// The key is PBKDF2(password, salt, iterations). Chunk i is sealed with
// nonce = prefix | uint32(i) | last (1 on the final chunk, else 0) and the
// whole header as additional data, so a wrong password, a changed header,
// and reordered, dropped, truncated or appended chunks all fail
// authentication. Every chunk but the last holds exactly chunkSize bytes
// of plaintext; the last holds 0..chunkSize and is always present.

const (
	encMagic       = "ETHERBK1"
	encKDFPBKDF2   = 1
	encSaltLen     = 16
	encPrefixLen   = 7
	encHeaderLen   = len(encMagic) + 1 + 4 + 4 + encSaltLen + encPrefixLen
	encChunk       = 64 << 10
	encTagLen      = 16
	minIterations  = 100_000
	maxIterations  = 2_000_000
	minPasswordLen = 12
	maxPasswordLen = 1024
)

// exportIterations is the PBKDF2 work factor for new exports (OWASP's
// recommendation for PBKDF2-HMAC-SHA256). Tests lower it.
var exportIterations = 600_000

// EncryptedExtension names exported bundles.
const EncryptedExtension = ".etherbackup"

var (
	// ErrPasswordPolicy rejects an export password that is too short.
	ErrPasswordPolicy = errors.New("backup password must be at least 12 characters")
	// ErrBadPassword is a wrong password or a damaged first chunk; the
	// two cannot be told apart.
	ErrBadPassword = errors.New("wrong password, or the backup is damaged")
	// ErrDamaged is an encrypted backup that fails authentication after
	// its first chunk, or is truncated or extended.
	ErrDamaged = errors.New("encrypted backup is damaged or incomplete")
	// ErrNotEncrypted is a file without the encrypted backup header.
	ErrNotEncrypted = errors.New("not an encrypted Ether backup")
)

// IsEncrypted reports whether b starts with the encrypted backup magic.
func IsEncrypted(b []byte) bool { return bytes.HasPrefix(b, []byte(encMagic)) }

func checkPassword(password string, export bool) error {
	if password == "" || len(password) > maxPasswordLen || !utf8.ValidString(password) {
		return errors.New("a backup password of at most 1024 bytes is required")
	}
	if export && utf8.RuneCountInString(password) < minPasswordLen {
		return ErrPasswordPolicy
	}
	return nil
}

type encHeader struct {
	raw        [encHeaderLen]byte
	iterations int
	salt       []byte
	prefix     []byte
}

// parseHeader validates a header before any key derivation, so a hostile
// file cannot ask for unbounded work.
func parseHeader(raw []byte) (encHeader, error) {
	var h encHeader
	if len(raw) != encHeaderLen || !IsEncrypted(raw) {
		return h, ErrNotEncrypted
	}
	copy(h.raw[:], raw)
	p := raw[len(encMagic):]
	if p[0] != encKDFPBKDF2 {
		return h, errors.New("unsupported backup encryption")
	}
	iter := binary.BigEndian.Uint32(p[1:5])
	if iter < minIterations || iter > maxIterations {
		return h, errors.New("unsupported backup encryption parameters")
	}
	if binary.BigEndian.Uint32(p[5:9]) != encChunk {
		return h, errors.New("unsupported backup encryption parameters")
	}
	h.iterations = int(iter)
	h.salt = h.raw[len(encMagic)+9 : len(encMagic)+9+encSaltLen]
	h.prefix = h.raw[len(encMagic)+9+encSaltLen:]
	return h, nil
}

func (h encHeader) aead(password string) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, password, h.salt, h.iterations, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (h encHeader) nonce(counter uint32, last bool) []byte {
	n := make([]byte, 12)
	copy(n, h.prefix)
	binary.BigEndian.PutUint32(n[encPrefixLen:], counter)
	if last {
		n[11] = 1
	}
	return n
}

// encryptWriter seals what is written to it; Close writes the final chunk.
type encryptWriter struct {
	w       io.Writer
	h       encHeader
	aead    cipher.AEAD
	buf     []byte
	counter uint32
	closed  bool
}

// NewEncryptWriter returns a writer that encrypts a bundle to w under
// password. Close must be called to write the final chunk; it does not
// close w.
func NewEncryptWriter(w io.Writer, password string) (io.WriteCloser, error) {
	if err := checkPassword(password, true); err != nil {
		return nil, err
	}
	raw := make([]byte, encHeaderLen)
	copy(raw, encMagic)
	p := raw[len(encMagic):]
	p[0] = encKDFPBKDF2
	binary.BigEndian.PutUint32(p[1:5], uint32(exportIterations))
	binary.BigEndian.PutUint32(p[5:9], encChunk)
	if _, err := rand.Read(p[9:]); err != nil {
		return nil, err
	}
	h, err := parseHeader(raw)
	if err != nil {
		return nil, err
	}
	aead, err := h.aead(password)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	return &encryptWriter{w: w, h: h, aead: aead, buf: make([]byte, 0, encChunk)}, nil
}

func (e *encryptWriter) seal(last bool) error {
	if e.counter == ^uint32(0) {
		return errors.New("backup too large to encrypt")
	}
	out := e.aead.Seal(nil, e.h.nonce(e.counter, last), e.buf, e.h.raw[:])
	e.counter++
	e.buf = e.buf[:0]
	_, err := e.w.Write(out)
	return err
}

func (e *encryptWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("write to closed backup encrypter")
	}
	n := 0
	for len(p) > 0 {
		// A full chunk is sealed only once more data follows, so the
		// final chunk can be a full one.
		if len(e.buf) == encChunk {
			if err := e.seal(false); err != nil {
				return n, err
			}
		}
		c := copy(e.buf[len(e.buf):encChunk], p)
		e.buf = e.buf[:len(e.buf)+c]
		p, n = p[c:], n+c
	}
	return n, nil
}

func (e *encryptWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.seal(true)
}

// decryptReader yields only authenticated plaintext. It returns io.EOF
// only after the final chunk authenticated and nothing follows it.
type decryptReader struct {
	r       *bufio.Reader
	h       encHeader
	aead    cipher.AEAD
	chunk   []byte
	plain   []byte
	counter uint32
	done    bool
	err     error
}

// NewDecryptReader checks the header of an encrypted bundle, derives the
// key from password and authenticates the first chunk, so a wrong password
// fails here (ErrBadPassword) before anything is read from the bundle.
func NewDecryptReader(r io.Reader, password string) (io.Reader, error) {
	if err := checkPassword(password, false); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(r, encChunk+encTagLen+1)
	raw := make([]byte, encHeaderLen)
	if _, err := io.ReadFull(br, raw); err != nil {
		return nil, ErrNotEncrypted
	}
	h, err := parseHeader(raw)
	if err != nil {
		return nil, err
	}
	aead, err := h.aead(password)
	if err != nil {
		return nil, err
	}
	d := &decryptReader{r: br, h: h, aead: aead, chunk: make([]byte, encChunk+encTagLen)}
	if err := d.next(); err != nil {
		if errors.Is(err, ErrDamaged) {
			err = ErrBadPassword
		}
		return nil, err
	}
	return d, nil
}

// next reads and opens one chunk into d.plain.
func (d *decryptReader) next() error {
	n, err := io.ReadFull(d.r, d.chunk)
	switch {
	case err == io.ErrUnexpectedEOF || err == io.EOF:
		if n < encTagLen {
			return ErrDamaged
		}
		d.done = true
	case err != nil:
		return err
	default:
		// A full-size chunk is the last one only if nothing follows.
		if _, perr := d.r.Peek(1); perr == io.EOF {
			d.done = true
		} else if perr != nil {
			return perr
		}
	}
	if !d.done && d.counter == ^uint32(0) {
		return ErrDamaged
	}
	plain, oerr := d.aead.Open(d.chunk[:0:0], d.h.nonce(d.counter, d.done), d.chunk[:n], d.h.raw[:])
	if oerr != nil || (!d.done && len(plain) != encChunk) {
		return ErrDamaged
	}
	d.counter++
	d.plain = plain
	return nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	for len(d.plain) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.done {
			d.err = io.EOF
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}
