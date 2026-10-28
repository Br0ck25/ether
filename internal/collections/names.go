package collections

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// nameResolver turns TMDB entity names into ids and ids into names, with a
// memory cache in front of a SQLite cache so templates and generated specs
// that name "Christopher Nolan" or "christmas" cost one lookup ever.
type nameResolver struct {
	db  *sql.DB
	api *tmdbAPI
	now func() time.Time
	ttl time.Duration

	mu  sync.Mutex
	mem map[string]ref
}

func newNameResolver(db *sql.DB, api *tmdbAPI, now func() time.Time) *nameResolver {
	return &nameResolver{db: db, api: api, now: now, ttl: 30 * 24 * time.Hour, mem: map[string]ref{}}
}

var errUnresolved = fmt.Errorf("no TMDB match")

// resolve fills in whichever of ID/Name is missing. kind is keyword,
// company, network, person, collection or watchProvider (region applies to
// providers).
func (n *nameResolver) resolve(ctx context.Context, kind string, r ref, region string) (ref, error) {
	if r.ID > 0 && r.Name != "" {
		return r, nil
	}
	if r.ID <= 0 && r.Name == "" {
		return r, errUnresolved
	}
	q := "#" + strconv.FormatInt(r.ID, 10)
	if r.ID <= 0 {
		q = normName(r.Name)
	}
	if kind == "watchProvider" {
		q += "@" + region
	}
	ck := kind + "|" + q
	n.mu.Lock()
	if v, ok := n.mem[ck]; ok {
		n.mu.Unlock()
		return mergeRef(r, v), nil
	}
	n.mu.Unlock()
	var id int64
	var name string
	var at int64
	err := n.db.QueryRowContext(ctx, `SELECT id,name,at FROM collections_names WHERE kind=? AND query=?`, kind, q).Scan(&id, &name, &at)
	if err == nil && n.now().Sub(time.Unix(at, 0)) < n.ttl && (id > 0 || name != "") {
		v := ref{ID: id, Name: name}
		n.remember(ck, v)
		return mergeRef(r, v), nil
	}
	v, lerr := n.lookup(ctx, kind, r, region)
	if lerr != nil {
		if err == nil && (id > 0 || name != "") {
			return mergeRef(r, ref{ID: id, Name: name}), nil // stale beats nothing
		}
		return r, lerr
	}
	n.remember(ck, v)
	_, _ = n.db.ExecContext(ctx, `INSERT INTO collections_names(kind,query,id,name,at) VALUES(?,?,?,?,?)
		ON CONFLICT(kind,query) DO UPDATE SET id=excluded.id, name=excluded.name, at=excluded.at`, kind, q, v.ID, v.Name, n.now().Unix())
	return mergeRef(r, v), nil
}

func (n *nameResolver) remember(k string, v ref) {
	n.mu.Lock()
	if len(n.mem) > 5000 {
		n.mem = map[string]ref{}
	}
	n.mem[k] = v
	n.mu.Unlock()
}

func mergeRef(r, v ref) ref {
	if r.ID <= 0 {
		r.ID = v.ID
	}
	if r.Name == "" {
		r.Name = v.Name
	}
	return r
}

func (n *nameResolver) lookup(ctx context.Context, kind string, r ref, region string) (ref, error) {
	switch kind {
	case "network":
		if r.ID > 0 {
			if name, ok := networkByID(r.ID); ok {
				return ref{ID: r.ID, Name: name}, nil
			}
			name, err := n.api.entityName(ctx, "network", r.ID)
			if err != nil {
				return r, err
			}
			return ref{ID: r.ID, Name: name}, nil
		}
		if hits := searchNetworks(r.Name); len(hits) > 0 {
			return ref{ID: hits[0].ID, Name: hits[0].Name}, nil
		}
		return r, errUnresolved
	case "watchProvider":
		ps, err := n.api.watchProviders(ctx, region)
		if err != nil {
			return r, err
		}
		if r.ID > 0 {
			for _, p := range ps {
				if p.ID == r.ID {
					return ref{ID: p.ID, Name: p.Name}, nil
				}
			}
			return ref{ID: r.ID, Name: r.Name}, nil
		}
		want := normName(r.Name)
		for _, p := range ps {
			if normName(p.Name) == want {
				return ref{ID: p.ID, Name: p.Name}, nil
			}
		}
		for _, p := range ps {
			if strings.HasPrefix(normName(p.Name), want) {
				return ref{ID: p.ID, Name: p.Name}, nil
			}
		}
		return r, errUnresolved
	}
	if r.ID > 0 {
		name, err := n.api.entityName(ctx, kind, r.ID)
		if err != nil {
			return r, err
		}
		return ref{ID: r.ID, Name: name}, nil
	}
	hits, err := n.api.search(ctx, kind, r.Name)
	if err != nil {
		return r, err
	}
	if best, ok := bestMatch(hits, r.Name); ok {
		return ref{ID: best.ID, Name: best.Name}, nil
	}
	return r, errUnresolved
}

// bestMatch prefers an exact (normalised) name, then a prefix match, then
// TMDB's first result (it ranks by relevance and popularity).
func bestMatch(hits []core.LookupResult, name string) (core.LookupResult, bool) {
	want := normName(name)
	for _, h := range hits {
		if normName(h.Name) == want {
			return h, true
		}
	}
	for _, h := range hits {
		if strings.HasPrefix(normName(h.Name), want) {
			return h, true
		}
	}
	if len(hits) > 0 {
		return hits[0], true
	}
	return core.LookupResult{}, false
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "+", " plus")
	s = strings.ReplaceAll(s, "&", " and ")
	return strings.Trim(nonAlnum.ReplaceAllString(s, " "), " ")
}

// TMDB has no network search endpoint, so the builder searches this list
// of major networks and streaming originals (TMDB network ids); any other
// network can be entered by id.
var knownNetworks = []core.LookupResult{
	{ID: 213, Name: "Netflix"}, {ID: 49, Name: "HBO"}, {ID: 3186, Name: "HBO Max"}, {ID: 1024, Name: "Prime Video"},
	{ID: 2739, Name: "Disney+"}, {ID: 2552, Name: "Apple TV+"}, {ID: 453, Name: "Hulu"}, {ID: 4330, Name: "Paramount+"},
	{ID: 3353, Name: "Peacock"}, {ID: 174, Name: "AMC"}, {ID: 88, Name: "FX"}, {ID: 67, Name: "Showtime"},
	{ID: 318, Name: "Starz"}, {ID: 2, Name: "ABC"}, {ID: 6, Name: "NBC"}, {ID: 16, Name: "CBS"}, {ID: 19, Name: "FOX"},
	{ID: 71, Name: "The CW"}, {ID: 4, Name: "BBC One"}, {ID: 332, Name: "BBC Two"}, {ID: 3, Name: "BBC Three"},
	{ID: 26, Name: "Channel 4"}, {ID: 9, Name: "ITV1"}, {ID: 47, Name: "Comedy Central"}, {ID: 80, Name: "Adult Swim"},
	{ID: 56, Name: "Cartoon Network"}, {ID: 13, Name: "Nickelodeon"}, {ID: 54, Name: "Disney Channel"},
	{ID: 77, Name: "Syfy"}, {ID: 30, Name: "USA Network"}, {ID: 41, Name: "TNT"}, {ID: 68, Name: "TBS"},
	{ID: 43, Name: "National Geographic"}, {ID: 64, Name: "Discovery"}, {ID: 65, Name: "History"},
	{ID: 14, Name: "PBS"}, {ID: 1267, Name: "Freeform"}, {ID: 359, Name: "Cinemax"}, {ID: 1063, Name: "Sky Atlantic"},
}

func networkByID(id int64) (string, bool) {
	for _, n := range knownNetworks {
		if n.ID == id {
			return n.Name, true
		}
	}
	return "", false
}

func searchNetworks(q string) []core.LookupResult {
	want := normName(q)
	var exact, prefix, contains []core.LookupResult
	for _, n := range knownNetworks {
		nn := normName(n.Name)
		switch {
		case want == "":
			contains = append(contains, n)
		case nn == want:
			exact = append(exact, n)
		case strings.HasPrefix(nn, want):
			prefix = append(prefix, n)
		case strings.Contains(nn, want):
			contains = append(contains, n)
		}
	}
	return append(append(exact, prefix...), contains...)
}
