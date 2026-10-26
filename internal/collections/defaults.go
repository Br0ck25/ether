package collections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("collections", 5, `CREATE TABLE collections_defaults (name TEXT PRIMARY KEY, spec_id INTEGER NOT NULL);`)
}

var movieGenres = []string{"Action", "Adventure", "Animation", "Comedy", "Crime", "Documentary", "Drama", "Family", "Fantasy", "History", "Horror", "Music", "Mystery", "Romance", "Science Fiction", "TV Movie", "Thriller", "War", "Western"}

func libraryDefault(name, media, field, value string) core.CollectionSpec {
	sp := core.CollectionSpec{Name: name, MediaType: media, LocalOnly: true, Sources: []core.CollectionSource{{Kind: "catalog"}}, Match: MatchAll, Sort: "popularity", SortDesc: true, Refresh: RefreshLive, Enabled: true, Artwork: core.CollectionArtwork{Mode: "auto"}, Targets: core.CollectionTargets{CollectionsTab: true, Users: core.AllUsers()}}
	sp.Rules = []core.CollectionRule{mkRule(field, "is", value)}
	sp.Description = "Automatically includes matching titles from your catalogue. Edit or delete this collection at any time."
	Normalize(&sp)
	return sp
}

// Seed each facet once. The registry survives deletion, so owner edits and
// deletions are permanent. Newly encountered TV studios/networks are added by
// the leader's existing collection scheduler, without any remote lookup.
func (s *Service) seedLibraryDefaults(ctx context.Context) error {
	if s.opts.noSeed {
		return nil
	}
	if at, err := strconv.ParseInt(s.meta(ctx, "library-defaults-checked"), 10, 64); err == nil && s.now().Sub(time.Unix(at, 0)) >= 0 && s.now().Sub(time.Unix(at, 0)) < time.Hour {
		return nil
	}
	studios, err := s.cat.Facet(ctx, core.TypeShow, "studio")
	if err != nil {
		return err
	}
	specs := map[string]core.CollectionSpec{}
	for _, g := range movieGenres {
		specs["movie.genre."+strings.ToLower(g)] = libraryDefault(g, MediaMovie, "genre", g)
	}
	for _, v := range studios {
		name := strings.TrimSpace(v.Title)
		if name == "" {
			name = strings.TrimSpace(v.Key)
		}
		if name != "" && len(name) <= 120 && v.Count > 0 {
			specs["show.studio."+strings.ToLower(name)] = libraryDefault(name, MediaShow, "studio", name)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = store.LockKey(ctx, store.DialectOf(s.db), tx, "collections:library-defaults"); err != nil {
		return err
	}
	existing := map[string]bool{}
	rs, err := tx.QueryContext(ctx, `SELECT name FROM collections_defaults`)
	if err != nil {
		return err
	}
	for rs.Next() {
		var k string
		if err = rs.Scan(&k); err != nil {
			rs.Close()
			return err
		}
		existing[k] = true
	}
	err = rs.Err()
	rs.Close()
	if err != nil {
		return err
	}
	var names []string
	for k := range specs {
		if !existing[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var id int64
		sp := specs[name]
		sp.TemplateID = "ether-default:" + name
		id, err = s.insertSpecTx(ctx, tx, sp, 0)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO collections_defaults(name,spec_id) VALUES(?,?)`, name, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO collections_meta(k,v) VALUES('library-defaults-checked',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, strconv.FormatInt(s.now().Unix(), 10)); err != nil {
		return err
	}
	return tx.Commit()
}

// One-time upgrade of shipped feed collections. Custom collections keep every
// publication choice. Stable IDs remain valid for existing Home policies/links.
func (s *Service) separateDiscoveryCollections(ctx context.Context) error {
	if s.meta(ctx, "library-separation-v1") != "" {
		return nil
	}
	rows, err := s.loadRows(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = store.LockKey(ctx, store.DialectOf(s.db), tx, "collections:library-separation"); err != nil {
		return err
	}
	var done string
	err = tx.QueryRowContext(ctx, `SELECT v FROM collections_meta WHERE k='library-separation-v1'`).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, r := range rows {
		if !r.builtin {
			continue
		}
		sp := r.spec
		sp.Targets.CollectionsTab = false
		raw, err := json.Marshal(sp)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE collections_specs SET spec_json=?,updated_at=? WHERE id=?`, string(raw), s.now().Unix(), r.id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO collections_meta(k,v) VALUES('library-separation-v1','1')`); err != nil {
		return fmt.Errorf("collection upgrade: %w", err)
	}
	return tx.Commit()
}

// A local single-facet collection reads an inverted index instead of scanning
// the whole catalogue once for every network. Owners can edit it freely: more
// complex rules automatically use the ordinary collection evaluator.
func localFacetSpec(sp core.CollectionSpec) (string, bool) {
	if !sp.LocalOnly || sp.Match != MatchAll || len(sp.Sources) != 1 || sp.Sources[0].Kind != "catalog" || len(sp.Rules) != 1 || len(sp.Exclude) != 0 || sp.Sort != "popularity" || !sp.SortDesc || sp.MediaType == MediaMixed {
		return "", false
	}
	r := sp.Rules[0]
	if (r.Field != "genre" && r.Field != "studio") || (r.Op != "is" && r.Op != "in") {
		return "", false
	}
	var value string
	if json.Unmarshal(r.Value, &value) != nil {
		return "", false
	}
	return sp.MediaType + "|" + r.Field + "|" + strings.ToLower(value), true
}
func (snap *snapshot) facetKeys(key string) []core.RatingKey {
	snap.facetsOnce.Do(func() {
		snap.facets = map[string][]core.RatingKey{}
		for _, e := range snap.order {
			for _, p := range []struct {
				name   string
				values []string
			}{{"genre", e.genresLC}, {"studio", e.studiosLC}} {
				for _, v := range p.values {
					k := string(e.typ) + "|" + p.name + "|" + v
					snap.facets[k] = append(snap.facets[k], e.key)
				}
			}
		}
	})
	return snap.facets[key]
}
