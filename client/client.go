// Package client wraps the cocoon-agent wire protocol for host-side use.
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

// Run executes argv over conn with kubectl-exec I/O semantics (nil stdin/stdout/stderr = no stdin / discard) and returns the exit code.
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

readLoop:
	for {
		frame, err := dec.Decode()
		if err != nil {
			// a stdin read failure closed the conn; report it, not the EOF it caused
			if serr := stdinErr(&stdinReadErr); serr != nil {
				return 0, serr
			}
			return 0, decodeErr(ctx, err)
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
	return exitCode, nil
}

// Reseed feeds host entropy after a clone or restore so clones do not share CRNG state; nil iff the agent exits 0.
func Reseed(ctx context.Context, conn io.ReadWriteCloser, entropy []byte, regenMachineID bool) error {
	_, dec, cancel, err := openSession(ctx, conn, agent.Message{Type: agent.MsgReseed, Data: entropy, RegenMachineID: regenMachineID})
	if err != nil {
		return err
	}
	defer cancel()
	for {
		frame, err := dec.Decode()
		if err != nil {
			return decodeErr(ctx, err)
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

func openSession(ctx context.Context, conn io.ReadWriteCloser, first agent.Message) (*agent.Encoder, *agent.Decoder, context.CancelFunc, error) {
	// Sub-ctx so the conn-closer doesn't outlive the session on a longer-lived caller ctx.
	sessCtx, cancel := context.WithCancel(ctx)
	context.AfterFunc(sessCtx, func() { _ = conn.Close() })
	enc := agent.NewEncoder(conn)
	if err := enc.Encode(first); err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("send %s frame: %w", first.Type, err)
	}
	return enc, agent.NewDecoder(conn), cancel, nil
}

func pumpStdin(r io.Reader, enc *agent.Encoder, errOut *atomic.Pointer[error], cancel context.CancelFunc) {
	buf := make([]byte, stdinChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
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

func stdinErr(p *atomic.Pointer[error]) error {
	if e := p.Load(); e != nil {
		return fmt.Errorf("read stdin: %w", *e)
	}
	return nil
}

// decodeErr maps a decode failure to the session outcome: ctx cancel closes the conn and surfaces as EOF.
func decodeErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, io.EOF) {
		return errNoExitFrame
	}
	return fmt.Errorf("read frame: %w", err)
}
