package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"go-python-runner/internal/notify"
)

// maxLoadIssues caps the LoadIssue slice so a flapping plugin (which
// re-registers a fresh issue every fsnotify cycle) cannot balloon memory or
// the banner stack. Oldest entries are dropped first.
const maxLoadIssues = 50

// loadIssueKeyPrefix is the banner-key prefix the registry owns. Every
// LoadIssue becomes a banner under this namespace; ReplaceBannersByPrefix
// uses it to atomically swap the current set on each scan.
const loadIssueKeyPrefix = "loadIssue:"

// Param describes a single parameter for a script.
//
// All params are string-valued at the protocol level; scripts parse to int/float
// themselves. A "type" field used to live here for future type-aware rendering,
// but the UI never branched on it and every script declared "string", so it
// was removed. Reintroduce it only when there's a concrete UI consumer.
type Param struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// ParallelConfig describes how a script supports one-click parallel dispatch.
type ParallelConfig struct {
	DefaultWorkers int      `json:"default_workers"`
	MaxWorkers     int      `json:"max_workers"`
	VaryParam      string   `json:"vary_param"`
	ChainParam     string   `json:"chain_param,omitempty"`
	Names          []string `json:"names,omitempty"`
}

// Script represents a registered script (builtin or plugin).
type Script struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Params      []Param         `json:"params"`
	Parallel    *ParallelConfig `json:"parallel,omitempty"`
	Source      string          `json:"source"` // "builtin" or "plugin"
	Dir         string          `json:"-"`      // absolute path to script directory
}

// LoadIssue records a script directory that failed to register. Surfaced via
// Issues() so the UI can display a persistent banner; one bad plugin must not
// silently disappear from the user's awareness.
type LoadIssue struct {
	Dir       string    `json:"dir"`
	Source    string    `json:"source"` // "builtin" or "plugin"
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
}

// Registry discovers and stores scripts from builtin and plugin directories.
// All read/write state is guarded by mu so the watcher can replace state
// concurrently with handler reads.
type Registry struct {
	mu          sync.RWMutex
	scripts     map[string]Script
	issues      []LoadIssue
	fingerprint string // of (scripts + issues), used by Reload to skip no-op swaps
	pluginDir   string
	reservoir   notify.Reservoir
}

// New creates a new Registry. The reservoir is the sole observability
// dependency — every LoadIssue produces an ongoing banner via
// ReplaceBannersByPrefix on each load, and plugin-override warnings flow
// through reservoir.Report.
func New(reservoir notify.Reservoir) *Registry {
	return &Registry{
		scripts:   make(map[string]Script),
		reservoir: reservoir,
	}
}

// LoadBuiltin scans the builtin scripts directory and registers all valid
// scripts. Used at boot; replaces only the builtin slice of state.
func (r *Registry) LoadBuiltin(scriptsDir string) error {
	scripts, issues, err := r.buildFromDir(scriptsDir, "builtin")
	if err != nil {
		return err
	}
	r.mu.Lock()
	for id, s := range scripts {
		r.scripts[id] = s
	}
	r.issues = appendIssuesCapped(r.issues, issues)
	r.fingerprint = computeFingerprint(r.scripts, r.issues)
	snapshot := append([]LoadIssue(nil), r.issues...)
	r.mu.Unlock()
	r.reservoir.ReplaceBannersByPrefix(loadIssueKeyPrefix, loadIssuesToEvents(snapshot))
	return nil
}

// LoadPlugins scans the user plugin directory and registers scripts.
// Plugins with matching IDs override builtin scripts. Missing plugin dir is
// not an error — it just means the user has no plugins yet.
func (r *Registry) LoadPlugins(pluginDir string) error {
	r.mu.Lock()
	r.pluginDir = pluginDir
	r.mu.Unlock()

	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		return nil
	}
	scripts, issues, err := r.buildFromDir(pluginDir, "plugin")
	if err != nil {
		return err
	}
	r.mu.Lock()
	var overrides []overrideNotice
	for id, s := range scripts {
		if existing, exists := r.scripts[id]; exists {
			overrides = append(overrides, overrideNotice{
				scriptID:   id,
				builtinDir: existing.Dir,
				pluginDir:  s.Dir,
			})
		}
		r.scripts[id] = s
	}
	r.issues = appendIssuesCapped(r.issues, issues)
	r.fingerprint = computeFingerprint(r.scripts, r.issues)
	snapshot := append([]LoadIssue(nil), r.issues...)
	r.mu.Unlock()
	for _, o := range overrides {
		r.reportOverride(o)
	}
	r.reservoir.ReplaceBannersByPrefix(loadIssueKeyPrefix, loadIssuesToEvents(snapshot))
	return nil
}

// Reload rescans both directories from scratch and atomically swaps the
// registry's state if anything changed. Returns (true, nil) when the new
// state differs from the previous fingerprint, (false, nil) when the rescan
// is idempotent. Atomic: a failed scan leaves the prior state intact.
func (r *Registry) Reload(scriptsDir, pluginDir string) (bool, error) {
	newScripts := make(map[string]Script)
	var newIssues []LoadIssue
	var overrides []overrideNotice

	// Builtin first; plugin overrides last so its IDs win.
	if scriptsDir != "" {
		s, i, err := r.buildFromDir(scriptsDir, "builtin")
		if err != nil {
			return false, fmt.Errorf("scanning builtin dir: %w", err)
		}
		for id, sc := range s {
			newScripts[id] = sc
		}
		newIssues = append(newIssues, i...)
	}
	if pluginDir != "" {
		if _, err := os.Stat(pluginDir); err == nil {
			s, i, err := r.buildFromDir(pluginDir, "plugin")
			if err != nil {
				return false, fmt.Errorf("scanning plugin dir: %w", err)
			}
			for id, sc := range s {
				if existing, override := newScripts[id]; override {
					overrides = append(overrides, overrideNotice{
						scriptID:   id,
						builtinDir: existing.Dir,
						pluginDir:  sc.Dir,
					})
				}
				newScripts[id] = sc
			}
			newIssues = append(newIssues, i...)
		}
	}

	if overflow := len(newIssues) - maxLoadIssues; overflow > 0 {
		newIssues = newIssues[overflow:]
	}
	newFingerprint := computeFingerprint(newScripts, newIssues)

	r.mu.Lock()
	if newFingerprint == r.fingerprint {
		r.mu.Unlock()
		return false, nil
	}
	r.scripts = newScripts
	r.issues = newIssues
	r.fingerprint = newFingerprint
	snapshot := append([]LoadIssue(nil), r.issues...)
	r.mu.Unlock()
	for _, o := range overrides {
		r.reportOverride(o)
	}
	r.reservoir.ReplaceBannersByPrefix(loadIssueKeyPrefix, loadIssuesToEvents(snapshot))
	return true, nil
}

// List returns all registered scripts in deterministic order
// (builtin before plugin, then by Name).
func (r *Registry) List() []Script {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Script, 0, len(r.scripts))
	for _, s := range r.scripts {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Source != result[j].Source {
			return result[i].Source < result[j].Source
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// Get returns a script by ID. The returned Script is a copy.
func (r *Registry) Get(id string) (Script, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.scripts[id]
	return s, ok
}

// Issues returns the current set of load failures. The returned slice is a copy.
func (r *Registry) Issues() []LoadIssue {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LoadIssue, len(r.issues))
	copy(out, r.issues)
	return out
}

// PluginDir returns the resolved plugin directory path.
func (r *Registry) PluginDir() string {
	r.mu.RLock()
	pd := r.pluginDir
	r.mu.RUnlock()
	if pd != "" {
		return pd
	}
	return DefaultPluginDir()
}

// DefaultPluginDir returns the platform-appropriate plugin directory.
func DefaultPluginDir() string {
	if dir := os.Getenv("PYRUNNER_PLUGIN_DIR"); dir != "" {
		return dir
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = "."
	}
	return filepath.Join(configDir, "go-python-runner", "scripts")
}

// overrideNotice carries the data needed to report a plugin-over-builtin
// override after the lock is released. Computed under the lock; reported
// outside it so reservoir.Report doesn't run with the registry mutex held.
type overrideNotice struct {
	scriptID   string
	builtinDir string
	pluginDir  string
}

func (r *Registry) reportOverride(o overrideNotice) {
	r.reservoir.Report(notify.Event{
		Severity:    notify.SeverityWarn,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Title:       "Plugin overriding builtin script",
		Message:     fmt.Sprintf("plugin %s at %s overrides builtin at %s", o.scriptID, o.pluginDir, o.builtinDir),
		ScriptID:    o.scriptID,
	})
}

// buildFromDir scans dir and returns (scripts by id, load issues, fatal error).
// A fatal error means the directory itself can't be read; per-script failures
// become LoadIssue records (which become banners via ReplaceBannersByPrefix
// at the caller) and don't fail the whole scan.
func (r *Registry) buildFromDir(dir, source string) (map[string]Script, []LoadIssue, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading directory %s: %w", dir, err)
	}

	scripts := make(map[string]Script)
	var issues []LoadIssue

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == "_lib" {
			continue
		}

		scriptDir := filepath.Join(dir, entry.Name())
		script, loadErr := r.loadScript(scriptDir, source)
		if loadErr != nil {
			issues = append(issues, LoadIssue{
				Dir:       scriptDir,
				Source:    source,
				Error:     loadErr.Error(),
				Timestamp: time.Now(),
			})
			continue
		}

		// Within a single dir scan, an ID collision is itself an issue: the
		// later script silently winning would be a load-order bug. Surface it.
		if existing, dup := scripts[script.ID]; dup {
			issues = append(issues, LoadIssue{
				Dir:       scriptDir,
				Source:    source,
				Error:     fmt.Sprintf("duplicate script ID %q (already loaded from %s)", script.ID, existing.Dir),
				Timestamp: time.Now(),
			})
			continue
		}

		scripts[script.ID] = script
	}

	return scripts, issues, nil
}

func (r *Registry) loadScript(dir string, source string) (Script, error) {
	metaPath := filepath.Join(dir, "script.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return Script{}, fmt.Errorf("reading script.json: %w", err)
	}

	var script Script
	if err := json.Unmarshal(data, &script); err != nil {
		return Script{}, fmt.Errorf("parsing script.json: %w", err)
	}

	if script.ID == "" {
		return Script{}, fmt.Errorf("script.json missing id field")
	}

	mainPath := filepath.Join(dir, "main.py")
	if _, err := os.Stat(mainPath); os.IsNotExist(err) {
		return Script{}, fmt.Errorf("missing main.py")
	}

	script.Source = source
	script.Dir = dir
	return script, nil
}

// appendIssuesCapped appends new issues and trims the front of the slice to
// stay within maxLoadIssues. Drops oldest entries first so a flapping plugin
// can't push useful issues out beyond the cap.
func appendIssuesCapped(existing, additions []LoadIssue) []LoadIssue {
	combined := append(existing, additions...)
	if overflow := len(combined) - maxLoadIssues; overflow > 0 {
		combined = combined[overflow:]
	}
	return combined
}

// loadIssuesToEvents is a pure transform from LoadIssue records to the
// notify.Event slice expected by ReplaceBannersByPrefix. Each issue becomes
// one banner keyed by its directory so re-emissions across rescan cycles
// dedupe via Key.
func loadIssuesToEvents(issues []LoadIssue) []notify.Event {
	events := make([]notify.Event, 0, len(issues))
	for _, iss := range issues {
		events = append(events, notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOngoing,
			Source:      notify.SourceBackend,
			Key:         loadIssueKeyPrefix + iss.Dir,
			Title:       "Plugin load issue",
			Message:     fmt.Sprintf("%s: %s", iss.Dir, iss.Error),
			Timestamp:   iss.Timestamp,
		})
	}
	return events
}

// computeFingerprint hashes the visible state of the registry. Two registries
// with the same script IDs, same script contents, and same issue lists hash
// identically — used to skip no-op swaps in Reload.
func computeFingerprint(scripts map[string]Script, issues []LoadIssue) string {
	h := sha256.New()

	ids := make([]string, 0, len(scripts))
	for id := range scripts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	enc := json.NewEncoder(h)
	for _, id := range ids {
		_ = enc.Encode(scripts[id])
	}

	issuesCopy := make([]LoadIssue, len(issues))
	copy(issuesCopy, issues)
	sort.Slice(issuesCopy, func(i, j int) bool {
		if issuesCopy[i].Dir != issuesCopy[j].Dir {
			return issuesCopy[i].Dir < issuesCopy[j].Dir
		}
		return issuesCopy[i].Error < issuesCopy[j].Error
	})
	for i := range issuesCopy {
		// Zero the timestamp — same script, same error at different times
		// shouldn't show as "changed."
		issuesCopy[i].Timestamp = time.Time{}
		_ = enc.Encode(issuesCopy[i])
	}

	return hex.EncodeToString(h.Sum(nil))
}
