//go:build stress

package stress

import (
	"sync"
	"testing"
	"time"

	"go-python-runner/internal/db"
	pb "go-python-runner/internal/gen"
	"go-python-runner/internal/notify"
	"go-python-runner/internal/runner"

	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// TestRunChannelFlagConsistency drives a real gRPC server with a single
// connected client that sends a randomized burst of Status/Error messages.
// Asserts the RunChannel flag invariants hold under concurrent reads:
//   - errorMessage non-nil iff gotError set
//   - flag transitions are monotonic (set → set, never set → unset)
//   - pythonStatus is first-write-wins: gotFailedStatus and gotCompletedStatus
//     are mutually exclusive (Python is expected to send one terminal status)
func TestRunChannelFlagConsistency(t *testing.T) {
	cache := runner.NewCacheManager()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	srv, err := runner.NewGRPCServer(cache, store, &notify.RecordingReservoir{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	const runID = "stress-run"
	msgCh := srv.RegisterRun(runID)

	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	client := pb.NewPythonRunnerClient(conn)
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("run-id", runID))
	stream, err := client.Execute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	<-srv.WaitConnected(runID)

	// Drain messages on a goroutine so the gRPC handler doesn't block on a full channel.
	var drainerWG sync.WaitGroup
	drainerWG.Add(1)
	go func() {
		defer drainerWG.Done()
		for range msgCh {
		}
	}()

	// Sender: interleaved Error and Status(failed) and Status(completed) messages.
	// After we send any Error, gotError must stay true. After Status(failed),
	// gotFailedStatus must stay true. Same for completed.
	var prevError, prevFailed, prevCompleted bool

	send := func(m *pb.ClientMessage) {
		if err := stream.Send(m); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	for i := 0; i < 200; i++ {
		switch i % 4 {
		case 0:
			send(&pb.ClientMessage{Msg: &pb.ClientMessage_Output{Output: &pb.Output{Text: "x"}}})
		case 1:
			send(&pb.ClientMessage{Msg: &pb.ClientMessage_Error{Error: &pb.Error{Message: "boom", Traceback: "tb"}}})
		case 2:
			send(&pb.ClientMessage{Msg: &pb.ClientMessage_Status{Status: &pb.Status{State: "failed"}}})
		case 3:
			send(&pb.ClientMessage{Msg: &pb.ClientMessage_Status{Status: &pb.Status{State: "completed"}}})
		}

		// Wait briefly for the message to be applied, then sample flags.
		// (Eventual consistency — gRPC is async — so this is a best-effort
		// observation that flags are at-least-monotonic.)
		time.Sleep(1 * time.Millisecond)

		got := srv.GotError(runID)
		if prevError && !got {
			t.Errorf("iter %d: gotError went true→false", i)
		}
		prevError = prevError || got

		if got && srv.ErrorMessage(runID) == "" {
			t.Errorf("iter %d: gotError set but ErrorMessage empty", i)
		}

		gotF := srv.GotFailedStatus(runID)
		if prevFailed && !gotF {
			t.Errorf("iter %d: gotFailedStatus went true→false", i)
		}
		prevFailed = prevFailed || gotF

		gotC := srv.GotCompletedStatus(runID)
		if prevCompleted && !gotC {
			t.Errorf("iter %d: gotCompletedStatus went true→false", i)
		}
		prevCompleted = prevCompleted || gotC
	}

	if !prevError {
		t.Errorf("never observed gotError set")
	}
	// First-write-wins: exactly one of failed/completed must be set, not both.
	// Order is deterministic (iter 2 sends failed before iter 3 sends completed),
	// so gotFailedStatus must win.
	if !prevFailed {
		t.Errorf("never observed gotFailedStatus set (expected: failed wins first-write CAS)")
	}
	if prevCompleted {
		t.Errorf("gotCompletedStatus set despite failed arriving first; first-write-wins violated")
	}

	stream.CloseSend()
	srv.WaitStreamDone(runID, 2*time.Second)
	srv.UnregisterRun(runID)
	drainerWG.Wait()
}
