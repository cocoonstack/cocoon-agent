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

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/cocoon-agent/agent"
)

// stdinChunkSize is how much we read from caller stdin per iteration.
// Mirrors the kernel pipe buffer; small enough to keep latency low for
// interactive sessions, large enough to amortize JSON framing overhead.
const stdinChunkSize = 32 * 1024

// Run executes argv on the connected agent and bridges I/O. Returns the
// child exit code on success.
//
// stdin/stdout/stderr may be nil. A nil stdin means "no stdin" — the
// agent observes immediate EOF on the child's stdin. A nil stdout/stderr
// means "discard". Matches kubectl exec / api.AttachIO semantics.
//
// Lifecycle: Run does NOT wait for the stdin pump to finish. After the
// agent sends MsgExit (or MsgError) we close conn so the pump's next
// Encode fails and the goroutine winds down. The pump's blocking Read
// on a TTY caller cannot be unblocked from inside Run; the goroutine
// drains when the caller closes its stdin or the conn is fully torn down.
func Run(ctx context.Context, conn io.ReadWriteCloser, argv []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("client: argv is empty")
	}

	// Sub-ctx scoped to this Run so the conn-closer goroutine doesn't
	// outlive Run on the caller's longer-lived ctx.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go func() {
		<-runCtx.Done()
		_ = conn.Close()
	}()

	enc := agent.NewEncoder(conn)
	if err := enc.Encode(agent.Message{Type: agent.MsgExec, Argv: argv, Env: env}); err != nil {
		return 0, fmt.Errorf("send exec frame: %w", err)
	}

	if stdin != nil {
		// Best-effort: enc is single-writer post-handshake (read loop
		// never writes), so no mutex is needed. The pump survives Run
		// returning until the caller's stdin closes or our conn close
		// trips its next Encode.
		go pumpStdin(stdin, enc)
	} else {
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
		default:
			log.WithFunc("client.Run").Warnf(ctx, "ignoring unknown frame type %q", frame.Type)
		}
	}

	if !sawExit {
		return 0, errors.New("agent: connection closed before exit frame")
	}
	return exitCode, nil
}

// pumpStdin streams the caller's stdin to the agent as MsgStdin frames,
// then sends MsgStdinClose on EOF. Errors are silenced because a child
// that closes stdin early is normal (e.g. `head -n 1`).
func pumpStdin(r io.Reader, enc *agent.Encoder) {
	buf := make([]byte, stdinChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buf[:n])
			if encErr := enc.Encode(agent.Message{Type: agent.MsgStdin, Data: payload}); encErr != nil {
				return
			}
		}
		if err != nil {
			_ = enc.Encode(agent.Message{Type: agent.MsgStdinClose})
			return
		}
	}
}
