//go:build integration

package integration

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"go-python-runner/internal/runner"
)

func TestParallelRuns(t *testing.T) {
	mgr, _, cleanup := testSetup(t)
	defer cleanup()

	labels := []string{"parallel-A", "parallel-B", "parallel-C"}
	var wg sync.WaitGroup
	results := make([][]runner.Message, len(labels))

	for i, label := range labels {
		wg.Add(1)
		go func(idx int, msg string) {
			defer wg.Done()
			_, msgCh, err := mgr.StartRun("echo", map[string]string{
				"message": msg,
			}, testdataDir(t, "echo_script"))
			if err != nil {
				t.Error(err)
				return
			}
			results[idx] = collectMessages(msgCh, 15*time.Second)
		}(i, label)
	}

	wg.Wait()

	// Verify each run produced messages AND that outputs don't cross-talk
	for i, msgs := range results {
		if len(msgs) == 0 {
			t.Errorf("run %d produced no messages", i)
			continue
		}

		expectedOutput := fmt.Sprintf("echo: %s", labels[i])
		foundOwnOutput := false
		for _, msg := range msgs {
			if out, ok := msg.(runner.OutputMsg); ok {
				if out.Text == expectedOutput {
					foundOwnOutput = true
				}
				// Verify no other run's output leaked into this channel
				for j, other := range labels {
					if j != i {
						otherOutput := fmt.Sprintf("echo: %s", other)
						if out.Text == otherOutput {
							t.Errorf("run %d received output from run %d: %q", i, j, out.Text)
						}
					}
				}
			}
		}
		if !foundOwnOutput {
			t.Errorf("run %d missing expected output %q", i, expectedOutput)
		}
	}
}
