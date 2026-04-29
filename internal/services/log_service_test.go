package services

import (
	"bytes"
	"testing"

	"go-python-runner/internal/logging"
	"go-python-runner/internal/notify"
)

// reservoirAndRing wires a real reservoir to a real ring buffer through a
// shared slog.Logger. This mirrors main.go's wiring exactly so tests exercise
// the same path: Report → slog → ring entry → GetLogs / log:entry stream.
func reservoirAndRing(_ *testing.T) (notify.Reservoir, *logging.RingBuffer) {
	ring := logging.NewRingBuffer(100)
	logger := logging.NewTestLogger(&bytes.Buffer{}, ring)
	return notify.New(logger), ring
}

func TestLogService_LogError(t *testing.T) {
	res, ring := reservoirAndRing(t)
	svc := NewLogService(ring, res)

	svc.LogError("frontend", "Uncaught TypeError", map[string]string{
		"component": "TaskCard",
		"url":       "/scripts",
	})

	entries := ring.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	if entries[0].Message != "Uncaught TypeError" {
		t.Errorf("expected message 'Uncaught TypeError', got %q", entries[0].Message)
	}
	if entries[0].Source != "frontend" {
		t.Errorf("expected source 'frontend', got %q", entries[0].Source)
	}
}

func TestLogService_GetLogs_ReturnsAll(t *testing.T) {
	res, ring := reservoirAndRing(t)
	svc := NewLogService(ring, res)

	svc.LogError("frontend", "JS error", nil)
	res.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     "go info",
	})
	res.Report(notify.Event{
		Severity:    notify.SeverityError,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourcePython,
		Message:     "py crash",
	})

	all := svc.GetLogs()
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}
}

func TestLogService_ErrorPropagation(t *testing.T) {
	// Verify the full path: LogError → reservoir → slog → ring buffer → GetLogs
	// with correct attributes. The reservoir must carry runID/scriptID/traceback
	// from the Event into the slog record so the LogViewer can filter on them.
	res, ring := reservoirAndRing(t)
	svc := NewLogService(ring, res)

	// Simulate all three error sources.
	svc.LogError("frontend", "React render error", map[string]string{"component": "App"})
	res.Report(notify.Event{
		Severity:    notify.SeverityError,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     "gRPC server failed",
		RunID:       "run-42",
	})
	res.Report(notify.Event{
		Severity:    notify.SeverityError,
		Persistence: notify.PersistenceInFlight,
		Source:      notify.SourcePython,
		Message:     "NameError: x not defined",
		RunID:       "run-99",
		Traceback:   "File main.py, line 5",
	})

	// Backend keeps no filter; client filters. Verify the ring captured all sources
	// and per-source attributes round-trip correctly.
	all := svc.GetLogs()
	if len(all) != 3 {
		t.Fatalf("expected 3 entries total, got %d", len(all))
	}

	bySource := map[string]logging.LogEntry{}
	for _, e := range all {
		bySource[e.Source] = e
	}
	if fe, ok := bySource["frontend"]; !ok || fe.Level != "ERROR" {
		t.Errorf("frontend error: expected ERROR entry, got %+v", fe)
	}
	if be, ok := bySource["backend"]; !ok || be.RunID != "run-42" {
		t.Errorf("backend error: expected runID 'run-42', got %+v", be)
	}
	if py, ok := bySource["python"]; !ok || py.Traceback != "File main.py, line 5" {
		t.Errorf("python error: expected traceback, got %+v", py)
	}
}
