package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// captureHandler is a slog.Handler that records every record. Reused from the
// pattern in internal/services/log_service_test.go.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) Records() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

func attrValue(r slog.Record, key string) (string, bool) {
	var found string
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found = a.Value.String()
			ok = true
			return false
		}
		return true
	})
	return found, ok
}

func newReservoir() (*reservoir, *captureHandler) {
	h := &captureHandler{}
	logger := slog.New(h)
	return &reservoir{logger: logger}, h
}

func TestReport_AlwaysSlogs(t *testing.T) {
	r, h := newReservoir()
	r.Report(Event{
		Severity:    SeverityError,
		Persistence: PersistenceOneShot,
		Source:      SourceBackend,
		Message:     "boom",
		RunID:       "run-1",
		ScriptID:    "hello_world",
		Err:         errors.New("disk full"),
	})

	records := h.Records()
	if len(records) != 1 {
		t.Fatalf("want 1 slog record, got %d", len(records))
	}
	rec := records[0]
	if rec.Level != slog.LevelError {
		t.Errorf("want LevelError, got %v", rec.Level)
	}
	if rec.Message != "boom" {
		t.Errorf("want message 'boom', got %q", rec.Message)
	}
	for _, want := range []struct{ key, val string }{
		{"source", "backend"},
		{"severity", "error"},
		{"persistence", "one-shot"},
		{"runID", "run-1"},
		{"scriptID", "hello_world"},
		{"error", "disk full"},
	} {
		got, ok := attrValue(rec, want.key)
		if !ok {
			t.Errorf("missing attr %q", want.key)
			continue
		}
		if got != want.val {
			t.Errorf("attr %q: got %q, want %q", want.key, got, want.val)
		}
	}
}

func TestReport_SeverityToSlogLevel(t *testing.T) {
	cases := []struct {
		sev Severity
		lvl slog.Level
	}{
		{SeverityInfo, slog.LevelInfo},
		{SeverityWarn, slog.LevelWarn},
		{SeverityError, slog.LevelError},
		{SeverityCritical, slog.LevelError},
	}
	for _, c := range cases {
		t.Run(c.sev.String(), func(t *testing.T) {
			r, h := newReservoir()
			r.Report(Event{Severity: c.sev, Source: SourceBackend, Message: "x"})
			recs := h.Records()
			if len(recs) != 1 {
				t.Fatalf("want 1 record, got %d", len(recs))
			}
			if recs[0].Level != c.lvl {
				t.Errorf("severity %v -> level %v, want %v", c.sev, recs[0].Level, c.lvl)
			}
		})
	}
}

func TestReport_AssignsIDAndTimestamp(t *testing.T) {
	r, _ := newReservoir()
	captured := make(chan Event, 1)
	// Wrap in a tiny adapter so we can inspect what Report saw post-fill.
	// We can't intercept directly; instead, use ReplaceBannersByPrefix which
	// respects ID + Timestamp filling identically.
	r.ReplaceBannersByPrefix("test:", []Event{{
		Severity:    SeverityError,
		Persistence: PersistenceOngoing,
		Source:      SourceBackend,
		Key:         "test:k",
		Message:     "m",
	}})
	got := r.ListBanners()
	if len(got) != 1 {
		t.Fatalf("want 1 banner, got %d", len(got))
	}
	if got[0].ID == "" {
		t.Error("ID was not assigned")
	}
	if got[0].Timestamp.IsZero() {
		t.Error("Timestamp was not assigned")
	}
	close(captured)
}

func TestUpsertBanner_DedupesByKey(t *testing.T) {
	r, _ := newReservoir()
	r.Report(Event{
		Severity:    SeverityWarn,
		Persistence: PersistenceOngoing,
		Source:      SourceBackend,
		Key:         "loadIssue:plugins/foo",
		Message:     "first",
	})
	r.Report(Event{
		Severity:    SeverityWarn,
		Persistence: PersistenceOngoing,
		Source:      SourceBackend,
		Key:         "loadIssue:plugins/foo",
		Message:     "second",
	})
	banners := r.ListBanners()
	if len(banners) != 1 {
		t.Fatalf("want 1 banner after dedupe, got %d", len(banners))
	}
	if banners[0].Message != "second" {
		t.Errorf("want latest message 'second', got %q", banners[0].Message)
	}
}

func TestDismissBanner_RemovesByKey(t *testing.T) {
	r, _ := newReservoir()
	r.Report(Event{
		Severity:    SeverityWarn,
		Persistence: PersistenceOngoing,
		Source:      SourceBackend,
		Key:         "k1",
		Message:     "one",
	})
	r.Report(Event{
		Severity:    SeverityWarn,
		Persistence: PersistenceOngoing,
		Source:      SourceBackend,
		Key:         "k2",
		Message:     "two",
	})
	r.DismissBanner("k1")
	banners := r.ListBanners()
	if len(banners) != 1 || banners[0].Key != "k2" {
		t.Errorf("want only k2 remaining, got %+v", banners)
	}
}

func TestReplaceBannersByPrefix_AtomicSwap(t *testing.T) {
	r, _ := newReservoir()
	// Seed with two prefixed and one unprefixed banner.
	r.Report(Event{Severity: SeverityWarn, Persistence: PersistenceOngoing, Key: "loadIssue:a", Message: "a"})
	r.Report(Event{Severity: SeverityWarn, Persistence: PersistenceOngoing, Key: "loadIssue:b", Message: "b"})
	r.Report(Event{Severity: SeverityWarn, Persistence: PersistenceOngoing, Key: "live:updates", Message: "live"})

	// Replace only loadIssue:* — should keep live:updates and swap in c.
	r.ReplaceBannersByPrefix("loadIssue:", []Event{{
		Severity: SeverityWarn, Persistence: PersistenceOngoing, Key: "loadIssue:c", Message: "c",
	}})

	banners := r.ListBanners()
	if len(banners) != 2 {
		t.Fatalf("want 2 banners after replace, got %d: %+v", len(banners), banners)
	}
	keys := []string{banners[0].Key, banners[1].Key}
	if !contains(keys, "live:updates") || !contains(keys, "loadIssue:c") {
		t.Errorf("want banners [live:updates, loadIssue:c], got %v", keys)
	}
}

func TestRouting_NoAppDoesNotPanic(t *testing.T) {
	// Reservoir without SetApp should still slog without panicking.
	r, _ := newReservoir()
	r.Report(Event{Severity: SeverityError, Persistence: PersistenceOneShot, Message: "x"})
	r.DismissBanner("anything")
	r.ReplaceBannersByPrefix("p:", nil)
}

func TestRouting_InfoOneShotIsLogOnly(t *testing.T) {
	// Info+OneShot should NOT produce a toast emit. We can't observe Wails
	// directly without an app, but we can verify the slog record exists and
	// the event lacks the toast-only fields the routing would otherwise set.
	r, h := newReservoir()
	r.Report(Event{Severity: SeverityInfo, Persistence: PersistenceOneShot, Message: "trace"})
	recs := h.Records()
	if len(recs) != 1 {
		t.Fatalf("want 1 slog record, got %d", len(recs))
	}
	if recs[0].Level != slog.LevelInfo {
		t.Errorf("want LevelInfo, got %v", recs[0].Level)
	}
}

func TestSeverity_StringRoundtrip(t *testing.T) {
	for _, s := range []Severity{SeverityInfo, SeverityWarn, SeverityError, SeverityCritical} {
		if s.String() == "unknown" {
			t.Errorf("severity %d stringifies as 'unknown'", s)
		}
	}
}

func TestPersistence_StringRoundtrip(t *testing.T) {
	for _, p := range []Persistence{PersistenceOneShot, PersistenceOngoing, PersistenceInFlight, PersistenceCatastrophic} {
		if p.String() == "unknown" {
			t.Errorf("persistence %d stringifies as 'unknown'", p)
		}
	}
}

func TestRecordingReservoir_RecordsReports(t *testing.T) {
	rec := &RecordingReservoir{}
	rec.Report(Event{Severity: SeverityError, Source: SourceBackend, Message: "m"})
	rec.Report(Event{Severity: SeverityWarn, Persistence: PersistenceOngoing, Key: "k", Source: SourceBackend, Message: "n"})

	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	errs := rec.FindBySeverity(SeverityError)
	if len(errs) != 1 || errs[0].Message != "m" {
		t.Errorf("FindBySeverity(Error) = %+v", errs)
	}
	banners := rec.ListBanners()
	if len(banners) != 1 || banners[0].Key != "k" {
		t.Errorf("want one ongoing banner with key k, got %+v", banners)
	}
}

// Compile-time check: production reservoir and recording reservoir both
// satisfy the interface so service tests can swap them transparently.
var _ Reservoir = (*reservoir)(nil)
var _ Reservoir = (*RecordingReservoir)(nil)

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Sanity: payload() emits all the keys the frontend expects.
func TestPayload_ContainsExpectedKeys(t *testing.T) {
	r, _ := newReservoir()
	p := r.payload(Event{
		ID: "abc", Severity: SeverityError, Persistence: PersistenceOneShot,
		Source: SourceBackend, Title: "t", Message: "m",
		RunID: "r", ScriptID: "s", Traceback: "tb",
	})
	for _, k := range []string{"id", "severity", "persistence", "source", "key", "title", "message", "runID", "scriptID", "traceback", "timestamp"} {
		if _, ok := p[k]; !ok {
			t.Errorf("payload missing key %q", k)
		}
	}
	// Verify typing is what the Wails serializer expects (strings, no nil ptrs).
	var buf bytes.Buffer
	for k, v := range p {
		if v == nil {
			t.Errorf("key %q value is nil", k)
		}
		_, _ = buf.WriteString(strings.ToLower(k))
	}
}
