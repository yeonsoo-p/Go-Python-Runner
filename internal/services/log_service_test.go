package services

import (
	"bytes"
	"testing"

	"go-python-runner/internal/logging"
)

func TestLogService_LogError(t *testing.T) {
	ring := logging.NewRingBuffer(100)
	logger := logging.NewTestLogger(&bytes.Buffer{}, ring)
	svc := NewLogService(logger, ring)

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
	ring := logging.NewRingBuffer(100)
	logger := logging.NewTestLogger(&bytes.Buffer{}, ring)
	svc := NewLogService(logger, ring)

	svc.LogError("frontend", "JS error", nil)
	logger.Info("go info", "source", "backend")
	logger.Error("py crash", "source", "python")

	all := svc.GetLogs()
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}
}

func TestLogService_ErrorPropagation(t *testing.T) {
	// Verify the full path: LogError → slog → ring buffer → GetLogs with correct attributes.
	ring := logging.NewRingBuffer(100)
	logger := logging.NewTestLogger(&bytes.Buffer{}, ring)
	svc := NewLogService(logger, ring)

	// Simulate all three error sources.
	svc.LogError("frontend", "React render error", map[string]string{"component": "App"})
	logger.Error("gRPC server failed", "source", "backend", "runID", "run-42")
	logger.Error("NameError: x not defined", "source", "python", "runID", "run-99", "traceback", "File main.py, line 5")

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
