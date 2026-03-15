package services

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"go-python-runner/internal/registry"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func writeTestScript(t *testing.T, dir, id, name string) {
	t.Helper()
	scriptDir := filepath.Join(dir, id)
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"` + id + `","name":"` + name + `","description":"test","params":[]}`
	if err := os.WriteFile(filepath.Join(scriptDir, "script.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "main.py"), []byte("pass"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScriptService_ListScripts(t *testing.T) {
	dir := t.TempDir()
	writeTestScript(t, dir, "hello", "Hello")
	writeTestScript(t, dir, "data", "Data Processor")

	reg := registry.New(testLogger())
	reg.LoadBuiltin(dir)

	svc := NewScriptService(reg)
	scripts := svc.ListScripts()
	if len(scripts) != 2 {
		t.Fatalf("expected 2 scripts, got %d", len(scripts))
	}
}

func TestScriptService_GetScript(t *testing.T) {
	dir := t.TempDir()
	writeTestScript(t, dir, "hello", "Hello")

	reg := registry.New(testLogger())
	reg.LoadBuiltin(dir)

	svc := NewScriptService(reg)
	script, err := svc.GetScript("hello")
	if err != nil {
		t.Fatal(err)
	}
	if script.Name != "Hello" {
		t.Errorf("expected name 'Hello', got %q", script.Name)
	}
}

func TestScriptService_GetScript_NotFound(t *testing.T) {
	reg := registry.New(testLogger())
	svc := NewScriptService(reg)

	_, err := svc.GetScript("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent script, got nil")
	}
}

func TestScriptService_GetPluginDir(t *testing.T) {
	reg := registry.New(testLogger())
	svc := NewScriptService(reg)

	dir := svc.GetPluginDir()
	if dir == "" {
		t.Error("expected non-empty plugin dir")
	}
}
