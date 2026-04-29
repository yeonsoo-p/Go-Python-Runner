package registry

import (
	"os"
	"path/filepath"
	"testing"

	"go-python-runner/internal/notify"
)

// FuzzLoadPlugins drives Registry.LoadPlugins with adversarial script.json
// content to ensure malformed plugins are skipped (with a banner report)
// rather than panicking the host process.
func FuzzLoadPlugins(f *testing.F) {
	f.Add([]byte(`{"id":"x","name":"X","description":"","params":[]}`))
	f.Add([]byte(`{"id":"y","name":"Y","description":"d","params":[{"name":"p","required":true,"default":"","description":""}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"id":null}`))
	f.Add([]byte(`{"id":"x","params":[{"name":"\xff\xfe"}]}`))
	f.Add([]byte(`{"id":"x","parallel":{"max_workers":-1,"vary_param":""}}`))
	f.Add([]byte(`{"id":"` + string(make([]byte, 8192)) + `"}`))

	f.Fuzz(func(t *testing.T, scriptJSON []byte) {
		dir := t.TempDir()
		pluginDir := filepath.Join(dir, "fuzz-plugin")
		if err := os.MkdirAll(pluginDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "script.json"), scriptJSON, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "main.py"), []byte("# stub\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		reg := New(&notify.RecordingReservoir{})
		// Must not panic regardless of input. Errors are acceptable; panics aren't.
		_ = reg.LoadPlugins(dir)
	})
}
