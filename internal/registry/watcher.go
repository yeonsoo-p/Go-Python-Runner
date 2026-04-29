package registry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDebounce is the default coalesce window for filesystem events.
// Editors save in bursts (write temp file → rename → cleanup); this window
// lets one logical save produce one Reload call.
const DefaultDebounce = 300 * time.Millisecond

// Watcher monitors the builtin scripts directory and the plugin directory for
// filesystem changes, debounces them, and triggers Registry.Reload. On a
// change-yielding reload it invokes onChange, which is wired in main.go to
// emit the scripts:changed Wails event through ScriptService.
type Watcher struct {
	reg        *Registry
	builtinDir string
	pluginDir  string
	debounce   time.Duration
	onChange   func()
	logger     *slog.Logger
}

// NewWatcher constructs a Watcher. onChange must not be nil; pass a no-op if
// you genuinely don't need to react to changes (the Reload still happens).
func NewWatcher(reg *Registry, builtinDir, pluginDir string, onChange func(), logger *slog.Logger) *Watcher {
	return &Watcher{
		reg:        reg,
		builtinDir: builtinDir,
		pluginDir:  pluginDir,
		debounce:   DefaultDebounce,
		onChange:   onChange,
		logger:     logger,
	}
}

// SetDebounce overrides the default coalesce window. Useful for fast tests.
func (w *Watcher) SetDebounce(d time.Duration) { w.debounce = d }

// Run blocks until ctx is cancelled. Returns the first fatal error from
// fsnotify; per-event errors are logged and the loop continues.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	// Make sure the plugin dir exists so we can watch it. Idempotent.
	if w.pluginDir != "" {
		if mkErr := os.MkdirAll(w.pluginDir, 0o755); mkErr != nil {
			w.logger.Warn("could not create plugin dir for watching",
				"dir", w.pluginDir, "error", mkErr.Error(), "source", "backend")
		}
	}

	if err := w.addRecursive(fsw, w.builtinDir); err != nil {
		w.logger.Warn("could not watch builtin scripts dir",
			"dir", w.builtinDir, "error", err.Error(), "source", "backend")
	}
	if err := w.addRecursive(fsw, w.pluginDir); err != nil {
		w.logger.Warn("could not watch plugin dir",
			"dir", w.pluginDir, "error", err.Error(), "source", "backend")
	}

	var (
		mu      sync.Mutex
		pending *time.Timer
	)

	fire := func() {
		changed, reloadErr := w.reg.Reload(w.builtinDir, w.pluginDir)
		if reloadErr != nil {
			w.logger.Error("registry reload failed",
				"error", reloadErr.Error(), "source", "backend")
			return
		}
		if changed {
			w.logger.Info("registry reloaded",
				"scripts", len(w.reg.List()),
				"issues", len(w.reg.Issues()),
				"source", "backend",
			)
			if w.onChange != nil {
				w.onChange()
			}
		}
	}

	schedule := func() {
		mu.Lock()
		defer mu.Unlock()
		if pending != nil {
			pending.Stop()
		}
		pending = time.AfterFunc(w.debounce, fire)
	}

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			if pending != nil {
				pending.Stop()
			}
			mu.Unlock()
			return nil
		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			// New directory created → start watching its contents too.
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					if addErr := fsw.Add(ev.Name); addErr != nil && !errors.Is(addErr, fsnotify.ErrEventOverflow) {
						w.logger.Debug("could not watch newly created dir",
							"dir", ev.Name, "error", addErr.Error(), "source", "backend")
					}
				}
			}
			schedule()
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.logger.Warn("fsnotify error", "error", err.Error(), "source", "backend")
		}
	}
}

// addRecursive walks dir and adds a watch on every directory found. fsnotify
// v1 doesn't watch recursively, so we walk explicitly. Missing dirs are
// returned as errors so the caller can log them; this is non-fatal.
func (w *Watcher) addRecursive(fsw *fsnotify.Watcher, dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; don't abort the walk
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "_lib" {
			return filepath.SkipDir
		}
		if addErr := fsw.Add(path); addErr != nil {
			w.logger.Debug("could not watch dir",
				"dir", path, "error", addErr.Error(), "source", "backend")
		}
		return nil
	})
}
