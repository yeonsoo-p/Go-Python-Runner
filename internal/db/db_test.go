package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMemory(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer d.Close()

	if err := d.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestMigrateCreatesTablesInMemory(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Verify runs table exists.
	var name string
	err = d.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='runs'").Scan(&name)
	if err != nil {
		t.Fatalf("runs table not found: %v", err)
	}

	// Verify kv table exists.
	err = d.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='kv'").Scan(&name)
	if err != nil {
		t.Fatalf("kv table not found: %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for i := 0; i < 3; i++ {
		if err := d.Migrate(); err != nil {
			t.Fatalf("Migrate (iteration %d): %v", i, err)
		}
	}
}

func TestRunsCRUD(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)

	// Insert
	_, err = d.Exec(
		"INSERT INTO runs (id, script_id, status, params, started_at) VALUES (?, ?, ?, ?, ?)",
		"run-1", "hello_world", "running", `{"name":"test"}`, now,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Read
	var id, scriptID, status, params string
	var startedAt time.Time
	err = d.QueryRow("SELECT id, script_id, status, params, started_at FROM runs WHERE id = ?", "run-1").
		Scan(&id, &scriptID, &status, &params, &startedAt)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if id != "run-1" || scriptID != "hello_world" || status != "running" {
		t.Fatalf("unexpected row: id=%s script_id=%s status=%s", id, scriptID, status)
	}

	// Update
	_, err = d.Exec("UPDATE runs SET status = ?, finished_at = ? WHERE id = ?", "completed", now, "run-1")
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	err = d.QueryRow("SELECT status FROM runs WHERE id = ?", "run-1").Scan(&status)
	if err != nil {
		t.Fatalf("SELECT after update: %v", err)
	}
	if status != "completed" {
		t.Fatalf("expected completed, got %s", status)
	}

	// Delete
	_, err = d.Exec("DELETE FROM runs WHERE id = ?", "run-1")
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}

	err = d.QueryRow("SELECT id FROM runs WHERE id = ?", "run-1").Scan(&id)
	if err == nil {
		t.Fatal("expected no rows after delete")
	}
}

func TestKVCRUD(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Insert
	_, err = d.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", "theme", "dark")
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Read
	var val string
	err = d.QueryRow("SELECT value FROM kv WHERE key = ?", "theme").Scan(&val)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if val != "dark" {
		t.Fatalf("expected dark, got %s", val)
	}

	// Upsert
	_, err = d.Exec(
		"INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP",
		"theme", "light",
	)
	if err != nil {
		t.Fatalf("UPSERT: %v", err)
	}

	err = d.QueryRow("SELECT value FROM kv WHERE key = ?", "theme").Scan(&val)
	if err != nil {
		t.Fatalf("SELECT after upsert: %v", err)
	}
	if val != "light" {
		t.Fatalf("expected light, got %s", val)
	}
}

func TestFileBackedPersistence(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")

	// Open, migrate, insert.
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	_, err = d.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", "persist", "yes")
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	d.Close()

	// Reopen and verify data survived.
	d2, err := Open(dsn)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer d2.Close()

	var val string
	err = d2.QueryRow("SELECT value FROM kv WHERE key = ?", "persist").Scan(&val)
	if err != nil {
		t.Fatalf("SELECT after reopen: %v", err)
	}
	if val != "yes" {
		t.Fatalf("expected yes, got %s", val)
	}
}

func TestDefaultDSN(t *testing.T) {
	dsn, err := DefaultDSN()
	if err != nil {
		t.Fatalf("DefaultDSN: %v", err)
	}

	dir := filepath.Dir(dsn)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%s): %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory, got file: %s", dir)
	}

	if filepath.Base(dsn) != "data.db" {
		t.Fatalf("expected data.db, got %s", filepath.Base(dsn))
	}
}
