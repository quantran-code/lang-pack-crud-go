package database

import (
	"fmt"
	"sync/atomic"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// MemoryDSN is a SQLite database that lives as long as the process. cache=shared lets every
// connection in the pool see the same database. Every caller of MemoryDSN shares that one
// database, so tests use NewMemoryDSN instead.
const MemoryDSN = "file::memory:?cache=shared"

var memoryDatabases atomic.Uint64

// NewMemoryDSN returns the DSN of a new, empty in-memory database that nothing else shares.
// Give each test its own, so rows written by one test never show up in another.
func NewMemoryDSN() string {
	return fmt.Sprintf("file:memdb%d?mode=memory&cache=shared", memoryDatabases.Add(1))
}

// Open connects to the SQLite database at dsn. The driver is pure Go, so no cgo is needed.
func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database: %w", err)
	}
	return db, nil
}
