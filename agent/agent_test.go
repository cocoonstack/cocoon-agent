package agent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cocoonstack/cocoon-agent/agent"
	"github.com/cocoonstack/cocoon-agent/client"
)

// dialTestServer runs the agent over loopback TCP and dials a client conn.
// Cleanup (cancel ctx, close server, wait for Serve to return, close conn)
// is registered via t.Cleanup so callers don't repeat it per test.
func dialTestServer(t *testing.T) (context.Context, net.Conn) {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	srv := agent.NewServer(tcp)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) //nolint:mnd
	var wg sync.WaitGroup
	wg.Go(func() { _ = srv.Serve(ctx) })
	conn, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		cancel()
		_ = srv.Close()
		wg.Wait()
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		wg.Wait()
		_ = conn.Close()
	})
	return ctx, conn
}

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

// TestServerMergesEnvCallerWins guards against the merge-order regression: a
// caller-supplied value for a key that also exists in the host env must reach
// the child, not be shadowed by the host's value. The historical bug was an
// append-host-then-append-caller layout — under libc getenv (which returns the
// FIRST matching entry), the host value survives.
//
// Cannot run with t.Parallel: t.Setenv mutates process-global os.Environ.
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
