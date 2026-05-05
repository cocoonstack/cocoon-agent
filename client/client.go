// Package client wraps the cocoon-agent wire protocol for host-side use.
// Transport-agnostic: Run takes io.ReadWriteCloser so callers swap in
// vsock.Dial, net.Dial, or an in-memory pipe for tests.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/cocoon-agent/agent"
)

const stdinChunkSize = 32 * 1024

// Run executes argv and bridges I/O, returning the child exit code.
// nil stdin/stdout/stderr → no-stdin / discard. Matches kubectl exec
// AttachIO semantics.
//
// Lifecycle: after MsgExit/MsgError, Run closes conn and returns without
// waiting for the stdin pump — its blocking Read on a TTY caller can't
// be unblocked from inside Run. The pump drains when the caller's stdin
// closes or the next Encode fails on the closed conn.
func Run(ctx context.Context, conn io.ReadWriteCloser, argv []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("client: argv is empty")
	}

	// Sub-ctx so the conn-closer doesn't outlive Run on a longer-lived caller ctx.
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
		// enc is single-writer post-handshake; no mutex needed.
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

// pumpStdin streams stdin → MsgStdin frames; on EOF sends MsgStdinClose.
// Errors are silent: child closing stdin early is normal (e.g. `head -1`).
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
