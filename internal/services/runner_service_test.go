package services

import (
	"errors"
	"testing"

	"go-python-runner/internal/db"
	"go-python-runner/internal/notify"
	"go-python-runner/internal/registry"
	"go-python-runner/internal/runner"
)

func TestRunnerService_StartRun_ScriptNotFound(t *testing.T) {
	rec := &notify.RecordingReservoir{}
	reg := registry.New(rec)
	svc := NewRunnerService(nil, reg, rec)

	_, err := svc.StartRun("nonexistent", map[string]string{})
	if err == nil {
		t.Error("expected error for nonexistent script")
	}

	notify.AssertContract(t, rec, notify.ContractExpectation{
		Severity:    notify.SeverityError,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		ScriptID:    "nonexistent",
	})
}

func testDB(t *testing.T) *db.DB {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestRunnerService_CancelRun_NotFound pins the orthodox cancellation
// pattern: cancelling a non-active run returns the runner.ErrRunNotActive
// sentinel and does NOT call reservoir.Report — cancellation is not a
// failure (CLAUDE.md § Cancellation vs failure).
func TestRunnerService_CancelRun_NotFound(t *testing.T) {
	cache := runner.NewCacheManager()
	rec := &notify.RecordingReservoir{}
	grpcSrv, err := runner.NewGRPCServer(cache, testDB(t), rec)
	if err != nil {
		t.Fatal(err)
	}
	defer grpcSrv.Stop()

	mgr := runner.NewManager(grpcSrv, cache, testDB(t), rec)
	reg := registry.New(rec)
	svc := NewRunnerService(mgr, reg, rec)

	err = svc.CancelRun("nonexistent-run-id")
	if err == nil {
		t.Fatal("expected error for cancelling nonexistent run")
	}
	if !errors.Is(err, runner.ErrRunNotActive) {
		t.Errorf("want ErrRunNotActive sentinel, got %v", err)
	}
	if got := rec.FindBySeverity(notify.SeverityError); len(got) != 0 {
		t.Errorf("cancellation of non-active run must not Report errors, got %d", len(got))
	}
}

func TestRunnerService_StartParallelRuns_ScriptNotFound(t *testing.T) {
	rec := &notify.RecordingReservoir{}
	reg := registry.New(rec)
	svc := NewRunnerService(nil, reg, rec)

	res, err := svc.StartParallelRuns("nonexistent", map[string]string{}, 3)
	if err == nil {
		t.Fatal("expected error for nonexistent script")
	}
	if res.GroupID != "" || len(res.RunIDs) != 0 {
		t.Errorf("want zero ParallelRunsResult on error, got %+v", res)
	}

	notify.AssertContract(t, rec, notify.ContractExpectation{
		Severity:    notify.SeverityError,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		ScriptID:    "nonexistent",
	})
}

func TestRunnerService_StartParallelRuns_NotParallel(t *testing.T) {
	rec := &notify.RecordingReservoir{}
	reg := registry.New(rec)

	dir := t.TempDir()
	// writeTestScript (defined in script_service_test.go) emits a script.json
	// with no parallel block, so Script.Parallel is nil after load.
	writeTestScript(t, dir, "noparallel", "NoParallel")
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	svc := NewRunnerService(nil, reg, rec)
	res, err := svc.StartParallelRuns("noparallel", map[string]string{}, 3)
	if err == nil {
		t.Fatal("expected error for non-parallel script")
	}
	if res.GroupID != "" || len(res.RunIDs) != 0 {
		t.Errorf("want zero ParallelRunsResult on error, got %+v", res)
	}

	notify.AssertContract(t, rec, notify.ContractExpectation{
		Severity:        notify.SeverityError,
		Persistence:     notify.PersistenceOneShot,
		Source:          notify.SourceBackend,
		ScriptID:        "noparallel",
		MessageContains: "does not support parallel",
	})
}

func TestRunnerService_CancelGroup_NotFound(t *testing.T) {
	rec := &notify.RecordingReservoir{}
	reg := registry.New(rec)
	svc := NewRunnerService(nil, reg, rec)

	err := svc.CancelGroup("missing-group")
	if err == nil {
		t.Fatal("expected error for missing group")
	}

	notify.AssertContract(t, rec, notify.ContractExpectation{
		Severity:        notify.SeverityError,
		Persistence:     notify.PersistenceOneShot,
		Source:          notify.SourceBackend,
		MessageContains: "missing-group",
	})
}

// newTestRunnerService wires a RunnerService against a real Manager + GRPCServer
// + in-memory DB. The Wails app is intentionally unset so emit() becomes a
// no-op — forwardMessages still records reservoir events (the orthodoxy:
// every event flows through one ingress regardless of UI state).
func newTestRunnerService(t *testing.T) (*RunnerService, *notify.RecordingReservoir, func()) {
	t.Helper()
	cache := runner.NewCacheManager()
	rec := &notify.RecordingReservoir{}
	store := testDB(t)
	grpcSrv, err := runner.NewGRPCServer(cache, store, rec)
	if err != nil {
		t.Fatal(err)
	}
	mgr := runner.NewManager(grpcSrv, cache, store, rec)
	reg := registry.New(rec)
	svc := NewRunnerService(mgr, reg, rec)
	return svc, rec, func() {
		grpcSrv.Stop()
		store.Close()
	}
}

// TestForwardMessages_FailureToastUsesRealError covers Part D1: the
// "Run failed" toast body should carry the real Python error text (and
// traceback) captured from the immediately-preceding ErrorMsg, not the
// useless "<scriptID> failed" placeholder. Sequence guarantee comes from
// runner.fail(): ErrorMsg is sent before StatusMsg(failed).
func TestForwardMessages_FailureToastUsesRealError(t *testing.T) {
	svc, rec, cleanup := newTestRunnerService(t)
	defer cleanup()

	ch := make(chan runner.Message, 4)
	ch <- runner.ErrorMsg{
		Message:   "Invalid number in 'data': could not convert string to float: 'wewe'",
		Traceback: "Traceback (most recent call last):\n  File ...\nValueError: ...",
	}
	ch <- runner.StatusMsg{State: runner.StatusFailed}
	close(ch)

	svc.forwardMessages("run-1", "numpy_stats", "", ch)

	// Find the "Run failed" toast.
	var failed *notify.Event
	for i := range rec.Events() {
		e := rec.Events()[i]
		if e.Title == "Run failed" {
			failed = &e
			break
		}
	}
	if failed == nil {
		t.Fatal("expected a 'Run failed' toast event")
	}
	if failed.Persistence != notify.PersistenceOneShot {
		t.Errorf("Persistence = %s, want OneShot", failed.Persistence)
	}
	wantMsg := "Invalid number in 'data': could not convert string to float: 'wewe'"
	if failed.Message != wantMsg {
		t.Errorf("Message = %q, want %q", failed.Message, wantMsg)
	}
	if failed.Traceback == "" {
		t.Error("Traceback missing on failure toast")
	}
}

// TestForwardMessages_FailureToastFallback covers the script-bug edge case:
// StatusMsg(failed) arrives without a preceding ErrorMsg. The toast must
// still tell the user something useful (point them at the Logs).
func TestForwardMessages_FailureToastFallback(t *testing.T) {
	svc, rec, cleanup := newTestRunnerService(t)
	defer cleanup()

	ch := make(chan runner.Message, 1)
	ch <- runner.StatusMsg{State: runner.StatusFailed}
	close(ch)

	svc.forwardMessages("run-2", "buggy_script", "", ch)

	var failed *notify.Event
	for i := range rec.Events() {
		e := rec.Events()[i]
		if e.Title == "Run failed" {
			failed = &e
			break
		}
	}
	if failed == nil {
		t.Fatal("expected a 'Run failed' toast event even without preceding ErrorMsg")
	}
	if failed.Message == "" || failed.Message == "buggy_script failed" {
		t.Errorf("fallback message should be informative, got %q", failed.Message)
	}
}

// TestForwardMessages_CancelledRunDemotesErrorMsg covers Part A4: when the
// run was cancelled by the user, Python's cooperative-cancel fail("Cancelled
// by user") arrives as an ErrorMsg. It must be demoted to Info+OneShot
// (log-only per the routing matrix) instead of routing to the per-run pane
// (PersistenceInFlight). The reservoir still records it — never dropped.
func TestForwardMessages_CancelledRunDemotesErrorMsg(t *testing.T) {
	svc, rec, cleanup := newTestRunnerService(t)
	defer cleanup()

	runID := "run-cancelled"
	// Register the run with the gRPC server and the manager so WasCancelled
	// has somewhere to look. Then mark it cancelled via CancelRun.
	svc.manager.GRPCServer().RegisterRun(runID)
	defer svc.manager.GRPCServer().UnregisterRun(runID)
	svc.manager.RegisterActiveRunForTest(runID, "parallel_worker")
	if err := svc.manager.CancelRun(runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	ch := make(chan runner.Message, 1)
	ch <- runner.ErrorMsg{Message: "[Alpha] Cancelled by user"}
	close(ch)

	svc.forwardMessages(runID, "parallel_worker", "", ch)

	// No PersistenceInFlight event should exist for this run — the cancel
	// path demotes to Info+OneShot.
	for _, e := range rec.Events() {
		if e.RunID == runID && e.Persistence == notify.PersistenceInFlight {
			t.Errorf("cancelled run produced InFlight error: %+v", e)
		}
	}
	// And we should still see the message recorded as Info+OneShot — log
	// preserved, just not surfaced as an error.
	var sawInfo bool
	for _, e := range rec.Events() {
		if e.RunID == runID && e.Severity == notify.SeverityInfo && e.Persistence == notify.PersistenceOneShot && e.Message == "[Alpha] Cancelled by user" {
			sawInfo = true
			break
		}
	}
	if !sawInfo {
		t.Error("cancelled run's ErrorMsg should be recorded as Info+OneShot (log-only)")
	}
}

// TestForwardMessages_GenuineErrorStillSurfacesForSibling covers the
// 1-error-out-of-3-parallel scenario: a sibling worker that wasn't cancelled
// must still surface its ErrorMsg as PersistenceInFlight (per-run pane).
// WasCancelled is per-runID, so cancelling worker B doesn't suppress
// worker A's genuine error.
func TestForwardMessages_GenuineErrorStillSurfacesForSibling(t *testing.T) {
	svc, rec, cleanup := newTestRunnerService(t)
	defer cleanup()

	cancelledID := "run-cancelled"
	siblingID := "run-genuine-error"
	svc.manager.GRPCServer().RegisterRun(cancelledID)
	svc.manager.GRPCServer().RegisterRun(siblingID)
	defer svc.manager.GRPCServer().UnregisterRun(cancelledID)
	defer svc.manager.GRPCServer().UnregisterRun(siblingID)
	svc.manager.RegisterActiveRunForTest(cancelledID, "parallel_worker")
	svc.manager.RegisterActiveRunForTest(siblingID, "parallel_worker")
	if err := svc.manager.CancelRun(cancelledID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	ch := make(chan runner.Message, 1)
	ch <- runner.ErrorMsg{Message: "KeyError: 'missing_key'"}
	close(ch)

	// Forward messages for the SIBLING (not the cancelled one) — sibling
	// must still surface the error.
	svc.forwardMessages(siblingID, "parallel_worker", "g1", ch)

	var sawInFlight bool
	for _, e := range rec.Events() {
		if e.RunID == siblingID && e.Persistence == notify.PersistenceInFlight {
			sawInFlight = true
			break
		}
	}
	if !sawInFlight {
		t.Error("sibling worker's genuine ErrorMsg should still route to InFlight")
	}
}

// TestRunnerService_GroupLifecycle exercises registerGroup + clearRunFromGroup
// directly. These are the only group-state mutations: end-to-end behavior
// (StartParallelRuns wiring, terminal-status clearing) is covered by integration
// tests that can spawn real Python workers.
func TestRunnerService_GroupLifecycle(t *testing.T) {
	rec := &notify.RecordingReservoir{}
	reg := registry.New(rec)
	svc := NewRunnerService(nil, reg, rec)

	svc.registerGroup("g1", "myscript", []string{"r1", "r2", "r3"})

	svc.groupsMu.Lock()
	if g, ok := svc.groups["g1"]; !ok {
		t.Fatal("group g1 not registered")
	} else if g.ScriptID != "myscript" || len(g.RunIDs) != 3 {
		t.Errorf("group g1 unexpected: %+v", g)
	}
	for _, id := range []string{"r1", "r2", "r3"} {
		if got := svc.runGroups[id]; got != "g1" {
			t.Errorf("runGroups[%s]=%q want g1", id, got)
		}
	}
	svc.groupsMu.Unlock()

	// Clearing one runID leaves the group present (others still active).
	svc.clearRunFromGroup("r1")
	svc.groupsMu.Lock()
	if _, ok := svc.groups["g1"]; !ok {
		t.Error("group g1 deleted prematurely after one terminal run")
	}
	if _, ok := svc.runGroups["r1"]; ok {
		t.Error("runGroups still tracks r1 after clear")
	}
	svc.groupsMu.Unlock()

	// Clearing the remaining runIDs deletes the group entry.
	svc.clearRunFromGroup("r2")
	svc.clearRunFromGroup("r3")
	svc.groupsMu.Lock()
	if _, ok := svc.groups["g1"]; ok {
		t.Error("group g1 should be deleted after all runs cleared")
	}
	svc.groupsMu.Unlock()

	// Clearing a runID with no group is a no-op.
	svc.clearRunFromGroup("not-in-any-group")
}
