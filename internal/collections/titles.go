package collections

import (
	"context"
	"database/sql"
	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/core"
	"time"
)

func loadTitles(ctx context.Context, db *sql.DB, keys []core.RatingKey) ([]core.Title, error) {
	return catalog.NewMirror(db, time.Now).Titles(ctx, keys)
}
