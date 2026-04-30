package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"go-python-runner/internal/notify"
	"go-python-runner/internal/registry"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type pathOpener func(string) error

type ScriptService struct {
	registry    *registry.Registry
	reservoir   notify.Reservoir
	app         atomic.Pointer[application.App]
	allowedRoot []string   // absolute, normalized; bounds OpenPath
	openHook    pathOpener // tests inject; SetApp installs app.Browser.OpenFile
}

// NewScriptService creates a ScriptService. allowedRoots bound which paths
// OpenPath will open — defense-in-depth against a compromised frontend.
func NewScriptService(reg *registry.Registry, reservoir notify.Reservoir, allowedRoots ...string) *ScriptService {
	roots := make([]string, 0, len(allowedRoots))
	for _, r := range allowedRoots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			abs = r
		}
		roots = append(roots, filepath.Clean(abs))
	}
	return &ScriptService{
		registry:    reg,
		reservoir:   reservoir,
		allowedRoot: roots,
	}
}

func (s *ScriptService) SetApp(app *application.App) {
	s.app.Store(app)
	if s.openHook == nil {
		s.openHook = app.Browser.OpenFile
	}
}

// NotifyChanged emits scripts:changed so the frontend re-fetches the catalog.
func (s *ScriptService) NotifyChanged() {
	if app := s.app.Load(); app != nil {
		app.Event.Emit("scripts:changed", nil)
	}
	s.reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     "scripts:changed emitted",
	})
}

func (s *ScriptService) ListScripts() []registry.Script {
	return s.registry.List()
}

func (s *ScriptService) ListIssues() []registry.LoadIssue {
	return s.registry.Issues()
}

// OpenPath opens absPath in the OS default handler. Restricted to paths under
// the configured allowed roots.
func (s *ScriptService) OpenPath(absPath string) error {
	clean := filepath.Clean(absPath)
	if !s.isAllowedPath(clean) {
		err := fmt.Errorf("path %q is outside allowed roots", absPath)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Open path rejected",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}
	if _, err := os.Stat(clean); err != nil {
		wrapped := fmt.Errorf("path does not exist: %w", err)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Open path failed",
			Message:     wrapped.Error(),
			Err:         wrapped,
		})
		return wrapped
	}
	if err := s.openHook(clean); err != nil {
		wrapped := fmt.Errorf("opening %s: %w", clean, err)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Open path failed",
			Message:     wrapped.Error(),
			Err:         wrapped,
		})
		return wrapped
	}
	s.reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     fmt.Sprintf("OpenPath opened %s", clean),
	})
	return nil
}

func (s *ScriptService) isAllowedPath(clean string) bool {
	if len(s.allowedRoot) == 0 {
		return false
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	for _, root := range s.allowedRoot {
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
