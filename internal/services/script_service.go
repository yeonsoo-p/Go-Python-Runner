package services

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"go-python-runner/internal/registry"

	"github.com/pkg/browser"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// pathOpener is the OS-level open hook. Tests override this to avoid actually
// invoking the user's default editor / file manager.
type pathOpener func(string) error

// ScriptService is a Wails service that exposes script information to the frontend.
type ScriptService struct {
	registry    *registry.Registry
	logger      *slog.Logger
	app         atomic.Pointer[application.App]
	allowedRoot []string  // absolute, normalized roots that OpenPath will permit
	openHook    pathOpener // injectable opener; defaults to browser.OpenFile
}

// NewScriptService creates a new ScriptService. allowedRoots are absolute paths
// that bound which paths OpenPath will open — defense-in-depth against a
// compromised frontend requesting arbitrary filesystem paths.
func NewScriptService(reg *registry.Registry, logger *slog.Logger, allowedRoots ...string) *ScriptService {
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
		logger:      logger,
		allowedRoot: roots,
		openHook:    browser.OpenFile,
	}
}

// SetApp wires the Wails app reference for emitting scripts:changed events.
// Called after app initialization, alongside RunnerService/LogService SetApp.
func (s *ScriptService) SetApp(app *application.App) {
	s.app.Store(app)
}

// NotifyChanged emits the scripts:changed Wails event so the frontend re-fetches
// the catalog. Called by the registry watcher when a Reload yielded changed=true.
func (s *ScriptService) NotifyChanged() {
	if app := s.app.Load(); app != nil {
		app.Event.Emit("scripts:changed", nil)
	}
	s.logger.Debug("scripts:changed emitted", "source", "backend")
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
		s.logger.Error("OpenPath rejected",
			"path", absPath,
			"error", err.Error(),
			"source", "backend",
		)
		return err
	}
	if _, err := os.Stat(clean); err != nil {
		s.logger.Error("OpenPath stat failed",
			"path", clean,
			"error", err.Error(),
			"source", "backend",
		)
		return fmt.Errorf("path does not exist: %w", err)
	}
	if err := s.openHook(clean); err != nil {
		s.logger.Error("OpenPath opener failed",
			"path", clean,
			"error", err.Error(),
			"source", "backend",
		)
		return fmt.Errorf("opening %s: %w", clean, err)
	}
	s.logger.Info("OpenPath opened", "path", clean, "source", "backend")
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
