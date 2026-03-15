//go:build integration

package integration

import (
	"testing"
	"time"

	"go-python-runner/internal/runner"
)

func TestPartialFailure(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	_, msgCh, err := mgr.StartRun("partial_fail", map[string]string{}, testdataDir(t, "partial_fail"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := collectMessages(msgCh, 15*time.Second)
	if len(msgs) == 0 {
		t.Fatal("expected messages from partial_fail script")
	}

	var outputs []string
	var progressCount int
	var hasError bool
	var errorMsg string
	var hasFailed bool

	for _, msg := range msgs {
		switch m := msg.(type) {
		case runner.OutputMsg:
			outputs = append(outputs, m.Text)
			t.Logf("Output: %s", m.Text)
		case runner.ProgressMsg:
			progressCount++
			t.Logf("Progress: %d/%d %s", m.Current, m.Total, m.Label)
		case runner.ErrorMsg:
			hasError = true
			errorMsg = m.Message
			t.Logf("Error: %s (traceback: %s)", m.Message, m.Traceback)
			if m.Traceback == "" {
				t.Error("error message should include a traceback")
			}
		case runner.StatusMsg:
			if m.State == "failed" {
				hasFailed = true
			}
			t.Logf("Status: %s", m.State)
		}
	}

	// Verify partial work was done before failure
	if len(outputs) < 2 {
		t.Errorf("expected at least 2 output messages before failure, got %d", len(outputs))
	}
	if progressCount < 2 {
		t.Errorf("expected at least 2 progress updates before failure, got %d", progressCount)
	}

	// Verify error was reported
	if !hasError {
		t.Error("expected an ErrorMsg from partial_fail script")
	}
	if errorMsg != "intentional failure at step 3" {
		t.Errorf("expected specific error message, got %q", errorMsg)
	}
	if !hasFailed {
		t.Error("expected StatusMsg with state='failed'")
	}
}
