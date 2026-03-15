//go:build integration

package integration

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"go-python-runner/internal/registry"
)

func TestPluginOverride(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	builtinDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}

	// Create a plugin dir with an override for "echo"
	pluginDir := t.TempDir()
	overrideDir := filepath.Join(pluginDir, "echo_script")
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatal(err)
	}

	meta := `{"id":"echo","name":"Echo Plugin Override","description":"overridden","params":[]}`
	if err := os.WriteFile(filepath.Join(overrideDir, "script.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, "main.py"), []byte("pass"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := registry.New(logger)
	if err := reg.LoadBuiltin(builtinDir); err != nil {
		t.Fatal(err)
	}
	if err := reg.LoadPlugins(pluginDir); err != nil {
		t.Fatal(err)
	}

	s, ok := reg.Get("echo")
	if !ok {
		t.Fatal("expected to find 'echo' script")
	}
	if s.Name != "Echo Plugin Override" {
		t.Errorf("expected plugin to override, got name %q", s.Name)
	}
	if s.Source != "plugin" {
		t.Errorf("expected source 'plugin', got %q", s.Source)
	}
}
