package runner

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// maxStderrLog is the maximum number of bytes of stderr output to log.
const maxStderrLog = 4096

// RunStatus represents the lifecycle state of a script run.
type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusFailed    RunStatus = "failed"
)

// IsTerminal returns true if the status represents a final state.
func (s RunStatus) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed
}

// RunState tracks the state of a single script execution.
type RunState struct {
	RunID    string
	ScriptID string
	Status   RunStatus
	process  *Process
	messages <-chan Message
	cancel   func()
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
	logger     *slog.Logger
	PythonPath string // optional override for python path
}

// NewManager creates a new process manager.
func NewManager(grpc *GRPCServer, cache *CacheManager, logger *slog.Logger) *Manager {
	return &Manager{
		activeRuns: make(map[string]*RunState),
		grpc:       grpc,
		cache:      cache,
		logger:     logger,
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
	proc := NewProcess(runID, scriptDir, params, m.grpc.Addr())
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

	m.mu.Lock()
	m.activeRuns[runID] = state
	m.mu.Unlock()

	m.logger.Info("run started",
		"runID", runID,
		"scriptID", scriptID,
		"source", "backend",
	)

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
func (m *Manager) waitAndSendStart(runID string, params map[string]string, proc *Process) {
	select {
	case <-m.grpc.WaitConnected(runID):
	case <-time.After(connectTimeout):
		m.logger.Error("timeout waiting for Python to connect, killing process", "runID", runID, "source", "backend")
		proc.Cancel()
		return
	}
	if err := m.grpc.SendStart(runID, params); err != nil {
		m.logger.Error("failed to send start params, killing process",
			"runID", runID,
			"error", err.Error(),
			"source", "backend",
		)
		proc.Cancel()
	}
}

// waitForExit waits for the process to exit, logs stderr if needed, and records the run.
func (m *Manager) waitForExit(runID, scriptID string, startedAt time.Time, proc *Process) {
	exitCode, stderrOutput, err := proc.Wait()
	endedAt := time.Now()

	// Wait for gRPC stream to fully close so all Python messages
	// (progress, error, status) are delivered before we synthesize a final status.
	m.grpc.WaitStreamDone(runID, 5*time.Second)

	finalStatus := StatusCompleted
	if err != nil || exitCode != 0 {
		finalStatus = StatusFailed
		// Only log stderr if we didn't already get a structured error via gRPC,
		// to avoid duplicate error events for the same failure.
		if stderrOutput != "" && !m.grpc.GotError(runID) {
			logStderr := stderrOutput
			if len(logStderr) > maxStderrLog {
				logStderr = logStderr[:maxStderrLog] + "\n... (truncated)"
			}
			m.grpc.TrySendError(runID, logStderr)
			m.logger.Error("script stderr",
				"runID", runID,
				"scriptID", scriptID,
				"stderr", logStderr,
				"source", "python",
			)
		}
	}

	// Python's fail() sends a gRPC Error message but exits with code 0.
	// Check the gotError flag before UnregisterRun clears the run channel.
	if finalStatus != StatusFailed && m.grpc.GotError(runID) {
		finalStatus = StatusFailed
	}

	// Guarantee frontend receives terminal status even if Python's
	// gRPC StatusMsg was lost (stream error, race, etc.).
	// The channel is buffered and frontend guards ignore duplicates.
	m.grpc.TrySendStatus(runID, finalStatus)

	m.cache.CleanupRun(runID)
	m.grpc.UnregisterRun(runID)

	m.mu.Lock()
	recordStatus := finalStatus
	if s, ok := m.activeRuns[runID]; ok {
		if !s.Status.IsTerminal() {
			s.Status = finalStatus
		} else {
			m.logger.Warn("ignoring status transition from terminal state",
				"runID", runID,
				"current", string(s.Status),
				"attempted", string(finalStatus),
				"source", "backend",
			)
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

	m.logger.Info("run finished",
		"runID", runID,
		"scriptID", scriptID,
		"status", recordStatus,
		"exitCode", exitCode,
		"source", "backend",
	)
}

// CancelRun cancels a running script.
func (m *Manager) CancelRun(runID string) error {
	m.mu.RLock()
	state, ok := m.activeRuns[runID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run %s not found", runID)
	}

	// Try to send cancel via gRPC first. If it fails, kill immediately.
	// If it succeeds, give the script a grace window to exit cleanly.
	if err := m.grpc.SendCancel(runID); err != nil {
		m.logger.Debug("gRPC cancel failed, killing process immediately", "runID", runID, "error", err.Error(), "source", "backend")
		state.cancel()
	} else {
		// Grace window: let the script notice is_cancelled() and exit cleanly.
		// If it doesn't exit in time, force-kill. cancel() is idempotent — if
		// the process already exited via waitForExit, this is a no-op.
		time.AfterFunc(cancelGracePeriod, state.cancel)
	}
	m.logger.Info("run cancel requested", "runID", runID, "source", "backend")
	return nil
}

// GetRunHistory returns all completed runs.
func (m *Manager) GetRunHistory() []RunRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]RunRecord, len(m.history))
	copy(result, m.history)
	return result
}

// ActiveRuns returns the number of currently running scripts.
func (m *Manager) ActiveRuns() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.activeRuns)
}

// CacheBlocks returns a snapshot of all cache blocks (for diagnostics/testing).
func (m *Manager) CacheBlocks() map[string]CacheBlock {
	return m.cache.Blocks()
}

// CacheLookup returns metadata for a cached block (for diagnostics/testing).
func (m *Manager) CacheLookup(key string) (shmName string, size int64, found bool) {
	return m.cache.Lookup(key)
}
