package registry

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func writeScript(t *testing.T, dir, id, name string) {
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

func TestLoadBuiltin(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "hello", "Hello")
	writeScript(t, dir, "data", "Data Processor")

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	scripts := reg.List()
	if len(scripts) != 2 {
		t.Fatalf("expected 2 scripts, got %d", len(scripts))
	}

	s, ok := reg.Get("hello")
	if !ok {
		t.Fatal("expected to find 'hello' script")
	}
	if s.Name != "Hello" {
		t.Errorf("expected name 'Hello', got %q", s.Name)
	}
	if s.Source != "builtin" {
		t.Errorf("expected source 'builtin', got %q", s.Source)
	}
}

func TestLoadBuiltin_SkipsLib(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "hello", "Hello")

	// Create _lib directory (should be skipped)
	libDir := filepath.Join(dir, "_lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 script (should skip _lib), got %d", len(reg.List()))
	}
}

func TestLoadBuiltin_SkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "good", "Good Script")

	// Create malformed script (invalid JSON)
	badDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "script.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 script (skip malformed), got %d", len(reg.List()))
	}
}

func TestLoadBuiltin_SkipsMissingMainPy(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "good", "Good Script")

	// Create script dir with valid JSON but no main.py
	noMainDir := filepath.Join(dir, "nomain")
	if err := os.MkdirAll(noMainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"nomain","name":"No Main","description":"test","params":[]}`
	if err := os.WriteFile(filepath.Join(noMainDir, "script.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 script (skip missing main.py), got %d", len(reg.List()))
	}
}

func TestPluginOverride(t *testing.T) {
	builtinDir := t.TempDir()
	pluginDir := t.TempDir()

	writeScript(t, builtinDir, "hello", "Hello Builtin")
	writeScript(t, pluginDir, "hello", "Hello Plugin")

	reg := New(testLogger())
	if err := reg.LoadBuiltin(builtinDir); err != nil {
		t.Fatal(err)
	}
	if err := reg.LoadPlugins(pluginDir); err != nil {
		t.Fatal(err)
	}

	s, ok := reg.Get("hello")
	if !ok {
		t.Fatal("expected to find 'hello'")
	}
	if s.Name != "Hello Plugin" {
		t.Errorf("expected plugin to override builtin, got name %q", s.Name)
	}
	if s.Source != "plugin" {
		t.Errorf("expected source 'plugin', got %q", s.Source)
	}
}

func TestPluginAddsNew(t *testing.T) {
	builtinDir := t.TempDir()
	pluginDir := t.TempDir()

	writeScript(t, builtinDir, "hello", "Hello")
	writeScript(t, pluginDir, "custom", "Custom Plugin")

	reg := New(testLogger())
	if err := reg.LoadBuiltin(builtinDir); err != nil {
		t.Fatal(err)
	}
	if err := reg.LoadPlugins(pluginDir); err != nil {
		t.Fatal(err)
	}

	if len(reg.List()) != 2 {
		t.Fatalf("expected 2 scripts (1 builtin + 1 plugin), got %d", len(reg.List()))
	}

	s, ok := reg.Get("custom")
	if !ok {
		t.Fatal("expected to find 'custom' plugin script")
	}
	if s.Source != "plugin" {
		t.Errorf("expected source 'plugin', got %q", s.Source)
	}
}

func TestPluginDir_Nonexistent(t *testing.T) {
	reg := New(testLogger())
	// Loading a nonexistent plugin dir should not error
	if err := reg.LoadPlugins("/nonexistent/path"); err != nil {
		t.Fatalf("expected no error for nonexistent plugin dir, got %v", err)
	}
}

func TestLoadBuiltin_SkipsMissingID(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "good", "Good Script")

	// Create script with valid JSON but no id field
	noIDDir := filepath.Join(dir, "noid")
	if err := os.MkdirAll(noIDDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noIDDir, "script.json"), []byte(`{"name":"No ID","description":"test","params":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noIDDir, "main.py"), []byte("pass"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 script (skip missing id), got %d", len(reg.List()))
	}
}

// TestRegistry_Reload_DetectsChanges verifies that adding a script after
// initial load is picked up by Reload and reflected in List().
func TestRegistry_Reload_DetectsChanges(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}
	if got := len(reg.List()); got != 1 {
		t.Fatalf("expected 1 script after load, got %d", got)
	}

	writeScript(t, dir, "beta", "Beta")

	changed, err := reg.Reload(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Errorf("expected changed=true after adding script")
	}
	if got := len(reg.List()); got != 2 {
		t.Errorf("expected 2 scripts after reload, got %d", got)
	}
	if _, ok := reg.Get("beta"); !ok {
		t.Errorf("expected to find 'beta' after reload")
	}
}

// TestRegistry_Reload_Idempotent verifies that a no-op rescan returns
// changed=false and doesn't churn state.
func TestRegistry_Reload_Idempotent(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		changed, err := reg.Reload(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Errorf("iteration %d: expected changed=false on idempotent reload", i)
		}
	}
}

// TestRegistry_Reload_DetectsRemovedScript verifies that deleting a script
// dir is reflected after Reload.
func TestRegistry_Reload_DetectsRemovedScript(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")
	writeScript(t, dir, "beta", "Beta")

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(dir, "beta")); err != nil {
		t.Fatal(err)
	}

	changed, err := reg.Reload(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Errorf("expected changed=true after removing script")
	}
	if _, ok := reg.Get("beta"); ok {
		t.Errorf("expected 'beta' to be gone after reload")
	}
	if got := len(reg.List()); got != 1 {
		t.Errorf("expected 1 script after removal, got %d", got)
	}
}

// TestRegistry_Reload_AtomicityOnError verifies that when scanning fails for
// a non-existent directory, the prior state remains live (no torn map).
func TestRegistry_Reload_AtomicityOnError(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	// Reload against a path that doesn't exist as builtin.
	_, err := reg.Reload("/definitely/does/not/exist", "")
	if err == nil {
		t.Fatal("expected error reloading from nonexistent dir")
	}

	// State must be intact.
	if got := len(reg.List()); got != 1 {
		t.Errorf("expected state preserved after failed reload, got %d scripts", got)
	}
	if _, ok := reg.Get("alpha"); !ok {
		t.Errorf("alpha should still be present after failed reload")
	}
}

// TestRegistry_Issues_PopulatedForMalformed verifies that malformed scripts
// surface as LoadIssue records, not silent skips.
func TestRegistry_Issues_PopulatedForMalformed(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "good", "Good")

	// Malformed JSON
	badDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "script.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	issues := reg.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Source != "builtin" {
		t.Errorf("expected source 'builtin', got %q", issues[0].Source)
	}
	if issues[0].Dir != badDir {
		t.Errorf("expected dir %q, got %q", badDir, issues[0].Dir)
	}
	if issues[0].Error == "" {
		t.Errorf("expected non-empty error message")
	}
	if issues[0].Timestamp.IsZero() {
		t.Errorf("expected non-zero timestamp")
	}
}

// TestRegistry_Issues_ClearedAfterFix verifies that fixing a malformed
// script and reloading clears the issue.
func TestRegistry_Issues_ClearedAfterFix(t *testing.T) {
	dir := t.TempDir()

	badDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "script.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}
	if got := len(reg.Issues()); got != 1 {
		t.Fatalf("expected 1 issue before fix, got %d", got)
	}

	// Fix the file.
	good := `{"id":"bad","name":"Now Good","description":"","params":[]}`
	if err := os.WriteFile(filepath.Join(badDir, "script.json"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "main.py"), []byte("pass"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := reg.Reload(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Errorf("expected changed=true after fix")
	}
	if got := len(reg.Issues()); got != 0 {
		t.Errorf("expected 0 issues after fix, got %d", got)
	}
	if _, ok := reg.Get("bad"); !ok {
		t.Errorf("expected 'bad' to be loaded after fix")
	}
}

// TestRegistry_ConcurrentReadsDuringReload runs many parallel readers while
// Reload swaps state. Race detector must not fire and readers must always
// see a coherent snapshot.
func TestRegistry_ConcurrentReadsDuringReload(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeScript(t, dir, "s"+string(rune('a'+i)), "Script "+string(rune('A'+i)))
	}

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan struct{}, 4)

	for r := 0; r < 4; r++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				select {
				case <-stop:
					return
				default:
				}
				list := reg.List()
				_ = reg.Issues()
				if len(list) < 1 {
					t.Errorf("reader saw empty list — torn state")
					return
				}
			}
		}()
	}

	for i := 0; i < 50; i++ {
		if _, err := reg.Reload(dir, ""); err != nil {
			t.Errorf("reload failed: %v", err)
		}
	}
	close(stop)
	for i := 0; i < 4; i++ {
		<-done
	}
}

func TestLoadBuiltin_MixedValidAndInvalid(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")
	writeScript(t, dir, "beta", "Beta")

	// Create one bad script (invalid JSON)
	badDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "script.json"), []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	scripts := reg.List()
	if len(scripts) != 2 {
		t.Fatalf("expected 2 valid scripts (skipping 1 bad), got %d", len(scripts))
	}
}
