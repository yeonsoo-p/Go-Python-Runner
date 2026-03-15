package logging

import (
	"fmt"
	"testing"
	"time"
)

func TestRingBuffer_PushAndLen(t *testing.T) {
	r := NewRingBuffer(5)
	if r.Len() != 0 {
		t.Fatalf("expected len 0, got %d", r.Len())
	}

	for i := 0; i < 3; i++ {
		r.Push(LogEntry{Message: fmt.Sprintf("msg%d", i)})
	}
	if r.Len() != 3 {
		t.Fatalf("expected len 3, got %d", r.Len())
	}
}

func TestRingBuffer_Eviction(t *testing.T) {
	r := NewRingBuffer(3)
	for i := 0; i < 5; i++ {
		r.Push(LogEntry{Message: fmt.Sprintf("msg%d", i)})
	}
	if r.Len() != 3 {
		t.Fatalf("expected len 3 after overflow, got %d", r.Len())
	}

	entries := r.Entries(LogFilter{})
	// Should have msg2, msg3, msg4 (oldest evicted)
	expected := []string{"msg2", "msg3", "msg4"}
	for i, e := range entries {
		if e.Message != expected[i] {
			t.Errorf("entry %d: expected %q, got %q", i, expected[i], e.Message)
		}
	}
}

func TestRingBuffer_ChronologicalOrder(t *testing.T) {
	r := NewRingBuffer(10)
	for i := 0; i < 5; i++ {
		r.Push(LogEntry{
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Message:   fmt.Sprintf("msg%d", i),
		})
	}

	entries := r.Entries(LogFilter{})
	for i := 1; i < len(entries); i++ {
		if entries[i].Timestamp.Before(entries[i-1].Timestamp) {
			t.Error("entries are not in chronological order")
		}
	}
}

func TestRingBuffer_FilterBySource(t *testing.T) {
	r := NewRingBuffer(10)
	r.Push(LogEntry{Source: "python", Message: "py1"})
	r.Push(LogEntry{Source: "backend", Message: "go1"})
	r.Push(LogEntry{Source: "python", Message: "py2"})

	entries := r.Entries(LogFilter{Source: "python"})
	if len(entries) != 2 {
		t.Fatalf("expected 2 python entries, got %d", len(entries))
	}
}

func TestRingBuffer_FilterByLevel(t *testing.T) {
	r := NewRingBuffer(10)
	r.Push(LogEntry{Level: "INFO", Message: "info1"})
	r.Push(LogEntry{Level: "ERROR", Message: "err1"})
	r.Push(LogEntry{Level: "INFO", Message: "info2"})

	entries := r.Entries(LogFilter{Level: "ERROR"})
	if len(entries) != 1 {
		t.Fatalf("expected 1 error entry, got %d", len(entries))
	}
}

func TestRingBuffer_FilterByRunID(t *testing.T) {
	r := NewRingBuffer(10)
	r.Push(LogEntry{RunID: "run-1", Message: "msg1"})
	r.Push(LogEntry{RunID: "run-2", Message: "msg2"})
	r.Push(LogEntry{RunID: "run-1", Message: "msg3"})

	entries := r.Entries(LogFilter{RunID: "run-1"})
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for run-1, got %d", len(entries))
	}
}

func TestRingBuffer_EmptyFilter(t *testing.T) {
	r := NewRingBuffer(10)
	r.Push(LogEntry{Source: "python", Level: "INFO"})
	r.Push(LogEntry{Source: "backend", Level: "ERROR"})

	entries := r.Entries(LogFilter{})
	if len(entries) != 2 {
		t.Fatalf("expected all 2 entries with empty filter, got %d", len(entries))
	}
}
