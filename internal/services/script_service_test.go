package services

import (
	"errors"
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

	svc := NewScriptService(reg, testLogger(), dir)
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

	svc := NewScriptService(reg, testLogger(), dir)
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

// TestScriptService_ListIssues_RoundTrip verifies that load issues recorded
// by the registry are reachable through the service.
func TestScriptService_ListIssues_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeTestScript(t, dir, "good", "Good")
	// Drop a malformed sibling.
	badDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "script.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := registry.New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	svc := NewScriptService(reg, testLogger(), dir)
	issues := svc.ListIssues()
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Dir != badDir {
		t.Errorf("expected issue.Dir=%q, got %q", badDir, issues[0].Dir)
	}
}

// openSpy records calls to the open hook so we can verify the path delivered
// to the OS layer matches what was requested.
type openSpy struct {
	calls []string
	err   error
}

func (s *openSpy) open(path string) error {
	s.calls = append(s.calls, path)
	return s.err
}

func newSvcWithOpenSpy(t *testing.T, allowedRoot string) (*ScriptService, *openSpy) {
	t.Helper()
	reg := registry.New(testLogger())
	svc := NewScriptService(reg, testLogger(), allowedRoot)
	spy := &openSpy{}
	svc.openHook = spy.open
	return svc, spy
}

// TestScriptService_OpenPath_Allowed_File verifies a file under the allowed
// root is opened.
func TestScriptService_OpenPath_Allowed_File(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, spy := newSvcWithOpenSpy(t, dir)
	if err := svc.OpenPath(target); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("expected 1 open call, got %d", len(spy.calls))
	}
}

// TestScriptService_OpenPath_Allowed_Directory verifies a directory under
// the allowed root is opened (browser.OpenFile handles dirs too).
func TestScriptService_OpenPath_Allowed_Directory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	svc, spy := newSvcWithOpenSpy(t, dir)
	if err := svc.OpenPath(sub); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("expected 1 open call, got %d", len(spy.calls))
	}
}

// TestScriptService_OpenPath_RejectsOutsidePath verifies path-allowlist
// validation. The hook must NOT be called.
func TestScriptService_OpenPath_RejectsOutsidePath(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir() // separate temp dir
	target := filepath.Join(outside, "file.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, spy := newSvcWithOpenSpy(t, allowed)
	err := svc.OpenPath(target)
	if err == nil {
		t.Fatal("expected error for path outside allowed roots")
	}
	if len(spy.calls) != 0 {
		t.Errorf("hook should not have been called for rejected path, got %d calls", len(spy.calls))
	}
}

// TestScriptService_OpenPath_RejectsNonexistent verifies missing-file rejection.
// The hook must NOT be called.
func TestScriptService_OpenPath_RejectsNonexistent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "does-not-exist.txt")

	svc, spy := newSvcWithOpenSpy(t, dir)
	err := svc.OpenPath(target)
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
	if len(spy.calls) != 0 {
		t.Errorf("hook should not have been called for nonexistent path")
	}
}

// TestScriptService_OpenPath_SurfacesOpenerError verifies that an error from
// the OS-level opener propagates back to the caller (four-part contract:
// "return the error").
func TestScriptService_OpenPath_SurfacesOpenerError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, spy := newSvcWithOpenSpy(t, dir)
	spy.err = errors.New("simulated OS opener failure")

	err := svc.OpenPath(target)
	if err == nil {
		t.Fatal("expected error from opener to surface")
	}
}

// TestScriptService_OpenPath_NoAllowedRoots_RejectsEverything verifies that
// constructing a service with no allowed roots is fail-closed.
func TestScriptService_OpenPath_NoAllowedRoots_RejectsEverything(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := registry.New(testLogger())
	svc := NewScriptService(reg, testLogger()) // no allowed roots
	if err := svc.OpenPath(target); err == nil {
		t.Errorf("expected fail-closed (rejection) when no allowed roots configured")
	}
}
