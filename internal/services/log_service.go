package services

import (
	"log/slog"
	"sync/atomic"
	"time"

	"go-python-runner/internal/logging"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// LogService is a Wails service that provides unified logging to the frontend.
type LogService struct {
	logger *slog.Logger
	ring   *logging.RingBuffer
	app    atomic.Pointer[application.App] // set after Wails init, read from goroutines
}

// NewLogService creates a new LogService.
func NewLogService(logger *slog.Logger, ring *logging.RingBuffer) *LogService {
	return &LogService{
		logger: logger,
		ring:   ring,
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

// LogError receives error reports from the frontend.
func (s *LogService) LogError(source, message string, context map[string]string) {
	attrs := []any{
		"source", source,
	}
	for k, v := range context {
		attrs = append(attrs, k, v)
	}
	s.logger.Error(message, attrs...)
}

// GetLogs returns all log entries from the ring buffer.
// Filtering is done client-side because real-time log:entry events bypass
// the backend; keeping the filter logic in one place (the frontend) avoids
// duplicating the predicate.
func (s *LogService) GetLogs() []logging.LogEntry {
	return s.ring.Entries()
}
