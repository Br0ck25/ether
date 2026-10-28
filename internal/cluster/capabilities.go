package cluster

import (
	"github.com/jackc/pgx/v5"
)

// pgxConnection is a package-specific optional capability.
type pgxConnection interface{ PgxConn() *pgx.Conn }
