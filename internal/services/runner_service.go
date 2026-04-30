package services

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"go-python-runner/internal/notify"
	"go-python-runner/internal/registry"
	"go-python-runner/internal/runner"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type ParallelRunsResult struct {
	GroupID string   `json:"groupID"`
	RunIDs  []string `json:"runIDs"`
}

type runGroup struct {
	GroupID  string
	ScriptID string
	RunIDs   []string
}

type RunnerService struct {
	manager   *runner.Manager
	registry  *registry.Registry
	reservoir notify.Reservoir
	app       atomic.Pointer[application.App]

	// Group is a batch-level construct owned here so Manager stays group-unaware.
	groupsMu  sync.Mutex
	groups    map[string]*runGroup
	runGroups map[string]string
}

func NewRunnerService(mgr *runner.Manager, reg *registry.Registry, reservoir notify.Reservoir) *RunnerService {
	return &RunnerService{
		manager:   mgr,
		registry:  reg,
		reservoir: reservoir,
		groups:    make(map[string]*runGroup),
		runGroups: make(map[string]string),
	}
}

func (s *RunnerService) SetApp(app *application.App) {
	s.app.Store(app)
}

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

	go s.forwardMessages(runID, scriptID, "", msgCh)

	return runID, nil
}

// StartParallelRuns spawns workerCount instances of a parallel-capable script.
// Each worker gets a unique VaryParam name; ChainParam wires worker[i] to read
// from worker[i-1].
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

	s.registerGroup(groupID, scriptID, runIDs)

	return ParallelRunsResult{GroupID: groupID, RunIDs: runIDs}, nil
}

func (s *RunnerService) CancelRun(runID string) error {
	return s.manager.CancelRun(runID)
}

// CancelGroup cancels every run in a parallel group. Per-worker cancel failures
// are individually reported and joined into the returned error.
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
		err := s.manager.CancelRun(id)
		if err == nil {
			continue
		}
		// Sibling already terminal — nothing to cancel, not a failure.
		if errors.Is(err, runner.ErrRunNotActive) {
			continue
		}
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
	return errors.Join(errs...)
}

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

// clearRunFromGroup drops runID from its group; deletes the group once every
// member has terminated.
func (s *RunnerService) clearRunFromGroup(runID string) {
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	groupID, ok := s.runGroups[runID]
	if !ok {
		return
	}
	delete(s.runGroups, runID)

	for _, gid := range s.runGroups {
		if gid == groupID {
			return
		}
	}
	delete(s.groups, groupID)
}

func (s *RunnerService) emit(event string, payload any) {
	if app := s.app.Load(); app != nil {
		app.Event.Emit(event, payload)
	}
}

func (s *RunnerService) forwardMessages(runID, scriptID, groupID string, ch <-chan runner.Message) {
	// Python's runner.fail() always sends ErrorMsg before StatusMsg(failed) on
	// the same stream, so the StatusFailed branch can surface the real error
	// text by reading the captured lastErrMessage/Traceback below.
	var lastErrMessage, lastErrTraceback string
	for msg := range ch {
		switch m := msg.(type) {
		case runner.OutputMsg:
			s.emit("run:output", map[string]string{
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
			s.emit("run:progress", map[string]any{
				"runID":    runID,
				"scriptID": scriptID,
				"groupID":  groupID,
				"current":  m.Current,
				"total":    m.Total,
				"label":    m.Label,
			})
		case runner.StatusMsg:
			s.emit("run:status", map[string]string{
				"runID":    runID,
				"scriptID": scriptID,
				"groupID":  groupID,
				"state":    string(m.State),
			})
			// Toast originates here (not Manager) because scriptID lives at
			// this layer. Cancel is not a failure surface and skips the toast.
			if m.State == runner.StatusFailed {
				body := lastErrMessage
				if body == "" {
					body = "Script reported failed status without an error message — see Logs for details."
				}
				s.reservoir.Report(notify.Event{
					Severity:    notify.SeverityError,
					Persistence: notify.PersistenceOneShot,
					Source:      notify.SourceBackend,
					Title:       "Run failed",
					Message:     body,
					Traceback:   lastErrTraceback,
					RunID:       runID,
					ScriptID:    scriptID,
				})
			}
			if m.State.IsTerminal() {
				s.clearRunFromGroup(runID)
			}
		case runner.ErrorMsg:
			// Cancelled runs surface fail("Cancelled by user") as an ErrorMsg;
			// demote to log-only so cancel doesn't open an error pane.
			// WasCancelled is per-runID, so sibling errors still route normally.
			sev := severityFromProto(m.Severity)
			persist := notify.PersistenceInFlight
			if s.manager.WasCancelled(runID) {
				sev = notify.SeverityInfo
				persist = notify.PersistenceOneShot
			}
			s.reservoir.Report(notify.Event{
				Severity:    sev,
				Persistence: persist,
				Source:      notify.SourcePython,
				Message:     m.Message,
				RunID:       runID,
				ScriptID:    scriptID,
				Traceback:   m.Traceback,
			})
			lastErrMessage = m.Message
			lastErrTraceback = m.Traceback
		case runner.DataMsg:
			s.emit("run:data", map[string]any{
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

// severityFromProto maps the proto Severity enum carried on ErrorMsg.
// UNSPECIFIED (0) defaults to Error.
func severityFromProto(p int32) notify.Severity {
	switch p {
	case 1: // SEVERITY_INFO
		return notify.SeverityInfo
	case 2: // SEVERITY_WARN
		return notify.SeverityWarn
	case 4: // SEVERITY_CRITICAL
		return notify.SeverityCritical
	default:
		return notify.SeverityError
	}
}
