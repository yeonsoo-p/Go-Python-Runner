package runner

import (
	"errors"
	"testing"

	"go-python-runner/internal/db"
	"go-python-runner/internal/notify"
)

// newTestManager wires a Manager with real GRPCServer + in-memory DB + a
// recording reservoir. Suitable for unit tests of deriveFinalStatus and
// CancelRun that don't need a live Python subprocess.
func newTestManager(t *testing.T) (*Manager, *GRPCServer, func()) {
	t.Helper()
	cache := NewCacheManager()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	rec := &notify.RecordingReservoir{}
	srv, err := NewGRPCServer(cache, store, rec)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(srv, cache, store, rec)
	return mgr, srv, func() {
		srv.Stop()
		store.Close()
	}
}


// TestDeriveFinalStatus_CancelOverridesEverything is the contract test for
// rule 0 of the trust order. A run with cancelRequested=true must resolve
// to StatusCancelled regardless of the exit code, gotError, gotFailedStatus,
// or gotCompletedStatus signals — cancellation is the user's intent and
// must not be conflated with failure.
func TestDeriveFinalStatus_CancelOverridesEverything(t *testing.T) {
	cases := []struct {
		name       string
		exitCode   int
		waitErr    error
		setupFlags func(srv *GRPCServer, runID string)
	}{
		{
			name:     "cancel overrides non-zero exit",
			exitCode: 137, // SIGKILL exit code
		},
		{
			name:    "cancel overrides waitErr",
			waitErr: errors.New("process wait failed"),
		},
		{
			name: "cancel overrides gotError",
			setupFlags: func(srv *GRPCServer, runID string) {
				if rc, ok := srv.runs[runID]; ok {
					rc.gotError.Store(true)
				}
			},
		},
		{
			name: "cancel overrides pythonStatus=failed",
			setupFlags: func(srv *GRPCServer, runID string) {
				if rc, ok := srv.runs[runID]; ok {
					s := StatusFailed
					rc.pythonStatus.Store(&s)
				}
			},
		},
		{
			name: "cancel overrides pythonStatus=completed",
			setupFlags: func(srv *GRPCServer, runID string) {
				if rc, ok := srv.runs[runID]; ok {
					s := StatusCompleted
					rc.pythonStatus.Store(&s)
				}
			},
		},
		{
			name:     "cancel overrides every flag at once",
			exitCode: 1,
			waitErr:  errors.New("boom"),
			setupFlags: func(srv *GRPCServer, runID string) {
				if rc, ok := srv.runs[runID]; ok {
					rc.gotError.Store(true)
					s := StatusFailed
					rc.pythonStatus.Store(&s)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, srv, cleanup := newTestManager(t)
			defer cleanup()

			runID := "run-cancel"
			srv.RegisterRun(runID)
			defer srv.UnregisterRun(runID)
			mgr.RegisterActiveRunForTest( runID, "test-script")

			if tc.setupFlags != nil {
				tc.setupFlags(srv, runID)
			}

			// Mark cancelRequested via the production code path.
			if err := mgr.CancelRun(runID); err != nil {
				t.Fatalf("CancelRun: %v", err)
			}
			if !mgr.WasCancelled(runID) {
				t.Fatal("WasCancelled should be true after CancelRun")
			}

			got := mgr.deriveFinalStatus(runID, tc.exitCode, tc.waitErr)
			if got != StatusCancelled {
				t.Errorf("deriveFinalStatus = %s, want %s", got, StatusCancelled)
			}
		})
	}
}

// TestDeriveFinalStatus_NonCancelPathsUnchanged is a regression: without
// cancelRequested, the trust order rules 1-5 must produce the same status
// they did before the cancellation override was added.
func TestDeriveFinalStatus_NonCancelPathsUnchanged(t *testing.T) {
	cases := []struct {
		name       string
		exitCode   int
		waitErr    error
		setupFlags func(srv *GRPCServer, runID string)
		want       RunStatus
	}{
		{name: "rule 1: non-zero exit", exitCode: 1, want: StatusFailed},
		{name: "rule 1: waitErr", waitErr: errors.New("oops"), want: StatusFailed},
		{
			name: "rule 2: gotError",
			setupFlags: func(srv *GRPCServer, runID string) {
				srv.runs[runID].gotError.Store(true)
			},
			want: StatusFailed,
		},
		{
			name: "rule 3: pythonStatus=failed only",
			setupFlags: func(srv *GRPCServer, runID string) {
				s := StatusFailed
				srv.runs[runID].pythonStatus.Store(&s)
			},
			want: StatusFailed,
		},
		{
			name: "rule 4: pythonStatus=completed",
			setupFlags: func(srv *GRPCServer, runID string) {
				s := StatusCompleted
				srv.runs[runID].pythonStatus.Store(&s)
			},
			want: StatusCompleted,
		},
		{name: "rule 5: clean exit, no signals", want: StatusFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, srv, cleanup := newTestManager(t)
			defer cleanup()

			runID := "run-nocancel"
			srv.RegisterRun(runID)
			defer srv.UnregisterRun(runID)
			mgr.RegisterActiveRunForTest( runID, "test-script")

			if tc.setupFlags != nil {
				tc.setupFlags(srv, runID)
			}

			got := mgr.deriveFinalStatus(runID, tc.exitCode, tc.waitErr)
			if got != tc.want {
				t.Errorf("deriveFinalStatus = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestCancelRun_NotActive is the contract for the ErrRunNotActive sentinel.
// CancelRun on an unknown runID must return ErrRunNotActive (not a wrapped
// error), so RunnerService.CancelGroup can filter it via errors.Is and
// avoid surfacing a "did not cancel" toast for an already-terminal worker.
func TestCancelRun_NotActive(t *testing.T) {
	mgr, _, cleanup := newTestManager(t)
	defer cleanup()

	err := mgr.CancelRun("never-registered")
	if !errors.Is(err, ErrRunNotActive) {
		t.Errorf("CancelRun(unknown) = %v, want errors.Is(ErrRunNotActive)", err)
	}
}

// TestCancelRun_SetsFlag verifies CancelRun sets cancelRequested=true on the
// active run state. Without this flag, deriveFinalStatus has no signal to
// distinguish user cancel from process crash.
func TestCancelRun_SetsFlag(t *testing.T) {
	mgr, srv, cleanup := newTestManager(t)
	defer cleanup()

	runID := "run-flag"
	srv.RegisterRun(runID)
	defer srv.UnregisterRun(runID)
	mgr.RegisterActiveRunForTest( runID, "test-script")

	if mgr.WasCancelled(runID) {
		t.Fatal("WasCancelled should be false before CancelRun")
	}
	if err := mgr.CancelRun(runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !mgr.WasCancelled(runID) {
		t.Error("WasCancelled should be true after CancelRun")
	}
}
