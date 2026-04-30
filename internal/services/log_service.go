package services

import (
	"strings"
	"sync/atomic"
	"time"

	"go-python-runner/internal/logging"
	"go-python-runner/internal/notify"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// LogService exposes the log ring buffer to the frontend and forwards
// frontend-originated errors through the reservoir. log:entry events stream
// the ring buffer in real time; that channel is independent of notify:*.
type LogService struct {
	ring      *logging.RingBuffer
	reservoir notify.Reservoir
	app       atomic.Pointer[application.App]
}

func NewLogService(ring *logging.RingBuffer, reservoir notify.Reservoir) *LogService {
	return &LogService{
		ring:      ring,
		reservoir: reservoir,
	}
}

// SetApp wires the app and installs the ring-buffer callback that streams
// log:entry events to the frontend.
func (s *LogService) SetApp(app *application.App) {
	s.app.Store(app)
	s.ring.SetOnPush(func(entry logging.LogEntry) {
		app := s.app.Load()
		if app == nil {
			return
		}
		app.Event.Emit("log:entry", map[string]any{
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

// LogError forwards a frontend-originated error through the reservoir.
// JS-specific keys (stack, source, line, column) are synthesized into
// Traceback when no explicit traceback was provided, so window.onerror /
// unhandledrejection events keep their location info.
func (s *LogService) LogError(source, message string, context map[string]string) {
	runID := context["runID"]
	scriptID := context["scriptID"]
	traceback := context["traceback"]
	if traceback == "" {
		traceback = synthesizeJSTraceback(context)
	}

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

// synthesizeJSTraceback assembles a stack-like string from the diagnostic
// keys window.onerror / unhandledrejection populate in main.tsx. Returns ""
// if none are present.
func synthesizeJSTraceback(context map[string]string) string {
	stack := context["stack"]
	src := context["source"]
	line := context["line"]
	col := context["column"]

	var parts []string
	if src != "" || line != "" || col != "" {
		loc := src
		if line != "" {
			loc += ":" + line
			if col != "" {
				loc += ":" + col
			}
		}
		parts = append(parts, "at "+loc)
	}
	if stack != "" {
		parts = append(parts, stack)
	}
	return strings.Join(parts, "\n")
}

// GetLogs returns the full ring buffer; the frontend filters because
// real-time log:entry events bypass the backend anyway.
func (s *LogService) GetLogs() []logging.LogEntry {
	return s.ring.Entries()
}
