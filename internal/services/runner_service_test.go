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
