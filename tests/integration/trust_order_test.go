//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"go-python-runner/internal/runner"
)

// findFinalStatus inspects collected messages and returns the last StatusMsg.
// Manager is the sole emitter of StatusMsg post-Round-1, so this is the
// authoritative final status as seen by the frontend.
func findFinalStatus(msgs []runner.Message) (runner.RunStatus, bool) {
	var status runner.RunStatus
	found := false
	for _, msg := range msgs {
		if s, ok := msg.(runner.StatusMsg); ok {
			status = s.State
			found = true
		}
	}
	return status, found
}

// TestExitZeroWithoutComplete exercises trust-order rule 5: process exits 0
// without ever calling complete() or fail() → Failed (script bug).
func TestExitZeroWithoutComplete(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	_, msgCh, err := mgr.StartRun("exit_zero_no_signal", map[string]string{},
		testdataDir(t, "exit_zero_no_signal"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := collectMessages(msgCh, 15*time.Second)
	status, ok := findFinalStatus(msgs)
	if !ok {
		t.Fatal("no StatusMsg received")
	}
	if status != runner.StatusFailed {
		t.Errorf("expected Failed (rule 5: clean exit without signal), got %s", status)
	}
}

// TestCompleteThenExit1 exercises trust-order rule 1: process exits non-zero
// even after Python sent Status(completed) → Failed (exit code dominates).
func TestCompleteThenExit1(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	_, msgCh, err := mgr.StartRun("complete_then_exit1", map[string]string{},
		testdataDir(t, "complete_then_exit1"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := collectMessages(msgCh, 15*time.Second)
	status, ok := findFinalStatus(msgs)
	if !ok {
		t.Fatal("no StatusMsg received")
	}
	if status != runner.StatusFailed {
		t.Errorf("expected Failed (rule 1: exit code wins over gotCompletedStatus), got %s", status)
	}
}

// TestErrorThenComplete exercises trust-order rule 2: gotError dominates
// gotCompletedStatus, even when Python sends Error followed by Status(completed)
// before exiting cleanly.
func TestErrorThenComplete(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	_, msgCh, err := mgr.StartRun("error_then_complete", map[string]string{},
		testdataDir(t, "error_then_complete"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := collectMessages(msgCh, 15*time.Second)
	status, ok := findFinalStatus(msgs)
	if !ok {
		t.Fatal("no StatusMsg received")
	}
	if status != runner.StatusFailed {
		t.Errorf("expected Failed (rule 2: gotError dominates gotCompletedStatus), got %s", status)
	}

	// Verify the explicit error message reached the message channel.
	var foundExplicitErr bool
	for _, msg := range msgs {
		if e, ok := msg.(runner.ErrorMsg); ok && strings.Contains(e.Message, "explicit error") {
			foundExplicitErr = true
			break
		}
	}
	if !foundExplicitErr {
		t.Error("expected ErrorMsg with 'explicit error' message to reach the channel")
	}
}

// TestKillIgnoresCancel exercises the cancel grace + force-kill path: a script
// that ignores SIGTERM is force-killed after the grace window. Even though the
// kill produces a non-zero exit, trust-order rule 0 (cancelRequested) wins
// and the run resolves to StatusCancelled — the user clicked cancel, that
// intent overrides the kill mechanics. Pre-cancellation-as-status, this
// asserted Failed (rule 1); the contract now expects Cancelled.
func TestKillIgnoresCancel(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	runID, msgCh, err := mgr.StartRun("ignore_sigterm", map[string]string{},
		testdataDir(t, "ignore_sigterm"))
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the script to signal it's running before issuing cancel.
	select {
	case msg, ok := <-msgCh:
		if !ok {
			t.Fatal("channel closed before script started")
		}
		if out, isOut := msg.(runner.OutputMsg); isOut {
			if !strings.Contains(out.Text, "ignoring SIGTERM") {
				t.Logf("got first msg: %q (continuing)", out.Text)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for first message")
	}

	if err := mgr.CancelRun(runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	// The script sleeps 15s ignoring SIGTERM. cancelGracePeriod is 3s, after
	// which Manager force-kills via cmd.Process.Kill(). Wait long enough for
	// the kill + cleanup but well under the script's 15s sleep — if the test
	// has to wait the full 15s, force-kill didn't fire.
	deadline := time.After(8 * time.Second)
	var msgs []runner.Message
collectLoop:
	for {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				break collectLoop
			}
			msgs = append(msgs, msg)
		case <-deadline:
			t.Fatal("timeout — force-kill did not fire within 8s of cancel")
		}
	}

	status, ok := findFinalStatus(msgs)
	if !ok {
		t.Fatal("no StatusMsg received")
	}
	if status != runner.StatusCancelled {
		t.Errorf("expected Cancelled (rule 0: cancelRequested overrides force-kill exit), got %s", status)
	}
}
