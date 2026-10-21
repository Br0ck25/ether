package catalog

import (
	"context"

	"github.com/braedonsaunders/ether/internal/tmdb"
)

// personDetailsSource is a package-specific optional capability.
type personDetailsSource interface {
	PersonDetails(ctx context.Context, id int64) (*tmdb.Person, error)
}
