// Package client wraps the cocoon-agent wire protocol for host-side use.
// Transport-agnostic: Run takes io.ReadWriteCloser so callers swap in
// vsock.Dial, net.Dial, or an in-memory pipe for tests.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/cocoon-agent/agent"
)

const stdinChunkSize = 32 * 1024

var errNoExitFrame = errors.New("agent: connection closed before exit frame")

// Run executes argv and bridges I/O, returning the child exit code.
// nil stdin/stdout/stderr → no-stdin / discard. Matches kubectl exec
// AttachIO semantics.
//
// Lifecycle: after MsgExit/MsgError, Run closes conn and returns without
// waiting for the stdin pump — its blocking Read on a TTY caller can't
// be unblocked from inside Run. The pump drains when the caller's stdin
// closes or the next Encode fails on the closed conn.
//
// A non-EOF read error from the local stdin reader is propagated through
// runCancel + a shared atomic so Run can override the child's exit code
// with the actual cause; otherwise broken local IO would masquerade as
// a clean MsgStdinClose.
func Run(ctx context.Context, conn io.ReadWriteCloser, argv []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("client: argv is empty")
	}

	enc, dec, runCancel, err := openSession(ctx, conn, agent.Message{Type: agent.MsgExec, Argv: argv, Env: env})
	if err != nil {
		return 0, err
	}
	defer runCancel()

	var stdinReadErr atomic.Pointer[error]
	if stdin != nil {
		go pumpStdin(stdin, enc, &stdinReadErr, runCancel)
	} else {
		_ = enc.Encode(agent.Message{Type: agent.MsgStdinClose})
	}

	exitCode := 0
	var sawExit bool

readLoop:
	for {
		frame, err := dec.Decode()
		if err != nil {
			// Stdin read failure trips runCancel → conn.Close → this
			// EOF/closed read. Surface the stdin error first so the
			// caller sees the real cause.
			if serr := stdinErr(&stdinReadErr); serr != nil {
				return 0, serr
			}
			// Prefer ctx.Err over EOF: ctx-cancel closes the conn,
			// surfacing as EOF here.
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, fmt.Errorf("read frame: %w", err)
		}
		switch frame.Type {
		case agent.MsgStarted:
		case agent.MsgStdout, agent.MsgStderr:
			w := stdout
			name := "stdout"
			if frame.Type == agent.MsgStderr {
				w, name = stderr, "stderr"
			}
			if w != nil {
				if _, err := w.Write(frame.Data); err != nil {
					return 0, fmt.Errorf("write %s: %w", name, err)
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

	if serr := stdinErr(&stdinReadErr); serr != nil {
		return 0, serr
	}
	if !sawExit {
		// Same ctx-cancel-races-MsgExit case as the readLoop EOF path.
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errNoExitFrame
	}
	return exitCode, nil
}

// Reseed sends host-fed entropy and a reseed order after a VM clone/restore,
// so N clones sharing byte-identical snapshot memory don't share
// byte-identical CRNG state. nil iff the agent reports exit code 0.
func Reseed(ctx context.Context, conn io.ReadWriteCloser, entropy []byte, regenMachineID bool) error {
	_, dec, cancel, err := openSession(ctx, conn, agent.Message{Type: agent.MsgReseed, Data: entropy, RegenMachineID: regenMachineID})
	if err != nil {
		return err
	}
	defer cancel()
	for {
		frame, err := dec.Decode()
		if err != nil {
			// Prefer ctx.Err over EOF: ctx-cancel closes the conn,
			// surfacing as EOF here.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return errNoExitFrame
			}
			return fmt.Errorf("read frame: %w", err)
		}
		switch frame.Type {
		case agent.MsgExit:
			if frame.ExitCode != 0 {
				return fmt.Errorf("agent: reseed exited with code %d", frame.ExitCode)
			}
			return nil
		case agent.MsgError:
			return fmt.Errorf("agent: %s", frame.Message)
		default:
			log.WithFunc("client.Reseed").Warnf(ctx, "ignoring unknown frame type %q", frame.Type)
		}
	}
}

// openSession wires ctx cancellation to conn.Close and sends the opening frame.
func openSession(ctx context.Context, conn io.ReadWriteCloser, first agent.Message) (*agent.Encoder, *agent.Decoder, context.CancelFunc, error) {
	// Sub-ctx so the conn-closer doesn't outlive the session on a longer-lived caller ctx.
	sessCtx, cancel := context.WithCancel(ctx)
	go func() {
		<-sessCtx.Done()
		_ = conn.Close()
	}()
	enc := agent.NewEncoder(conn)
	if err := enc.Encode(first); err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("send %s frame: %w", first.Type, err)
	}
	return enc, agent.NewDecoder(conn), cancel, nil
}

// pumpStdin streams stdin → MsgStdin frames; on EOF sends MsgStdinClose.
// Encode errors are silent (child closing stdin early is normal). A
// non-EOF Read error is recorded in errOut and triggers cancel so Run's
// readLoop unblocks and surfaces the failure.
func pumpStdin(r io.Reader, enc *agent.Encoder, errOut *atomic.Pointer[error], cancel context.CancelFunc) {
	buf := make([]byte, stdinChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// buf[:n] is safe to alias here: Encode → json.Marshal copies
			// Data into its own buffer before returning, and the loop
			// won't reuse buf until Encode does.
			if encErr := enc.Encode(agent.Message{Type: agent.MsgStdin, Data: buf[:n]}); encErr != nil {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				errCopy := err
				errOut.Store(&errCopy)
				cancel()
			}
			_ = enc.Encode(agent.Message{Type: agent.MsgStdinClose})
			return
		}
	}
}

// stdinErr returns the recorded stdin read failure wrapped, or nil if the
// pump never stored one.
func stdinErr(p *atomic.Pointer[error]) error {
	if e := p.Load(); e != nil {
		return fmt.Errorf("read stdin: %w", *e)
	}
	return nil
}
