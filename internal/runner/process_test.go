package runner

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestProcessStart_NonexistentPython verifies Process.Start returns a clean
// error when PythonPath points at a binary that doesn't exist. This is the
// pre-lifecycle failure mode — StartRun in Manager bubbles this up to the
// caller without ever spawning a subprocess.
func TestProcessStart_NonexistentPython(t *testing.T) {
	tmp := t.TempDir()
	p := NewProcess("test-run", tmp, "", map[string]string{}, "127.0.0.1:0")
	p.PythonPath = filepath.Join(tmp, "nonexistent-python.exe")

	err := p.Start()
	if err == nil {
		t.Fatal("expected Start to fail with nonexistent python path, got nil")
	}
	if !strings.Contains(err.Error(), "starting process") {
		t.Errorf("expected wrapped 'starting process' error, got: %v", err)
	}
}

// TestProcessDone_ClosedAfterCancel verifies the Done() channel closes when
// Cancel() is called, even before Start. (Used by waitAndSendStart to bail
// when a run is cancelled before Python connects.)
func TestProcessDone_ClosedAfterCancel(t *testing.T) {
	p := NewProcess("test-run", t.TempDir(), "", map[string]string{}, "127.0.0.1:0")

	select {
	case <-p.Done():
		t.Fatal("Done channel closed before Cancel")
	default:
	}

	p.Cancel()

	select {
	case <-p.Done():
		// Expected.
	default:
		t.Fatal("Done channel did not close after Cancel")
	}
}
