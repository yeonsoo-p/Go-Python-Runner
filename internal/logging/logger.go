package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// NewLogger creates a structured logger that writes to both a rotating log file
// and an in-memory ring buffer. The log file uses JSON lines format.
func NewLogger(logDir string, ring *RingBuffer) (*slog.Logger, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}

	logPath := filepath.Join(logDir, "app.log")
	fileWriter := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    10, // MB
		MaxBackups: 3,
		MaxAge:     28, // days
	}

	fileHandler := slog.NewJSONHandler(fileWriter, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	multi := &multiHandler{
		file: fileHandler,
		ring: ring,
	}

	return slog.New(multi), nil
}

// DefaultLogDir returns the platform-appropriate log directory.
func DefaultLogDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = "."
	}
	return filepath.Join(configDir, "go-python-runner", "logs")
}

// multiHandler fans out slog records to a file handler and a ring buffer.
type multiHandler struct {
	file slog.Handler
	ring *RingBuffer
	attrs []slog.Attr
	group string
}

func (h *multiHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	// Write to file
	if err := h.file.Handle(ctx, r); err != nil {
		return err
	}

	// Extract attributes for ring buffer entry
	entry := LogEntry{
		Timestamp: r.Time,
		Level:     r.Level.String(),
		Message:   r.Message,
	}
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "source":
			entry.Source = a.Value.String()
		case "runID":
			entry.RunID = a.Value.String()
		case "scriptID":
			entry.ScriptID = a.Value.String()
		case "traceback", "stderr":
			entry.Traceback = a.Value.String()
		}
		return true
	})
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	if entry.Source == "" {
		entry.Source = "system"
	}

	h.ring.Push(entry)
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &multiHandler{
		file:  h.file.WithAttrs(attrs),
		ring:  h.ring,
		attrs: append(h.attrs, attrs...),
		group: h.group,
	}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	return &multiHandler{
		file:  h.file.WithGroup(name),
		ring:  h.ring,
		attrs: h.attrs,
		group: name,
	}
}

// NewTestLogger creates a logger that writes to the given writer and ring buffer.
// Useful for testing without creating real log files.
func NewTestLogger(w io.Writer, ring *RingBuffer) *slog.Logger {
	fileHandler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	multi := &multiHandler{
		file: fileHandler,
		ring: ring,
	}
	return slog.New(multi)
}
