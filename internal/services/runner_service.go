package services

import (
	"fmt"
	"log/slog"

	"go-python-runner/internal/registry"
	"go-python-runner/internal/runner"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// RunnerService is a Wails service that manages script execution.
type RunnerService struct {
	manager  *runner.Manager
	registry *registry.Registry
	logger   *slog.Logger
	app      *application.App
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
	s.app = app
}

// StartRun starts a script and returns the run ID.
func (s *RunnerService) StartRun(scriptID string, params map[string]string) (string, error) {
	script, ok := s.registry.Get(scriptID)
	if !ok {
		return "", fmt.Errorf("script not found: %s", scriptID)
	}

	runID, msgCh, err := s.manager.StartRun(scriptID, params, script.Dir)
	if err != nil {
		return "", err
	}

	// Goroutine: read messages from the channel and emit Wails events
	go s.forwardMessages(runID, scriptID, msgCh)

	return runID, nil
}

// CancelRun cancels a running script.
func (s *RunnerService) CancelRun(runID string) error {
	return s.manager.CancelRun(runID)
}

// GetRunHistory returns all completed runs.
func (s *RunnerService) GetRunHistory() []runner.RunRecord {
	return s.manager.GetRunHistory()
}

func (s *RunnerService) forwardMessages(runID, scriptID string, ch <-chan runner.Message) {
	for msg := range ch {
		if s.app == nil {
			continue
		}
		switch m := msg.(type) {
		case runner.OutputMsg:
			s.app.Event.Emit("run:output", map[string]string{
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
			s.app.Event.Emit("run:progress", map[string]any{
				"runID":    runID,
				"scriptID": scriptID,
				"current":  m.Current,
				"total":    m.Total,
				"label":    m.Label,
			})
		case runner.StatusMsg:
			s.app.Event.Emit("run:status", map[string]string{
				"runID":    runID,
				"scriptID": scriptID,
				"state":    m.State,
			})
		case runner.ErrorMsg:
			s.app.Event.Emit("run:error", map[string]string{
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
		}
	}
}
