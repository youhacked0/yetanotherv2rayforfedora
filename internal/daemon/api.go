package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/notxaked/customv2ray/internal/config"
)

const (
	apiReadTimeout  = 30 * time.Second
	apiWriteTimeout = 30 * time.Second
	// maxRequestSize limits the size of a single request to 1 MiB.
	maxRequestSize = 1 << 20
)

// APIServer listens on a Unix domain socket and handles one request-response
// per connection using JSON-encoded DaemonRequest / DaemonResponse messages.
type APIServer struct {
	socketPath string
	listener   net.Listener
	handler    func(config.DaemonRequest) config.DaemonResponse
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	log        *log.Logger
}

// NewAPIServer creates an APIServer that will listen on the given socketPath.
// Each incoming connection is decoded as a DaemonRequest, dispatched to
// handler, and the DaemonResponse is written back.
func NewAPIServer(socketPath string, handler func(config.DaemonRequest) config.DaemonResponse) *APIServer {
	return &APIServer{
		socketPath: socketPath,
		handler:    handler,
		log:        log.New(os.Stderr, "[api] ", log.LstdFlags|log.Lmsgprefix),
	}
}

// Start begins accepting connections. It removes any stale socket file,
// creates a new Unix listener, and spawns the accept loop.
func (s *APIServer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Clean up a leftover socket file from a previous run.
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.socketPath, err)
	}
	s.listener = ln

	// Make the socket world-accessible so non-root CLI users can query
	// status (the daemon itself runs as root).
	if err := os.Chmod(s.socketPath, 0666); err != nil {
		s.log.Printf("chmod socket: %v", err)
	}

	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// Stop shuts down the accept loop, waits for in-flight connections, and
// removes the socket file.
func (s *APIServer) Stop() error {
	s.cancel()

	if s.listener != nil {
		s.listener.Close()
	}

	s.wg.Wait()

	// Best-effort cleanup.
	os.Remove(s.socketPath)
	s.log.Println("API server stopped")
	return nil
}

// acceptLoop runs until the context is cancelled or the listener is closed.
func (s *APIServer) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// If the context was cancelled the error is expected.
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Printf("accept: %v", err)
			continue
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConnection(conn)
		}()
	}
}

// handleConnection processes a single request-response exchange on conn.
func (s *APIServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	// ── Read request ───────────────────────────────────────────────
	if err := conn.SetReadDeadline(time.Now().Add(apiReadTimeout)); err != nil {
		s.log.Printf("set read deadline: %v", err)
		return
	}

	var req config.DaemonRequest
	decoder := json.NewDecoder(io.LimitReader(conn, maxRequestSize))
	if err := decoder.Decode(&req); err != nil {
		s.writeError(conn, fmt.Sprintf("invalid request: %v", err))
		return
	}

	// ── Dispatch ───────────────────────────────────────────────────
	resp := s.handler(req)

	// ── Write response ─────────────────────────────────────────────
	if err := conn.SetWriteDeadline(time.Now().Add(apiWriteTimeout)); err != nil {
		s.log.Printf("set write deadline: %v", err)
		return
	}

	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		s.log.Printf("write response: %v", err)
	}
}

// writeError sends a failure DaemonResponse with the given message.
func (s *APIServer) writeError(conn net.Conn, msg string) {
	_ = conn.SetWriteDeadline(time.Now().Add(apiWriteTimeout))

	resp := config.DaemonResponse{
		Success: false,
		Message: msg,
	}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		s.log.Printf("write error response: %v", err)
	}
}
