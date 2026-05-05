package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/projecteru2/core/log"
)

// DefaultPort is the default vsock port the agent listens on. 1024 is below
// the privileged port boundary on Linux but vsock has no such concept; the
// number is arbitrary, picked to avoid collision with well-known TCP ports
// in case anyone ever maps vsock<->TCP.
const DefaultPort = 1024

// Listener is the interface a transport-specific listener must implement.
// Production uses vsock.Listen; tests use a TCP or pipe listener so the
// accept loop can run without a kernel vsock module.
type Listener interface {
	Accept() (net.Conn, error)
	Close() error
	Addr() net.Addr
}

// Server runs the agent accept loop. It is goroutine-safe and stops when
// the supplied context is canceled or Close is called.
type Server struct {
	listener Listener
}

// NewServer wraps a listener. The caller owns Listener lifecycle; Close
// calls listener.Close so callers don't need to layer their own defers.
func NewServer(listener Listener) *Server {
	return &Server{listener: listener}
}

// Serve accepts connections until ctx is canceled or the listener returns
// a permanent error. Each accepted connection runs in its own goroutine
// with its own logical "session" — one exec per session.
func (s *Server) Serve(ctx context.Context) error {
	logger := log.WithFunc("agent.Server.Serve")
	logger.Infof(ctx, "agent listening on %s", s.listener.Addr())

	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()

	var connWG sync.WaitGroup
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				connWG.Wait()
				return nil
			}
			logger.Errorf(ctx, err, "accept")
			return fmt.Errorf("accept: %w", err)
		}
		connWG.Add(1)
		go func(c net.Conn) {
			defer connWG.Done()
			s.handleConn(ctx, c)
		}(conn)
	}
}

// Close stops the accept loop. Safe to call multiple times.
func (s *Server) Close() error {
	return s.listener.Close()
}

// handleConn drives one client session. It reads the initial MsgExec frame,
// dispatches to runExec, and propagates further client frames as stdin to
// the running command. The session ends when the command exits (MsgExit)
// or the connection drops.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	logger := log.WithFunc("agent.Server.handleConn")
	defer conn.Close() //nolint:errcheck

	dec := NewDecoder(conn)
	enc := NewEncoder(conn)
	var encMu sync.Mutex

	first, err := dec.Decode()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			logger.Warnf(ctx, "decode initial frame from %s: %v", conn.RemoteAddr(), err)
		}
		return
	}
	if first.Type != MsgExec {
		_ = sendErrorf(enc, &encMu, "expected first frame type %q, got %q", MsgExec, first.Type)
		return
	}

	stdinFrames := make(chan Message, 8) //nolint:mnd
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		for {
			frame, err := dec.Decode()
			if err != nil {
				close(stdinFrames)
				return
			}
			select {
			case stdinFrames <- frame:
			case <-execCtx.Done():
				close(stdinFrames)
				return
			}
			if frame.Type == MsgStdinClose {
				close(stdinFrames)
				return
			}
		}
	}()

	if err := runExec(execCtx, first.Argv, first.Env, stdinFrames, enc, &encMu); err != nil {
		logger.Warnf(ctx, "exec session ended with error: %v", err)
	}
	cancel()
	<-stdinDone
}
