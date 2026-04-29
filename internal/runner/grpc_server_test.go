package runner

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"go-python-runner/internal/db"
	pb "go-python-runner/internal/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func testGRPCServer(t *testing.T) (*GRPCServer, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cache := NewCacheManager()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	srv, err := NewGRPCServer(cache, store, logger)
	if err != nil {
		t.Fatal(err)
	}
	return srv, func() { srv.Stop() }
}

func connectClient(t *testing.T, addr, runID string) (pb.PythonRunner_ExecuteClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}

	client := pb.NewPythonRunnerClient(conn)
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("run-id", runID))
	stream, err := client.Execute(ctx)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return stream, func() { conn.Close() }
}

func TestGRPCServer_OutputMessage(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	msgCh := srv.RegisterRun("run-1")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-1")
	defer connCleanup()

	<-srv.WaitConnected("run-1")

	// Send output from "Python"
	err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Output{
			Output: &pb.Output{Text: "Hello from Python"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Receive on Go side
	select {
	case msg := <-msgCh:
		out, ok := msg.(OutputMsg)
		if !ok {
			t.Fatalf("expected OutputMsg, got %T", msg)
		}
		if out.Text != "Hello from Python" {
			t.Errorf("expected 'Hello from Python', got %q", out.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestGRPCServer_ProgressMessage(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	msgCh := srv.RegisterRun("run-2")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-2")
	defer connCleanup()

	<-srv.WaitConnected("run-2")

	err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Progress{
			Progress: &pb.Progress{Current: 3, Total: 10, Label: "Processing"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-msgCh:
		p, ok := msg.(ProgressMsg)
		if !ok {
			t.Fatalf("expected ProgressMsg, got %T", msg)
		}
		if p.Current != 3 || p.Total != 10 || p.Label != "Processing" {
			t.Errorf("unexpected progress: %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

// Status messages from Python are consumed as Manager-internal flags and
// MUST NOT be forwarded to the message channel — Manager is the sole emitter
// of run:status events to the frontend.
func TestGRPCServer_StatusMessage_UpdatesFlagOnly(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	msgCh := srv.RegisterRun("run-3")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-3")
	defer connCleanup()

	<-srv.WaitConnected("run-3")

	if err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Status{Status: &pb.Status{State: "completed"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Allow the server goroutine to process the message.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.GotCompletedStatus("run-3") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !srv.GotCompletedStatus("run-3") {
		t.Fatal("expected GotCompletedStatus(run-3) = true after Status(completed)")
	}
	if srv.GotFailedStatus("run-3") {
		t.Error("did not expect GotFailedStatus(run-3) to be set")
	}

	// Crucially: the message must NOT have been forwarded to the channel.
	select {
	case msg := <-msgCh:
		t.Fatalf("StatusMsg unexpectedly forwarded to channel: %T %+v — Manager must be the sole emitter", msg, msg)
	case <-time.After(200 * time.Millisecond):
		// good — channel is silent, as required.
	}
}

func TestGRPCServer_StatusMessage_FailedSetsFlag(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	srv.RegisterRun("run-3-fail")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-3-fail")
	defer connCleanup()

	<-srv.WaitConnected("run-3-fail")

	if err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Status{Status: &pb.Status{State: "failed"}},
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.GotFailedStatus("run-3-fail") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !srv.GotFailedStatus("run-3-fail") {
		t.Fatal("expected GotFailedStatus(run-3-fail) = true after Status(failed)")
	}
	if srv.GotCompletedStatus("run-3-fail") {
		t.Error("did not expect GotCompletedStatus(run-3-fail) to be set")
	}
}

func TestGRPCServer_CacheCreateAndLookup(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	srv.RegisterRun("run-cache")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-cache")
	defer connCleanup()

	<-srv.WaitConnected("run-cache")

	// Register a cache block
	err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_CacheCreate{
			CacheCreate: &pb.CacheCreate{
				Key:     "features",
				Size:    4096,
				ShmName: "shm_test_001",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Lookup via gRPC — the cache_create is processed server-side before
	// this lookup arrives (same stream, ordered delivery)
	err = stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_CacheLookup{
			CacheLookup: &pb.CacheLookup{Key: "features"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Receive CacheInfo response
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	info := resp.GetCacheInfo()
	if info == nil {
		t.Fatal("expected CacheInfo response")
	}
	if !info.Found || info.ShmName != "shm_test_001" || info.Size != 4096 {
		t.Errorf("unexpected cache info: %+v", info)
	}
}

func TestGRPCServer_SendStart(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	srv.RegisterRun("run-start")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-start")
	defer connCleanup()

	<-srv.WaitConnected("run-start")

	// Send start params from Go to Python
	err := srv.SendStart("run-start", map[string]string{"name": "World"})
	if err != nil {
		t.Fatal(err)
	}

	// Receive on "Python" side
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	start := resp.GetStart()
	if start == nil {
		t.Fatal("expected StartRequest")
	}
	if start.Params["name"] != "World" {
		t.Errorf("expected param name=World, got %v", start.Params)
	}
}

func TestGRPCServer_UnknownRunID(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	// Connect without registering the run — server should reject
	stream, connCleanup := connectClient(t, srv.Addr(), "nonexistent-run")
	defer connCleanup()

	// Try to send a message — should fail because Execute() returned error
	err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Output{Output: &pb.Output{Text: "hello"}},
	})
	if err != nil {
		return // send error is expected
	}

	// If send succeeded, recv should return the error
	_, err = stream.Recv()
	if err == nil {
		t.Error("expected error for unknown run ID, got nil")
	}
}

func TestGRPCServer_SendStartNotRegistered(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	err := srv.SendStart("nonexistent", map[string]string{"a": "b"})
	if err == nil {
		t.Error("expected error for SendStart on unregistered run")
	}
}

func TestGRPCServer_SendStartNotConnected(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	// Register but don't connect a client
	srv.RegisterRun("run-no-client")

	err := srv.SendStart("run-no-client", map[string]string{"a": "b"})
	if err == nil {
		t.Error("expected error for SendStart before client connects")
	}
}

func TestGRPCServer_ErrorMessage(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	msgCh := srv.RegisterRun("run-err")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-err")
	defer connCleanup()

	<-srv.WaitConnected("run-err")

	err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Error{
			Error: &pb.Error{
				Message:   "something broke",
				Traceback: "line 42 in main.py",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-msgCh:
		e, ok := msg.(ErrorMsg)
		if !ok {
			t.Fatalf("expected ErrorMsg, got %T", msg)
		}
		if e.Message != "something broke" {
			t.Errorf("expected message 'something broke', got %q", e.Message)
		}
		if e.Traceback != "line 42 in main.py" {
			t.Errorf("expected traceback 'line 42 in main.py', got %q", e.Traceback)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for error message")
	}

	// Verify the accessor methods that waitForExit relies on for DB persistence.
	if got := srv.ErrorMessage("run-err"); got != "something broke" {
		t.Errorf("ErrorMessage() = %q, want %q", got, "something broke")
	}
	if !srv.GotError("run-err") {
		t.Error("expected GotError to return true")
	}
}

func TestGRPCServer_DbExecuteAndQuery(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	srv.RegisterRun("run-db")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-db")
	defer connCleanup()

	<-srv.WaitConnected("run-db")

	// CREATE TABLE via DbExecute
	err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_DbExecute{
			DbExecute: &pb.DbExecute{
				Sql: "CREATE TABLE test_items (id INTEGER PRIMARY KEY, name TEXT)",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	result := resp.GetDbResult()
	if result == nil {
		t.Fatal("expected DbResult response")
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}

	// INSERT via DbExecute with params
	err = stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_DbExecute{
			DbExecute: &pb.DbExecute{
				Sql:    "INSERT INTO test_items (name) VALUES (?)",
				Params: []string{"hello"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err = stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	result = resp.GetDbResult()
	if result == nil {
		t.Fatal("expected DbResult response")
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.RowsAffected != 1 {
		t.Errorf("expected 1 row affected, got %d", result.RowsAffected)
	}

	// SELECT via DbQuery
	err = stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_DbQuery{
			DbQuery: &pb.DbQuery{
				Sql: "SELECT id, name FROM test_items",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err = stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	qr := resp.GetDbQueryResult()
	if qr == nil {
		t.Fatal("expected DbQueryResult response")
	}
	if qr.Error != "" {
		t.Fatalf("unexpected error: %s", qr.Error)
	}
	if len(qr.Columns) != 2 || qr.Columns[0] != "id" || qr.Columns[1] != "name" {
		t.Errorf("unexpected columns: %v", qr.Columns)
	}
	if len(qr.Rows) != 1 || qr.Rows[0].Values[1] != "hello" {
		t.Errorf("unexpected rows: %v", qr.Rows)
	}
}

func TestGRPCServer_DbQueryError(t *testing.T) {
	srv, cleanup := testGRPCServer(t)
	defer cleanup()

	srv.RegisterRun("run-db-err")
	stream, connCleanup := connectClient(t, srv.Addr(), "run-db-err")
	defer connCleanup()

	<-srv.WaitConnected("run-db-err")

	// Invalid SQL
	err := stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_DbQuery{
			DbQuery: &pb.DbQuery{
				Sql: "SELECT * FROM nonexistent_table",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	qr := resp.GetDbQueryResult()
	if qr == nil {
		t.Fatal("expected DbQueryResult response")
	}
	if qr.Error == "" {
		t.Error("expected error for invalid table, got empty string")
	}
}
