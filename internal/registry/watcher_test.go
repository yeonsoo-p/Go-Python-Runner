package registry

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// startWatcher launches a Watcher with a short debounce in a goroutine and
// returns a cancel func and a counter of onChange invocations.
func startWatcher(t *testing.T, builtinDir, pluginDir string) (context.CancelFunc, *atomic.Int32, *Registry) {
	t.Helper()
	reg := New(testLogger())
	if builtinDir != "" {
		if err := reg.LoadBuiltin(builtinDir); err != nil {
			t.Fatal(err)
		}
	}
	if pluginDir != "" {
		if err := reg.LoadPlugins(pluginDir); err != nil {
			t.Fatal(err)
		}
	}

	var changes atomic.Int32
	w := NewWatcher(reg, builtinDir, pluginDir, func() {
		changes.Add(1)
	}, testLogger())
	w.SetDebounce(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Give the watcher a moment to register its initial set of watches.
	time.Sleep(80 * time.Millisecond)

	return cancel, &changes, reg
}

// waitForChange polls until the change counter exceeds baseline or the
// timeout elapses. Returns the observed count.
func waitForChange(t *testing.T, changes *atomic.Int32, baseline int32, timeout time.Duration) int32 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cur := changes.Load(); cur > baseline {
			return cur
		}
		time.Sleep(10 * time.Millisecond)
	}
	return changes.Load()
}

func TestWatcher_FiresOnScriptJsonEdit(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")

	_, changes, reg := startWatcher(t, dir, "")
	baseline := changes.Load()

	// Edit the script.json (rename "Alpha" → "Alpha 2").
	updated := `{"id":"alpha","name":"Alpha 2","description":"","params":[]}`
	if err := os.WriteFile(filepath.Join(dir, "alpha", "script.json"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	got := waitForChange(t, changes, baseline, 2*time.Second)
	if got <= baseline {
		t.Fatalf("expected onChange to fire after script.json edit (baseline=%d, got=%d)", baseline, got)
	}
	s, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("expected alpha to still be registered")
	}
	if s.Name != "Alpha 2" {
		t.Errorf("expected name 'Alpha 2' after edit, got %q", s.Name)
	}
}

func TestWatcher_FiresOnNewScriptDir(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")

	_, changes, reg := startWatcher(t, dir, "")
	baseline := changes.Load()

	writeScript(t, dir, "beta", "Beta")

	got := waitForChange(t, changes, baseline, 2*time.Second)
	if got <= baseline {
		t.Fatalf("expected onChange to fire on new script dir (baseline=%d, got=%d)", baseline, got)
	}
	if _, ok := reg.Get("beta"); !ok {
		t.Errorf("expected 'beta' to be registered after creation")
	}
}

func TestWatcher_FiresOnRemovedScriptDir(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")
	writeScript(t, dir, "beta", "Beta")

	_, changes, reg := startWatcher(t, dir, "")
	baseline := changes.Load()

	if err := os.RemoveAll(filepath.Join(dir, "beta")); err != nil {
		t.Fatal(err)
	}

	got := waitForChange(t, changes, baseline, 2*time.Second)
	if got <= baseline {
		t.Fatalf("expected onChange to fire on script dir removal (baseline=%d, got=%d)", baseline, got)
	}
	if _, ok := reg.Get("beta"); ok {
		t.Errorf("expected 'beta' to be gone after removal")
	}
}

func TestWatcher_DebouncesBurst(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")

	_, changes, _ := startWatcher(t, dir, "")
	baseline := changes.Load()

	// Five rapid writes within the debounce window. Each rewrites script.json
	// with the same content (so net change is 0); should produce at most
	// one Reload, and since content matches what's already loaded,
	// zero onChange invocations. Content matches what writeScript() emitted.
	contents := `{"id":"alpha","name":"Alpha","description":"test","params":[]}`
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, "alpha", "script.json"), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Wait through the debounce + reload.
	time.Sleep(200 * time.Millisecond)
	if got := changes.Load(); got != baseline {
		t.Errorf("expected debounce + idempotent reload to produce 0 onChange (baseline=%d, got=%d)", baseline, got)
	}
}

func TestWatcher_StopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "alpha", "Alpha")

	reg := New(testLogger())
	if err := reg.LoadBuiltin(dir); err != nil {
		t.Fatal(err)
	}

	w := NewWatcher(reg, dir, "", func() {}, testLogger())
	w.SetDebounce(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error on context cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not return within 2s of context cancel")
	}
}

func TestWatcher_PluginDirCreatedAtRunStart(t *testing.T) {
	// Plugin dir doesn't exist yet — Watcher.Run should create it so it can
	// be watched. Verifies the os.MkdirAll path inside Run.
	parent := t.TempDir()
	pluginDir := filepath.Join(parent, "not-yet-existing", "plugins")

	reg := New(testLogger())
	w := NewWatcher(reg, "", pluginDir, func() {}, testLogger())
	w.SetDebounce(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait for setup to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pluginDir); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("plugin dir %q not created by watcher within 2s", pluginDir)
}
