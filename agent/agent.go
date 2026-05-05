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
}

func NewServer(listener net.Listener) *Server {
	return &Server{listener: listener}
}

// Serve accepts until ctx is canceled or the listener errors permanently.
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

func (s *Server) Close() error {
	return s.listener.Close()
}

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

	stdinFrames := make(chan Message, stdinFrameBuffer)
	stdinDone := make(chan struct{})
	go readStdinFrames(ctx, dec, stdinFrames, stdinDone)

	if err := runExec(ctx, first.Argv, first.Env, stdinFrames, enc, &encMu); err != nil {
		logger.Warnf(ctx, "exec session ended with error: %v", err)
	}
	// Close conn so readStdinFrames' blocking Decode returns; otherwise
	// the goroutine leaks until the client side closes first.
	_ = conn.Close()
	<-stdinDone
}

func readStdinFrames(ctx context.Context, dec *Decoder, stdinFrames chan<- Message, done chan<- struct{}) {
	defer close(done)
	defer close(stdinFrames)
	for {
		frame, err := dec.Decode()
		if err != nil {
			return
		}
		select {
		case stdinFrames <- frame:
		case <-ctx.Done():
			return
		}
		if frame.Type == MsgStdinClose {
			return
		}
	}
}
