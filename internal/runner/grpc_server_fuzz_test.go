package runner

import (
	"context"
	"io"
	"testing"

	"go-python-runner/internal/db"
	pb "go-python-runner/internal/gen"
	"go-python-runner/internal/notify"

	"google.golang.org/grpc/metadata"
)

// noopExecuteServer satisfies pb.PythonRunner_ExecuteServer (which embeds
// grpc.ServerStream) without any real network. Used by FuzzClientMessageHandler
// so handlers that call streamSend don't deref a nil stream.
type noopExecuteServer struct{}

func (noopExecuteServer) Recv() (*pb.ClientMessage, error) { return nil, io.EOF }
func (noopExecuteServer) Send(*pb.ServerMessage) error     { return nil }
func (noopExecuteServer) Context() context.Context         { return context.Background() }
func (noopExecuteServer) SetHeader(metadata.MD) error      { return nil }
func (noopExecuteServer) SendHeader(metadata.MD) error     { return nil }
func (noopExecuteServer) SetTrailer(metadata.MD)           {}
func (noopExecuteServer) SendMsg(any) error                { return nil }
func (noopExecuteServer) RecvMsg(any) error                { return io.EOF }

// FuzzClientMessageHandler drives GRPCServer.handleClientMessage with constructed
// ClientMessage values to exercise the dispatcher with random field content
// (oversized strings, weird unicode, large byte payloads, edge-case ints) and
// verify the handler upholds its flag invariants.
//
// Important contract: this fuzz only constructs messages that match what the
// wire path can actually produce. Protobuf unmarshaling always materializes a
// oneof's sub-message as a non-nil struct (an empty payload becomes
// `&Output{}`, not nil), so we never pass a nil sub-message. Doing so would be
// testing in-process bugs in this fuzz harness, not bugs the handler can hit
// in production.
func FuzzClientMessageHandler(f *testing.F) {
	// variant: selects which oneof case to construct (modulo number of variants;
	//          one extra value leaves the oneof unset, also wire-realistic).
	// s1, s2: arbitrary string fields.
	// i1, i2: arbitrary int32 fields.
	// b: arbitrary bytes (used for DataMsg.Value).
	f.Add(uint8(0), "hello", "world", int32(0), int32(0), []byte("d"))
	f.Add(uint8(1), "", "label", int32(1), int32(10), []byte{})
	f.Add(uint8(2), "completed", "", int32(0), int32(0), []byte{})
	f.Add(uint8(2), "failed", "", int32(0), int32(0), []byte{})
	f.Add(uint8(2), "running", "", int32(0), int32(0), []byte{}) // not a terminal state
	f.Add(uint8(2), "", "", int32(0), int32(0), []byte{})        // empty state string
	f.Add(uint8(3), "err msg", "tb", int32(0), int32(0), []byte{})
	f.Add(uint8(4), "key", "", int32(0), int32(0), []byte("payload"))
	f.Add(uint8(5), "key", "shm", int32(1024), int32(0), []byte{}) // CacheCreate
	f.Add(uint8(6), "key", "", int32(0), int32(0), []byte{})       // CacheLookup
	f.Add(uint8(7), "key", "", int32(0), int32(0), []byte{})       // CacheRelease
	f.Add(uint8(99), "", "", int32(0), int32(0), []byte{})         // unset oneof

	cache := NewCacheManager()
	store, err := db.Open(":memory:")
	if err != nil {
		f.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		f.Fatal(err)
	}
	srv := &GRPCServer{
		runs:      make(map[string]*RunChannel),
		cache:     cache,
		db:        store,
		reservoir: &notify.RecordingReservoir{},
	}

	f.Fuzz(func(t *testing.T, variant uint8, s1, s2 string, i1, i2 int32, b []byte) {
		// Cap fuzz-supplied lengths to keep a single iteration fast.
		if len(s1) > 4096 {
			s1 = s1[:4096]
		}
		if len(s2) > 4096 {
			s2 = s2[:4096]
		}
		if len(b) > 4096 {
			b = b[:4096]
		}

		runID := "fuzz-run"
		ch := &RunChannel{
			Messages:   make(chan Message, messageBufferSize),
			cancel:     make(chan struct{}),
			connected:  make(chan struct{}),
			streamDone: make(chan struct{}),
			stream:     noopExecuteServer{},
		}
		srv.mu.Lock()
		srv.runs[runID] = ch
		srv.mu.Unlock()
		t.Cleanup(func() {
			srv.mu.Lock()
			delete(srv.runs, runID)
			srv.mu.Unlock()
			for {
				select {
				case <-ch.Messages:
				default:
					return
				}
			}
		})

		msg := buildClientMessage(variant, s1, s2, i1, i2, b)

		// Must not panic regardless of input.
		_ = srv.handleClientMessage(runID, ch, msg, noopExecuteServer{})

		// Flag invariant: errorMessage non-nil iff gotError set.
		if ch.gotError.Load() && ch.errorMessage.Load() == nil {
			t.Errorf("gotError set but errorMessage nil")
		}
	})
}

// buildClientMessage constructs a wire-realistic ClientMessage: every set
// oneof variant has a non-nil sub-message, mirroring protobuf unmarshaler
// behavior. Variant N >= 9 leaves the oneof unset (also wire-valid).
func buildClientMessage(variant uint8, s1, s2 string, i1, i2 int32, b []byte) *pb.ClientMessage {
	msg := &pb.ClientMessage{}
	switch variant % 9 {
	case 0:
		msg.Msg = &pb.ClientMessage_Output{Output: &pb.Output{Text: s1}}
	case 1:
		msg.Msg = &pb.ClientMessage_Progress{Progress: &pb.Progress{Current: i1, Total: i2, Label: s2}}
	case 2:
		msg.Msg = &pb.ClientMessage_Status{Status: &pb.Status{State: s1}}
	case 3:
		msg.Msg = &pb.ClientMessage_Error{Error: &pb.Error{Message: s1, Traceback: s2}}
	case 4:
		msg.Msg = &pb.ClientMessage_Data{Data: &pb.DataResult{Key: s1, Value: b}}
	case 5:
		msg.Msg = &pb.ClientMessage_CacheCreate{CacheCreate: &pb.CacheCreateRequest{Key: s1, ShmName: s2, Size: int64(i1)}}
	case 6:
		msg.Msg = &pb.ClientMessage_CacheLookup{CacheLookup: &pb.CacheLookupRequest{Key: s1}}
	case 7:
		msg.Msg = &pb.ClientMessage_CacheRelease{CacheRelease: &pb.CacheRelease{Key: s1}}
	case 8:
		// Unset oneof — msg.Msg stays nil. Dispatcher's type switch falls through.
	}
	return msg
}
