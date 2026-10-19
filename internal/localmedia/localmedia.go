// Package localmedia indexes owner-selected folders and exposes preferred local sources.
package localmedia

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("localmedia", 1, `CREATE TABLE localmedia_folders(path TEXT PRIMARY KEY); CREATE TABLE localmedia_files(path TEXT PRIMARY KEY, root TEXT NOT NULL, key INTEGER NOT NULL DEFAULT 0, size INTEGER NOT NULL, modified INTEGER NOT NULL, info TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '', manual INTEGER NOT NULL DEFAULT 0); CREATE INDEX localmedia_key ON localmedia_files(key);`)
}

type Prober interface {
	Probe(context.Context, string) (*core.MediaInfo, error)
}
type Service struct {
	db      *sql.DB
	catalog core.Catalog
	probe   Prober
	scanMu  sync.Mutex
}

func New(db *sql.DB, catalog core.Catalog, probe Prober) *Service {
	return &Service{db: db, catalog: catalog, probe: probe}
}

type File struct {
	Path     string         `json:"path"`
	Root     string         `json:"root"`
	Key      core.RatingKey `json:"key"`
	Size     int64          `json:"size"`
	Modified int64          `json:"modified"`
	Info     core.MediaInfo `json:"info"`
	Error    string         `json:"error,omitempty"`
	Manual   bool           `json:"manual"`
}

func (s *Service) AddFolder(ctx context.Context, path string) error {
	p, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	p, e = filepath.EvalSymlinks(p)
	if e != nil {
		return e
	}
	st, e := os.Stat(p)
	if e != nil {
		return e
	}
	if !st.IsDir() {
		return errors.New("folder must be a directory")
	}
	_, e = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO localmedia_folders(path) VALUES(?)`, p)
	return e
}
func (s *Service) Folders(ctx context.Context) ([]string, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT path FROM localmedia_folders ORDER BY path`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) Files(ctx context.Context) ([]File, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT path,root,key,size,modified,info,error,manual FROM localmedia_files ORDER BY path`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		var f File
		var raw string
		if e = rows.Scan(&f.Path, &f.Root, &f.Key, &f.Size, &f.Modified, &raw, &f.Error, &f.Manual); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &f.Info); e != nil {
			return nil, e
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *Service) RemoveFolder(ctx context.Context, path string) error {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM localmedia_files WHERE root=?`, path); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM localmedia_folders WHERE path=?`, path); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Service) Match(ctx context.Context, path string, key core.RatingKey) error {
	if s.catalog == nil {
		return errors.New("catalog unavailable")
	}
	t, e := s.catalog.Get(ctx, key)
	if e != nil {
		return e
	}
	if t.Type != core.TypeMovie && t.Type != core.TypeEpisode {
		return errors.New("match must be a movie or episode")
	}
	r, e := s.db.ExecContext(ctx, `UPDATE localmedia_files SET key=?,manual=1,error='' WHERE path=?`, key, path)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}
func (s *Service) match(ctx context.Context, name string) (core.RatingKey, error) {
	if s.catalog == nil {
		return 0, core.ErrNotFound
	}
	parsed := releaseparse.Parse(name)
	typ := core.TypeMovie
	searchType := core.TypeMovie
	if len(parsed.Episodes) > 0 {
		typ = core.TypeEpisode
		searchType = core.TypeShow
	}
	ts, e := s.catalog.Search(ctx, parsed.Title, []core.MediaType{searchType}, 8)
	if e != nil {
		return 0, e
	}
	matches := []core.Title{}
	for _, t := range ts {
		if !strings.EqualFold(t.Title, parsed.Title) || parsed.Year != 0 && t.Year != parsed.Year {
			continue
		}
		matches = append(matches, t)
	}
	if len(matches) != 1 {
		return 0, errors.New("no unique title match; review required")
	}
	if typ == core.TypeEpisode && len(parsed.Seasons) > 0 {
		key := core.EpisodeKey(matches[0].IDs.TMDB, parsed.Seasons[0], parsed.Episodes[0])
		_, e = s.catalog.Get(ctx, key)
		return key, e
	}
	return matches[0].Key, nil
}

// Scan never follows symlinks and only removes old entries after a successful walk.
func (s *Service) Scan(ctx context.Context) error {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	roots, e := s.Folders(ctx)
	if e != nil {
		return e
	}
	for _, root := range roots {
		seen := map[string]bool{}
		e = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".webm", ".ts":
			default:
				return nil
			}
			st, e := d.Info()
			if e != nil {
				return e
			}
			if !st.Mode().IsRegular() {
				return nil
			}
			seen[path] = true
			var prevSize, prevMod int64
			var key core.RatingKey
			var manual bool
			e = s.db.QueryRowContext(ctx, `SELECT size,modified,key,manual FROM localmedia_files WHERE path=?`, path).Scan(&prevSize, &prevMod, &key, &manual)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e == nil && prevSize == st.Size() && prevMod == st.ModTime().UnixNano() {
				return nil
			}
			f := File{Path: path, Root: root, Size: st.Size(), Modified: st.ModTime().UnixNano(), Key: key, Manual: manual}
			if !manual {
				f.Key, e = s.match(ctx, d.Name())
				if e != nil {
					f.Error = e.Error()
					f.Key = 0
				}
			}
			if s.probe != nil {
				info, pe := s.probe.Probe(ctx, path)
				if pe != nil {
					f.Error = pe.Error()
				} else if info != nil {
					f.Info = *info
				}
			}
			if s.probe == nil {
				f.Info.Estimated = true
			}
			f.Info.Size = st.Size()
			if f.Info.Container == "" {
				f.Info.Container = strings.TrimPrefix(filepath.Ext(path), ".")
			}
			b, e := json.Marshal(f.Info)
			if e != nil {
				return e
			}
			_, e = s.db.ExecContext(ctx, `INSERT INTO localmedia_files(path,root,key,size,modified,info,error,manual) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET root=excluded.root,key=excluded.key,size=excluded.size,modified=excluded.modified,info=excluded.info,error=excluded.error,manual=excluded.manual`, path, root, f.Key, f.Size, f.Modified, string(b), f.Error, manual)
			return e
		})
		if e != nil {
			return fmt.Errorf("scan %s: %w", root, e)
		}
		files, e := s.Files(ctx)
		if e != nil {
			return e
		}
		for _, f := range files {
			if f.Root == root && !seen[f.Path] {
				if _, e = s.db.ExecContext(ctx, `DELETE FROM localmedia_files WHERE path=?`, f.Path); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

// Watch polls portable filesystem metadata; cancellation stops all work.
func (s *Service) Watch(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		if e := s.Scan(ctx); e != nil && onError != nil {
			onError(e)
		}
	}
	run()
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			run()
		}
	}
}
func (s *Service) preferred(ctx context.Context, key core.RatingKey) (File, error) {
	files, e := s.Files(ctx)
	if e != nil {
		return File{}, e
	}
	for _, f := range files {
		if f.Key != key {
			continue
		}
		real, e := filepath.EvalSymlinks(f.Path)
		if e != nil || real != f.Path {
			continue
		}
		rel, e := filepath.Rel(f.Root, real)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		st, e := os.Stat(real)
		if e == nil && st.Mode().IsRegular() && st.Size() == f.Size {
			return f, nil
		}
	}
	return File{}, core.ErrNotFound
}
func (s *Service) Available(ctx context.Context, key core.RatingKey) bool {
	_, e := s.preferred(ctx, key)
	return e == nil
}
func (s *Service) MediaInfo(ctx context.Context, key core.RatingKey) (*core.MediaInfo, error) {
	f, e := s.preferred(ctx, key)
	if e != nil {
		return nil, e
	}
	return &f.Info, nil
}

type source struct {
	*os.File
	size int64
	id   string
}

func (s *source) Size() int64       { return s.size }
func (s *source) Name() string      { return filepath.Base(s.File.Name()) }
func (s *source) ReleaseID() string { return s.id }
func (s *Service) Preferred(ctx context.Context, key core.RatingKey) (core.MediaSource, error) {
	f, e := s.preferred(ctx, key)
	if e != nil {
		return nil, e
	}
	file, e := os.Open(f.Path)
	if e != nil {
		return nil, e
	}
	return &source{File: file, size: f.Size, id: "local:" + f.Path}, nil
}

// SourceHandler must be mounted behind the normal Plex viewer authentication.
func (s *Service) SourceHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, e := core.ParseRatingKey(r.PathValue("key"))
		if e != nil {
			http.NotFound(w, r)
			return
		}
		src, e := s.Preferred(r.Context(), key)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		defer src.Close()
		f := src.(*source)
		st, e := f.Stat()
		if e != nil {
			http.Error(w, "file unavailable", 404)
			return
		}
		http.ServeContent(w, r, f.Name(), st.ModTime(), f)
	})
}
func (s *Service) Handler() http.Handler {
	m := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any, e error) {
		w.Header().Set("Content-Type", "application/json")
		if e != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invalid_request", "message": e.Error()}})
			return
		}
		json.NewEncoder(w).Encode(v)
	}
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		fs, e := s.Files(r.Context())
		roots, re := s.Folders(r.Context())
		if e == nil {
			e = re
		}
		reply(w, map[string]any{"folders": roots, "files": fs}, e)
	})
	m.HandleFunc("POST /folders", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Path string `json:"path"`
		}
		e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&q)
		if e == nil {
			e = s.AddFolder(r.Context(), q.Path)
		}
		reply(w, map[string]bool{"ok": e == nil}, e)
	})
	m.HandleFunc("DELETE /folders", func(w http.ResponseWriter, r *http.Request) {
		e := s.RemoveFolder(r.Context(), r.URL.Query().Get("path"))
		reply(w, map[string]bool{"ok": e == nil}, e)
	})
	m.HandleFunc("POST /scan", func(w http.ResponseWriter, r *http.Request) {
		e := s.Scan(r.Context())
		reply(w, map[string]bool{"ok": e == nil}, e)
	})
	m.HandleFunc("POST /match", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Path    string         `json:"path"`
			Key     core.RatingKey `json:"key"`
			ShowKey core.RatingKey `json:"showKey"`
			Season  int            `json:"season"`
			Episode int            `json:"episode"`
		}
		e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&q)
		if e == nil {
			if q.ShowKey != 0 {
				if q.ShowKey.Kind() != core.KindShow || q.Season < 0 || q.Season > 999 || q.Episode < 1 || q.Episode > 999 {
					e = errors.New("choose a valid show, season and episode")
				} else {
					q.Key = core.EpisodeKey(q.ShowKey.TMDB(), q.Season, q.Episode)
				}
			}
			if e == nil {
				e = s.Match(r.Context(), q.Path, q.Key)
			}
		}
		reply(w, map[string]bool{"ok": e == nil}, e)
	})
	return m
}
