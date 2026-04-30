package runner

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go-python-runner/internal/db"
	pb "go-python-runner/internal/gen"
	"go-python-runner/internal/notify"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const messageBufferSize = 100

// Message is the interface for typed messages received from Python.
type Message interface {
	messageType()
}

type OutputMsg struct{ Text string }
type ProgressMsg struct{ Current, Total int32; Label string }
type StatusMsg struct{ State RunStatus }
type ErrorMsg struct {
	Message, Traceback string
	// Severity carries the proto runner.Severity enum value verbatim;
	// SEVERITY_UNSPECIFIED (0) is mapped to Error downstream.
	Severity int32
}
type DataMsg struct{ Key string; Value []byte }

func (OutputMsg) messageType()   {}
func (ProgressMsg) messageType() {}
func (StatusMsg) messageType()   {}
func (ErrorMsg) messageType()    {}
func (DataMsg) messageType()     {}

// RunPhase is the lifecycle of a RunChannel. Three phases with callers:
// Registered (constructed), Connected (Python's Execute arrived), Done
// (Messages chan closed). transitionTo enforces monotonic progression.
type RunPhase int32

const (
	PhaseRegistered RunPhase = iota
	PhaseConnected
	PhaseDone
)

func (p RunPhase) String() string {
	switch p {
	case PhaseRegistered:
		return "registered"
	case PhaseConnected:
		return "connected"
	case PhaseDone:
		return "done"
	}
	return "unknown"
}

type RunChannel struct {
	Messages   chan Message
	stream     pb.PythonRunner_ExecuteServer
	streamMu   sync.Mutex // gRPC stream Send() is not safe for concurrent use
	cancel     chan struct{}
	connected  chan struct{} // closed on transition to PhaseConnected
	streamDone chan struct{} // closed when Execute() returns

	phase   atomic.Int32 // RunPhase
	phaseMu sync.Mutex   // serializes Messages-chan close vs concurrent trySend

	// Payloads from Python; orthogonal to phase. pythonStatus carries the
	// terminal status Python reported (Completed | Failed); first-write-wins.
	pythonStatus atomic.Pointer[RunStatus]
	gotError     atomic.Bool
	errorMessage atomic.Pointer[string]
}

func newRunChannel() *RunChannel {
	return &RunChannel{
		Messages:   make(chan Message, messageBufferSize),
		cancel:     make(chan struct{}),
		connected:  make(chan struct{}),
		streamDone: make(chan struct{}),
	}
}

// transitionTo atomically advances phase. Returns true if the transition was
// applied. Invalid transitions (backward, skip-forward to non-Done) return
// false and leave the phase unchanged. Side effects (closing channels) fire
// only on the winning CAS.
func (ch *RunChannel) transitionTo(target RunPhase) bool {
	if target == PhaseDone {
		ch.phaseMu.Lock()
		defer ch.phaseMu.Unlock()
	}
	for {
		cur := RunPhase(ch.phase.Load())
		if !legalPhaseTransition(cur, target) {
			return false
		}
		if ch.phase.CompareAndSwap(int32(cur), int32(target)) {
			switch target {
			case PhaseConnected:
				close(ch.connected)
			case PhaseDone:
				close(ch.Messages)
			}
			return true
		}
	}
}

func legalPhaseTransition(from, to RunPhase) bool {
	if to == PhaseDone {
		return from != PhaseDone
	}
	return from == PhaseRegistered && to == PhaseConnected
}

func (ch *RunChannel) Phase() RunPhase {
	return RunPhase(ch.phase.Load())
}

type GRPCServer struct {
	pb.UnimplementedPythonRunnerServer

	mu        sync.RWMutex
	runs      map[string]*RunChannel
	cache     *CacheManager
	db        *db.DB
	reservoir notify.Reservoir
	server    *grpc.Server
	listener  net.Listener
	dialog    atomic.Value // DialogHandler, set after Wails init
	serveErr  chan error
}

// NewGRPCServer starts a gRPC server on a random localhost port.
func NewGRPCServer(cache *CacheManager, store *db.DB, reservoir notify.Reservoir) (*GRPCServer, error) {
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	s := &GRPCServer{
		runs:      make(map[string]*RunChannel),
		cache:     cache,
		db:        store,
		reservoir: reservoir,
		listener:  lis,
	}

	s.server = grpc.NewServer()
	pb.RegisterPythonRunnerServer(s.server, s)

	s.serveErr = make(chan error, 1)
	go func() {
		if err := s.server.Serve(lis); err != nil {
			s.reservoir.Report(notify.Event{
				Severity:    notify.SeverityError,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "gRPC server error",
				Message:     err.Error(),
				Err:         err,
			})
			s.serveErr <- err
		}
	}()

	return s, nil
}

func (s *GRPCServer) ServeErr() <-chan error {
	return s.serveErr
}

func (s *GRPCServer) SetDialogHandler(d DialogHandler) {
	s.dialog.Store(d)
}

func (s *GRPCServer) Addr() string {
	return s.listener.Addr().String()
}

// RegisterRun creates the message channel for runID. Must be called before
// the Python script connects.
func (s *GRPCServer) RegisterRun(runID string) <-chan Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := newRunChannel()
	s.runs[runID] = ch
	return ch.Messages
}

func (s *GRPCServer) UnregisterRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.runs[runID]; ok {
		ch.transitionTo(PhaseDone)
		delete(s.runs, runID)
	}
}

func (ch *RunChannel) trySend(msg Message) bool {
	ch.phaseMu.Lock()
	defer ch.phaseMu.Unlock()
	if ch.Phase() >= PhaseDone {
		return false
	}
	ch.Messages <- msg
	return true
}


// WaitConnected blocks until the Python client connects, or returns an
// already-closed channel if runID is unknown.
func (s *GRPCServer) WaitConnected(runID string) <-chan struct{} {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		c := make(chan struct{})
		close(c)
		return c
	}
	return ch.connected
}

// WaitStreamDone blocks until Execute() returns for runID. Short-circuits
// when the client never connected (callers invoke this after proc.Wait, so
// "connected still open" reliably means "client never arrived").
func (s *GRPCServer) WaitStreamDone(runID string, timeout time.Duration) {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case <-ch.connected:
		// Client did connect — wait (with timeout) for the stream to fully drain.
	default:
		// Never connected; nothing to wait for.
		return
	}
	select {
	case <-ch.streamDone:
	case <-time.After(timeout):
	}
}

// SendStart delivers the StartRequest to runID's Python client.
func (s *GRPCServer) SendStart(runID string, params map[string]string) error {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run %s not registered", runID)
	}
	ch.streamMu.Lock()
	defer ch.streamMu.Unlock()
	if ch.stream == nil {
		return fmt.Errorf("run %s not connected yet", runID)
	}
	return ch.stream.Send(&pb.ServerMessage{
		Msg: &pb.ServerMessage_Start{
			Start: &pb.StartRequest{Params: params},
		},
	})
}

// SendCancel delivers a CancelRequest to runID's Python client.
func (s *GRPCServer) SendCancel(runID string) error {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run %s not registered", runID)
	}
	ch.streamMu.Lock()
	defer ch.streamMu.Unlock()
	if ch.stream == nil {
		return fmt.Errorf("run %s not connected yet", runID)
	}
	return ch.stream.Send(&pb.ServerMessage{
		Msg: &pb.ServerMessage_Cancel{
			Cancel: &pb.CancelRequest{},
		},
	})
}

// Execute handles the bidirectional stream from a Python client. runID is
// passed via gRPC metadata.
func (s *GRPCServer) Execute(stream pb.PythonRunner_ExecuteServer) error {
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		return fmt.Errorf("missing metadata")
	}
	runIDs := md.Get("run-id")
	if len(runIDs) == 0 {
		return fmt.Errorf("missing run-id in metadata")
	}
	runID := runIDs[0]

	s.mu.Lock()
	ch, exists := s.runs[runID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("unknown run ID: %s", runID)
	}
	ch.stream = stream
	ch.transitionTo(PhaseConnected)
	s.mu.Unlock()
	defer close(ch.streamDone)

	s.reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     "Python client connected",
		RunID:       runID,
	})

	for {
		msg, err := stream.Recv()
		if err != nil {
			s.reservoir.Report(notify.Event{
				Severity:    notify.SeverityInfo,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Message:     fmt.Sprintf("stream ended: %s", err.Error()),
				RunID:       runID,
			})
			return nil
		}
		if sendErr := s.handleClientMessage(runID, ch, msg, stream); sendErr != nil {
			s.reservoir.Report(notify.Event{
				Severity:    notify.SeverityError,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Stream send failed",
				Message:     sendErr.Error(),
				RunID:       runID,
				Err:         sendErr,
			})
			return sendErr
		}
	}
}

func (ch *RunChannel) streamSend(msg *pb.ServerMessage) error {
	ch.streamMu.Lock()
	defer ch.streamMu.Unlock()
	return ch.stream.Send(msg)
}

// handleClientMessage assumes wire-deserialized input: protobuf oneof
// sub-messages are always materialized as non-nil structs (empty payloads
// become zero values, not nil pointers).
func (s *GRPCServer) handleClientMessage(runID string, ch *RunChannel, msg *pb.ClientMessage, stream pb.PythonRunner_ExecuteServer) error {
	switch m := msg.Msg.(type) {
	case *pb.ClientMessage_Output:
		ch.trySend(OutputMsg{Text: m.Output.Text})
	case *pb.ClientMessage_Progress:
		ch.trySend(ProgressMsg{
			Current: m.Progress.Current,
			Total:   m.Progress.Total,
			Label:   m.Progress.Label,
		})
	case *pb.ClientMessage_Status:
		// Python's StatusMsg feeds Manager's status derivation only; do not
		// forward to the channel or it would race the single authoritative
		// run:status event Manager.waitForExit emits. First-write-wins —
		// Python should send at most one terminal status per run.
		var s RunStatus
		switch m.Status.State {
		case string(StatusCompleted):
			s = StatusCompleted
		case string(StatusFailed):
			s = StatusFailed
		default:
			return nil
		}
		ch.pythonStatus.CompareAndSwap(nil, &s)
	case *pb.ClientMessage_Error:
		ch.gotError.Store(true)
		msg := m.Error.Message
		ch.errorMessage.Store(&msg)
		ch.trySend(ErrorMsg{
			Message:   m.Error.Message,
			Traceback: m.Error.Traceback,
			Severity:  int32(m.Error.Severity),
		})
	case *pb.ClientMessage_Data:
		ch.trySend(DataMsg{
			Key:   m.Data.Key,
			Value: m.Data.Value,
		})
	case *pb.ClientMessage_CacheCreate:
		s.handleCacheCreate(runID, m.CacheCreate)
	case *pb.ClientMessage_CacheLookup:
		if err := s.handleCacheLookup(runID, ch, m.CacheLookup); err != nil {
			return err
		}
	case *pb.ClientMessage_CacheRelease:
		s.handleCacheRelease(runID, m.CacheRelease)
	case *pb.ClientMessage_FileDialog:
		return s.handleFileDialog(runID, ch, m.FileDialog)
	case *pb.ClientMessage_DbExecute:
		return s.handleDbExecute(runID, ch, m.DbExecute)
	case *pb.ClientMessage_DbQuery:
		return s.handleDbQuery(runID, ch, m.DbQuery)
	}
	return nil
}

func (s *GRPCServer) handleCacheCreate(runID string, req *pb.CacheCreate) {
	s.cache.Register(req.Key, req.ShmName, req.Size, runID)
	s.reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     fmt.Sprintf("cache block registered: key=%s shm=%s size=%d", req.Key, req.ShmName, req.Size),
		RunID:       runID,
	})
}

func (s *GRPCServer) handleCacheLookup(runID string, ch *RunChannel, req *pb.CacheLookup) error {
	shmName, size, found := s.cache.LookupAndRef(req.Key, runID)
	return ch.streamSend(&pb.ServerMessage{
		Msg: &pb.ServerMessage_CacheInfo{
			CacheInfo: &pb.CacheInfo{
				Key:     req.Key,
				ShmName: shmName,
				Size:    size,
				Found:   found,
			},
		},
	})
}

func (s *GRPCServer) handleCacheRelease(runID string, req *pb.CacheRelease) {
	if released := s.cache.Release(req.Key, runID); released {
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityInfo,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Message:     fmt.Sprintf("cache block released: key=%s", req.Key),
			RunID:       runID,
		})
	} else {
		// Warn+OneShot routes to log-only — the script asked to release a block
		// it didn't reference; the operator should see this in the LogViewer
		// but it doesn't warrant a UI surface.
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Message:     fmt.Sprintf("cache release ignored: run was not referencing key=%s", req.Key),
			RunID:       runID,
		})
	}
}

func (s *GRPCServer) handleFileDialog(runID string, ch *RunChannel, req *pb.FileDialogRequest) error {
	d, _ := s.dialog.Load().(DialogHandler)
	if d == nil {
		return ch.streamSend(&pb.ServerMessage{
			Msg: &pb.ServerMessage_FileDialogResponse{
				FileDialogResponse: &pb.FileDialogResponse{Cancelled: true},
			},
		})
	}

	var filters []FileFilterDef
	for _, f := range req.Filters {
		filters = append(filters, FileFilterDef{DisplayName: f.DisplayName, Pattern: f.Pattern})
	}

	var path string
	var err error
	switch req.Type {
	case "save":
		path, err = d.SaveFile(req.Title, req.Directory, req.Filename, filters)
	default:
		path, err = d.OpenFile(req.Title, req.Directory, filters)
	}

	resp := &pb.FileDialogResponse{}
	switch {
	case errors.Is(err, ErrDialogCancelled):
		// User-driven non-completion is silent — not a failure.
		resp.Cancelled = true
	case err != nil:
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "File dialog failed",
			Message:     err.Error(),
			RunID:       runID,
			Err:         err,
		})
		resp.Cancelled = true
		resp.Error = err.Error()
	case path == "":
		resp.Cancelled = true
	default:
		resp.Paths = []string{path}
	}

	return ch.streamSend(&pb.ServerMessage{
		Msg: &pb.ServerMessage_FileDialogResponse{
			FileDialogResponse: resp,
		},
	})
}

func (s *GRPCServer) handleDbExecute(runID string, ch *RunChannel, req *pb.DbExecute) error {
	resp := &pb.DbResult{}

	args := make([]any, len(req.Params))
	for i, p := range req.Params {
		args[i] = p
	}

	result, err := s.db.Exec(req.Sql, args...)
	if err != nil {
		// Surface DB failures durably at the Go layer instead of relying on
		// Python to re-emit them as ErrorMsgs. Even if the script swallows
		// the SQL error, the operator sees it in the LogViewer.
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "SQL execute failed",
			Message:     fmt.Sprintf("db.Exec: %s", err.Error()),
			RunID:       runID,
			Err:         err,
		})
		resp.Error = err.Error()
	} else {
		if ra, err := result.RowsAffected(); err == nil {
			resp.RowsAffected = ra
		}
		if li, err := result.LastInsertId(); err == nil {
			resp.LastInsertId = li
		}
	}

	return ch.streamSend(&pb.ServerMessage{
		Msg: &pb.ServerMessage_DbResult{DbResult: resp},
	})
}

func (s *GRPCServer) handleDbQuery(runID string, ch *RunChannel, req *pb.DbQuery) error {
	resp := &pb.DbQueryResult{}

	args := make([]any, len(req.Params))
	for i, p := range req.Params {
		args[i] = p
	}

	rows, err := s.db.Query(req.Sql, args...)
	if err != nil {
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "SQL query failed",
			Message:     fmt.Sprintf("db.Query: %s", err.Error()),
			RunID:       runID,
			Err:         err,
		})
		resp.Error = err.Error()
	} else {
		defer rows.Close()
		cols, colErr := rows.Columns()
		if colErr != nil {
			s.reservoir.Report(notify.Event{
				Severity:    notify.SeverityError,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "SQL query failed",
				Message:     fmt.Sprintf("rows.Columns: %s", colErr.Error()),
				RunID:       runID,
				Err:         colErr,
			})
			resp.Error = colErr.Error()
		} else {
			resp.Columns = cols
		}

		for resp.Error == "" && rows.Next() {
			scanArgs := make([]any, len(cols))
			values := make([]*sql.NullString, len(cols))
			for i := range cols {
				values[i] = &sql.NullString{}
				scanArgs[i] = values[i]
			}
			if err := rows.Scan(scanArgs...); err != nil {
				resp.Error = err.Error()
				resp.Rows = nil
				break
			}
			row := &pb.DbRow{Values: make([]string, len(cols))}
			for i, v := range values {
				if v.Valid {
					row.Values[i] = v.String
				}
			}
			resp.Rows = append(resp.Rows, row)
		}
		if err := rows.Err(); err != nil && resp.Error == "" {
			resp.Error = err.Error()
		}
	}

	return ch.streamSend(&pb.ServerMessage{
		Msg: &pb.ServerMessage_DbQueryResult{DbQueryResult: resp},
	})
}

// TrySendError synthesizes an ErrorMsg for the run, used when Python crashed
// without calling fail() and the only signal is captured stderr.
func (s *GRPCServer) TrySendError(runID, message string) {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	ch.trySend(ErrorMsg{Message: message, Traceback: ""})
}

// TrySendStatus delivers the final StatusMsg to runID's channel.
// waitForExit uses this to guarantee a terminal status reaches the frontend
// even when the Python→gRPC path didn't.
func (s *GRPCServer) TrySendStatus(runID string, status RunStatus) {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	ch.trySend(StatusMsg{State: status})
}

func (s *GRPCServer) GotError(runID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.runs[runID]
	if !ok {
		return false
	}
	return ch.gotError.Load()
}

// PythonReportedStatus returns the terminal status Python reported via
// StatusMsg, or "" if none was reported. GotFailedStatus and
// GotCompletedStatus are thin wrappers preserved for tests / external readers.
func (s *GRPCServer) PythonReportedStatus(runID string) RunStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.runs[runID]
	if !ok {
		return ""
	}
	if p := ch.pythonStatus.Load(); p != nil {
		return *p
	}
	return ""
}

func (s *GRPCServer) GotFailedStatus(runID string) bool {
	return s.PythonReportedStatus(runID) == StatusFailed
}

func (s *GRPCServer) GotCompletedStatus(runID string) bool {
	return s.PythonReportedStatus(runID) == StatusCompleted
}

func (s *GRPCServer) ErrorMessage(runID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.runs[runID]
	if !ok {
		return ""
	}
	if p := ch.errorMessage.Load(); p != nil {
		return *p
	}
	return ""
}

func (s *GRPCServer) Stop() {
	s.server.GracefulStop()
}

