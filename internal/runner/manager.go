package runner

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"go-python-runner/internal/db"
	"go-python-runner/internal/notify"
)

// maxStderrLog is the maximum number of bytes of stderr output to log.
const maxStderrLog = 4096

// RunStatus represents the lifecycle state of a script run.
type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

// IsTerminal returns true if the status represents a final state.
func (s RunStatus) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// RunState tracks the state of a single script execution.
type RunState struct {
	RunID    string
	ScriptID string
	Status   RunStatus
	process  *Process
	messages <-chan Message
	cancel   func()
	// cancelRequested is set by CancelRun under Manager.mu and read by
	// deriveFinalStatus to override the final status to StatusCancelled
	// regardless of how the process exits or what Python signals. This is
	// the single structural fact that distinguishes user-driven cancel
	// from genuine failure.
	cancelRequested bool
}

// RunRecord is a completed run entry for history.
type RunRecord struct {
	RunID     string
	ScriptID  string
	Status    RunStatus
	StartedAt time.Time
	EndedAt   time.Time
}

// Manager orchestrates Python script execution.
type Manager struct {
	mu         sync.RWMutex
	activeRuns map[string]*RunState
	history    []RunRecord
	grpc       *GRPCServer
	cache      *CacheManager
	db         *db.DB
	reservoir  notify.Reservoir
	PythonPath string // optional override for python path
	LibDir     string // path to scripts/_lib (prepended to PYTHONPATH for spawned scripts)
}

// NewManager creates a new process manager.
// If store is non-nil, persisted run history is loaded from SQLite.
// The reservoir is the sole observability dependency — every trace, warn,
// and error event flows through reservoir.Report.
func NewManager(grpc *GRPCServer, cache *CacheManager, store *db.DB, reservoir notify.Reservoir) *Manager {
	mgr := &Manager{
		activeRuns: make(map[string]*RunState),
		grpc:       grpc,
		cache:      cache,
		db:         store,
		reservoir:  reservoir,
	}
	if store != nil {
		mgr.loadHistory()
	}
	return mgr
}

// loadHistory populates the in-memory history slice from the SQLite runs table.
func (m *Manager) loadHistory() {
	rows, err := m.db.Query(
		"SELECT id, script_id, status, started_at, finished_at FROM runs " +
			"WHERE status IN ('completed', 'failed', 'cancelled') ORDER BY started_at DESC",
	)
	if err != nil {
		m.reservoir.Report(notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Run history load failed",
			Message:     err.Error(),
			Err:         err,
		})
		return
	}
	defer rows.Close()

	for rows.Next() {
		var rec RunRecord
		var status string
		if err := rows.Scan(&rec.RunID, &rec.ScriptID, &status, &rec.StartedAt, &rec.EndedAt); err != nil {
			m.reservoir.Report(notify.Event{
				Severity:    notify.SeverityWarn,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Run history scan failed",
				Message:     err.Error(),
				Err:         err,
			})
			continue
		}
		rec.Status = RunStatus(status)
		m.history = append(m.history, rec)
	}
}

// StartRun starts a Python script and returns the runID and a message channel.
// The caller should read from the channel to receive typed messages from the script.
func (m *Manager) StartRun(scriptID string, params map[string]string, scriptDir string) (string, <-chan Message, error) {
	runID := uuid.New().String()
	startedAt := time.Now()

	// Register run with gRPC server to get message channel
	msgCh := m.grpc.RegisterRun(runID)

	// Create and start the process
	proc := NewProcess(runID, scriptDir, m.LibDir, params, m.grpc.Addr())
	proc.PythonPath = m.PythonPath
	if err := proc.Start(); err != nil {
		m.grpc.UnregisterRun(runID)
		return "", nil, fmt.Errorf("starting script %s: %w", scriptID, err)
	}

	state := &RunState{
		RunID:    runID,
		ScriptID: scriptID,
		Status:   StatusRunning,
		process:  proc,
		messages: msgCh,
		cancel:   proc.Cancel,
	}

	func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.activeRuns[runID] = state
	}()

	m.reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     "run started",
		RunID:       runID,
		ScriptID:    scriptID,
	})

	// Persist initial run record to SQLite for history across restarts.
	if m.db != nil {
		paramsJSON, _ := json.Marshal(params)
		if _, err := m.db.Exec(
			"INSERT INTO runs (id, script_id, status, params, started_at) VALUES (?, ?, ?, ?, ?)",
			runID, scriptID, string(StatusRunning), string(paramsJSON), startedAt,
		); err != nil {
			m.reservoir.Report(notify.Event{
				Severity:    notify.SeverityWarn,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Run start record failed",
				Message:     err.Error(),
				RunID:       runID,
				Err:         err,
			})
		}
	}

	go m.waitAndSendStart(runID, params, proc)
	go m.waitForExit(runID, scriptID, startedAt, proc)

	return runID, msgCh, nil
}

// connectTimeout is the maximum time to wait for a Python script to connect via gRPC.
const connectTimeout = 30 * time.Second

// cancelGracePeriod is the time to wait for a script to exit gracefully after
// receiving a CancelRequest before force-killing the process.
const cancelGracePeriod = 3 * time.Second

// waitAndSendStart waits for the Python script to connect, then sends start params.
// Bails early if the process exits or is cancelled before connecting — otherwise
// a cancelled-before-connect run would leak this goroutine for a full
// connectTimeout (30s).
func (m *Manager) waitAndSendStart(runID string, params map[string]string, proc *Process) {
	select {
	case <-m.grpc.WaitConnected(runID):
	case <-proc.Done():
		// Process exited or was cancelled before Python connected.
		// Nothing to send; waitForExit handles the cleanup path.
		return
	case <-time.After(connectTimeout):
		m.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Python connect timeout",
			Message:     "timeout waiting for Python to connect, killing process",
			RunID:       runID,
		})
		proc.Cancel()
		return
	}
	if err := m.grpc.SendStart(runID, params); err != nil {
		m.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Send start params failed",
			Message:     err.Error(),
			RunID:       runID,
			Err:         err,
		})
		proc.Cancel()
	}
}

// deriveFinalStatus is Manager's authoritative status decision for a finished run.
// See the trust-order comment in waitForExit for the rule list.
func (m *Manager) deriveFinalStatus(runID string, exitCode int, waitErr error) RunStatus {
	// Rule 0: user-driven cancellation overrides everything below. A
	// cancelled run is not a failure — even if the kill produced a non-zero
	// exit, even if Python's cooperative cancel path called fail(), the
	// terminal state is StatusCancelled. This is the single fact that
	// keeps cancellation from being conflated with failure downstream.
	if m.WasCancelled(runID) {
		return StatusCancelled
	}
	if waitErr != nil || exitCode != 0 {
		return StatusFailed
	}
	if m.grpc.GotError(runID) {
		return StatusFailed
	}
	if m.grpc.GotFailedStatus(runID) {
		return StatusFailed
	}
	if m.grpc.GotCompletedStatus(runID) {
		return StatusCompleted
	}
	// Process exited 0 but Python signaled neither completion nor failure.
	// Treat as a failure so users see a problem rather than a silent
	// "completed" for a script that crashed before reaching complete()/fail().
	return StatusFailed
}

// WasCancelled reports whether CancelRun has been called for this run. Used
// by RunnerService.forwardMessages to demote in-flight ErrorMsgs from
// cancelled runs to log-only routing (Python's cooperative-cancel path
// emits fail("Cancelled by user"), which would otherwise surface as a
// per-run error pane).
func (m *Manager) WasCancelled(runID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.activeRuns[runID]; ok {
		return s.cancelRequested
	}
	return false
}

// RegisterActiveRunForTest installs a minimal RunState in the activeRuns map
// without spawning a Python process. Test-only — production code only adds
// to activeRuns via StartRun. Cross-package tests (e.g. RunnerService unit
// tests in internal/services) need this to exercise WasCancelled / CancelRun
// against a Manager without a live subprocess.
func (m *Manager) RegisterActiveRunForTest(runID, scriptID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeRuns[runID] = &RunState{
		RunID:    runID,
		ScriptID: scriptID,
		Status:   StatusRunning,
		cancel:   func() {},
	}
}

// GRPCServer returns the underlying gRPC server. Test-only accessor used by
// cross-package tests to register/unregister run channels without spawning
// a Python subprocess. Production code reaches the gRPC server through
// constructor injection, not this getter.
func (m *Manager) GRPCServer() *GRPCServer { return m.grpc }

// History returns a snapshot of completed-run records, newest first by index
// of insertion. Used by integration / stress tests that need to assert
// terminal state without relying on the gRPC server's per-run channels
// (which are unregistered as soon as a run completes).
func (m *Manager) History() []RunRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RunRecord, len(m.history))
	copy(out, m.history)
	return out
}

// waitForExit waits for the process to exit, logs stderr if needed, and records the run.
func (m *Manager) waitForExit(runID, scriptID string, startedAt time.Time, proc *Process) {
	exitCode, stderrOutput, err := proc.Wait()
	endedAt := time.Now()

	// Wait for gRPC stream to fully close so all Python messages
	// (progress, error, status) are delivered before we synthesize a final status.
	m.grpc.WaitStreamDone(runID, 5*time.Second)

	// Manager is the SOLE authority on terminal run status. Python's StatusMsg
	// is consumed as a flag (gotCompletedStatus / gotFailedStatus) by the gRPC
	// handler and never reaches the frontend on its own — this method emits
	// exactly one run:status event below.
	//
	// Trust order:
	//   0. cancelRequested flag     → Cancelled (user-driven; not a failure, overrides everything below)
	//   1. process exited non-zero  → Failed   (process crashed; ignore Python's intent)
	//   2. gotError flag            → Failed   (Python called fail())
	//   3. gotFailedStatus flag     → Failed   (Python sent Status(failed) without ErrorMsg)
	//   4. gotCompletedStatus flag  → Completed (Python called complete())
	//   5. otherwise                → Failed   (script exited cleanly without signaling — bug in script)
	finalStatus := m.deriveFinalStatus(runID, exitCode, err)

	if finalStatus == StatusFailed && exitCode != 0 && stderrOutput != "" && !m.grpc.GotError(runID) {
		// Process crashed without sending a structured error. Route the
		// captured stderr through the reservoir so the per-run pane (via
		// run:error) and LogViewer (via slog) both see it through the same
		// single ingress as structured errors. Persistence is InFlight so
		// the routing matches a Python-originated fail() exactly.
		logStderr := stderrOutput
		if len(logStderr) > maxStderrLog {
			logStderr = logStderr[:maxStderrLog] + "\n... (truncated)"
		}
		m.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceInFlight,
			Source:      notify.SourcePython,
			Title:       "Script crashed",
			Message:     logStderr,
			RunID:       runID,
			ScriptID:    scriptID,
			Traceback:   logStderr,
		})
	}

	// Sole emitter of run:status — frontend is guaranteed to receive exactly
	// one terminal status event per run.
	m.grpc.TrySendStatus(runID, finalStatus)

	// Capture structured error message before UnregisterRun clears the RunChannel.
	// Priority: stderr (unstructured crash) > gRPC ErrorMsg (structured fail()) > process error.
	var errorMessage string
	if finalStatus == StatusFailed {
		if stderrOutput != "" {
			errorMessage = stderrOutput
			if len(errorMessage) > maxStderrLog {
				errorMessage = errorMessage[:maxStderrLog] + "\n... (truncated)"
			}
		} else if msg := m.grpc.ErrorMessage(runID); msg != "" {
			errorMessage = msg
		} else if err != nil {
			errorMessage = err.Error()
		}
	}

	m.cache.CleanupRun(runID)
	m.grpc.UnregisterRun(runID)

	m.mu.Lock()
	recordStatus := finalStatus
	if s, ok := m.activeRuns[runID]; ok {
		if !s.Status.IsTerminal() {
			s.Status = finalStatus
		} else {
			m.reservoir.Report(notify.Event{
				Severity:    notify.SeverityWarn,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Message:     fmt.Sprintf("ignoring status transition from terminal state: current=%s attempted=%s", s.Status, finalStatus),
				RunID:       runID,
			})
		}
		recordStatus = s.Status
	}
	delete(m.activeRuns, runID)
	m.history = append(m.history, RunRecord{
		RunID:     runID,
		ScriptID:  scriptID,
		Status:    recordStatus,
		StartedAt: startedAt,
		EndedAt:   endedAt,
	})
	m.mu.Unlock()

	// Update the persistent run record in SQLite.
	if m.db != nil {
		if _, dbErr := m.db.Exec(
			"UPDATE runs SET status = ?, finished_at = ?, exit_code = ?, error_message = ? WHERE id = ?",
			string(recordStatus), endedAt, exitCode, errorMessage, runID,
		); dbErr != nil {
			m.reservoir.Report(notify.Event{
				Severity:    notify.SeverityWarn,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Run record update failed",
				Message:     dbErr.Error(),
				RunID:       runID,
				Err:         dbErr,
			})
		}
	}

	m.reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     fmt.Sprintf("run finished: status=%s exitCode=%d", recordStatus, exitCode),
		RunID:       runID,
		ScriptID:    scriptID,
	})
}

// CancelRun cancels a running script. Returns ErrRunNotActive if the run
// is not in activeRuns (already terminal, or never registered) — that
// sentinel is filtered by CancelGroup so partial group cancels don't
// produce spurious "did not cancel" toasts for siblings that already
// completed or errored organically.
func (m *Manager) CancelRun(runID string) error {
	m.mu.Lock()
	state, ok := m.activeRuns[runID]
	if !ok {
		m.mu.Unlock()
		return ErrRunNotActive
	}
	state.cancelRequested = true
	m.mu.Unlock()

	// Try to send cancel via gRPC first. If it fails, kill immediately.
	// If it succeeds, give the script a grace window to exit cleanly.
	if err := m.grpc.SendCancel(runID); err != nil {
		// Cancel-path failures are deep-trace; log-only via Info.
		m.reservoir.Report(notify.Event{
			Severity:    notify.SeverityInfo,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Message:     fmt.Sprintf("gRPC cancel failed, killing process immediately: %s", err.Error()),
			RunID:       runID,
		})
		state.cancel()
	} else {
		// Grace window: let the script notice is_cancelled() and exit cleanly.
		// If it doesn't exit in time, force-kill. cancel() is idempotent — if
		// the process already exited via waitForExit, this is a no-op.
		time.AfterFunc(cancelGracePeriod, state.cancel)
	}
	m.reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     "run cancel requested",
		RunID:       runID,
	})
	return nil
}

