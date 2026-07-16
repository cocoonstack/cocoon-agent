package agent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cocoonstack/cocoon-agent/agent"
	"github.com/cocoonstack/cocoon-agent/client"
)

const (
	initialGoroutineDumpSize = 1 << 16
	maxGoroutineDumpSize     = 1 << 24

	serveWatcherFrame = "(*Server).Serve.func1"
	handleConnFrame   = "(*Server).handleConn"
)

func TestServerExecHelloWorld(t *testing.T) {
	t.Parallel()
	ctx, conn := dialTestServer(t)

	var stdout, stderr bytes.Buffer
	exit, err := client.Run(ctx, conn, []string{"sh", "-c", "echo hello && echo bye 1>&2"}, nil, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("client run: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if got := strings.TrimSpace(stdout.String()); got != "hello" {
		t.Errorf("stdout = %q, want \"hello\"", got)
	}
	if got := strings.TrimSpace(stderr.String()); got != "bye" {
		t.Errorf("stderr = %q, want \"bye\"", got)
	}
}

func TestServerExecBackgroundChildDoesNotPinSession(t *testing.T) {
	t.Parallel()
	ctx, conn := dialTestServer(t)

	var stdout bytes.Buffer
	start := time.Now()
	exit, err := client.Run(ctx, conn, []string{"sh", "-c", "sleep 6 & echo started"}, nil, nil, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("client run: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if got := strings.TrimSpace(stdout.String()); got != "started" {
		t.Errorf("stdout = %q, want \"started\"", got)
	}
	// Without WaitDelay the session is pinned until the background sleep exits.
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("session took %v, want < 4s (pinned by background child)", elapsed)
	}
}

func TestServerPropagatesNonZeroExit(t *testing.T) {
	t.Parallel()
	ctx, conn := dialTestServer(t)

	exit, err := client.Run(ctx, conn, []string{"sh", "-c", "exit 7"}, nil, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("client run: %v", err)
	}
	if exit != 7 {
		t.Errorf("exit = %d, want 7", exit)
	}
}

func TestServerStreamsStdin(t *testing.T) {
	t.Parallel()
	ctx, conn := dialTestServer(t)

	var stdout bytes.Buffer
	exit, err := client.Run(ctx, conn, []string{"cat"}, nil, strings.NewReader("hello-stdin\n"), &stdout, io.Discard)
	if err != nil {
		t.Fatalf("client run: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if got := strings.TrimSpace(stdout.String()); got != "hello-stdin" {
		t.Errorf("stdout = %q, want \"hello-stdin\"", got)
	}
}

// TestServerMsgStdinCloseTerminatesChildStdin: child must see EOF after the
// close frame and exit 0; wc -c also confirms pre-close payload arrived.
func TestServerMsgStdinCloseTerminatesChildStdin(t *testing.T) {
	t.Parallel()
	ctx, conn := dialTestServer(t)

	payload := "abcde"
	var stdout bytes.Buffer
	exit, err := client.Run(
		ctx, conn,
		[]string{"sh", "-c", "wc -c"},
		nil,
		strings.NewReader(payload),
		&stdout, io.Discard,
	)
	if err != nil {
		t.Fatalf("client run: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if got := strings.TrimSpace(stdout.String()); got != "5" {
		t.Errorf("wc -c stdout = %q, want \"5\"", got)
	}
}

func TestServerRejectsNonExecFirstFrame(t *testing.T) {
	t.Parallel()
	_, conn := dialTestServer(t)

	enc := agent.NewEncoder(conn)
	if err := enc.Encode(agent.Message{Type: agent.MsgStdin, Data: []byte("nope")}); err != nil {
		t.Fatalf("encode bogus first frame: %v", err)
	}
	dec := agent.NewDecoder(conn)
	frame, err := dec.Decode()
	if err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if frame.Type != agent.MsgError {
		t.Errorf("expected error frame, got %#v", frame)
	}
}

// TestServerDispatchesReseedFirstFrame guards the handleConn dispatch: a
// MsgReseed first frame must reach runReseed rather than the unknown-type
// rejection path. On non-Linux dev builds runReseed is the reseed_other
// stub, so a MsgError is the correct terminal frame here.
func TestServerDispatchesReseedFirstFrame(t *testing.T) {
	t.Parallel()
	_, conn := dialTestServer(t)

	enc := agent.NewEncoder(conn)
	if err := enc.Encode(agent.Message{Type: agent.MsgReseed, Data: []byte("host-entropy"), RegenMachineID: true}); err != nil {
		t.Fatalf("encode reseed: %v", err)
	}

	dec := agent.NewDecoder(conn)
	frame, err := dec.Decode()
	if err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if runtime.GOOS != "linux" {
		if frame.Type != agent.MsgError {
			t.Fatalf("expected MsgError on %s (reseed unsupported), got %#v", runtime.GOOS, frame)
		}
		if !strings.Contains(frame.Message, "not supported") {
			t.Errorf("expected unsupported-OS message, got %q", frame.Message)
		}
		return
	}
	if frame.Type != agent.MsgExit && frame.Type != agent.MsgError {
		t.Fatalf("expected terminal frame on linux, got %#v", frame)
	}
}

// TestServerRejectsUnknownFirstFrameType guards the dispatch default branch:
// a type that is neither MsgExec nor MsgReseed must still be rejected.
func TestServerRejectsUnknownFirstFrameType(t *testing.T) {
	t.Parallel()
	_, conn := dialTestServer(t)

	enc := agent.NewEncoder(conn)
	if err := enc.Encode(agent.Message{Type: "bogus"}); err != nil {
		t.Fatalf("encode bogus first frame: %v", err)
	}
	dec := agent.NewDecoder(conn)
	frame, err := dec.Decode()
	if err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if frame.Type != agent.MsgError {
		t.Errorf("expected error frame, got %#v", frame)
	}
}

func TestServerNonexistentCommand(t *testing.T) {
	t.Parallel()
	ctx, conn := dialTestServer(t)

	_, err := client.Run(ctx, conn, []string{"/does-not-exist-binary"}, nil, nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error for nonexistent command, got nil")
	}
	if !strings.Contains(err.Error(), "agent:") {
		t.Errorf("expected agent error wrap, got %v", err)
	}
}

// TestServerRejectsMalformedStdinFrame guards against the silent-EOF and
// double-terminal regressions: a malformed mid-stream frame must surface as
// MsgError and must not be followed by MsgExit.
func TestServerRejectsMalformedStdinFrame(t *testing.T) {
	t.Parallel()
	_, conn := dialTestServer(t)

	enc := agent.NewEncoder(conn)
	if err := enc.Encode(agent.Message{Type: agent.MsgExec, Argv: []string{"cat"}}); err != nil {
		t.Fatalf("encode exec: %v", err)
	}
	if _, err := conn.Write([]byte("not-json\n")); err != nil {
		t.Fatalf("inject malformed: %v", err)
	}

	dec := agent.NewDecoder(conn)
	var sawError bool
	for {
		frame, err := dec.Decode()
		if err != nil {
			break
		}
		if frame.Type == agent.MsgError {
			sawError = true
			break
		}
	}
	if !sawError {
		t.Fatal("expected MsgError after malformed stdin frame")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	frame, err := dec.Decode()
	if err == nil {
		t.Fatalf("expected connection close after MsgError, got frame %#v", frame)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after MsgError, got %v", err)
	}
}

// TestClientPropagatesStdinReadError guards against the local-IO-failure
// regression: a non-EOF Read error must surface from Run, not be hidden
// behind a successful exit code.
func TestClientPropagatesStdinReadError(t *testing.T) {
	t.Parallel()
	ctx, conn := dialTestServer(t)

	failingStdin := &erroringReader{after: 5, err: io.ErrUnexpectedEOF, payload: []byte("hello")}
	_, err := client.Run(ctx, conn, []string{"cat"}, nil, failingStdin, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error to propagate from broken local stdin")
	}
	if !strings.Contains(err.Error(), "read stdin") {
		t.Errorf("expected read stdin wrap, got %v", err)
	}
}

func TestServerMergesEnvWithHost(t *testing.T) {
	t.Parallel()
	ctx, conn := dialTestServer(t)

	var stdout bytes.Buffer
	exit, err := client.Run(
		ctx, conn,
		[]string{"sh", "-c", "echo $COCOON_AGENT_TEST_VAR && echo PATH=$PATH"},
		map[string]string{"COCOON_AGENT_TEST_VAR": "merged-value"},
		nil, &stdout, io.Discard,
	)
	if err != nil {
		t.Fatalf("client run: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	out := stdout.String()
	if !strings.Contains(out, "merged-value") {
		t.Errorf("caller env var not propagated: %q", out)
	}
	if !strings.Contains(out, "PATH=") || strings.Contains(out, "PATH=\n") {
		t.Errorf("host PATH not preserved on merge: %q", out)
	}
}

// Not parallel: t.Setenv mutates process-global os.Environ.
func TestServerMergesEnvCallerWins(t *testing.T) {
	t.Setenv("COCOON_AGENT_OVERRIDE_VAR", "host-value")
	ctx, conn := dialTestServer(t)

	var stdout bytes.Buffer
	exit, err := client.Run(
		ctx, conn,
		[]string{"sh", "-c", "printf %s \"$COCOON_AGENT_OVERRIDE_VAR\""},
		map[string]string{"COCOON_AGENT_OVERRIDE_VAR": "caller-value"},
		nil, &stdout, io.Discard,
	)
	if err != nil {
		t.Fatalf("client run: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if got := stdout.String(); got != "caller-value" {
		t.Errorf("caller env did not win on collision: got %q, want %q", got, "caller-value")
	}
}

func TestServerShutdownClosesIdleConn(t *testing.T) {
	tests := []struct {
		name     string
		shutdown func(*agent.Server, context.CancelFunc)
	}{
		{
			name: "context cancel",
			shutdown: func(_ *agent.Server, cancel context.CancelFunc) {
				cancel()
			},
		},
		{
			name: "server close",
			shutdown: func(srv *agent.Server, _ context.CancelFunc) {
				_ = srv.Close()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tcp, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			srv := agent.NewServer(tcp)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			errCh := make(chan error, 1)
			go func() { errCh <- srv.Serve(ctx) }()

			// A conn that connects but never sends: its handler parks in Decode, so
			// Serve can only return if shutdown force-closes the tracked conn.
			conn, err := net.Dial("tcp", tcp.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close() //nolint:errcheck

			tt.shutdown(srv, cancel)
			select {
			case <-errCh:
			case <-time.After(3 * time.Second):
				t.Fatalf("Serve did not return after shutdown with an idle conn:\n%s", goroutineDump())
			}
		})
	}
}

// TestServerWatcherExitsOnPermanentAcceptError: on a non-ErrClosed Accept
// failure, Serve must release its shutdown watcher rather than leak it until
// the (possibly never-canceled) parent ctx fires.
//
// Not parallel: asserts against the specific Serve watcher goroutine and keeps
// other tests from creating extra matching stacks while it samples.
func TestServerWatcherExitsOnPermanentAcceptError(t *testing.T) {
	before := countGoroutines(serveWatcherFrame)

	srv := agent.NewServer(&errorAcceptListener{err: errors.New("synthetic permanent accept failure")})
	// Long-lived ctx so only Serve's own return path can release the watcher.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected permanent accept error to surface")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after permanent accept error")
	}

	// Watcher release is async; poll until the specific watcher stack count
	// returns to baseline.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countGoroutines(serveWatcherFrame) <= before {
			return
		}
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Serve watcher goroutine still present:\n%s", goroutineDump())
}

// A reader parked on a full stdinFrames send is only released by ctx cancel,
// not conn.Close. Not parallel: samples the process-wide goroutine set.
func TestServerDrainsStdinAfterEarlyChildExit(t *testing.T) {
	before := countGoroutines(handleConnFrame)
	_, conn := dialTestServer(t)

	enc := agent.NewEncoder(conn)
	if err := enc.Encode(agent.Message{Type: agent.MsgExec, Argv: []string{"sh", "-c", "exit 0"}}); err != nil {
		t.Fatalf("encode exec: %v", err)
	}
	// Flood stdin so the server's frame reader fills stdinFrames while the
	// exited child leaves the pump unable to drain it.
	go func() {
		chunk := make([]byte, 32*1024)
		for enc.Encode(agent.Message{Type: agent.MsgStdin, Data: chunk}) == nil {
		}
	}()

	dec := agent.NewDecoder(conn)
	for {
		frame, err := dec.Decode()
		if err != nil {
			break
		}
		if frame.Type == agent.MsgExit || frame.Type == agent.MsgError {
			break
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countGoroutines(handleConnFrame) <= before {
			return
		}
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("handleConn goroutines leaked:\n%s", goroutineDump())
}

// dialTestServer runs the agent over loopback TCP and dials a client conn;
// full teardown is registered via t.Cleanup so callers don't repeat it.
func dialTestServer(t *testing.T) (context.Context, net.Conn) {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	srv := agent.NewServer(tcp)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	var wg sync.WaitGroup
	wg.Go(func() { _ = srv.Serve(ctx) })
	conn, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		cancel()
		wg.Wait()
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		_ = conn.Close()
	})
	return ctx, conn
}

type erroringReader struct {
	payload []byte
	after   int
	err     error
}

func (r *erroringReader) Read(p []byte) (int, error) {
	if r.after > 0 {
		n := copy(p, r.payload)
		r.after = 0
		return n, nil
	}
	return 0, r.err
}

// errorAcceptListener returns err on every Accept — drives Serve's
// permanent-error return path.
type errorAcceptListener struct {
	err error
}

func (l *errorAcceptListener) Accept() (net.Conn, error) {
	return nil, l.err
}

func (l *errorAcceptListener) Close() error { return nil }
func (l *errorAcceptListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func goroutineDump() string {
	size := initialGoroutineDumpSize
	for {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < len(buf) || size == maxGoroutineDumpSize {
			return string(buf[:n])
		}
		size = min(size*2, maxGoroutineDumpSize)
	}
}

func countGoroutines(substr string) int {
	return strings.Count(goroutineDump(), substr)
}
