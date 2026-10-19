package segcache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// On-disk layout (all under the cache root):
//
//	pins.json                         persisted pins {"version":1,"pins":{id:[reasons]}}
//	data/<hh>/<h32>/release.json      {"version":1,"releaseId":"<real id>"}
//	data/<hh>/<h32>/<n>.seg           segment n: 16-byte header + payload
//	**/*.tmp                          in-flight writes; removed on Open
//
// <h32> is the hex of the first 16 bytes of SHA-256(releaseID) and <hh> its
// first two characters, fanning release directories out over 256 subdirs.
//
// Segment header: magic "ESG1" | CRC-32C(payload) uint32 LE | payload length
// uint64 LE. Files are written to a temp file and renamed into place, so a
// reader only ever sees a complete version; the checksum additionally guards
// against files torn by a power loss (we do not fsync every segment).

const (
	dataDirName  = "data"
	metaFileName = "release.json"
	pinsFileName = "pins.json"
	segExt       = ".seg"
	tmpExt       = ".tmp"
	headerSize   = 16
)

var (
	headerMagic = [4]byte{'E', 'S', 'G', '1'}
	castagnoli  = crc32.MakeTable(crc32.Castagnoli)
	errCorrupt  = errors.New("corrupt segment file")
)

type releaseMeta struct {
	Version   int    `json:"version"`
	ReleaseID string `json:"releaseId"`
}

type pinsFile struct {
	Version int                 `json:"version"`
	Pins    map[string][]string `json:"pins"`
}

// releaseDirRel returns the release directory relative to the data dir.
func releaseDirRel(releaseID string) string {
	sum := sha256.Sum256([]byte(releaseID))
	h := hex.EncodeToString(sum[:16])
	return filepath.Join(h[:2], h)
}

func segName(seg int) string { return strconv.Itoa(seg) + segExt }

// parseSegName returns the segment number of a canonical "<n>.seg" name.
func parseSegName(name string) (int, bool) {
	s, ok := strings.CutSuffix(name, segExt)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

// writeSegmentTemp writes header+data to a new temp file in dir and returns
// its path. The caller renames it into place.
func writeSegmentTemp(dir string, seg int, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, segName(seg)+".*"+tmpExt)
	if err != nil {
		return "", err
	}
	var hdr [headerSize]byte
	copy(hdr[:4], headerMagic[:])
	binary.LittleEndian.PutUint32(hdr[4:8], crc32.Checksum(data, castagnoli))
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(len(data)))
	_, err = f.Write(hdr[:])
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// readSegment reads and verifies a segment file whose payload is expected to
// be size bytes. It returns fs.ErrNotExist (wrapped) for missing files and
// errCorrupt for anything that fails verification.
func readSegment(path string, size int64) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != size+headerSize || [4]byte(b[:4]) != headerMagic {
		return nil, errCorrupt
	}
	payload := b[headerSize:]
	if binary.LittleEndian.Uint64(b[8:16]) != uint64(len(payload)) ||
		binary.LittleEndian.Uint32(b[4:8]) != crc32.Checksum(payload, castagnoli) {
		return nil, errCorrupt
	}
	return payload, nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// and a rename. With sync the temp file is fsynced before the rename.
func writeFileAtomic(path string, data []byte, sync bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*"+tmpExt)
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil && sync {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeReleaseMeta(dir, releaseID string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(releaseMeta{Version: 1, ReleaseID: releaseID})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, metaFileName), b, true)
}

func readReleaseMeta(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, metaFileName))
	if err != nil {
		return "", err
	}
	var m releaseMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return "", fmt.Errorf("decode %s: %w", metaFileName, err)
	}
	if m.ReleaseID == "" {
		return "", fmt.Errorf("decode %s: empty release id", metaFileName)
	}
	return m.ReleaseID, nil
}
