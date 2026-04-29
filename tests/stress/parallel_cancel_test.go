//go:build stress

package stress

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-python-runner/internal/db"
	"go-python-runner/internal/notify"
	"go-python-runner/internal/registry"
	"go-python-runner/internal/runner"
	"go-python-runner/internal/services"
)

// TestCancelGroup_ContractEndToEnd is the regression test for the cancel-group
// bug: cancelling a parallel run group surfaced "RUN FAILED" toasts for every
// worker plus an "RUN RECORD UPDATE FAILED — SQLITE_BUSY" toast.
//
// It encodes the four-part contract:
//  1. No "Run failed" toast for any cancelled worker.
//  2. No "Run record update failed" toast (Part B's DB-pool fix).
//  3. No PersistenceInFlight ErrorMsg from Python's "Cancelled by user" fail()
//     reaching the per-run pane (Part A4's demotion).
//  4. Every cancelled run lands at StatusCancelled.
//
// Critically, this test uses a FILE-BACKED database, which is the production
// path. The other stress tests use db.Open(":memory:") which used to be the
// only path with SetMaxOpenConns(1) — that's why the bug went unnoticed.
func TestCancelGroup_ContractEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cancel-group contract test in short mode")
	}

	rec := &notify.RecordingReservoir{}
	cache := runner.NewCacheManager()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	srv, err := runner.NewGRPCServer(cache, store, rec)
	if err != nil {
		t.Fatalf("NewGRPCServer: %v", err)
	}
	defer srv.Stop()

	mgr := runner.NewManager(srv, cache, store, rec)

	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pythonPath := filepath.Join(projectRoot, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(pythonPath); os.IsNotExist(err) {
		pythonPath = filepath.Join(projectRoot, ".venv", "bin", "python3")
	}
	mgr.PythonPath = pythonPath
	mgr.LibDir = filepath.Join(projectRoot, "scripts", "_lib")

	reg := registry.New(rec)
	if err := reg.LoadBuiltin(filepath.Join(projectRoot, "scripts")); err != nil {
		t.Fatalf("LoadBuiltin: %v", err)
	}

	svc := services.NewRunnerService(mgr, reg, rec)

	const workers = 6
	res, err := svc.StartParallelRuns("parallel_worker", map[string]string{
		"steps":        "20",
		"delay":        "0.2",
		"hold_seconds": "10",
	}, workers)
	if err != nil {
		t.Fatalf("StartParallelRuns: %v", err)
	}
	if got := len(res.RunIDs); got != workers {
		t.Fatalf("StartParallelRuns spawned %d runs, want %d", got, workers)
	}

	// Let workers connect and start their first step before cancelling.
	time.Sleep(500 * time.Millisecond)

	if err := svc.CancelGroup(res.GroupID); err != nil {
		t.Errorf("CancelGroup: %v", err)
	}

	// Wait for every run to terminate. waitForExit closes the message
	// channel; svc.forwardMessages returns when ch is drained. Poll the
	// manager's history snapshot until all our runs are present (or time
	// out).
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		recorded := historyContains(mgr, res.RunIDs)
		if recorded == len(res.RunIDs) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Contract assertions — inspect the recording reservoir for forbidden
	// events.
	for _, ev := range rec.Events() {
		if ev.Title == "Run failed" {
			t.Errorf("contract violated: cancelled run produced 'Run failed' toast: runID=%s msg=%s",
				ev.RunID, ev.Message)
		}
		if ev.Title == "Run record update failed" {
			t.Errorf("contract violated: DB contention surfaced as toast: %s", ev.Message)
		}
		if ev.Persistence == notify.PersistenceInFlight && ev.Source == notify.SourcePython {
			// Per-run pane error from Python — for cancelled runs this
			// should have been demoted to Info+OneShot in Part A4.
			t.Errorf("contract violated: cancelled run produced InFlight Python error: runID=%s msg=%s",
				ev.RunID, ev.Message)
		}
	}

	// Every run should have landed at StatusCancelled.
	hist := mgr.History()
	for _, runID := range res.RunIDs {
		var status runner.RunStatus
		var found bool
		for _, h := range hist {
			if h.RunID == runID {
				status = h.Status
				found = true
				break
			}
		}
		if !found {
			t.Errorf("run %s not in history (did it terminate?)", runID)
			continue
		}
		if status != runner.StatusCancelled {
			t.Errorf("run %s ended in status %q, want %q", runID, status, runner.StatusCancelled)
		}
	}

	// And the SQLite rows should reflect cancelled status — production
	// path, not in-memory.
	for _, runID := range res.RunIDs {
		var status string
		err := store.QueryRow("SELECT status FROM runs WHERE id = ?", runID).Scan(&status)
		if err != nil {
			t.Errorf("DB row missing for run %s: %v", runID, err)
			continue
		}
		if status != "cancelled" {
			t.Errorf("DB row for %s has status %q, want %q", runID, status, "cancelled")
		}
	}
}

// historyContains returns how many of runIDs appear in mgr.History().
func historyContains(mgr *runner.Manager, runIDs []string) int {
	hist := mgr.History()
	want := make(map[string]struct{}, len(runIDs))
	for _, id := range runIDs {
		want[id] = struct{}{}
	}
	count := 0
	for _, h := range hist {
		if _, ok := want[h.RunID]; ok {
			count++
		}
	}
	return count
}
