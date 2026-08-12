// Package auditstorage owns the ordered SQLite schema shared by audit writers.
package auditstorage

import (
	"context"
	"database/sql"
)

// Migration applies one ordered schema version inside its caller's transaction.
type Migration struct {
	Version int
	Apply   func(context.Context, *sql.Tx) error
}
