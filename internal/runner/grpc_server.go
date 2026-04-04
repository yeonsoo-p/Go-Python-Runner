package runner

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go-python-runner/internal/db"
	pb "go-python-runner/internal/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// messageBufferSize is the capacity of the per-run message channel.
const messageBufferSize = 100

// Message is the interface for typed messages received from Python scripts.
type Message interface {
	messageType()
}

type OutputMsg struct{ Text string }
type ProgressMsg struct{ Current, Total int32; Label string }
type StatusMsg struct{ State RunStatus }
type ErrorMsg struct{ Message, Traceback string }
type DataMsg struct{ Key string; Value []byte }

func (OutputMsg) messageType()   {}
func (ProgressMsg) messageType() {}
func (StatusMsg) messageType()   {}
func (ErrorMsg) messageType()    {}
func (DataMsg) messageType()     {}

// RunChannel holds the message channel and server-to-client stream for a run.
type RunChannel struct {
	Messages      chan Message
	stream        pb.PythonRunner_ExecuteServer
	streamMu      sync.Mutex   // protects stream.Send() — gRPC streams are not safe for concurrent sends
	cancel        chan struct{}
	connected     chan struct{} // closed when Python connects
	connectOnce   sync.Once    // prevents double-close on connected
	streamDone    chan struct{} // closed when Execute() returns (all messages forwarded)
	closed        bool         // true after UnregisterRun closes Messages
	closedMu      sync.Mutex   // protects closed flag and channel sends
	gotError        atomic.Bool  // true if an ErrorMsg was received via gRPC
	gotFailedStatus atomic.Bool  // true if a StatusMsg with state "failed" was received
	errorMessage    atomic.Value // stores the last ErrorMsg text (string) for DB persistence
}

// DialogHandler opens native file dialogs. Implemented by the Wails app layer.
type DialogHandler interface {
	OpenFile(title string, directory string, filters []FileFilterDef) (string, error)
	SaveFile(title string, directory string, filename string, filters []FileFilterDef) (string, error)
}

// FileFilterDef describes a file type filter for dialogs.
type FileFilterDef struct {
	DisplayName string
	Pattern     string
}

// GRPCServer implements the PythonRunner gRPC service.
type GRPCServer struct {
	pb.UnimplementedPythonRunnerServer

	mu       sync.RWMutex
	runs     map[string]*RunChannel // runID -> channel
	cache    *CacheManager
	db       *db.DB
	logger   *slog.Logger
	server   *grpc.Server
	listener net.Listener
	dialog   atomic.Value // stores DialogHandler; set after Wails init, read from goroutines
	serveErr chan error   // buffered(1), receives Serve() error if it fails
}

// NewGRPCServer creates and starts a gRPC server on a random port.
func NewGRPCServer(cache *CacheManager, store *db.DB, logger *slog.Logger) (*GRPCServer, error) {
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	s := &GRPCServer{
		runs:     make(map[string]*RunChannel),
		cache:    cache,
		db:       store,
		logger:   logger,
		listener: lis,
	}

	s.server = grpc.NewServer()
	pb.RegisterPythonRunnerServer(s.server, s)

	s.serveErr = make(chan error, 1)
	go func() {
		if err := s.server.Serve(lis); err != nil {
			logger.Error("gRPC server error", "error", err.Error(), "source", "backend")
			s.serveErr <- err
		}
	}()

	return s, nil
}

// ServeErr returns a channel that receives an error if the gRPC server fails at runtime.
func (s *GRPCServer) ServeErr() <-chan error {
	return s.serveErr
}

// SetDialogHandler sets the native file dialog handler (called after Wails app init).
func (s *GRPCServer) SetDialogHandler(d DialogHandler) {
	s.dialog.Store(d)
}

// Addr returns the address the gRPC server is listening on.
func (s *GRPCServer) Addr() string {
	return s.listener.Addr().String()
}

// RegisterRun creates a message channel for a run. Must be called before
// the Python script connects.
func (s *GRPCServer) RegisterRun(runID string) <-chan Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := &RunChannel{
		Messages:   make(chan Message, messageBufferSize),
		cancel:     make(chan struct{}),
		connected:  make(chan struct{}),
		streamDone: make(chan struct{}),
	}
	s.runs[runID] = ch
	return ch.Messages
}

// UnregisterRun removes a run's channel.
func (s *GRPCServer) UnregisterRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.runs[runID]; ok {
		ch.closedMu.Lock()
		ch.closed = true
		close(ch.Messages)
		ch.closedMu.Unlock()
		delete(s.runs, runID)
	}
}

// trySend safely sends a message to the run channel, returning false if closed.
func (ch *RunChannel) trySend(msg Message) bool {
	ch.closedMu.Lock()
	defer ch.closedMu.Unlock()
	if ch.closed {
		return false
	}
	ch.Messages <- msg
	return true
}

// WaitConnected blocks until the Python client for a run has connected,
// or the context is cancelled.
func (s *GRPCServer) WaitConnected(runID string) <-chan struct{} {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		// Return already-closed channel
		c := make(chan struct{})
		close(c)
		return c
	}
	return ch.connected
}

// WaitStreamDone blocks until the gRPC Execute() handler returns for the given run,
// meaning all Python messages have been forwarded to the Messages channel.
func (s *GRPCServer) WaitStreamDone(runID string, timeout time.Duration) {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case <-ch.streamDone:
	case <-time.After(timeout):
	}
}

// SendStart sends a StartRequest to the Python script for the given run.
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

// SendCancel sends a CancelRequest to the Python script for the given run.
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

// Execute handles a bidirectional gRPC stream from a Python client.
// The runID is passed via gRPC metadata.
func (s *GRPCServer) Execute(stream pb.PythonRunner_ExecuteServer) error {
	// Extract runID from metadata
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
	ch.connectOnce.Do(func() { close(ch.connected) })
	s.mu.Unlock()
	defer close(ch.streamDone)

	s.logger.Info("Python client connected", "runID", runID, "source", "backend")

	// Read messages from Python
	for {
		msg, err := stream.Recv()
		if err != nil {
			s.logger.Debug("stream ended", "runID", runID, "error", err.Error(), "source", "backend")
			return nil
		}
		if sendErr := s.handleClientMessage(runID, ch, msg, stream); sendErr != nil {
			s.logger.Error("stream send failed, closing", "runID", runID, "error", sendErr.Error(), "source", "backend")
			return sendErr
		}
	}
}

// streamSend sends a ServerMessage on the gRPC stream, protected by streamMu.
func (ch *RunChannel) streamSend(msg *pb.ServerMessage) error {
	ch.streamMu.Lock()
	defer ch.streamMu.Unlock()
	return ch.stream.Send(msg)
}

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
		if m.Status.State == string(StatusFailed) {
			ch.gotFailedStatus.Store(true)
		}
		ch.trySend(StatusMsg{State: RunStatus(m.Status.State)})
	case *pb.ClientMessage_Error:
		ch.gotError.Store(true)
		ch.errorMessage.Store(m.Error.Message)
		ch.trySend(ErrorMsg{
			Message:   m.Error.Message,
			Traceback: m.Error.Traceback,
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
	s.logger.Info("cache block registered",
		"key", req.Key,
		"shmName", req.ShmName,
		"size", req.Size,
		"runID", runID,
		"source", "backend",
	)
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
		s.logger.Info("cache block released",
			"key", req.Key,
			"runID", runID,
			"source", "backend",
		)
	} else {
		s.logger.Warn("cache release ignored: run was not referencing this block",
			"key", req.Key,
			"runID", runID,
			"source", "backend",
		)
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
	if err != nil {
		s.logger.Error("file dialog error", "error", err.Error(), "runID", runID, "source", "backend")
		resp.Cancelled = true
		resp.Error = err.Error()
	} else if path == "" {
		resp.Cancelled = true
	} else {
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
		resp.Error = err.Error()
	} else {
		defer rows.Close()
		cols, colErr := rows.Columns()
		if colErr != nil {
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

// TrySendError sends a synthetic ErrorMsg to a run's message channel.
// Used to forward stderr output to the frontend when Python crashes without calling fail().
func (s *GRPCServer) TrySendError(runID, message string) {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	ch.trySend(ErrorMsg{Message: message, Traceback: ""})
}

// TrySendStatus sends a final StatusMsg to a run's message channel.
// Used by waitForExit to guarantee the frontend receives a terminal status
// even if the Python→gRPC path failed to deliver one.
func (s *GRPCServer) TrySendStatus(runID string, status RunStatus) {
	s.mu.RLock()
	ch, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	ch.trySend(StatusMsg{State: status})
}

// GotError returns whether the run received a structured error via gRPC.
func (s *GRPCServer) GotError(runID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.runs[runID]
	if !ok {
		return false
	}
	return ch.gotError.Load()
}

// GotFailedStatus returns whether the run received a "failed" StatusMsg via gRPC.
func (s *GRPCServer) GotFailedStatus(runID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.runs[runID]
	if !ok {
		return false
	}
	return ch.gotFailedStatus.Load()
}

// ErrorMessage returns the last structured error message text received via gRPC, if any.
func (s *GRPCServer) ErrorMessage(runID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.runs[runID]
	if !ok {
		return ""
	}
	if v := ch.errorMessage.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// Stop gracefully shuts down the gRPC server.
func (s *GRPCServer) Stop() {
	s.server.GracefulStop()
}

