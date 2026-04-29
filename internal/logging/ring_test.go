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

	entries := r.Entries()
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

	entries := r.Entries()
	for i := 1; i < len(entries); i++ {
		if entries[i].Timestamp.Before(entries[i-1].Timestamp) {
			t.Error("entries are not in chronological order")
		}
	}
}

func TestRingBuffer_ReturnsAllEntries(t *testing.T) {
	r := NewRingBuffer(10)
	r.Push(LogEntry{Source: "python", Level: "INFO"})
	r.Push(LogEntry{Source: "backend", Level: "ERROR"})

	entries := r.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected all 2 entries, got %d", len(entries))
	}
}

// TestRingBuffer_HighVolumeFIFO — push 100k entries through a cap-1000
// buffer; verify FIFO eviction (only the last 1000 survive, oldest first).
// Catches off-by-one errors in head/count arithmetic that small tests miss.
func TestRingBuffer_HighVolumeFIFO(t *testing.T) {
	const capacity = 1000
	const total = 100_000
	r := NewRingBuffer(capacity)

	for i := 0; i < total; i++ {
		r.Push(LogEntry{Message: fmt.Sprintf("msg-%d", i)})
	}

	if r.Len() != capacity {
		t.Fatalf("expected len %d after %d pushes, got %d", capacity, total, r.Len())
	}

	entries := r.Entries()
	if len(entries) != capacity {
		t.Fatalf("expected %d entries, got %d", capacity, len(entries))
	}

	// First entry should be msg-(total-capacity); last should be msg-(total-1).
	expectFirst := fmt.Sprintf("msg-%d", total-capacity)
	expectLast := fmt.Sprintf("msg-%d", total-1)
	if entries[0].Message != expectFirst {
		t.Errorf("oldest entry: expected %q, got %q", expectFirst, entries[0].Message)
	}
	if entries[len(entries)-1].Message != expectLast {
		t.Errorf("newest entry: expected %q, got %q", expectLast, entries[len(entries)-1].Message)
	}
}

// TestRingBuffer_ConcurrentPush — N writers push into the same buffer
// concurrently. Run under -race to catch any data races in the head/count
// arithmetic. Final entry count must equal cap (or total pushes if smaller).
func TestRingBuffer_ConcurrentPush(t *testing.T) {
	const capacity = 500
	const writers = 16
	const perWriter = 5000
	r := NewRingBuffer(capacity)

	done := make(chan struct{})
	for w := 0; w < writers; w++ {
		go func(wid int) {
			for i := 0; i < perWriter; i++ {
				r.Push(LogEntry{Message: fmt.Sprintf("w%d-%d", wid, i)})
			}
			done <- struct{}{}
		}(w)
	}
	for i := 0; i < writers; i++ {
		<-done
	}

	if r.Len() != capacity {
		t.Errorf("expected len %d after %d concurrent pushes, got %d",
			capacity, writers*perWriter, r.Len())
	}

	// Sanity: Entries() returns a slice equal to Len.
	entries := r.Entries()
	if len(entries) != r.Len() {
		t.Errorf("Entries() len=%d disagrees with Len()=%d", len(entries), r.Len())
	}
}
