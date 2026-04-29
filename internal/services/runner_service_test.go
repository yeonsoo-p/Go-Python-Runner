package services

import (
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

	// Four-part contract: error returned (above) + reservoir reported.
	events := rec.FindBySeverity(notify.SeverityError)
	if len(events) != 1 {
		t.Fatalf("want 1 reservoir error event, got %d", len(events))
	}
	if events[0].Persistence != notify.PersistenceOneShot {
		t.Errorf("want OneShot persistence, got %v", events[0].Persistence)
	}
	if events[0].ScriptID != "nonexistent" {
		t.Errorf("want scriptID=nonexistent, got %q", events[0].ScriptID)
	}
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
		t.Error("expected error for cancelling nonexistent run")
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
