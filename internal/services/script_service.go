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

// pathOpener is the OS-level open hook. Tests override this to avoid actually
// invoking the user's default editor / file manager.
type pathOpener func(string) error

// ScriptService is a Wails service that exposes script information to the frontend.
type ScriptService struct {
	registry    *registry.Registry
	reservoir   notify.Reservoir
	app         atomic.Pointer[application.App]
	allowedRoot []string   // absolute, normalized roots that OpenPath will permit
	openHook    pathOpener // injectable opener; defaults to app.Browser.OpenFile once SetApp wires the app
}

// NewScriptService creates a new ScriptService. allowedRoots are absolute paths
// that bound which paths OpenPath will open — defense-in-depth against a
// compromised frontend requesting arbitrary filesystem paths.
//
// reservoir is the sole observability dependency; every user-visible error
// from this service flows through reservoir.Report.
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

// SetApp wires the Wails app reference for emitting scripts:changed events.
// Called after app initialization, alongside RunnerService/LogService SetApp.
// Also sets the default OpenPath hook to app.Browser.OpenFile if a test
// hasn't already overridden it.
func (s *ScriptService) SetApp(app *application.App) {
	s.app.Store(app)
	if s.openHook == nil {
		s.openHook = app.Browser.OpenFile
	}
}

// NotifyChanged emits the scripts:changed Wails event so the frontend re-fetches
// the catalog. Called by the registry watcher when a Reload yielded changed=true.
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

// ListScripts returns all registered scripts in deterministic order.
func (s *ScriptService) ListScripts() []registry.Script {
	return s.registry.List()
}

// ListIssues returns the current set of script load failures so the frontend
// can render a persistent banner. Read-only; can't fail.
func (s *ScriptService) ListIssues() []registry.LoadIssue {
	return s.registry.Issues()
}

// OpenPath opens a script file or directory in the OS default handler
// (editor for files, file manager for directories). Restricted to paths
// under the configured allowed roots.
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

// isAllowedPath reports whether clean is within any of the allowed roots.
// Both inputs are expected to be absolute and filepath.Clean'd.
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
