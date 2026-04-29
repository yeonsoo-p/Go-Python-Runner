package services

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"

	"go-python-runner/internal/notify"
	"go-python-runner/internal/registry"
	"go-python-runner/internal/runner"
)

// ParallelRunsResult is the typed return of StartParallelRuns. The GroupID
// identifies the batch on the frontend; RunIDs are the individual workers
// in the order they were spawned.
type ParallelRunsResult struct {
	GroupID string   `json:"groupID"`
	RunIDs  []string `json:"runIDs"`
}

// runGroup tracks the membership of a parallel-run batch. The frontend uses
// the GroupID to render an aggregate progress bar over the children.
type runGroup struct {
	GroupID  string
	ScriptID string
	RunIDs   []string // immutable after Register
}

// RunnerService is a Wails service that manages script execution.
type RunnerService struct {
	manager   *runner.Manager
	registry  *registry.Registry
	reservoir notify.Reservoir
	app       atomic.Pointer[application.App] // set after Wails init, read from goroutines

	// Group registry for parallel runs. Manager is run-level; group is a
	// batch-level construct owned here so Manager stays group-unaware.
	groupsMu  sync.Mutex
	groups    map[string]*runGroup // groupID -> group
	runGroups map[string]string    // runID  -> groupID (reverse lookup)
}

// NewRunnerService creates a new RunnerService. The reservoir is the sole
// observability dependency — every user-visible error and trace event flows
// through reservoir.Report, including script output and data-result traces
// (which route to log-only via Severity=Info + Persistence=OneShot).
func NewRunnerService(mgr *runner.Manager, reg *registry.Registry, reservoir notify.Reservoir) *RunnerService {
	return &RunnerService{
		manager:   mgr,
		registry:  reg,
		reservoir: reservoir,
		groups:    make(map[string]*runGroup),
		runGroups: make(map[string]string),
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
		err := fmt.Errorf("script not found: %s", scriptID)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Run not started",
			Message:     err.Error(),
			ScriptID:    scriptID,
			Err:         err,
		})
		return "", err
	}

	runID, msgCh, err := s.manager.StartRun(scriptID, params, script.Dir)
	if err != nil {
		wrapped := fmt.Errorf("starting run for %s: %w", scriptID, err)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Run not started",
			Message:     wrapped.Error(),
			ScriptID:    scriptID,
			Err:         wrapped,
		})
		return "", wrapped
	}

	// Goroutine: read messages from the channel and emit Wails events.
	// Empty groupID => the run is not part of a parallel group.
	go s.forwardMessages(runID, scriptID, "", msgCh)

	return runID, nil
}

// StartParallelRuns starts multiple instances of a parallel-capable script.
// Each instance gets a unique name via the script's VaryParam and is chained
// via ChainParam so that worker[i] reads from worker[i-1]. The returned
// GroupID identifies the batch on the frontend so it can render an aggregate
// progress bar over the workers.
func (s *RunnerService) StartParallelRuns(scriptID string, params map[string]string, workerCount int) (ParallelRunsResult, error) {
	script, ok := s.registry.Get(scriptID)
	if !ok {
		err := fmt.Errorf("script not found: %s", scriptID)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Parallel run not started",
			Message:     err.Error(),
			ScriptID:    scriptID,
			Err:         err,
		})
		return ParallelRunsResult{}, err
	}
	if script.Parallel == nil {
		err := fmt.Errorf("script %s does not support parallel execution", scriptID)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Parallel run not started",
			Message:     err.Error(),
			ScriptID:    scriptID,
			Err:         err,
		})
		return ParallelRunsResult{}, err
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

	groupID := uuid.NewString()

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
			// Cancel already-started runs on failure. Each rollback failure is
			// surfaced individually (toast) AND folded into the joined error
			// returned to the frontend — no silent slog.Warn that the user
			// can't see. See CLAUDE.md § Cascading failures.
			primary := fmt.Errorf("starting worker %d for %s: %w", i, scriptID, err)
			s.reservoir.Report(notify.Event{
				Severity:    notify.SeverityError,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Parallel run failed",
				Message:     primary.Error(),
				ScriptID:    scriptID,
				Err:         primary,
			})

			var cleanup []error
			for _, id := range runIDs {
				if cancelErr := s.manager.CancelRun(id); cancelErr != nil {
					rollbackErr := fmt.Errorf("rollback %s: %w", id, cancelErr)
					s.reservoir.Report(notify.Event{
						Severity:    notify.SeverityError,
						Persistence: notify.PersistenceOneShot,
						Source:      notify.SourceBackend,
						Title:       "Parallel rollback incomplete",
						Message:     rollbackErr.Error(),
						RunID:       id,
						ScriptID:    scriptID,
						Err:         rollbackErr,
					})
					cleanup = append(cleanup, rollbackErr)
				}
			}
			return ParallelRunsResult{}, errors.Join(append([]error{primary}, cleanup...)...)
		}

		go s.forwardMessages(runID, scriptID, groupID, msgCh)
		runIDs = append(runIDs, runID)
	}

	// Commit group registration only after every worker spawned successfully.
	// On partial failure we already rolled back via CancelRun above.
	s.registerGroup(groupID, scriptID, runIDs)

	return ParallelRunsResult{GroupID: groupID, RunIDs: runIDs}, nil
}

// CancelRun cancels a running script.
func (s *RunnerService) CancelRun(runID string) error {
	return s.manager.CancelRun(runID)
}

// CancelGroup cancels every run in a parallel group. Per-worker cancel
// failures are individually reported (toast) AND folded into the joined error
// returned to the binding — same cascading-failure pattern as StartParallelRuns.
func (s *RunnerService) CancelGroup(groupID string) error {
	s.groupsMu.Lock()
	g, ok := s.groups[groupID]
	s.groupsMu.Unlock()
	if !ok {
		err := fmt.Errorf("group not found: %s", groupID)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Cancel all failed",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}

	var errs []error
	for _, id := range g.RunIDs {
		if err := s.manager.CancelRun(id); err != nil {
			wrapped := fmt.Errorf("cancel %s: %w", id, err)
			s.reservoir.Report(notify.Event{
				Severity:    notify.SeverityError,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Cancel all: worker did not cancel",
				Message:     wrapped.Error(),
				RunID:       id,
				ScriptID:    g.ScriptID,
				Err:         wrapped,
			})
			errs = append(errs, wrapped)
		}
	}
	return errors.Join(errs...)
}

// registerGroup records a successfully-spawned parallel batch. Caller must
// hold no lock; this method takes groupsMu internally.
func (s *RunnerService) registerGroup(groupID, scriptID string, runIDs []string) {
	g := &runGroup{
		GroupID:  groupID,
		ScriptID: scriptID,
		RunIDs:   append([]string(nil), runIDs...),
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	s.groups[groupID] = g
	for _, id := range runIDs {
		s.runGroups[id] = groupID
	}
}

// clearRunFromGroup removes a single runID from its group's reverse-lookup
// map and deletes the group entry once every member has reached a terminal
// state. Called from forwardMessages on terminal run:status.
func (s *RunnerService) clearRunFromGroup(runID string) {
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	groupID, ok := s.runGroups[runID]
	if !ok {
		return
	}
	delete(s.runGroups, runID)

	// Once no live runID still maps to this group, delete the group entry.
	for _, gid := range s.runGroups {
		if gid == groupID {
			return
		}
	}
	delete(s.groups, groupID)
}

func (s *RunnerService) forwardMessages(runID, scriptID, groupID string, ch <-chan runner.Message) {
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
				"groupID":  groupID,
				"text":     m.Text,
			})
			s.reservoir.Report(notify.Event{
				Severity:    notify.SeverityInfo,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourcePython,
				Message:     m.Text,
				RunID:       runID,
				ScriptID:    scriptID,
			})
		case runner.ProgressMsg:
			app.Event.Emit("run:progress", map[string]any{
				"runID":    runID,
				"scriptID": scriptID,
				"groupID":  groupID,
				"current":  m.Current,
				"total":    m.Total,
				"label":    m.Label,
			})
		case runner.StatusMsg:
			app.Event.Emit("run:status", map[string]string{
				"runID":    runID,
				"scriptID": scriptID,
				"groupID":  groupID,
				"state":    string(m.State),
			})
			// Mid-run terminal toast: a collapsed task card no longer fails
			// silently. Manager is the authoritative source of terminal state
			// (see manager.waitForExit), but we only learn the scriptID here,
			// so the toast originates at this layer.
			if m.State == runner.StatusFailed {
				s.reservoir.Report(notify.Event{
					Severity:    notify.SeverityError,
					Persistence: notify.PersistenceOneShot,
					Source:      notify.SourceBackend,
					Title:       "Run failed",
					Message:     fmt.Sprintf("%s failed", scriptID),
					RunID:       runID,
					ScriptID:    scriptID,
				})
			}
			if m.State.IsTerminal() {
				s.clearRunFromGroup(runID)
			}
		case runner.ErrorMsg:
			// In-flight error: routes to the per-run pane (run:error) and
			// also writes a slog.Error record for the LogViewer / log file.
			s.reservoir.Report(notify.Event{
				Severity:    severityFromProto(m.Severity),
				Persistence: notify.PersistenceInFlight,
				Source:      notify.SourcePython,
				Message:     m.Message,
				RunID:       runID,
				ScriptID:    scriptID,
				Traceback:   m.Traceback,
			})
		case runner.DataMsg:
			app.Event.Emit("run:data", map[string]any{
				"runID":    runID,
				"scriptID": scriptID,
				"groupID":  groupID,
				"key":      m.Key,
				"value":    m.Value,
			})
			s.reservoir.Report(notify.Event{
				Severity:    notify.SeverityInfo,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourcePython,
				Message:     fmt.Sprintf("data result: key=%s", m.Key),
				RunID:       runID,
				ScriptID:    scriptID,
			})
		}
	}
}

// severityFromProto maps the proto Severity enum value carried on ErrorMsg
// to the notify.Severity type. Unspecified defaults to Error for back-compat
// with scripts that predate the severity field.
func severityFromProto(p int32) notify.Severity {
	switch p {
	case 1: // SEVERITY_INFO
		return notify.SeverityInfo
	case 2: // SEVERITY_WARN
		return notify.SeverityWarn
	case 4: // SEVERITY_CRITICAL
		return notify.SeverityCritical
	default: // 0 (UNSPECIFIED) and 3 (ERROR)
		return notify.SeverityError
	}
}
