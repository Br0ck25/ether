package playback

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/store"
)

func init() {
	store.Register("playback", 1, `
CREATE TABLE playback_resume (
	user_id      INTEGER NOT NULL,
	rating_key   INTEGER NOT NULL,
	release_id   TEXT    NOT NULL,
	release_json TEXT    NOT NULL,
	nzb_hash     TEXT    NOT NULL,
	profile      TEXT    NOT NULL DEFAULT '',
	size         INTEGER NOT NULL DEFAULT 0,
	duration_ms  INTEGER NOT NULL DEFAULT 0,
	updated_at   INTEGER NOT NULL,
	PRIMARY KEY (user_id, rating_key)
);
CREATE INDEX playback_resume_release ON playback_resume(release_id);
CREATE INDEX playback_resume_hash ON playback_resume(nzb_hash);
`)
}

// ResumePoint is the release that last served a user's title, so a resume
// (or a prefetched next episode) opens the same release and hits cached
// segments without asking the indexer again.
type ResumePoint struct {
	UserID    int64
	Key       core.RatingKey
	Release   core.Release
	NZB       []byte
	Profile   string        // quality profile it was resolved for
	Size      int64         // media file size, 0 if never opened
	Duration  time.Duration // probed duration, 0 if unknown
	UpdatedAt time.Time
}

// ReleaseID is the stable release identifier.
func (p *ResumePoint) ReleaseID() string { return p.Release.ID }

type resumeRecord struct {
	userID    int64
	key       core.RatingKey
	releaseID string
	release   core.Release
	nzbHash   string
	profile   string
	size      int64
	duration  time.Duration
	updatedAt time.Time
}

// resumeStore persists playback_resume rows; NZB bytes live on disk,
// content-addressed by SHA-256, so a resume never needs the indexer.
type resumeStore struct {
	db  *sql.DB
	dir string
}

func newResumeStore(db *sql.DB, dataDir string) (*resumeStore, error) {
	dir := filepath.Join(dataDir, "playback", "nzb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("playback: nzb dir: %w", err)
	}
	return &resumeStore{db: db, dir: dir}, nil
}

func nzbHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *resumeStore) nzbPath(hash string) string { return filepath.Join(s.dir, hash+".nzb") }

func (s *resumeStore) writeNZB(nzb []byte) (string, error) {
	hash := nzbHash(nzb)
	p := s.nzbPath(hash)
	if _, err := os.Stat(p); err == nil {
		return hash, nil
	}
	f, err := os.CreateTemp(s.dir, "nzb-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	if _, err := f.Write(nzb); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return hash, nil
}

func (s *resumeStore) readNZB(hash string) ([]byte, error) {
	b, err := os.ReadFile(s.nzbPath(hash))
	if err != nil {
		return nil, err
	}
	if nzbHash(b) != hash {
		return nil, fmt.Errorf("playback: nzb %s is corrupt", hash)
	}
	return b, nil
}

func (s *resumeStore) save(ctx context.Context, rec resumeRecord, nzb []byte) error {
	if len(nzb) == 0 {
		return errors.New("playback: empty nzb")
	}
	hash, err := s.writeNZB(nzb)
	if err != nil {
		return fmt.Errorf("playback: store nzb: %w", err)
	}
	rel, err := json.Marshal(rec.release)
	if err != nil {
		return err
	}
	var oldHash string
	err = s.db.QueryRowContext(ctx, `SELECT nzb_hash FROM playback_resume WHERE user_id=? AND rating_key=?`,
		rec.userID, int64(rec.key)).Scan(&oldHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO playback_resume (user_id, rating_key, release_id, release_json, nzb_hash, profile, size, duration_ms, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (user_id, rating_key) DO UPDATE SET
	release_id=excluded.release_id, release_json=excluded.release_json, nzb_hash=excluded.nzb_hash,
	profile=excluded.profile, size=excluded.size, duration_ms=excluded.duration_ms, updated_at=excluded.updated_at`,
		rec.userID, int64(rec.key), rec.release.ID, string(rel), hash, rec.profile, rec.size,
		rec.duration.Milliseconds(), rec.updatedAt.UnixMilli())
	if err != nil {
		return err
	}
	if oldHash != "" && oldHash != hash {
		s.gcNZB(ctx, oldHash)
	}
	return nil
}

const resumeColumns = `user_id, rating_key, release_id, release_json, nzb_hash, profile, size, duration_ms, updated_at`

func scanResume(sc interface{ Scan(...any) error }) (resumeRecord, error) {
	var (
		rec          resumeRecord
		key          int64
		relJSON      string
		durMS, updMS int64
	)
	if err := sc.Scan(&rec.userID, &key, &rec.releaseID, &relJSON, &rec.nzbHash, &rec.profile, &rec.size, &durMS, &updMS); err != nil {
		return rec, err
	}
	rec.key = core.RatingKey(key)
	rec.duration = time.Duration(durMS) * time.Millisecond
	rec.updatedAt = time.UnixMilli(updMS)
	if err := json.Unmarshal([]byte(relJSON), &rec.release); err != nil {
		return rec, fmt.Errorf("playback: resume release json: %w", err)
	}
	if rec.release.ID == "" {
		rec.release.ID = rec.releaseID
	}
	return rec, nil
}

// get returns the record for a user and title, or nil when there is none.
func (s *resumeStore) get(ctx context.Context, userID int64, key core.RatingKey) (*resumeRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+resumeColumns+` FROM playback_resume WHERE user_id=? AND rating_key=?`,
		userID, int64(key))
	rec, err := scanResume(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *resumeStore) all(ctx context.Context) ([]resumeRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+resumeColumns+` FROM playback_resume ORDER BY updated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []resumeRecord
	for rows.Next() {
		rec, err := scanResume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *resumeStore) delete(ctx context.Context, userID int64, key core.RatingKey) error {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT nzb_hash FROM playback_resume WHERE user_id=? AND rating_key=?`,
		userID, int64(key)).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM playback_resume WHERE user_id=? AND rating_key=?`, userID, int64(key)); err != nil {
		return err
	}
	s.gcNZB(ctx, hash)
	return nil
}

// gcNZB removes an NZB file no row references any more.
func (s *resumeStore) gcNZB(ctx context.Context, hash string) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playback_resume WHERE nzb_hash=?`, hash).Scan(&n); err != nil || n > 0 {
		return
	}
	_ = os.Remove(s.nzbPath(hash))
}

func (rec *resumeRecord) point(nzb []byte) *ResumePoint {
	return &ResumePoint{
		UserID:    rec.userID,
		Key:       rec.key,
		Release:   rec.release,
		NZB:       nzb,
		Profile:   rec.profile,
		Size:      rec.size,
		Duration:  rec.duration,
		UpdatedAt: rec.updatedAt,
	}
}
