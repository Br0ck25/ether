package resolver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Blocklist sources.
const (
	BlockAuto  = "auto"  // health checks and playback failover (MarkBad)
	BlockOwner = "owner" // the owner blocked it
)

// BlockEntry is one blocklisted release. Manual (owner) entries are never
// expired by retention pruning.
type BlockEntry struct {
	ReleaseID string         `json:"releaseId"`
	Name      string         `json:"name,omitempty"`
	Indexer   string         `json:"indexer,omitempty"`
	TitleKey  core.RatingKey `json:"titleKey,string"`
	TitleName string         `json:"titleName,omitempty"`
	Reason    string         `json:"reason"`
	Source    string         `json:"source"`
	Manual    bool           `json:"manual"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// BlockFilter narrows Blocklist. Zero values match everything.
type BlockFilter struct {
	TitleKey core.RatingKey
	Source   string // BlockAuto or BlockOwner
	Query    string // substring of the release or title name
	Limit    int    // default 100, at most 500
	Offset   int
}

// blockEntryFor builds a blocklist row, naming the release and title from
// recent search results (or a pin) when known.
func (s *Service) blockEntryFor(releaseID, reason string, manual bool) BlockEntry {
	e := BlockEntry{ReleaseID: releaseID, Reason: reason, Source: BlockAuto, Manual: manual}
	if manual {
		e.Source = BlockOwner
	}
	s.con.mu.Lock()
	if r, ok := s.con.recent[releaseID]; ok {
		e.Name, e.Indexer, e.TitleKey, e.TitleName = r.rel.Name, r.rel.Indexer, r.titleKey, r.titleName
	} else {
		for _, p := range s.con.pins {
			if p.Release.ID == releaseID {
				e.Name, e.Indexer, e.TitleKey, e.TitleName = p.Release.Name, p.Release.Indexer, p.TitleKey, p.TitleName
				break
			}
		}
	}
	if s.db == nil {
		now := s.now()
		prev, had := s.con.blocks[releaseID]
		e.CreatedAt, e.UpdatedAt = now, now
		if had { // merge as persistBad's upsert does
			e.CreatedAt = prev.CreatedAt
			if prev.Manual && !manual {
				e.Reason, e.Source, e.Manual = prev.Reason, prev.Source, true
			}
			if e.Name == "" {
				e.Name = prev.Name
			}
			if e.Indexer == "" {
				e.Indexer = prev.Indexer
			}
			if e.TitleKey == 0 {
				e.TitleKey, e.TitleName = prev.TitleKey, prev.TitleName
			}
		}
		s.con.blocks[releaseID] = e
	}
	s.con.mu.Unlock()
	return e
}

// BlockRequest describes a release the owner blocks. Name, Indexer and
// TitleName are filled from recent search results when empty.
type BlockRequest struct {
	ReleaseID string
	Name      string
	Indexer   string
	TitleKey  core.RatingKey
	TitleName string
}

// Block puts a release on the blocklist on the owner's behalf (manual).
// Resolve never picks it again until it is unblocked.
func (s *Service) Block(ctx context.Context, req BlockRequest, reason string) (BlockEntry, error) {
	if req.ReleaseID == "" {
		return BlockEntry{}, fmt.Errorf("resolver: empty release id")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "blocked by owner"
	}
	if req.TitleKey != 0 {
		if rel, ok := s.LookupRelease(req.ReleaseID); ok {
			s.rememberTitleFor(rel, req.TitleKey)
		}
	}
	s.mu.Lock()
	s.bad[req.ReleaseID] = reason
	s.mu.Unlock()
	s.forgetVerified(ctx, req.ReleaseID)
	s.notePinsBlocked(ctx, req.ReleaseID, reason)
	e := s.blockEntryFor(req.ReleaseID, reason, true)
	if e.Name == "" {
		e.Name = req.Name
	}
	if e.Indexer == "" {
		e.Indexer = req.Indexer
	}
	if req.TitleKey != 0 {
		e.TitleKey = req.TitleKey
	}
	if req.TitleName != "" {
		e.TitleName = req.TitleName
	}
	if s.db == nil {
		s.con.mu.Lock()
		s.con.blocks[req.ReleaseID] = e
		s.con.mu.Unlock()
		return e, nil
	}
	if err := s.persistBad(ctx, e); err != nil {
		return BlockEntry{}, err
	}
	return s.blockEntry(ctx, req.ReleaseID)
}

func (s *Service) blockEntry(ctx context.Context, id string) (BlockEntry, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+blockColumns+` FROM resolver_bad WHERE release_id=?`, id)
	e, err := scanBlock(row)
	if errors.Is(err, sql.ErrNoRows) {
		return BlockEntry{}, core.ErrNotFound
	}
	return e, err
}

// Unblock removes a release from the blocklist (whatever its source) and
// forgets its verified health, so the next resolution checks it afresh;
// the title's availability cool-off is cleared too. It returns the
// removed entry, or core.ErrNotFound.
func (s *Service) Unblock(ctx context.Context, releaseID string) (BlockEntry, error) {
	s.mu.Lock()
	reason, had := s.bad[releaseID]
	delete(s.bad, releaseID)
	s.mu.Unlock()
	s.forgetVerified(ctx, releaseID)
	s.con.mu.Lock()
	delete(s.con.checks, releaseID)
	e, inMem := s.con.blocks[releaseID]
	delete(s.con.blocks, releaseID)
	s.con.mu.Unlock()
	if s.db != nil {
		// One statement: delete and report the row.
		row := s.db.QueryRowContext(ctx, `DELETE FROM resolver_bad WHERE release_id=? RETURNING `+blockColumns, releaseID)
		got, err := scanBlock(row)
		switch {
		case err == nil:
			e, inMem = got, true
		case !errors.Is(err, sql.ErrNoRows):
			return BlockEntry{}, err
		}
	}
	if !inMem {
		if !had {
			return BlockEntry{}, core.ErrNotFound
		}
		e = BlockEntry{ReleaseID: releaseID, Reason: reason, Source: BlockAuto}
	}
	if e.TitleKey != 0 {
		if err := s.forgetAvailability(ctx, e.TitleKey); err != nil {
			return e, err
		}
	}
	return e, nil
}

const blockColumns = `release_id, reason, created_at, name, indexer, rating_key, title_name, source, manual, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanBlock(r rowScanner) (BlockEntry, error) {
	var e BlockEntry
	var created, updated, key int64
	var manual int
	if err := r.Scan(&e.ReleaseID, &e.Reason, &created, &e.Name, &e.Indexer, &key, &e.TitleName, &e.Source, &manual, &updated); err != nil {
		return BlockEntry{}, err
	}
	e.TitleKey = core.RatingKey(key)
	e.Manual = manual == 1
	e.CreatedAt = time.Unix(created, 0)
	if updated == 0 {
		updated = created
	}
	e.UpdatedAt = time.Unix(updated, 0)
	return e, nil
}

// Blocklist lists blocklisted releases, most recently marked first, with
// the total matching count.
func (s *Service) Blocklist(ctx context.Context, f BlockFilter) ([]BlockEntry, int, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if s.db == nil {
		return s.memBlocklist(f), s.memBlocklistTotal(f), nil
	}
	var where []string
	var args []any
	if f.TitleKey != 0 {
		where = append(where, "rating_key=?")
		args = append(args, int64(f.TitleKey))
	}
	if f.Source != "" {
		where = append(where, "source=?")
		args = append(args, f.Source)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		pat := likePattern(q)
		where = append(where, `(LOWER(name) LIKE ? ESCAPE '\' OR LOWER(title_name) LIKE ? ESCAPE '\' OR LOWER(release_id) LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat, pat)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM resolver_bad`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+blockColumns+` FROM resolver_bad`+cond+
		` ORDER BY updated_at DESC, created_at DESC, release_id LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []BlockEntry{}
	for rows.Next() {
		e, err := scanBlock(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

func (s *Service) memBlocklistAll(f BlockFilter) []BlockEntry {
	s.mu.RLock()
	ids := make(map[string]string, len(s.bad))
	for id, r := range s.bad {
		ids[id] = r
	}
	s.mu.RUnlock()
	s.con.mu.Lock()
	defer s.con.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var out []BlockEntry
	for id, reason := range ids {
		e, ok := s.con.blocks[id]
		if !ok {
			e = BlockEntry{ReleaseID: id, Reason: reason, Source: BlockAuto}
		}
		if f.TitleKey != 0 && e.TitleKey != f.TitleKey || f.Source != "" && e.Source != f.Source {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Name+" "+e.TitleName+" "+e.ReleaseID), q) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ReleaseID < out[j].ReleaseID
	})
	return out
}

func (s *Service) memBlocklist(f BlockFilter) []BlockEntry {
	all := s.memBlocklistAll(f)
	if f.Offset >= len(all) {
		return []BlockEntry{}
	}
	all = all[f.Offset:]
	if len(all) > f.Limit {
		all = all[:f.Limit]
	}
	return all
}

func (s *Service) memBlocklistTotal(f BlockFilter) int { return len(s.memBlocklistAll(f)) }
