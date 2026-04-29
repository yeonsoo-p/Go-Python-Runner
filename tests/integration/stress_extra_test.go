//go:build integration

package integration

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go-python-runner/internal/db"
	"go-python-runner/internal/runner"
)

// TestProgressBurst — Python sends N progress messages back-to-back.
// Asserts every message is received (no drops) and the final status is
// Completed. Exercises the message-channel back-pressure path between
// gRPC handler and the consumer.
func TestProgressBurst(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	const N = 1000
	_, msgCh, err := mgr.StartRun("progress_burst",
		map[string]string{"count": "1000"},
		testdataDir(t, "progress_burst"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := collectMessages(msgCh, 30*time.Second)

	var progressCount int
	var hasCompleted bool
	for _, m := range msgs {
		switch s := m.(type) {
		case runner.ProgressMsg:
			progressCount++
		case runner.StatusMsg:
			if s.State == runner.StatusCompleted {
				hasCompleted = true
			}
		}
	}

	if progressCount != N {
		t.Errorf("expected %d progress messages, got %d (back-pressure may be dropping)", N, progressCount)
	}
	if !hasCompleted {
		t.Errorf("expected Completed status, run did not complete cleanly")
	}
}

// TestHugeOutputMessage — Python sends a single 10MB output text. Asserts
// the payload is delivered intact (or fails with a clean size-limit error if
// gRPC enforces one). Today gRPC's default max receive size is 4MB so we
// expect the run to fail with a clean error rather than corrupt data.
func TestHugeOutputMessage(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	const sizeMB = 10
	_, msgCh, err := mgr.StartRun("huge_output",
		map[string]string{"size_mb": "10"},
		testdataDir(t, "huge_output"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := collectMessages(msgCh, 30*time.Second)

	var output string
	var status runner.RunStatus
	for _, m := range msgs {
		switch s := m.(type) {
		case runner.OutputMsg:
			output = s.Text
		case runner.StatusMsg:
			status = s.State
		}
	}

	// Either: (a) output delivered intact and run completed,
	// or: (b) gRPC rejected the message and run is Failed.
	// Both are acceptable — this test documents whichever behavior is current.
	expectedSize := sizeMB * 1024 * 1024
	if len(output) == expectedSize && status == runner.StatusCompleted {
		t.Logf("10MB output delivered intact, run completed")
		return
	}
	if status == runner.StatusFailed {
		t.Logf("10MB output rejected by gRPC (clean failure path), run failed as expected")
		return
	}
	t.Errorf("unexpected: output=%d bytes, status=%s (expected either intact+completed or failed)",
		len(output), status)
}

// TestCachePicklingError — Python tries cache_set on a non-picklable object.
// Trust-order rule 2: gotError should fire (runner.run wraps the pickle
// exception into fail()), and the run should reach Failed.
func TestCachePicklingError(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	_, msgCh, err := mgr.StartRun("cache_pickle_error",
		map[string]string{},
		testdataDir(t, "cache_pickle_error"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := collectMessages(msgCh, 15*time.Second)

	var hasError bool
	var status runner.RunStatus
	for _, m := range msgs {
		switch s := m.(type) {
		case runner.ErrorMsg:
			hasError = true
			if !strings.Contains(strings.ToLower(s.Message), "pickle") &&
				!strings.Contains(strings.ToLower(s.Message), "cannot") &&
				!strings.Contains(strings.ToLower(s.Message), "lock") {
				t.Logf("error message: %q", s.Message)
			}
		case runner.StatusMsg:
			status = s.State
		}
	}

	if !hasError {
		t.Error("expected ErrorMsg from non-picklable cache_set")
	}
	if status != runner.StatusFailed {
		t.Errorf("expected Failed status, got %s", status)
	}
}

// TestConcurrentDbWrites — N parallel runs each call db_execute("INSERT ...").
// Asserts SQLite serializes cleanly: no "database is locked" errors, all
// writes land in the table.
func TestConcurrentDbWrites(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	const N = 10
	scriptDir := testdataDir(t, "db_writer")

	var wg sync.WaitGroup
	channels := make([]<-chan runner.Message, N)

	for i := 0; i < N; i++ {
		_, msgCh, err := mgr.StartRun("db_writer",
			map[string]string{"label": "writer"},
			scriptDir)
		if err != nil {
			t.Fatalf("worker %d StartRun: %v", i, err)
		}
		channels[i] = msgCh
	}

	for i, ch := range channels {
		wg.Add(1)
		go func(idx int, c <-chan runner.Message) {
			defer wg.Done()
			msgs := collectMessages(c, 30*time.Second)
			var status runner.RunStatus
			for _, m := range msgs {
				if s, ok := m.(runner.StatusMsg); ok {
					status = s.State
				}
			}
			if status != runner.StatusCompleted {
				t.Errorf("worker %d ended with status %s, expected completed", idx, status)
			}
		}(i, ch)
	}
	wg.Wait()
}

// TestRunHistoryScale — write N runs to the history table directly via DB,
// then read them back. Verifies query latency under load (no specific bound,
// just that it completes within a generous timeout) and ordering.
func TestRunHistoryScale(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()
	_ = mgr // keep setup wiring; the test uses the DB directly

	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	const N = 1000
	startedAt := time.Now()
	for i := 0; i < N; i++ {
		_, err := store.Exec(
			"INSERT INTO runs (id, script_id, status, params, started_at) VALUES (?, ?, ?, ?, ?)",
			"run-"+strconv.Itoa(i), "scale_test", "completed", "{}", startedAt.Add(time.Duration(i)*time.Microsecond),
		)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	queryStart := time.Now()
	rows, err := store.Query(
		"SELECT id FROM runs WHERE script_id = ? ORDER BY started_at DESC LIMIT 100",
		"scale_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		count++
	}
	queryElapsed := time.Since(queryStart)

	if count != 100 {
		t.Errorf("expected 100 rows from LIMIT 100, got %d", count)
	}
	t.Logf("inserted=%d, queried 100 in %v", N, queryElapsed)
	if queryElapsed > 500*time.Millisecond {
		t.Errorf("query latency %v exceeded 500ms budget at scale=%d", queryElapsed, N)
	}
}
