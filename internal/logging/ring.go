package logging

import (
	"sync"
	"time"
)

type LogEntry struct {
	Timestamp time.Time
	Level     string
	Source    string // "frontend", "backend", "python"
	Message   string
	RunID     string
	ScriptID  string
	Traceback string
}

// RingBuffer is a thread-safe fixed-capacity circular buffer of LogEntry.
type RingBuffer struct {
	mu       sync.RWMutex
	entries  []LogEntry
	cap      int
	head     int
	count    int
	onPushMu sync.RWMutex
	onPush   func(LogEntry)
}

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

// Push appends an entry, evicting the oldest if full.
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

func (r *RingBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Entries returns stored entries in chronological order. The LogViewer
// applies any source/level/runID filtering client-side.
func (r *RingBuffer) Entries() []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]LogEntry, 0, r.count)
	start := (r.head - r.count + r.cap) % r.cap
	for i := 0; i < r.count; i++ {
		idx := (start + i) % r.cap
		result = append(result, r.entries[idx])
	}
	return result
}
