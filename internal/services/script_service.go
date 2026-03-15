package services

import (
	"fmt"

	"go-python-runner/internal/registry"
)

// ScriptService is a Wails service that exposes script information to the frontend.
type ScriptService struct {
	registry *registry.Registry
}

// NewScriptService creates a new ScriptService.
func NewScriptService(reg *registry.Registry) *ScriptService {
	return &ScriptService{registry: reg}
}

// ListScripts returns all registered scripts.
func (s *ScriptService) ListScripts() []registry.Script {
	return s.registry.List()
}

// GetScript returns a single script by ID.
func (s *ScriptService) GetScript(id string) (registry.Script, error) {
	script, ok := s.registry.Get(id)
	if !ok {
		return registry.Script{}, fmt.Errorf("script not found: %s", id)
	}
	return script, nil
}

// GetPluginDir returns the plugin directory path.
func (s *ScriptService) GetPluginDir() string {
	return s.registry.PluginDir()
}
