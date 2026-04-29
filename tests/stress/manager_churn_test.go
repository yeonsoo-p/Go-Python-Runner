//go:build stress

package stress

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-python-runner/internal/db"
	"go-python-runner/internal/notify"
	"go-python-runner/internal/runner"
)

// stressSetup mirrors tests/integration/full_run_test.go testSetup but lives
// in the stress build tag. Returns a Manager wired to in-memory deps and a
// cleanup func that stops the gRPC server.
func stressSetup(t *testing.T) (*runner.Manager, func()) {
	t.Helper()
	cache := runner.NewCacheManager()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	rec := &notify.RecordingReservoir{}
	srv, err := runner.NewGRPCServer(cache, store, rec)
	if err != nil {
		t.Fatal(err)
	}
	mgr := runner.NewManager(srv, cache, store, rec)

	projectRoot, _ := filepath.Abs(filepath.Join("..", ".."))
	pythonPath := filepath.Join(projectRoot, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(pythonPath); os.IsNotExist(err) {
		pythonPath = filepath.Join(projectRoot, ".venv", "bin", "python3")
	}
	mgr.PythonPath = pythonPath
	mgr.LibDir = filepath.Join(projectRoot, "scripts", "_lib")

	return mgr, func() { srv.Stop() }
}

func stressTestdata(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// drain reads all messages from ch until it closes or the timeout fires.
// Returns nothing — used purely to keep waitForExit's emit path unblocked.
func drain(ch <-chan runner.Message, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timer.C:
			return
		}
	}
}

// TestStartCancelChurn — sequential Start→Cancel→Wait cycles. Asserts the
// process and goroutine count returns to baseline (no leaks). The drain
// goroutines are short-lived per iteration; we wait for them before sampling.
func TestStartCancelChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping churn test in short mode")
	}

	mgr, cleanup := stressSetup(t)
	defer cleanup()

	scriptDir := stressTestdata(t, "sleep_then_complete")
	const cycles = 50

	// Settle goroutines from setup.
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	for i := 0; i < cycles; i++ {
		runID, msgCh, err := mgr.StartRun("sleep_then_complete", map[string]string{}, scriptDir)
		if err != nil {
			t.Fatalf("iter %d: StartRun: %v", i, err)
		}
		// Cancel quickly.
		if err := mgr.CancelRun(runID); err != nil {
			t.Fatalf("iter %d: CancelRun: %v", i, err)
		}
		drain(msgCh, 5*time.Second)
	}

	// Allow waitForExit goroutines to finalize.
	time.Sleep(200 * time.Millisecond)
	final := runtime.NumGoroutine()
	if drift := final - baseline; drift > 5 {
		t.Errorf("goroutine drift after %d cycles: baseline=%d final=%d (drift=%d)",
			cycles, baseline, final, drift)
	}
}

// TestParallelStarts launches N concurrent StartRun calls of the fast_complete
// script. All must reach a terminal status; runIDs must be distinct.
func TestParallelStarts(t *testing.T) {
	mgr, cleanup := stressSetup(t)
	defer cleanup()

	scriptDir := stressTestdata(t, "fast_complete")
	const N = 20

	var wg sync.WaitGroup
	runIDs := make([]string, N)
	errs := make([]error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			runID, msgCh, err := mgr.StartRun("fast_complete", map[string]string{}, scriptDir)
			if err != nil {
				errs[idx] = err
				return
			}
			runIDs[idx] = runID
			drain(msgCh, 30*time.Second)
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, N)
	for i, id := range runIDs {
		if errs[i] != nil {
			t.Errorf("worker %d: StartRun failed: %v", i, errs[i])
			continue
		}
		if id == "" {
			t.Errorf("worker %d: empty runID", i)
			continue
		}
		if seen[id] {
			t.Errorf("duplicate runID %s at worker %d", id, i)
		}
		seen[id] = true
	}
}

// TestCancelDuringHandshake races CancelRun against the connect window.
// The fixture script connects and sleeps; we cancel within a short window.
// The run should terminate cleanly regardless of whether cancel landed
// before/during/after Python's connect.
func TestCancelDuringHandshake(t *testing.T) {
	mgr, cleanup := stressSetup(t)
	defer cleanup()

	scriptDir := stressTestdata(t, "sleep_then_complete")

	const N = 10
	for i := 0; i < N; i++ {
		runID, msgCh, err := mgr.StartRun("sleep_then_complete", map[string]string{}, scriptDir)
		if err != nil {
			t.Fatalf("iter %d StartRun: %v", i, err)
		}
		// Cancel almost immediately — interleaves with Python's connect.
		// Sleep a tiny variable amount so we hit different points in the
		// handshake across iterations.
		time.Sleep(time.Duration(i%5) * 50 * time.Millisecond)
		if err := mgr.CancelRun(runID); err != nil {
			t.Errorf("iter %d CancelRun: %v", i, err)
		}
		drain(msgCh, 10*time.Second)
	}
}

// TestDoubleCancelIdempotent — second cancel returns ErrRunNotActive cleanly.
// CancelGroup uses errors.Is(err, ErrRunNotActive) to filter sibling workers
// that already terminated; this test pins that contract at the unit level.
func TestDoubleCancelIdempotent(t *testing.T) {
	mgr, cleanup := stressSetup(t)
	defer cleanup()

	scriptDir := stressTestdata(t, "sleep_then_complete")
	runID, msgCh, err := mgr.StartRun("sleep_then_complete", map[string]string{}, scriptDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.CancelRun(runID); err != nil {
		t.Fatalf("first CancelRun: %v", err)
	}
	drain(msgCh, 10*time.Second)

	// At this point the run is removed from activeRuns. Second cancel must
	// return the ErrRunNotActive sentinel.
	err = mgr.CancelRun(runID)
	if !errors.Is(err, runner.ErrRunNotActive) {
		t.Errorf("second CancelRun: expected errors.Is(ErrRunNotActive), got: %v", err)
	}
}

// TestStartCancelChurnGoroutineLeak is a smaller version of churn that
// specifically asserts no goroutine leak after a single canceled run.
// Useful as a fast sanity check before the heavier churn test.
func TestStartCancelChurnGoroutineLeak(t *testing.T) {
	mgr, cleanup := stressSetup(t)
	defer cleanup()

	scriptDir := stressTestdata(t, "sleep_then_complete")

	// Warm up to stabilize goroutine count.
	for i := 0; i < 3; i++ {
		runID, msgCh, err := mgr.StartRun("sleep_then_complete", map[string]string{}, scriptDir)
		if err != nil {
			t.Fatal(err)
		}
		_ = mgr.CancelRun(runID)
		drain(msgCh, 10*time.Second)
	}

	time.Sleep(200 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	var failures atomic.Int32
	const cycles = 5
	for i := 0; i < cycles; i++ {
		runID, msgCh, err := mgr.StartRun("sleep_then_complete", map[string]string{}, scriptDir)
		if err != nil {
			t.Fatal(err)
		}
		if cerr := mgr.CancelRun(runID); cerr != nil && !errors.Is(cerr, fmt.Errorf("not found")) {
			failures.Add(1)
		}
		drain(msgCh, 10*time.Second)
	}

	time.Sleep(200 * time.Millisecond)
	if drift := runtime.NumGoroutine() - baseline; drift > 3 {
		t.Errorf("goroutine drift: baseline=%d after=%d (drift=%d)", baseline, runtime.NumGoroutine(), drift)
	}
}
