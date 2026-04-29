package services

import (
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

// ListScripts returns all registered scripts in deterministic order.
// This is the only method the frontend uses; per-id lookup happens client-side
// against the cached list, and the plugin directory has no UI surface today.
func (s *ScriptService) ListScripts() []registry.Script {
	return s.registry.List()
}
