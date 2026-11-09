package auditstorage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/mattn/go-sqlite3"
)

// SQLiteConnection preserves the SQLite driver methods and rejects reuse after invalidation.
type SQLiteConnection struct {
	*sqlite3.SQLiteConn
	owner      *writerDriver
	invalid    atomic.Bool
	closeOnce  sync.Once
	closeError error
}

// IsValid lets database/sql discard an invalid physical connection on release.
func (connection *SQLiteConnection) IsValid() bool {
	return !connection.invalid.Load()
}

// Close closes SQLite and removes this physical connection from its pool registry.
func (connection *SQLiteConnection) Close() error {
	connection.closeOnce.Do(func() {
		connection.invalid.Store(true)
		connection.closeError = connection.SQLiteConn.Close()
		connection.owner.mu.Lock()
		delete(connection.owner.connections, connection)
		connection.owner.mu.Unlock()
	})

	return connection.closeError
}

type writerDriver struct {
	sqlite3.SQLiteDriver
	mu          sync.Mutex
	connections map[*SQLiteConnection]struct{}
}

type writerConnector struct {
	location string
	driver   *writerDriver
}

func (connector *writerConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, storageError("connect audit writer", err)
	}

	raw, err := connector.driver.Open(connector.location)
	if err != nil {
		return nil, storageError("open audit writer connection", err)
	}

	sqlite, ok := raw.(*sqlite3.SQLiteConn)
	if !ok {
		return nil, storageError(
			"open typed audit writer",
			errors.Join(errors.New("audit writer requires a SQLite connection"), raw.Close()),
		)
	}

	connection := &SQLiteConnection{
		SQLiteConn: sqlite,
		owner:      connector.driver,
		invalid:    atomic.Bool{},
		closeOnce:  sync.Once{},
		closeError: nil,
	}
	connector.driver.mu.Lock()
	if connector.driver.connections == nil {
		connector.driver.connections = make(map[*SQLiteConnection]struct{})
	}

	connector.driver.connections[connection] = struct{}{}
	connector.driver.mu.Unlock()

	return connection, nil
}

func (connector *writerConnector) Driver() driver.Driver {
	return connector.driver
}

// WriterConnection borrows one physical connection without taking ownership of its pool.
type WriterConnection struct {
	*sql.Conn
	physical *SQLiteConnection
}

// BorrowWriterConnection requires the serialized pool created by OpenWriter.
func BorrowWriterConnection(ctx context.Context, database *sql.DB) (*WriterConnection, error) {
	pool, ok := database.Driver().(*writerDriver)
	if !ok {
		return nil, errors.New("deferred retry requires an auditstorage.OpenWriter database")
	}

	if database.Stats().MaxOpenConnections != 1 {
		return nil, errors.New("deferred retry requires exactly one open writer connection")
	}

	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, storageError("borrow audit writer connection", err)
	}

	pool.mu.Lock()
	var physical *SQLiteConnection
	if len(pool.connections) == 1 {
		for candidate := range pool.connections {
			physical = candidate
		}
	}

	pool.mu.Unlock()
	if physical == nil {
		return nil, storageError(
			"borrow audit writer",
			errors.Join(
				errors.New("deferred retry requires one physical writer connection"),
				connection.Close(),
			),
		)
	}

	return &WriterConnection{Conn: connection, physical: physical}, nil
}

// InTransaction checks SQLite's actual state after an interrupted operation.
func (connection *WriterConnection) InTransaction() bool {
	return !connection.physical.AutoCommit()
}

// Discard prevents reuse of this borrowed physical connection after failed cleanup.
func (connection *WriterConnection) Discard() error {
	connection.physical.invalid.Store(true)
	releaseError := connection.Close()
	closeError := connection.physical.Close()
	if err := errors.Join(releaseError, closeError); err != nil {
		return storageError("discard audit writer connection", err)
	}

	return nil
}
