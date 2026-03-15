package runner

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"go-python-runner/internal/db"
	pb "go-python-runner/internal/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Message is the interface for typed messages received from Python scripts.
type Message interface {
	messageType()
}

type OutputMsg struct{ Text string }
type ProgressMsg struct{ Current, Total int32; Label string }
type StatusMsg struct{ State string }
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
	cancel        chan struct{}
	connected     chan struct{} // closed when Python connects
	connectOnce   sync.Once    // prevents double-close on connected
	closed        bool         // true after UnregisterRun closes Messages
	closedMu      sync.Mutex   // protects closed flag and channel sends
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
	dialog   DialogHandler
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

	go func() {
		if err := s.server.Serve(lis); err != nil {
			logger.Error("gRPC server error", "error", err.Error(), "source", "backend")
		}
	}()

	return s, nil
}

// SetDialogHandler sets the native file dialog handler (called after Wails app init).
func (s *GRPCServer) SetDialogHandler(d DialogHandler) {
	s.dialog = d
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
		Messages:  make(chan Message, 100),
		cancel:    make(chan struct{}),
		connected: make(chan struct{}),
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

// SendStart sends a StartRequest to the Python script for the given run.
func (s *GRPCServer) SendStart(runID string, params map[string]string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.runs[runID]
	if !ok {
		return fmt.Errorf("run %s not registered", runID)
	}
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
	defer s.mu.RUnlock()
	ch, ok := s.runs[runID]
	if !ok {
		return fmt.Errorf("run %s not registered", runID)
	}
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
		ch.trySend(StatusMsg{State: m.Status.State})
	case *pb.ClientMessage_Error:
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
		s.cache.Register(m.CacheCreate.Key, m.CacheCreate.ShmName, m.CacheCreate.Size, runID)
		s.logger.Info("cache block registered",
			"key", m.CacheCreate.Key,
			"shmName", m.CacheCreate.ShmName,
			"size", m.CacheCreate.Size,
			"runID", runID,
			"source", "backend",
		)
	case *pb.ClientMessage_CacheLookup:
		shmName, size, found := s.cache.Lookup(m.CacheLookup.Key)
		if found {
			s.cache.AddRef(m.CacheLookup.Key, runID)
		}
		if err := stream.Send(&pb.ServerMessage{
			Msg: &pb.ServerMessage_CacheInfo{
				CacheInfo: &pb.CacheInfo{
					Key:     m.CacheLookup.Key,
					ShmName: shmName,
					Size:    size,
					Found:   found,
				},
			},
		}); err != nil {
			return fmt.Errorf("send cache info: %w", err)
		}
	case *pb.ClientMessage_CacheRelease:
		s.cache.Release(m.CacheRelease.Key, runID)
		s.logger.Info("cache block released",
			"key", m.CacheRelease.Key,
			"runID", runID,
			"source", "backend",
		)
	case *pb.ClientMessage_FileDialog:
		return s.handleFileDialog(runID, m.FileDialog, stream)
	case *pb.ClientMessage_DbExecute:
		return s.handleDbExecute(runID, m.DbExecute, stream)
	case *pb.ClientMessage_DbQuery:
		return s.handleDbQuery(runID, m.DbQuery, stream)
	}
	return nil
}

func (s *GRPCServer) handleFileDialog(runID string, req *pb.FileDialogRequest, stream pb.PythonRunner_ExecuteServer) error {
	if s.dialog == nil {
		return stream.Send(&pb.ServerMessage{
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
		path, err = s.dialog.SaveFile(req.Title, req.Directory, req.Filename, filters)
	default:
		path, err = s.dialog.OpenFile(req.Title, req.Directory, filters)
	}

	resp := &pb.FileDialogResponse{}
	if err != nil {
		s.logger.Error("file dialog error", "error", err.Error(), "runID", runID, "source", "backend")
		resp.Cancelled = true
	} else if path == "" {
		resp.Cancelled = true
	} else {
		resp.Paths = []string{path}
	}

	return stream.Send(&pb.ServerMessage{
		Msg: &pb.ServerMessage_FileDialogResponse{
			FileDialogResponse: resp,
		},
	})
}

func (s *GRPCServer) handleDbExecute(runID string, req *pb.DbExecute, stream pb.PythonRunner_ExecuteServer) error {
	resp := &pb.DbResult{}

	args := make([]any, len(req.Params))
	for i, p := range req.Params {
		args[i] = p
	}

	result, err := s.db.Exec(req.Sql, args...)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.RowsAffected, _ = result.RowsAffected()
		resp.LastInsertId, _ = result.LastInsertId()
	}

	return stream.Send(&pb.ServerMessage{
		Msg: &pb.ServerMessage_DbResult{DbResult: resp},
	})
}

func (s *GRPCServer) handleDbQuery(runID string, req *pb.DbQuery, stream pb.PythonRunner_ExecuteServer) error {
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
		cols, _ := rows.Columns()
		resp.Columns = cols

		for rows.Next() {
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

	return stream.Send(&pb.ServerMessage{
		Msg: &pb.ServerMessage_DbQueryResult{DbQueryResult: resp},
	})
}

// Stop gracefully shuts down the gRPC server.
func (s *GRPCServer) Stop() {
	s.server.GracefulStop()
}

