package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewTestLogger_WritesToBoth(t *testing.T) {
	var buf bytes.Buffer
	ring := NewRingBuffer(100)
	logger := NewTestLogger(&buf, ring)

	logger.Info("hello world",
		"source", "backend",
		"runID", "run-123",
	)

	// Check file output (JSON)
	output := buf.String()
	if !strings.Contains(output, "hello world") {
		t.Error("expected log file to contain message")
	}

	// Check ring buffer
	entries := ring.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 ring entry, got %d", len(entries))
	}
	if entries[0].Message != "hello world" {
		t.Errorf("expected message 'hello world', got %q", entries[0].Message)
	}
	if entries[0].Source != "backend" {
		t.Errorf("expected source 'backend', got %q", entries[0].Source)
	}
	if entries[0].RunID != "run-123" {
		t.Errorf("expected runID 'run-123', got %q", entries[0].RunID)
	}
}

func TestNewTestLogger_LevelExtracted(t *testing.T) {
	ring := NewRingBuffer(100)
	logger := NewTestLogger(&bytes.Buffer{}, ring)

	logger.Error("something broke", "source", "python", "traceback", "line 42")

	entries := ring.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Level != "ERROR" {
		t.Errorf("expected level ERROR, got %q", entries[0].Level)
	}
	if entries[0].Traceback != "line 42" {
		t.Errorf("expected traceback, got %q", entries[0].Traceback)
	}
}

func TestNewTestLogger_MultipleMessages(t *testing.T) {
	ring := NewRingBuffer(100)
	logger := NewTestLogger(&bytes.Buffer{}, ring)

	logger.Info("msg1", "source", "frontend")
	logger.Warn("msg2", "source", "backend")
	logger.Error("msg3", "source", "python")

	entries := ring.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}
