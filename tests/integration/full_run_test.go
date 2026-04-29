//go:build integration

package integration

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-python-runner/internal/db"
	"go-python-runner/internal/runner"
)

func testSetup(t *testing.T) (*runner.Manager, *runner.GRPCServer, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cache := runner.NewCacheManager()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	grpcServer, err := runner.NewGRPCServer(cache, store, logger)
	if err != nil {
		t.Fatal(err)
	}
	mgr := runner.NewManager(grpcServer, cache, store, logger)

	// Find Python from project root .venv
	projectRoot, _ := filepath.Abs(filepath.Join("..", ".."))
	pythonPath := filepath.Join(projectRoot, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(pythonPath); os.IsNotExist(err) {
		// Unix fallback
		pythonPath = filepath.Join(projectRoot, ".venv", "bin", "python3")
	}
	mgr.PythonPath = pythonPath
	mgr.LibDir = filepath.Join(projectRoot, "scripts", "_lib")

	return mgr, grpcServer, func() { grpcServer.Stop() }
}

func testdataDir(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func collectMessages(ch <-chan runner.Message, timeout time.Duration) []runner.Message {
	var msgs []runner.Message
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return msgs
			}
			msgs = append(msgs, msg)
		case <-timer.C:
			return msgs
		}
	}
}

func TestFullRun(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	runID, msgCh, err := mgr.StartRun("echo", map[string]string{"message": "world"}, testdataDir(t, "echo_script"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Started run: %s", runID)

	msgs := collectMessages(msgCh, 15*time.Second)
	if len(msgs) == 0 {
		t.Fatal("expected messages from run")
	}

	// Should have: output, progress(x3), status(completed)
	var hasOutput, hasProgress, hasCompleted bool
	for _, msg := range msgs {
		switch m := msg.(type) {
		case runner.OutputMsg:
			if m.Text == "echo: world" {
				hasOutput = true
			}
			t.Logf("Output: %s", m.Text)
		case runner.ProgressMsg:
			hasProgress = true
			t.Logf("Progress: %d/%d %s", m.Current, m.Total, m.Label)
		case runner.StatusMsg:
			if m.State == runner.StatusCompleted {
				hasCompleted = true
			}
			t.Logf("Status: %s", m.State)
		case runner.ErrorMsg:
			t.Logf("Error: %s\n%s", m.Message, m.Traceback)
		}
	}

	if !hasOutput {
		t.Error("expected output message 'echo: world'")
	}
	if !hasProgress {
		t.Error("expected progress messages")
	}
	if !hasCompleted {
		t.Error("expected completed status")
	}
}

func TestScriptCrash(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	_, msgCh, err := mgr.StartRun("crash", map[string]string{}, testdataDir(t, "crash_script"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := collectMessages(msgCh, 15*time.Second)

	var hasError, hasFailed bool
	for _, msg := range msgs {
		switch m := msg.(type) {
		case runner.ErrorMsg:
			hasError = true
			t.Logf("Error: %s", m.Message)
		case runner.StatusMsg:
			if m.State == "failed" {
				hasFailed = true
			}
		}
	}

	if !hasError {
		t.Error("expected error message from crash script")
	}
	if !hasFailed {
		t.Error("expected failed status from crash script")
	}
}

func TestCancelMidRun(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	runID, msgCh, err := mgr.StartRun("slow", map[string]string{}, testdataDir(t, "slow_script"))
	if err != nil {
		t.Fatal(err)
	}

	// Wait for first output, then cancel
	select {
	case msg := <-msgCh:
		t.Logf("Got first message: %T", msg)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for first message")
	}

	// Cancel the run
	if err := mgr.CancelRun(runID); err != nil {
		t.Fatalf("CancelRun failed: %v", err)
	}

	// Drain remaining messages — should end quickly after cancel
	msgs := collectMessages(msgCh, 5*time.Second)
	t.Logf("Got %d remaining messages after cancel", len(msgs))

	// Verify the run ended by asserting observable behavior: a follow-up
	// CancelRun on the same runID must return "not found", which only happens
	// after waitForExit removes the run from the activeRuns map.
	if err := mgr.CancelRun(runID); err == nil {
		t.Error("expected CancelRun on terminated run to return error, got nil")
	}
}
