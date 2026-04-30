// Package notify is the central error reservoir. Every event from every
// source flows through Reservoir.Report, which owns the (Severity ×
// Persistence) → UI surface routing and writes the structured slog record
// in the same call.
package notify

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Severity classifies how bad an event is. Mirrors proto runner.Severity and
// runner.py SEVERITY_* constants. Order matters: higher = worse.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarn
	SeverityError
	SeverityCritical
)

// String returns the lowercase canonical name used in Wails event payloads
// and slog "level" attribute formatting.
func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityWarn:
		return "warn"
	case SeverityError:
		return "error"
	case SeverityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Persistence answers: does the user need to take an action that persists
// across renders? It chooses the surface, independently of severity.
type Persistence int

const (
	// PersistenceOneShot — transient toast, auto-dismiss.
	PersistenceOneShot Persistence = iota
	// PersistenceOngoing — sticky banner; cleared by an explicit dismiss
	// (DismissBanner / ReplaceBannersByPrefix) when the underlying condition
	// resolves.
	PersistenceOngoing
	// PersistenceInFlight — streamed into a per-run pane (existing run:error
	// channel). Used for Python-originated errors during an active run.
	PersistenceInFlight
	// PersistenceCatastrophic — full-screen pane. Reserved for unrecoverable
	// failures the user must acknowledge before continuing.
	PersistenceCatastrophic
)

// String returns the kebab-case canonical name used in Wails event payloads.
func (p Persistence) String() string {
	switch p {
	case PersistenceOneShot:
		return "one-shot"
	case PersistenceOngoing:
		return "ongoing"
	case PersistenceInFlight:
		return "in-flight"
	case PersistenceCatastrophic:
		return "catastrophic"
	default:
		return "unknown"
	}
}

// Source identifies where an event originated. Lowercase strings to match
// the slog "source" attribute and frontend filter dropdown — established
// convention; do not change.
type Source string

const (
	SourceBackend  Source = "backend"
	SourcePython   Source = "python"
	SourceFrontend Source = "frontend"
)

// Wails event names. Stay inside the existing namespace:action convention
// (run:*, log:*, scripts:*, env:*).
const (
	EventToast            = "notify:toast"
	EventBanner           = "notify:banner"
	EventBannerDismiss    = "notify:banner:dismiss"
	EventCritical         = "notify:critical"
	EventBannersList      = "notify:banners:list"
	EventRunErrorInFlight = "run:error" // pre-existing per-run pane channel
)

// Event is what callers pass to Report. ID and Timestamp are filled in by
// the Reservoir if zero-valued.
type Event struct {
	ID          string      // unique; assigned by Reservoir if empty
	Severity    Severity
	Persistence Persistence
	Source      Source
	// Key dedupes ongoing banners. If non-empty and Persistence=Ongoing,
	// re-reporting the same Key replaces the prior banner instead of stacking.
	Key       string
	Title     string // short label for toast/banner header (optional)
	Message   string // detail
	RunID     string // optional
	ScriptID  string // optional
	Err       error  // optional; .Error() appended to slog record
	Traceback string // optional (Python errors)
	Timestamp time.Time
}

// Reservoir is the single ingress point for user-visible events.
type Reservoir interface {
	// Report writes the event to slog and routes it to the appropriate Wails
	// surface(s). Safe to call from any goroutine.
	Report(Event)
	// DismissBanner clears an ongoing banner with the given Key. No-op if
	// no banner with that Key exists.
	DismissBanner(key string)
	// ReplaceBannersByPrefix swaps every ongoing banner whose Key starts with
	// keyPrefix for the given replacements. Used by the registry watcher to
	// publish the current set of plugin LoadIssues atomically on each reload.
	ReplaceBannersByPrefix(keyPrefix string, replacements []Event)
	// ListBanners returns a snapshot of currently-ongoing banners. The
	// frontend calls this on (re)connect to populate its banner stack.
	ListBanners() []Event
	// SetApp wires the Wails app reference. Called once after application.New.
	// Until set, events are still slog'd but Wails Emits are dropped silently.
	SetApp(*application.App)
}

type reservoir struct {
	logger *slog.Logger
	app    atomic.Pointer[application.App]

	bannersMu sync.Mutex
	banners   []Event // ongoing banners, append-newest
}

// New creates a Reservoir bound to the given logger. Call SetApp after
// Wails initialization to enable UI-surface emission.
func New(logger *slog.Logger) Reservoir {
	return &reservoir{logger: logger}
}

func (r *reservoir) SetApp(app *application.App) {
	r.app.Store(app)
}

func (r *reservoir) Report(ev Event) {
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}

	r.logSlog(ev)
	// Banner bookkeeping must happen regardless of whether an app is wired,
	// so ListBanners() stays coherent during early startup and tests.
	if ev.Persistence == PersistenceOngoing {
		r.upsertBanner(ev)
	}
	r.routeWails(ev)
}

func (r *reservoir) logSlog(ev Event) {
	attrs := []any{
		"source", string(ev.Source),
		"severity", ev.Severity.String(),
		"persistence", ev.Persistence.String(),
		"id", ev.ID,
	}
	if ev.Key != "" {
		attrs = append(attrs, "key", ev.Key)
	}
	if ev.RunID != "" {
		attrs = append(attrs, "runID", ev.RunID)
	}
	if ev.ScriptID != "" {
		attrs = append(attrs, "scriptID", ev.ScriptID)
	}
	if ev.Err != nil {
		attrs = append(attrs, "error", ev.Err.Error())
	}
	if ev.Traceback != "" {
		attrs = append(attrs, "traceback", ev.Traceback)
	}

	msg := ev.Message
	if msg == "" && ev.Err != nil {
		msg = ev.Err.Error()
	}

	switch ev.Severity {
	case SeverityInfo:
		r.logger.Info(msg, attrs...)
	case SeverityWarn:
		r.logger.Warn(msg, attrs...)
	case SeverityError, SeverityCritical:
		r.logger.Error(msg, attrs...)
	default:
		r.logger.Error(msg, attrs...)
	}
}

func (r *reservoir) routeWails(ev Event) {
	app := r.app.Load()
	if app == nil {
		return
	}

	payload := r.payload(ev)

	// Critical always gets the full-screen pane regardless of persistence.
	if ev.Severity == SeverityCritical {
		app.Event.Emit(EventCritical, payload)
		return
	}

	switch ev.Persistence {
	case PersistenceCatastrophic:
		app.Event.Emit(EventCritical, payload)

	case PersistenceOngoing:
		// Banner state already updated in Report(); just emit the event.
		app.Event.Emit(EventBanner, payload)

	case PersistenceInFlight:
		// Per-run pane channel. Goes only to errors of an active run; warns
		// in-flight are still toasts so they don't get lost in a collapsed card.
		if ev.Severity >= SeverityError && ev.RunID != "" {
			app.Event.Emit(EventRunErrorInFlight, map[string]any{
				"runID":     ev.RunID,
				"scriptID":  ev.ScriptID,
				"message":   ev.Message,
				"traceback": ev.Traceback,
				"severity":  ev.Severity.String(),
			})
		} else {
			app.Event.Emit(EventToast, payload)
		}

	case PersistenceOneShot:
		// Info-OneShot is log-only by default — info that's worth a toast
		// should be sent as Warn or with explicit Persistence above.
		if ev.Severity == SeverityInfo {
			return
		}
		app.Event.Emit(EventToast, payload)
	}
}

func (r *reservoir) payload(ev Event) map[string]any {
	return map[string]any{
		"id":          ev.ID,
		"severity":    ev.Severity.String(),
		"persistence": ev.Persistence.String(),
		"source":      string(ev.Source),
		"key":         ev.Key,
		"title":       ev.Title,
		"message":     ev.Message,
		"runID":       ev.RunID,
		"scriptID":    ev.ScriptID,
		"traceback":   ev.Traceback,
		"timestamp":   ev.Timestamp.Format(time.RFC3339),
	}
}

func (r *reservoir) upsertBanner(ev Event) {
	r.bannersMu.Lock()
	defer r.bannersMu.Unlock()
	if ev.Key != "" {
		for i, existing := range r.banners {
			if existing.Key == ev.Key {
				r.banners[i] = ev
				return
			}
		}
	}
	r.banners = append(r.banners, ev)
}

func (r *reservoir) DismissBanner(key string) {
	if key == "" {
		return
	}
	r.bannersMu.Lock()
	for i, existing := range r.banners {
		if existing.Key == key {
			r.banners = append(r.banners[:i], r.banners[i+1:]...)
			break
		}
	}
	r.bannersMu.Unlock()

	if app := r.app.Load(); app != nil {
		app.Event.Emit(EventBannerDismiss, map[string]any{"key": key})
	}
}

func (r *reservoir) ReplaceBannersByPrefix(keyPrefix string, replacements []Event) {
	r.bannersMu.Lock()
	kept := r.banners[:0:0]
	for _, b := range r.banners {
		if !hasPrefix(b.Key, keyPrefix) {
			kept = append(kept, b)
		}
	}
	for _, ev := range replacements {
		if ev.ID == "" {
			ev.ID = uuid.NewString()
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now()
		}
		kept = append(kept, ev)
	}
	r.banners = kept
	snapshot := append([]Event(nil), r.banners...)
	r.bannersMu.Unlock()

	// Slog and route each replacement individually so they reach the LogViewer.
	for _, ev := range replacements {
		r.logSlog(ev)
	}

	if app := r.app.Load(); app != nil {
		payloads := make([]map[string]any, 0, len(snapshot))
		for _, b := range snapshot {
			payloads = append(payloads, r.payload(b))
		}
		app.Event.Emit(EventBannersList, map[string]any{"banners": payloads})
	}
}

func (r *reservoir) ListBanners() []Event {
	r.bannersMu.Lock()
	defer r.bannersMu.Unlock()
	out := make([]Event, len(r.banners))
	copy(out, r.banners)
	return out
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
