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

	entries := ring.Entries(logging.LogFilter{})
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

func TestLogService_GetLogs_Filtered(t *testing.T) {
	ring := logging.NewRingBuffer(100)
	logger := logging.NewTestLogger(&bytes.Buffer{}, ring)
	svc := NewLogService(logger, ring)

	svc.LogError("frontend", "JS error", nil)
	logger.Info("go info", "source", "backend")
	logger.Error("py crash", "source", "python")

	all := svc.GetLogs(logging.LogFilter{})
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}

	pyOnly := svc.GetLogs(logging.LogFilter{Source: "python"})
	if len(pyOnly) != 1 {
		t.Fatalf("expected 1 python entry, got %d", len(pyOnly))
	}
}

func TestLogService_ErrorPropagation(t *testing.T) {
	// Verify the full path: LogError → slog → ring buffer → GetLogs with correct attributes
	ring := logging.NewRingBuffer(100)
	logger := logging.NewTestLogger(&bytes.Buffer{}, ring)
	svc := NewLogService(logger, ring)

	// Simulate all three error sources
	svc.LogError("frontend", "React render error", map[string]string{"component": "App"})
	logger.Error("gRPC server failed", "source", "backend", "runID", "run-42")
	logger.Error("NameError: x not defined", "source", "python", "runID", "run-99", "traceback", "File main.py, line 5")

	// Verify each source reaches the ring with correct attributes
	feEntries := svc.GetLogs(logging.LogFilter{Source: "frontend"})
	if len(feEntries) != 1 || feEntries[0].Level != "ERROR" {
		t.Errorf("frontend error: expected 1 ERROR entry, got %d", len(feEntries))
	}

	beEntries := svc.GetLogs(logging.LogFilter{Source: "backend"})
	if len(beEntries) != 1 || beEntries[0].RunID != "run-42" {
		t.Errorf("backend error: expected runID 'run-42', got %+v", beEntries)
	}

	pyEntries := svc.GetLogs(logging.LogFilter{Source: "python"})
	if len(pyEntries) != 1 || pyEntries[0].Traceback != "File main.py, line 5" {
		t.Errorf("python error: expected traceback, got %+v", pyEntries)
	}

	// Verify ERROR-only filter
	errOnly := svc.GetLogs(logging.LogFilter{Level: "ERROR"})
	if len(errOnly) != 3 {
		t.Fatalf("expected 3 ERROR entries total, got %d", len(errOnly))
	}
}
