package services

import (
	"sync/atomic"
	"time"

	"go-python-runner/internal/logging"
	"go-python-runner/internal/notify"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// LogService is a Wails service that exposes the unified log ring buffer to
// the frontend and routes frontend-originated errors through the central
// reservoir. Streamed log:entry events are how the LogViewer pane stays
// real-time; that channel is independent of the notify:* surfaces, which
// the reservoir handles directly.
type LogService struct {
	ring      *logging.RingBuffer
	reservoir notify.Reservoir
	app       atomic.Pointer[application.App] // set after Wails init, read from goroutines
}

// NewLogService creates a new LogService. The reservoir is the sole
// observability dependency; LogError forwards frontend errors through it
// so they land in slog (and the ring buffer) plus the toast surface in
// one call.
func NewLogService(ring *logging.RingBuffer, reservoir notify.Reservoir) *LogService {
	return &LogService{
		ring:      ring,
		reservoir: reservoir,
	}
}

// SetApp sets the Wails app reference for emitting events.
// It also registers a ring buffer callback to stream log entries to the frontend.
func (s *LogService) SetApp(app *application.App) {
	s.app.Store(app)
	s.ring.SetOnPush(func(entry logging.LogEntry) {
		a := s.app.Load()
		if a == nil {
			return
		}
		a.Event.Emit("log:entry", map[string]any{
			"Timestamp": entry.Timestamp.Format(time.RFC3339),
			"Level":     entry.Level,
			"Source":    entry.Source,
			"Message":   entry.Message,
			"RunID":     entry.RunID,
			"ScriptID":  entry.ScriptID,
			"Traceback": entry.Traceback,
		})
	})
}

// LogError receives error reports from the frontend and routes them through
// the reservoir so they reach slog (LogViewer + log file) and the toast
// surface in one call. The optional context map becomes a flattened
// "key=value, key=value" suffix on the message — the structured attrs
// already capture source/runID/scriptID via the Event fields.
func (s *LogService) LogError(source, message string, context map[string]string) {
	runID := context["runID"]
	scriptID := context["scriptID"]
	traceback := context["traceback"]

	s.reservoir.Report(notify.Event{
		Severity:    notify.SeverityError,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.Source(source),
		Message:     message,
		RunID:       runID,
		ScriptID:    scriptID,
		Traceback:   traceback,
	})
}

// GetLogs returns all log entries from the ring buffer.
// Filtering is done client-side because real-time log:entry events bypass
// the backend; keeping the filter logic in one place (the frontend) avoids
// duplicating the predicate.
func (s *LogService) GetLogs() []logging.LogEntry {
	return s.ring.Entries()
}
