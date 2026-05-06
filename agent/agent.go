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

// Server runs the agent accept loop. One exec per accepted connection.
type Server struct {
	listener net.Listener

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// NewServer wraps listener so callers can drive Serve / Close.
func NewServer(listener net.Listener) *Server {
	return &Server{
		listener: listener,
		conns:    make(map[net.Conn]struct{}),
	}
}

// Serve accepts until ctx is canceled or the listener errors permanently.
// On shutdown it closes the listener AND every in-flight conn so a slow
// or non-reading peer can't hold framedWriter.Write hostage and pin
// connWG.Wait below.
func (s *Server) Serve(ctx context.Context) error {
	logger := log.WithFunc("agent.Server.Serve")
	logger.Infof(ctx, "agent listening on %s", s.listener.Addr())

	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
		s.closeAllConns()
	}()

	var connWG sync.WaitGroup
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				connWG.Wait()
				return nil
			}
			logger.Error(ctx, err, "accept")
			return fmt.Errorf("accept: %w", err)
		}
		connWG.Add(1)
		go func(c net.Conn) {
			defer connWG.Done()
			s.handleConn(ctx, c)
		}(conn)
	}
}

// Close stops the accept loop and tears down every in-flight session.
// Symmetric with the ctx-cancel shutdown path so callers using Close()
// as the shutdown API don't get a Serve that hangs on slow peers.
func (s *Server) Close() error {
	err := s.listener.Close()
	s.closeAllConns()
	return err
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	logger := log.WithFunc("agent.Server.handleConn")
	s.trackConn(conn)
	defer s.untrackConn(conn)
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

	// Per-session ctx so a stdin protocol error can kill the child
	// via runExec's CommandContext.
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	stdinFrames := make(chan Message, stdinFrameBuffer)
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		defer close(stdinFrames)
		for {
			frame, err := dec.Decode()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					// Don't let protocol corruption (malformed JSON,
					// over-limit frame, mid-stream truncation) become
					// a clean child stdin EOF — surface it as MsgError
					// and kill the child.
					_ = sendErrorf(enc, &encMu, "stdin: %v", err)
					execCancel()
				}
				return
			}
			select {
			case stdinFrames <- frame:
			case <-execCtx.Done():
				return
			}
			if frame.Type == MsgStdinClose {
				return
			}
		}
	}()

	if err := runExec(execCtx, first.Argv, first.Env, stdinFrames, enc, &encMu); err != nil {
		logger.Warnf(ctx, "exec session ended with error: %v", err)
	}
	// Close conn so the stdin goroutine's blocking Decode returns;
	// otherwise it leaks until the client side closes first.
	_ = conn.Close()
	<-stdinDone
}

func (s *Server) trackConn(c net.Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrackConn(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) closeAllConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_ = c.Close()
	}
}
