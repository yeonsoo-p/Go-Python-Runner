//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"go-python-runner/internal/runner"
)

// TestCacheShareObject verifies the cache contract end-to-end:
//   - producer caches a dict and holds the handle alive
//   - consumer running in parallel calls cache_get and receives the same data
//
// On Windows the producer must outlive the consumer's open() because the OS
// reclaims the shm block once the last handle closes; testdata/cache_producer
// holds for ~10s by default which is comfortably enough for the consumer to
// attach and unpickle.
func TestCacheShareObject(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	// Start producer (holds handle for `hold_seconds`).
	_, prodCh, err := mgr.StartRun("cache_producer", map[string]string{}, testdataDir(t, "cache_producer"))
	if err != nil {
		t.Fatal(err)
	}

	// Wait until the producer reports it cached the block. Only then is it safe
	// to start the consumer — before that, the registry entry isn't yet present.
	if !waitForOutput(prodCh, "cached:shared_data", 15*time.Second, t) {
		t.Fatal("producer did not signal cache_set within timeout")
	}

	// Start consumer concurrently — producer is still holding its handle.
	_, consCh, err := mgr.StartRun("cache_consumer", map[string]string{}, testdataDir(t, "cache_consumer"))
	if err != nil {
		t.Fatal(err)
	}

	// Drain consumer messages and look for the retrieval payload.
	var retrieved string
	var consCompleted bool
	for msg := range readWithTimeout(consCh, 15*time.Second) {
		switch m := msg.(type) {
		case runner.OutputMsg:
			if strings.HasPrefix(m.Text, "retrieved:") {
				retrieved = strings.TrimPrefix(m.Text, "retrieved:")
			}
		case runner.StatusMsg:
			if m.State == runner.StatusCompleted {
				consCompleted = true
			}
		case runner.ErrorMsg:
			t.Errorf("consumer error: %s\n%s", m.Message, m.Traceback)
		}
	}

	if !consCompleted {
		t.Fatal("consumer did not complete")
	}
	const want = `{"key": "value", "nested": {"a": true}, "numbers": [1, 2, 3]}`
	if retrieved != want {
		t.Errorf("consumer retrieved unexpected payload:\n  got:  %s\n  want: %s", retrieved, want)
	}

	// Drain producer (it'll complete once its hold elapses, well after the consumer).
	_ = collectMessages(prodCh, 15*time.Second)
}

// TestCacheCleanupOnCrash verifies that after an owning script crashes, a
// follow-up consumer cannot access the block — the registry must be cleaned
// (no stale "found" entry pointing at reclaimed shm).
func TestCacheCleanupOnCrash(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	// Start crash script — it caches data then os._exit(1).
	_, crashCh, err := mgr.StartRun("cache_crash", map[string]string{}, testdataDir(t, "cache_crash"))
	if err != nil {
		t.Fatal(err)
	}
	_ = collectMessages(crashCh, 15*time.Second)

	// Now run a consumer that asks for crash_data. The behavior we care about is
	// observable from the consumer side: cache_get raises KeyError, which the
	// runner translates into a failed run with that message.
	_, consCh, err := mgr.StartRun("cache_consumer_crashkey", map[string]string{}, testdataDir(t, "cache_consumer_crashkey"))
	if err != nil {
		t.Fatal(err)
	}

	var sawKeyError, sawFailed bool
	for msg := range readWithTimeout(consCh, 15*time.Second) {
		switch m := msg.(type) {
		case runner.ErrorMsg:
			if strings.Contains(m.Message, "crash_data") {
				sawKeyError = true
			}
		case runner.StatusMsg:
			if m.State == runner.StatusFailed {
				sawFailed = true
			}
		}
	}
	if !sawKeyError {
		t.Error("expected consumer's cache_get to raise KeyError for crash_data")
	}
	if !sawFailed {
		t.Error("expected consumer to end with failed status")
	}
}

// readWithTimeout returns a channel that yields messages until the source
// channel closes or the timeout elapses, whichever comes first.
func readWithTimeout(ch <-chan runner.Message, timeout time.Duration) <-chan runner.Message {
	out := make(chan runner.Message, 16)
	go func() {
		defer close(out)
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				out <- msg
			case <-timer.C:
				return
			}
		}
	}()
	return out
}

// waitForOutput blocks until an OutputMsg matching `text` arrives on `ch`,
// or the timeout expires. Returns true on hit. Logs every output for triage.
func waitForOutput(ch <-chan runner.Message, text string, timeout time.Duration, t *testing.T) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return false
			}
			if out, isOutput := msg.(runner.OutputMsg); isOutput {
				t.Logf("producer output: %s", out.Text)
				if out.Text == text {
					return true
				}
			}
		case <-timer.C:
			return false
		}
	}
}
