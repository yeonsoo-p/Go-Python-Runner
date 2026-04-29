package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps a sql.DB connection to a SQLite database.
type DB struct {
	*sql.DB
}

// Open opens a SQLite database at the given DSN.
// Use ":memory:" for a pure in-memory database, or a file path for persistence.
func Open(dsn string) (*DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Single connection for both in-memory and file-backed databases:
	//   - In-memory: each pooled connection has its own independent DB.
	//   - File-backed: concurrent writers race for the SQLite file lock;
	//     under load the busy_timeout retry can still surface SQLITE_BUSY
	//     (e.g. parallel waitForExit goroutines + Python db_execute).
	// Serializing at the Go pool layer eliminates both classes of bug. It
	// also guarantees the PRAGMAs below apply for the process lifetime —
	// they're set on the only connection that ever exists.
	db.SetMaxOpenConns(1)

	// Set pragmas for performance and correctness.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %q: %w", p, err)
		}
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return &DB{DB: db}, nil
}

// Migrate creates the initial schema tables if they do not exist.
func (d *DB) Migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			script_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			params TEXT,
			started_at DATETIME,
			finished_at DATETIME,
			exit_code INTEGER,
			error_message TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, m := range migrations {
		if _, err := d.Exec(m); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// DefaultDSN returns the default file-backed database path in the OS app data directory.
// On Windows: %APPDATA%/go-python-runner/data.db
// On Linux:   ~/.config/go-python-runner/data.db
func DefaultDSN() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	dir := filepath.Join(configDir, "go-python-runner")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return filepath.Join(dir, "data.db"), nil
}
