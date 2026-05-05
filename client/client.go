// Package client is a small wrapper around the cocoon-agent wire protocol
// for use by host-side callers. The production caller is vk-cocoon (it
// will eventually shell out via `cocoon vm exec`, but during bring-up the
// client is also useful for direct vsock testing). Keep it transport-
// agnostic — Run takes an io.ReadWriteCloser, so callers can swap in
// vsock.Dial, net.Dial("tcp"), or an in-memory pipe for tests.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cocoonstack/cocoon-agent/agent"
)

// Run executes argv on the connected agent and bridges I/O. Returns the
// child exit code on success. ctx cancellation closes conn so the agent
// observes EOF and the running command is reaped via its cmd context.
//
// stdin/stdout/stderr may be nil. A nil stdin means "no stdin" — the
// agent observes immediate EOF on the child's stdin. A nil stdout/stderr
// means "discard". This matches kubectl exec / api.AttachIO semantics.
func Run(ctx context.Context, conn io.ReadWriteCloser, argv []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("client: argv is empty")
	}

	// Cancel-on-ctx: closing the conn unblocks dec.Decode and pumpStdin.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	enc := agent.NewEncoder(conn)
	if err := enc.Encode(agent.Message{Type: agent.MsgExec, Argv: argv, Env: env}); err != nil {
		return 0, fmt.Errorf("send exec frame: %w", err)
	}

	stdinDone := make(chan struct{})
	if stdin != nil {
		go pumpStdin(stdin, enc, stdinDone)
	} else {
		close(stdinDone)
		_ = enc.Encode(agent.Message{Type: agent.MsgStdinClose})
	}

	dec := agent.NewDecoder(conn)
	exitCode := 0
	var sawExit bool

readLoop:
	for {
		frame, err := dec.Decode()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, fmt.Errorf("read frame: %w", err)
		}
		switch frame.Type {
		case agent.MsgStarted:
			// Useful for debug; non-fatal otherwise.
		case agent.MsgStdout:
			if stdout != nil {
				if _, err := stdout.Write(frame.Data); err != nil {
					return 0, fmt.Errorf("write stdout: %w", err)
				}
			}
		case agent.MsgStderr:
			if stderr != nil {
				if _, err := stderr.Write(frame.Data); err != nil {
					return 0, fmt.Errorf("write stderr: %w", err)
				}
			}
		case agent.MsgExit:
			exitCode = frame.ExitCode
			sawExit = true
			break readLoop
		case agent.MsgError:
			return 0, fmt.Errorf("agent: %s", frame.Message)
		}
	}

	<-stdinDone
	if !sawExit {
		return 0, errors.New("agent: connection closed before exit frame")
	}
	return exitCode, nil
}

// pumpStdin streams the caller's stdin to the agent as MsgStdin frames,
// then sends MsgStdinClose on EOF. Errors are silenced because a child
// that closes stdin early is normal (e.g. `head -n 1`).
func pumpStdin(r io.Reader, enc *agent.Encoder, done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 32*1024) //nolint:mnd
	var sendMu sync.Mutex
	for {
		n, err := r.Read(buf)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buf[:n])
			sendMu.Lock()
			sendErr := enc.Encode(agent.Message{Type: agent.MsgStdin, Data: payload})
			sendMu.Unlock()
			if sendErr != nil {
				return
			}
		}
		if err != nil {
			sendMu.Lock()
			_ = enc.Encode(agent.Message{Type: agent.MsgStdinClose})
			sendMu.Unlock()
			return
		}
	}
}
