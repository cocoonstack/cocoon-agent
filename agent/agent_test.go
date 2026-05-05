package agent_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cocoonstack/cocoon-agent/agent"
	"github.com/cocoonstack/cocoon-agent/client"
)

// newLoopbackServer wires the server onto a loopback TCP listener so the
// accept loop, connection lifecycle, and runExec run end-to-end without
// needing vsock. Server takes net.Listener directly — no wrapper needed.
func newLoopbackServer(t *testing.T) (*agent.Server, string) {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	return agent.NewServer(tcp), tcp.Addr().String()
}

func TestServerExecHelloWorld(t *testing.T) {
	t.Parallel()

	srv, addr := newLoopbackServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) //nolint:mnd
	defer cancel()

	var serveWG sync.WaitGroup
	serveWG.Add(1)
	go func() {
		defer serveWG.Done()
		_ = srv.Serve(ctx)
	}()
	defer func() {
		_ = srv.Close()
		serveWG.Wait()
	}()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

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

	srv, addr := newLoopbackServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) //nolint:mnd
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	defer srv.Close() //nolint:errcheck

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
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

	srv, addr := newLoopbackServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) //nolint:mnd
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	defer srv.Close() //nolint:errcheck

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
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

	srv, addr := newLoopbackServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) //nolint:mnd
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	defer srv.Close() //nolint:errcheck

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close() //nolint:errcheck

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

	srv, addr := newLoopbackServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) //nolint:mnd
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	defer srv.Close() //nolint:errcheck

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, err = client.Run(ctx, conn, []string{"/does-not-exist-binary"}, nil, nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error for nonexistent command, got nil")
	}
	if !strings.Contains(err.Error(), "agent:") {
		t.Errorf("expected agent error wrap, got %v", err)
	}
}

func TestServerMergesEnvWithHost(t *testing.T) {
	t.Parallel()

	srv, addr := newLoopbackServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) //nolint:mnd
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	defer srv.Close() //nolint:errcheck

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
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
