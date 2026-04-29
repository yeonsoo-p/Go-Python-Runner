package db

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestConcurrentWritesNoBusy is a regression for the SQLITE_BUSY toast that
// surfaced when a parallel run group was cancelled: N waitForExit goroutines
// raced to UPDATE the same `runs` table simultaneously, and the file lock
// rejected contending writers even with WAL + busy_timeout configured.
//
// Pre-fix behavior: Open() left file-backed databases on the default
// connection pool (~25 conns), so each goroutine drew its own SQLite
// connection and competed at the file lock. This test would flake with
// "database is locked (5) (SQLITE_BUSY)".
//
// Post-fix: SetMaxOpenConns(1) applies to every Open(), so writes serialize
// at the Go pool layer and never reach the SQLite file lock contention.
func TestConcurrentWritesNoBusy(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const n = 16
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < n; i++ {
		_, err := d.Exec(
			"INSERT INTO runs (id, script_id, status, started_at) VALUES (?, ?, 'running', ?)",
			fmt.Sprintf("r%d", i), "test", now,
		)
		if err != nil {
			t.Fatalf("seed INSERT %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines simultaneously to maximize contention
			_, err := d.Exec(
				"UPDATE runs SET status = ?, finished_at = ?, exit_code = ? WHERE id = ?",
				"cancelled", time.Now(), 0, fmt.Sprintf("r%d", i),
			)
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: %w", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Errorf("concurrent UPDATE returned error: %v", e)
	}
}
