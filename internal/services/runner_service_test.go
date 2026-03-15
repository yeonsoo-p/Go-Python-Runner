package services

import (
	"testing"

	"go-python-runner/internal/db"
	"go-python-runner/internal/registry"
	"go-python-runner/internal/runner"
)

func TestRunnerService_StartRun_ScriptNotFound(t *testing.T) {
	reg := registry.New(testLogger())
	svc := NewRunnerService(nil, reg, testLogger())

	_, err := svc.StartRun("nonexistent", map[string]string{})
	if err == nil {
		t.Error("expected error for nonexistent script")
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
	grpcSrv, err := runner.NewGRPCServer(cache, testDB(t), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer grpcSrv.Stop()

	mgr := runner.NewManager(grpcSrv, cache, testLogger())
	reg := registry.New(testLogger())
	svc := NewRunnerService(mgr, reg, testLogger())

	err = svc.CancelRun("nonexistent-run-id")
	if err == nil {
		t.Error("expected error for cancelling nonexistent run")
	}
}

func TestRunnerService_GetRunHistory_Empty(t *testing.T) {
	cache := runner.NewCacheManager()
	grpcSrv, err := runner.NewGRPCServer(cache, testDB(t), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer grpcSrv.Stop()

	mgr := runner.NewManager(grpcSrv, cache, testLogger())
	reg := registry.New(testLogger())
	svc := NewRunnerService(mgr, reg, testLogger())

	history := svc.GetRunHistory()
	if len(history) != 0 {
		t.Errorf("expected empty history, got %d entries", len(history))
	}
}
