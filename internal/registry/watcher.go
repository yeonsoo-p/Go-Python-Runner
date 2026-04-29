package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go-python-runner/internal/notify"

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
	reservoir  notify.Reservoir
}

// NewWatcher constructs a Watcher. onChange must not be nil; pass a no-op if
// you genuinely don't need to react to changes (the Reload still happens).
// The reservoir is the sole observability dependency — fsnotify failures
// and reload errors flow through reservoir.Report.
func NewWatcher(reg *Registry, builtinDir, pluginDir string, onChange func(), reservoir notify.Reservoir) *Watcher {
	return &Watcher{
		reg:        reg,
		builtinDir: builtinDir,
		pluginDir:  pluginDir,
		debounce:   DefaultDebounce,
		onChange:   onChange,
		reservoir:  reservoir,
	}
}

// SetDebounce overrides the default coalesce window. Useful for fast tests.
func (w *Watcher) SetDebounce(d time.Duration) { w.debounce = d }

// Run blocks until ctx is cancelled. Returns the first fatal error from
// fsnotify; per-event errors are surfaced via reservoir.Report and the loop
// continues.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	// Make sure the plugin dir exists so we can watch it. Idempotent.
	if w.pluginDir != "" {
		if mkErr := os.MkdirAll(w.pluginDir, 0o755); mkErr != nil {
			w.reservoir.Report(notify.Event{
				Severity:    notify.SeverityWarn,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Plugin dir unavailable",
				Message:     fmt.Sprintf("could not create plugin dir for watching: %s", w.pluginDir),
				Err:         mkErr,
			})
		}
	}

	if err := w.addRecursive(fsw, w.builtinDir); err != nil {
		w.reservoir.Report(notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Builtin scripts dir unwatched",
			Message:     fmt.Sprintf("could not watch builtin scripts dir: %s", w.builtinDir),
			Err:         err,
		})
	}
	if err := w.addRecursive(fsw, w.pluginDir); err != nil {
		w.reservoir.Report(notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Plugin dir unwatched",
			Message:     fmt.Sprintf("could not watch plugin dir: %s", w.pluginDir),
			Err:         err,
		})
	}

	var (
		mu      sync.Mutex
		pending *time.Timer
	)

	fire := func() {
		changed, reloadErr := w.reg.Reload(w.builtinDir, w.pluginDir)
		if reloadErr != nil {
			w.reservoir.Report(notify.Event{
				Severity:    notify.SeverityError,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Registry reload failed",
				Message:     reloadErr.Error(),
				Err:         reloadErr,
			})
			return
		}
		if changed {
			w.reservoir.Report(notify.Event{
				Severity:    notify.SeverityInfo,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Message:     fmt.Sprintf("registry reloaded: %d scripts, %d issues", len(w.reg.List()), len(w.reg.Issues())),
			})
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
						// Per-dir watch failure is a deep-trace event; log-only via Info.
						w.reservoir.Report(notify.Event{
							Severity:    notify.SeverityInfo,
							Persistence: notify.PersistenceOneShot,
							Source:      notify.SourceBackend,
							Message:     fmt.Sprintf("could not watch newly created dir %s: %s", ev.Name, addErr.Error()),
						})
					}
				}
			}
			schedule()
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.reservoir.Report(notify.Event{
				Severity:    notify.SeverityWarn,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "fsnotify error",
				Message:     err.Error(),
				Err:         err,
			})
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
			// Per-dir watch failure is a deep-trace event; log-only via Info.
			w.reservoir.Report(notify.Event{
				Severity:    notify.SeverityInfo,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Message:     fmt.Sprintf("could not watch dir %s: %s", path, addErr.Error()),
			})
		}
		return nil
	})
}
