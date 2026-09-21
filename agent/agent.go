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

const (
	// DefaultPort is the default vsock port the agent listens on.
	DefaultPort = 1024

	stdinFrameBuffer = 8
)

// Server handles one request per accepted connection.
type Server struct {
	listener net.Listener

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// NewServer wraps listener so callers can drive Serve / Close.
func NewServer(listener net.Listener) *Server {
	return &Server{
		listener: listener,
		conns:    make(map[net.Conn]struct{}),
	}
}

// Serve accepts until ctx is canceled or the listener errors permanently.
func (s *Server) Serve(ctx context.Context) error {
	logger := log.WithFunc("agent.Server.Serve")
	logger.Infof(ctx, "agent listening on %s", s.listener.Addr())

	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()

	var connWG sync.WaitGroup
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				connWG.Wait()
				return nil
			}
			logger.Error(ctx, err, "accept")
			// closing connections releases handlers blocked on peer I/O
			_ = s.Close()
			connWG.Wait()
			return fmt.Errorf("accept: %w", err)
		}
		if !s.trackConn(conn) {
			_ = conn.Close()
			continue
		}
		connWG.Go(func() { s.handleConn(ctx, conn) })
	}
}

// Close closes the listener and all active connections.
func (s *Server) Close() error {
	err := s.listener.Close()
	s.closeAllConns()
	return err
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	logger := log.WithFunc("agent.Server.handleConn")
	defer s.untrackConn(conn)
	defer conn.Close() //nolint:errcheck

	dec := NewDecoder(conn)
	enc := NewEncoder(conn)

	first, err := dec.Decode()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			logger.Warnf(ctx, "decode initial frame from %s: %v", conn.RemoteAddr(), err)
		}
		return
	}
	switch first.Type {
	case MsgExec:
	case MsgReseed:
		if err := runReseed(ctx, first, enc); err != nil {
			logger.Warnf(ctx, "reseed session ended: %v", err)
		}
		return
	default:
		if err := enc.SendErrorf("expected first frame type %q or %q, got %q", MsgExec, MsgReseed, first.Type); err != nil {
			logger.Warnf(ctx, "send rejection error frame to %s: %v", conn.RemoteAddr(), err)
		}
		return
	}

	// peer disconnects must cancel the child without stopping the server
	execCtx, execCancel := context.WithCancelCause(ctx)
	defer execCancel(nil)

	stdinFrames := make(chan Message, stdinFrameBuffer)
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		defer close(stdinFrames)
		defer execCancel(nil)
		stdinClosed := false
		for {
			frame, err := dec.Decode()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					// protocol corruption must not masquerade as a clean stdin EOF
					_ = enc.SendErrorf("stdin: %v", err)
					execCancel(errTerminalFrameSent)
				}
				return
			}
			if stdinClosed {
				continue
			}
			select {
			case stdinFrames <- frame:
			case <-execCtx.Done():
				return
			}
			stdinClosed = frame.Type == MsgStdinClose
		}
	}()

	if err := runExec(execCtx, first.Argv, first.Env, stdinFrames, enc); err != nil {
		logger.Warnf(ctx, "exec session ended: %v", err)
	}
	// cancel unblocks the stdin reader parked on a full send, Close unblocks it parked in Decode
	execCancel(nil)
	_ = conn.Close()
	<-stdinDone
}

func (s *Server) trackConn(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrackConn(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) closeAllConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for c := range s.conns {
		_ = c.Close()
	}
}
