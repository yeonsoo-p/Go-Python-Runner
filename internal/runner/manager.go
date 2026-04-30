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

const maxStderrLog = 4096

type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

func (s RunStatus) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

type RunState struct {
	RunID    string
	ScriptID string
	Status   RunStatus
	process  *Process
	messages <-chan Message
	cancel   func()
	// cancelRequested is the single structural fact that distinguishes
	// user-driven cancellation from genuine failure; deriveFinalStatus reads
	// it to force StatusCancelled regardless of exit code or Python's signal.
	cancelRequested bool
}

type RunRecord struct {
	RunID     string
	ScriptID  string
	Status    RunStatus
	StartedAt time.Time
	EndedAt   time.Time
}

type Manager struct {
	mu         sync.RWMutex
	activeRuns map[string]*RunState
	history    []RunRecord
	grpc       *GRPCServer
	cache      *CacheManager
	db         *db.DB
	reservoir  notify.Reservoir
	PythonPath string
	LibDir     string
}

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

// StartRun spawns a Python script and returns the runID plus the channel
// that carries typed messages from the script.
func (m *Manager) StartRun(scriptID string, params map[string]string, scriptDir string) (string, <-chan Message, error) {
	runID := uuid.New().String()
	startedAt := time.Now()

	msgCh := m.grpc.RegisterRun(runID)

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

	if m.db != nil {
		paramsJSON, _ := json.Marshal(params) //nolint:errcheck // map[string]string never fails to marshal
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

const connectTimeout = 30 * time.Second
const cancelGracePeriod = 3 * time.Second

// waitAndSendStart waits for Python to connect, then sends StartRequest.
// Bails on proc.Done so a cancelled-before-connect run doesn't leak this
// goroutine for the full connectTimeout.
func (m *Manager) waitAndSendStart(runID string, params map[string]string, proc *Process) {
	select {
	case <-m.grpc.WaitConnected(runID):
	case <-proc.Done():
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

// deriveFinalStatus is the authoritative terminal-status decision. The trust
// order is documented at the call site in waitForExit.
func (m *Manager) deriveFinalStatus(runID string, exitCode int, waitErr error) RunStatus {
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
	// Exit 0 with no signal — script crashed before reaching complete()/fail().
	return StatusFailed
}

func (m *Manager) WasCancelled(runID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.activeRuns[runID]; ok {
		return s.cancelRequested
	}
	return false
}

// RegisterActiveRunForTest installs a minimal RunState without spawning a
// subprocess. Test-only.
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

// GRPCServer is a test-only accessor.
func (m *Manager) GRPCServer() *GRPCServer { return m.grpc }

// History returns a snapshot of completed-run records, in insertion order.
func (m *Manager) History() []RunRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RunRecord, len(m.history))
	copy(out, m.history)
	return out
}

// waitForExit reaps the subprocess and emits the single terminal run:status.
// Manager is the sole emitter; Python's StatusMsg is folded into atomic flags
// by the gRPC handler and never reaches the frontend on its own.
//
// Trust order for deriveFinalStatus:
//
//	0. cancelRequested        → Cancelled (overrides everything)
//	1. process exited non-zero → Failed
//	2. Python sent ErrorMsg    → Failed
//	3. Python sent Status(failed) without ErrorMsg → Failed
//	4. Python sent Status(completed) → Completed
//	5. otherwise               → Failed (script exited 0 without signaling)
func (m *Manager) waitForExit(runID, scriptID string, startedAt time.Time, proc *Process) {
	exitCode, stderrOutput, err := proc.Wait()
	endedAt := time.Now()

	// Drain remaining Python messages before synthesizing the final status.
	m.grpc.WaitStreamDone(runID, 5*time.Second)

	finalStatus := m.deriveFinalStatus(runID, exitCode, err)

	if finalStatus == StatusFailed && exitCode != 0 && stderrOutput != "" && !m.grpc.GotError(runID) {
		// Crash without a structured error — route stderr through the
		// reservoir as InFlight so it appears in the per-run pane like a
		// Python-originated fail() would.
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

	m.grpc.TrySendStatus(runID, finalStatus)

	// Capture before UnregisterRun clears the RunChannel.
	// Priority: stderr > gRPC ErrorMsg > process error.
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

// CancelAll cancels every active run. Snapshots ids outside the lock because
// CancelRun reacquires it per call.
func (m *Manager) CancelAll() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.activeRuns))
	for id := range m.activeRuns {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		_ = m.CancelRun(id) //nolint:errcheck // race with organic completion is fine
	}
}

// CancelRun cancels a running script. Returns ErrRunNotActive when the run
// is no longer in activeRuns; CancelGroup filters that sentinel so siblings
// that completed organically don't produce spurious "did not cancel" toasts.
func (m *Manager) CancelRun(runID string) error {
	m.mu.Lock()
	state, ok := m.activeRuns[runID]
	if !ok {
		m.mu.Unlock()
		return ErrRunNotActive
	}
	state.cancelRequested = true
	m.mu.Unlock()

	// Try cooperative cancel first; on gRPC failure kill immediately.
	if err := m.grpc.SendCancel(runID); err != nil {
		m.reservoir.Report(notify.Event{
			Severity:    notify.SeverityInfo,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Message:     fmt.Sprintf("gRPC cancel failed, killing process immediately: %s", err.Error()),
			RunID:       runID,
		})
		state.cancel()
	} else {
		// Grace window for is_cancelled() to exit cleanly; force-kill after.
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

