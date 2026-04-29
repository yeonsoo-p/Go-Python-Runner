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
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	svc := NewScriptService(reg)
	scripts := svc.ListScripts()
	if len(scripts) != 2 {
		t.Fatalf("expected 2 scripts, got %d", len(scripts))
	}
}

// ListScripts returns scripts in deterministic order (builtin first, then by Name).
// Locks down the contract that frontend renders consistent task-card ordering.
func TestScriptService_ListScripts_DeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	writeTestScript(t, dir, "zeta", "Zeta")
	writeTestScript(t, dir, "alpha", "Alpha")
	writeTestScript(t, dir, "mu", "Mu")

	reg := registry.New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	svc := NewScriptService(reg)
	first := svc.ListScripts()
	second := svc.ListScripts()
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("ListScripts is non-deterministic at index %d: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
	want := []string{"Alpha", "Mu", "Zeta"}
	for i, name := range want {
		if first[i].Name != name {
			t.Errorf("position %d: expected %q, got %q", i, name, first[i].Name)
		}
	}
}
