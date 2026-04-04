package logging

import (
	"sync"
	"time"
)

// LogEntry represents a single structured log record.
type LogEntry struct {
	Timestamp time.Time
	Level     string
	Source    string // "frontend", "backend", "python"
	Message   string
	RunID     string
	ScriptID  string
	Traceback string
}

// LogFilter specifies criteria for filtering log entries.
type LogFilter struct {
	Source   string // empty = all sources
	Level   string // empty = all levels
	RunID   string // empty = all runs
	ScriptID string // empty = all scripts
}

// RingBuffer is a fixed-capacity circular buffer of LogEntry values.
// It is safe for concurrent use.
type RingBuffer struct {
	mu       sync.RWMutex
	entries  []LogEntry
	cap      int
	head     int // next write position
	count    int
	onPushMu sync.RWMutex
	onPush   func(LogEntry) // called after each push, outside the write lock
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		entries: make([]LogEntry, capacity),
		cap:     capacity,
	}
}

// SetOnPush registers a callback invoked after each Push, outside the write lock.
func (r *RingBuffer) SetOnPush(fn func(LogEntry)) {
	r.onPushMu.Lock()
	defer r.onPushMu.Unlock()
	r.onPush = fn
}

// Push adds an entry to the ring buffer, evicting the oldest if full.
func (r *RingBuffer) Push(entry LogEntry) {
	r.mu.Lock()
	r.entries[r.head] = entry
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
	r.mu.Unlock()

	r.onPushMu.RLock()
	fn := r.onPush
	r.onPushMu.RUnlock()
	if fn != nil {
		fn(entry)
	}
}

// Len returns the number of entries currently stored.
func (r *RingBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Entries returns all stored entries matching the filter, in chronological order.
func (r *RingBuffer) Entries(filter LogFilter) []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]LogEntry, 0, r.count)
	start := (r.head - r.count + r.cap) % r.cap
	for i := 0; i < r.count; i++ {
		idx := (start + i) % r.cap
		e := r.entries[idx]
		if matchesFilter(e, filter) {
			result = append(result, e)
		}
	}
	return result
}

func matchesFilter(e LogEntry, f LogFilter) bool {
	if f.Source != "" && e.Source != f.Source {
		return false
	}
	if f.Level != "" && e.Level != f.Level {
		return false
	}
	if f.RunID != "" && e.RunID != f.RunID {
		return false
	}
	if f.ScriptID != "" && e.ScriptID != f.ScriptID {
		return false
	}
	return true
}
