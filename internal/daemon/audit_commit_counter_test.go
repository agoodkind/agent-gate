package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func qualificationCommitCounter(b testing.TB, database *sql.DB) *atomic.Int64 {
	b.Helper()
	counter := &atomic.Int64{}
	connection, err := database.Conn(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	err = connection.Raw(func(driverConnection any) error {
		sqlite, ok := driverConnection.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected SQLite connection %T", driverConnection)
		}
		sqlite.RegisterCommitHook(func() int { counter.Add(1); return 0 })
		return nil
	})
	closeError := connection.Close()
	if err != nil || closeError != nil {
		b.Fatalf("commit hook: %v, %v", err, closeError)
	}
	return counter
}
