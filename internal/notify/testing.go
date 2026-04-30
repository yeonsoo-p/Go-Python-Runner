package notify

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// RecordingReservoir is a test double that captures every Event passed to
// Report and every banner-modification call. Service tests use it to assert
// the four-part error contract: a method that returns an error must also
// surface an Event with the expected severity/persistence/source.
//
// Naming mirrors net/http/httptest. Construct with &RecordingReservoir{}.
type RecordingReservoir struct {
	mu       sync.Mutex
	events   []Event
	banners  []Event
	dismiss  []string
	replaces []ReplaceCall
}

// ReplaceCall captures a single ReplaceBannersByPrefix invocation.
type ReplaceCall struct {
	Prefix       string
	Replacements []Event
}

// Report records the event.
func (r *RecordingReservoir) Report(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	if ev.Persistence == PersistenceOngoing {
		r.banners = append(r.banners, ev)
	}
}

// DismissBanner records the dismissed key.
func (r *RecordingReservoir) DismissBanner(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dismiss = append(r.dismiss, key)
	kept := r.banners[:0:0]
	for _, b := range r.banners {
		if b.Key != key {
			kept = append(kept, b)
		}
	}
	r.banners = kept
}

// ReplaceBannersByPrefix records the replacement and updates the banner set.
func (r *RecordingReservoir) ReplaceBannersByPrefix(keyPrefix string, replacements []Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replaces = append(r.replaces, ReplaceCall{Prefix: keyPrefix, Replacements: append([]Event(nil), replacements...)})
	kept := r.banners[:0:0]
	for _, b := range r.banners {
		if !hasPrefix(b.Key, keyPrefix) {
			kept = append(kept, b)
		}
	}
	r.banners = append(kept, replacements...)
}

// ListBanners returns a snapshot of recorded ongoing banners.
func (r *RecordingReservoir) ListBanners() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.banners))
	copy(out, r.banners)
	return out
}

// SetApp is a no-op for the recording reservoir.
func (r *RecordingReservoir) SetApp(*application.App) {}

// Events returns a snapshot of every Report call, in order.
func (r *RecordingReservoir) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Dismissals returns a snapshot of every DismissBanner call, in order.
func (r *RecordingReservoir) Dismissals() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.dismiss))
	copy(out, r.dismiss)
	return out
}

// Replacements returns a snapshot of every ReplaceBannersByPrefix call.
func (r *RecordingReservoir) Replacements() []ReplaceCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ReplaceCall, len(r.replaces))
	copy(out, r.replaces)
	return out
}

// FindBySeverity returns events matching the given severity, in order.
func (r *RecordingReservoir) FindBySeverity(s Severity) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, ev := range r.events {
		if ev.Severity == s {
			out = append(out, ev)
		}
	}
	return out
}

// Reset clears all recorded events. Useful between subtests.
func (r *RecordingReservoir) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
	r.banners = nil
	r.dismiss = nil
	r.replaces = nil
}

// ContractExpectation describes the shape of a single Report event a test
// expects to see. Empty fields mean "don't assert on this attribute" so a
// caller can pin only the axes that matter for the contract under test.
type ContractExpectation struct {
	Severity    Severity
	Persistence Persistence
	Source      Source
	RunID       string
	ScriptID    string
	// MessageContains is checked via substring match if non-empty.
	MessageContains string
}

// AssertContract verifies that the most recent event matching want.Severity
// was reported with the expected persistence/source/RunID/ScriptID and (if
// set) MessageContains substring.
//
// Usage:
//
//	rec := &notify.RecordingReservoir{}
//	svc := NewFooService(rec)
//	err := svc.DoThingThatFails()
//	require.Error(t, err)
//	notify.AssertContract(t, rec, notify.ContractExpectation{
//	    Severity:    notify.SeverityError,
//	    Persistence: notify.PersistenceOneShot,
//	    Source:      notify.SourceBackend,
//	})
func AssertContract(t TestingT, rec *RecordingReservoir, want ContractExpectation) {
	t.Helper()
	matches := rec.FindBySeverity(want.Severity)
	if len(matches) == 0 {
		t.Errorf("AssertContract: no event with severity=%s recorded", want.Severity)
		return
	}
	got := matches[len(matches)-1]

	if want.Persistence != got.Persistence {
		t.Errorf("AssertContract: persistence got=%s want=%s", got.Persistence, want.Persistence)
	}
	if want.Source != "" && got.Source != want.Source {
		t.Errorf("AssertContract: source got=%s want=%s", got.Source, want.Source)
	}
	if want.RunID != "" && got.RunID != want.RunID {
		t.Errorf("AssertContract: runID got=%q want=%q", got.RunID, want.RunID)
	}
	if want.ScriptID != "" && got.ScriptID != want.ScriptID {
		t.Errorf("AssertContract: scriptID got=%q want=%q", got.ScriptID, want.ScriptID)
	}
	if want.MessageContains != "" && !containsSubstring(got.Message, want.MessageContains) {
		t.Errorf("AssertContract: message %q does not contain %q", got.Message, want.MessageContains)
	}
}

// TestingT is the minimal subset of *testing.T used by AssertContract.
// Defining it locally keeps notify/testing.go free of a testing import in
// non-test consumers (it would otherwise pull testing into every importer).
type TestingT interface {
	Errorf(format string, args ...any)
	Helper()
}

func containsSubstring(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
