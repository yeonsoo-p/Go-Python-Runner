package registry

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

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

// Registry discovers and stores scripts from builtin and plugin directories.
type Registry struct {
	scripts   map[string]Script
	pluginDir string
	logger    *slog.Logger
}

// New creates a new Registry.
func New(logger *slog.Logger) *Registry {
	return &Registry{
		scripts: make(map[string]Script),
		logger:  logger,
	}
}

// LoadBuiltin scans the builtin scripts directory and registers all valid scripts.
func (r *Registry) LoadBuiltin(scriptsDir string) error {
	return r.loadDir(scriptsDir, "builtin")
}

// LoadPlugins scans the user plugin directory and registers scripts.
// Plugins with matching IDs override builtin scripts.
func (r *Registry) LoadPlugins(pluginDir string) error {
	r.pluginDir = pluginDir

	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		return nil // plugin dir doesn't exist yet, that's fine
	}
	return r.loadDir(pluginDir, "plugin")
}

// List returns all registered scripts in deterministic order
// (builtin before plugin, then by Name).
func (r *Registry) List() []Script {
	result := make([]Script, 0, len(r.scripts))
	for _, s := range r.scripts {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Source != result[j].Source {
			// "builtin" < "plugin" alphabetically — happens to be the order we want
			return result[i].Source < result[j].Source
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// Get returns a script by ID.
func (r *Registry) Get(id string) (Script, bool) {
	s, ok := r.scripts[id]
	return s, ok
}

// PluginDir returns the resolved plugin directory path.
func (r *Registry) PluginDir() string {
	if r.pluginDir != "" {
		return r.pluginDir
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

func (r *Registry) loadDir(dir string, source string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Skip _lib directory (helper library, not a script)
		if entry.Name() == "_lib" {
			continue
		}

		scriptDir := filepath.Join(dir, entry.Name())
		script, err := r.loadScript(scriptDir, source)
		if err != nil {
			r.logger.Warn("skipping malformed script",
				"dir", scriptDir,
				"error", err.Error(),
				"source", "backend",
			)
			continue
		}

		if existing, exists := r.scripts[script.ID]; exists && source == "plugin" {
			r.logger.Warn("plugin overriding builtin script",
				"scriptID", script.ID,
				"builtinDir", existing.Dir,
				"pluginDir", script.Dir,
				"source", "backend",
			)
		}

		r.scripts[script.ID] = script
	}

	return nil
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

	// Verify main.py exists
	mainPath := filepath.Join(dir, "main.py")
	if _, err := os.Stat(mainPath); os.IsNotExist(err) {
		return Script{}, fmt.Errorf("missing main.py")
	}

	script.Source = source
	script.Dir = dir
	return script, nil
}
