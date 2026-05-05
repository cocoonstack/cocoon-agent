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
	// DefaultPort is the default vsock port the agent listens on. The
	// number is arbitrary; vsock has no privileged-port concept. Picked
	// to avoid collision with well-known TCP ports in case anyone ever
	// maps vsock<->TCP.
	DefaultPort = 1024

	// stdinFrameBuffer caps in-flight stdin chunks per session. 8 is
	// enough to keep a fast typist or a `cat largefile | kubectl exec`
	// from stalling on the channel while the child reads.
	stdinFrameBuffer = 8
)

// Server runs the agent accept loop. It is goroutine-safe and stops when
// the supplied context is canceled or Close is called. The listener is a
// plain net.Listener so production (vsock) and tests (loopback TCP) share
// the same wiring.
type Server struct {
	listener net.Listener
}

// NewServer wraps a listener. The caller owns Listener lifecycle; Close
// calls listener.Close so callers don't need to layer their own defers.
func NewServer(listener net.Listener) *Server {
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

// Close stops the accept loop. Idempotent — net.Listener.Close returns
// net.ErrClosed on second call, which Serve treats as a clean shutdown.
func (s *Server) Close() error {
	return s.listener.Close()
}

// handleConn drives one client session: read MsgExec, dispatch to runExec,
// forward subsequent client frames as stdin to the running command, send
// MsgExit on completion. The session ends when the command exits or the
// connection drops.
//
// Lifecycle: we close conn explicitly before waiting on the stdin reader
// so its blocking Read returns. Without that, the reader would hold the
// goroutine open until the client side closed first — a leak when the
// client is well-behaved-but-slow.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	logger := log.WithFunc("agent.Server.handleConn")
	defer conn.Close() //nolint:errcheck // idempotent; main close happens below

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
	// Closing conn unblocks readStdinFrames' Decode call so the goroutine
	// can drain. The deferred Close runs again after we return, harmlessly.
	_ = conn.Close()
	<-stdinDone
}

// readStdinFrames pumps client frames from dec into stdinFrames until the
// client sends MsgStdinClose, the connection drops, or the context fires.
// It owns the channel close so runExec's pumpStdin observes a clean EOF.
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
