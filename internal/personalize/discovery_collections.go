package personalize

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"time"
)

// The high end of the SmartKey payload namespace is reserved for virtual
// discovery collections; ordinary SQLite smart-collection IDs start at one.
const reviewCollectionBase int64 = 900_000_000_000_000

func reviewCollectionKey(year int, typ core.MediaType) core.RatingKey {
	kind := int64(1)
	if typ == core.TypeShow {
		kind = 2
	}
	return core.SmartKey(reviewCollectionBase + int64(year)*10 + kind)
}
func reviewCollectionParts(key core.RatingKey) (int, core.MediaType, bool) {
	if key.Kind() != core.KindSmart {
		return 0, "", false
	}
	id := int64(key-core.SmartKey(0)) - reviewCollectionBase
	year := int(id / 10)
	if year < 1900 || year > 9999 {
		return 0, "", false
	}
	switch id % 10 {
	case 1:
		return year, core.TypeMovie, true
	case 2:
		return year, core.TypeShow, true
	}
	return 0, "", false
}

type reviewCollections struct {
	d    *Discovery
	base core.Collections
}

// ReviewCollections retains the collection engine as the authority for owner
// collections and adds virtual collections assembled from the viewer's history.
func (d *Discovery) ReviewCollections(base core.Collections) core.Collections {
	return &reviewCollections{d: d, base: base}
}
func (c *reviewCollections) review(ctx context.Context, u *core.User, year int, typ core.MediaType) (*core.Collection, []core.Title, error) {
	if u == nil {
		return nil, nil, core.ErrUnauthorized
	}
	hub, e := c.d.YearInReview(ctx, u, year, 10000)
	if e != nil {
		return nil, nil, e
	}
	items := []core.Title{}
	for _, t := range hub.Items {
		if t.Type == typ {
			items = append(items, t)
		}
	}
	col := &core.Collection{Key: reviewCollectionKey(year, typ), Title: hub.Title, Summary: "Titles you completed this year, based on your private watch history.", Kind: "user", Type: typ, OwnerUserID: u.ID, ChildCount: len(items)}
	if len(items) > 0 {
		col.PosterPath = items[0].PosterPath
		col.BackdropPath = items[0].BackdropPath
		col.UpdatedAt = time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return col, items, nil
}
func (c *reviewCollections) List(ctx context.Context, u *core.User, typ core.MediaType) ([]core.Collection, error) {
	var out []core.Collection
	if c.base != nil {
		var e error
		out, e = c.base.List(ctx, u, typ)
		if e != nil {
			return nil, e
		}
	}
	year := c.d.now().Year()
	if c.d.now().Month() < time.December {
		year--
	}
	types := []core.MediaType{typ}
	if typ == "" {
		types = []core.MediaType{core.TypeMovie, core.TypeShow}
	}
	for _, t := range types {
		if t != core.TypeMovie && t != core.TypeShow {
			continue
		}
		col, items, e := c.review(ctx, u, year, t)
		if e == nil && len(items) > 0 {
			out = append(out, *col)
		}
	}
	return out, nil
}
func (c *reviewCollections) Get(ctx context.Context, u *core.User, key core.RatingKey) (*core.Collection, error) {
	if year, typ, ok := reviewCollectionParts(key); ok {
		col, _, e := c.review(ctx, u, year, typ)
		return col, e
	}
	if c.base == nil {
		return nil, core.ErrNotFound
	}
	return c.base.Get(ctx, u, key)
}
func (c *reviewCollections) Items(ctx context.Context, u *core.User, key core.RatingKey, offset, limit int) (core.Page, error) {
	if year, typ, ok := reviewCollectionParts(key); ok {
		_, items, e := c.review(ctx, u, year, typ)
		if e != nil {
			return core.Page{}, e
		}
		total := len(items)
		if offset < 0 {
			offset = 0
		}
		if offset > total {
			offset = total
		}
		if limit <= 0 {
			limit = total
		}
		end := offset + limit
		if end < offset || end > total {
			end = total
		}
		return core.Page{Items: items[offset:end], Total: total}, nil
	}
	if c.base == nil {
		return core.Page{}, core.ErrNotFound
	}
	return c.base.Items(ctx, u, key, offset, limit)
}
func (c *reviewCollections) CreateSmart(ctx context.Context, col core.Collection) (*core.Collection, error) {
	if c.base == nil {
		return nil, core.ErrNotFound
	}
	return c.base.CreateSmart(ctx, col)
}
func (c *reviewCollections) DeleteSmart(ctx context.Context, key core.RatingKey) error {
	if _, _, ok := reviewCollectionParts(key); ok {
		return core.ErrNotFound
	}
	if c.base == nil {
		return core.ErrNotFound
	}
	return c.base.DeleteSmart(ctx, key)
}
func (c *reviewCollections) CollectionArtwork(ctx context.Context, u *core.User, key core.RatingKey, element string, width int) ([]byte, string, bool, error) {
	if _, _, ok := reviewCollectionParts(key); ok {
		return nil, "", false, nil
	}
	if source, ok := c.base.(core.CollectionArtworkSource); ok {
		return source.CollectionArtwork(ctx, u, key, element, width)
	}
	return nil, "", false, nil
}
