package services

import (
	"log/slog"

	"go-python-runner/internal/logging"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// LogService is a Wails service that provides unified logging to the frontend.
type LogService struct {
	logger *slog.Logger
	ring   *logging.RingBuffer
	app    *application.App
}

// NewLogService creates a new LogService.
func NewLogService(logger *slog.Logger, ring *logging.RingBuffer) *LogService {
	return &LogService{
		logger: logger,
		ring:   ring,
	}
}

// SetApp sets the Wails app reference for emitting events.
func (s *LogService) SetApp(app *application.App) {
	s.app = app
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

// GetLogs returns filtered log entries from the ring buffer.
func (s *LogService) GetLogs(filter logging.LogFilter) []logging.LogEntry {
	return s.ring.Entries(filter)
}
