package services

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"go-python-runner/internal/registry"
	"go-python-runner/internal/runner"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// RunnerService is a Wails service that manages script execution.
type RunnerService struct {
	manager  *runner.Manager
	registry *registry.Registry
	logger   *slog.Logger
	app      atomic.Pointer[application.App] // set after Wails init, read from goroutines
}

// NewRunnerService creates a new RunnerService.
func NewRunnerService(mgr *runner.Manager, reg *registry.Registry, logger *slog.Logger) *RunnerService {
	return &RunnerService{
		manager:  mgr,
		registry: reg,
		logger:   logger,
	}
}

// SetApp sets the Wails app reference for emitting events.
// Called after app initialization.
func (s *RunnerService) SetApp(app *application.App) {
	s.app.Store(app)
}

// StartRun starts a script and returns the run ID.
func (s *RunnerService) StartRun(scriptID string, params map[string]string) (string, error) {
	script, ok := s.registry.Get(scriptID)
	if !ok {
		return "", fmt.Errorf("script not found: %s", scriptID)
	}

	runID, msgCh, err := s.manager.StartRun(scriptID, params, script.Dir)
	if err != nil {
		return "", fmt.Errorf("starting run for %s: %w", scriptID, err)
	}

	// Goroutine: read messages from the channel and emit Wails events
	go s.forwardMessages(runID, scriptID, msgCh)

	return runID, nil
}

// StartParallelRuns starts multiple instances of a parallel-capable script.
// Each instance gets a unique name via the script's VaryParam and is chained
// via ChainParam so that worker[i] reads from worker[i-1].
func (s *RunnerService) StartParallelRuns(scriptID string, params map[string]string, workerCount int) ([]string, error) {
	script, ok := s.registry.Get(scriptID)
	if !ok {
		return nil, fmt.Errorf("script not found: %s", scriptID)
	}
	if script.Parallel == nil {
		return nil, fmt.Errorf("script %s does not support parallel execution", scriptID)
	}

	pc := script.Parallel
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > pc.MaxWorkers {
		workerCount = pc.MaxWorkers
	}

	// One canonical name resolver. Used both for the worker's own name and for
	// the chain link to its predecessor, so the Names[]/Worker-N fallback is
	// expressed exactly once.
	nameForIndex := func(idx int) string {
		if idx < len(pc.Names) {
			return pc.Names[idx]
		}
		return fmt.Sprintf("Worker-%d", idx+1)
	}

	var runIDs []string
	for i := 0; i < workerCount; i++ {
		// Clone params so each worker gets its own copy.
		wp := make(map[string]string, len(params)+2)
		for k, v := range params {
			wp[k] = v
		}

		wp[pc.VaryParam] = nameForIndex(i)

		// Auto-chain: worker[i] reads from worker[i-1]. Worker 0 gets empty.
		if pc.ChainParam != "" {
			if i > 0 {
				wp[pc.ChainParam] = nameForIndex(i - 1)
			} else {
				wp[pc.ChainParam] = ""
			}
		}

		runID, msgCh, err := s.manager.StartRun(scriptID, wp, script.Dir)
		if err != nil {
			// Cancel already-started runs on failure.
			for _, id := range runIDs {
				if cancelErr := s.manager.CancelRun(id); cancelErr != nil {
					s.logger.Warn("rollback cancel failed",
						"runID", id,
						"error", cancelErr.Error(),
						"source", "backend",
					)
				}
			}
			return nil, fmt.Errorf("starting worker %d for %s: %w", i, scriptID, err)
		}

		go s.forwardMessages(runID, scriptID, msgCh)
		runIDs = append(runIDs, runID)
	}

	return runIDs, nil
}

// CancelRun cancels a running script.
func (s *RunnerService) CancelRun(runID string) error {
	return s.manager.CancelRun(runID)
}

func (s *RunnerService) forwardMessages(runID, scriptID string, ch <-chan runner.Message) {
	for msg := range ch {
		app := s.app.Load()
		if app == nil {
			continue
		}
		switch m := msg.(type) {
		case runner.OutputMsg:
			app.Event.Emit("run:output", map[string]string{
				"runID":    runID,
				"scriptID": scriptID,
				"text":     m.Text,
			})
			s.logger.Info("script output",
				"text", m.Text,
				"source", "python",
				"runID", runID,
				"scriptID", scriptID,
			)
		case runner.ProgressMsg:
			app.Event.Emit("run:progress", map[string]any{
				"runID":    runID,
				"scriptID": scriptID,
				"current":  m.Current,
				"total":    m.Total,
				"label":    m.Label,
			})
		case runner.StatusMsg:
			app.Event.Emit("run:status", map[string]string{
				"runID":    runID,
				"scriptID": scriptID,
				"state":    string(m.State),
			})
		case runner.ErrorMsg:
			app.Event.Emit("run:error", map[string]string{
				"runID":     runID,
				"scriptID":  scriptID,
				"message":   m.Message,
				"traceback": m.Traceback,
			})
			s.logger.Error(m.Message,
				"source", "python",
				"runID", runID,
				"scriptID", scriptID,
				"traceback", m.Traceback,
			)
		case runner.DataMsg:
			app.Event.Emit("run:data", map[string]any{
				"runID":    runID,
				"scriptID": scriptID,
				"key":      m.Key,
				"value":    m.Value,
			})
			s.logger.Info("script data result",
				"key", m.Key,
				"source", "python",
				"runID", runID,
				"scriptID", scriptID,
			)
		}
	}
}
