package runner

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RunState tracks the state of a single script execution.
type RunState struct {
	RunID    string
	ScriptID string
	Status   string // "running", "completed", "failed"
	process  *Process
	messages <-chan Message
	cancel   func()
}

// RunRecord is a completed run entry for history.
type RunRecord struct {
	RunID     string
	ScriptID  string
	Status    string
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
		Status:   "running",
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

	// Send start params to Python once it connects
	go func() {
		// Wait for Python to connect
		select {
		case <-m.grpc.WaitConnected(runID):
		case <-time.After(30 * time.Second):
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
	}()

	// Goroutine: wait for process exit, then cleanup
	go func() {
		exitCode, stderrOutput, err := proc.Wait()
		endedAt := time.Now()

		finalStatus := "completed"
		if err != nil || exitCode != 0 {
			finalStatus = "failed"
			if stderrOutput != "" {
				const maxStderr = 4096
				logStderr := stderrOutput
				if len(logStderr) > maxStderr {
					logStderr = logStderr[:maxStderr] + "\n... (truncated)"
				}
				m.logger.Error("script stderr",
					"runID", runID,
					"scriptID", scriptID,
					"stderr", logStderr,
					"source", "python",
				)
			}
		}

		// Clean up
		m.cache.CleanupRun(runID)
		m.grpc.UnregisterRun(runID)

		m.mu.Lock()
		if s, ok := m.activeRuns[runID]; ok {
			s.Status = finalStatus
		}
		delete(m.activeRuns, runID)
		m.history = append(m.history, RunRecord{
			RunID:     runID,
			ScriptID:  scriptID,
			Status:    finalStatus,
			StartedAt: startedAt,
			EndedAt:   endedAt,
		})
		m.mu.Unlock()

		m.logger.Info("run finished",
			"runID", runID,
			"scriptID", scriptID,
			"status", finalStatus,
			"exitCode", exitCode,
			"source", "backend",
		)
	}()

	return runID, msgCh, nil
}

// CancelRun cancels a running script.
func (m *Manager) CancelRun(runID string) error {
	m.mu.RLock()
	state, ok := m.activeRuns[runID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run %s not found", runID)
	}

	// Try to send cancel via gRPC first (best-effort; process kill follows)
	if err := m.grpc.SendCancel(runID); err != nil {
		m.logger.Debug("gRPC cancel failed, falling back to process kill", "runID", runID, "error", err.Error(), "source", "backend")
	}

	// Then kill the process
	state.cancel()
	m.logger.Info("run cancelled", "runID", runID, "source", "backend")
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
