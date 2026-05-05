package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// runExec runs argv against the framed channel, returning when the child
// exits or the wire dies. Empty argv → MsgError with no MsgExit. env is
// merged on top of os.Environ (caller keys win).
func runExec(parentCtx context.Context, argv []string, env map[string]string, stdinFrames <-chan Message, enc *Encoder, encMu *sync.Mutex) error {
	if len(argv) == 0 {
		return sendErrorf(enc, encMu, "exec: argv is empty")
	}

	// Inner ctx so an encoder failure can kill the child via
	// exec.CommandContext instead of letting it run against a dead conn.
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv from trusted vsock peer
	setProcessGroup(cmd)
	if len(env) > 0 {
		cmd.Env = mergeEnv(env)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return sendErrorf(enc, encMu, "exec: open stdin pipe: %v", err)
	}
	// cmd.Stdout/Stderr (vs StdoutPipe/StderrPipe) lets cmd.Wait drain
	// before returning. The pipe-based API closes the parent read fd as
	// soon as the child exits, racing the pump's last read.
	stdoutW := newFramedWriter(MsgStdout, enc, encMu, cancel)
	stderrW := newFramedWriter(MsgStderr, enc, encMu, cancel)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return sendErrorf(enc, encMu, "exec: start %s: %v", argv[0], err)
	}
	if err := sendLocked(enc, encMu, Message{Type: MsgStarted, PID: cmd.Process.Pid}); err != nil {
		cancel()
	}

	stdinDone := make(chan struct{})
	go pumpStdin(stdinPipe, stdinFrames, stdinDone)

	waitErr := cmd.Wait()
	<-stdinDone

	// Surface any encoder error in preference to the child's exit —
	// the client never received MsgExit anyway.
	if encErr := firstNonNil(stdoutW.err(), stderrW.err()); encErr != nil {
		return fmt.Errorf("write child output: %w", encErr)
	}

	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exitErr):
		exitCode = exitErr.ExitCode()
	default:
		return sendErrorf(enc, encMu, "exec: wait %s: %v", argv[0], waitErr)
	}

	return sendLocked(enc, encMu, Message{Type: MsgExit, ExitCode: exitCode})
}

// pumpStdin drains stdin frames into the child's pipe; close on
// MsgStdinClose, channel close, or write error. Write errors are silent —
// child closing stdin early is normal (e.g. `head -1`).
func pumpStdin(w io.WriteCloser, frames <-chan Message, done chan<- struct{}) {
	defer close(done)
	defer w.Close() //nolint:errcheck
	for frame := range frames {
		if frame.Type == MsgStdinClose {
			return
		}
		if frame.Type != MsgStdin || len(frame.Data) == 0 {
			continue
		}
		if _, err := w.Write(frame.Data); err != nil {
			return
		}
	}
}

func sendLocked(enc *Encoder, mu *sync.Mutex, m Message) error {
	mu.Lock()
	defer mu.Unlock()
	return enc.Encode(m)
}

func sendErrorf(enc *Encoder, mu *sync.Mutex, format string, args ...any) error {
	return sendLocked(enc, mu, Message{Type: MsgError, Message: fmt.Sprintf(format, args...)})
}

// mergeEnv layers caller env over os.Environ; caller keys win on collision.
func mergeEnv(env map[string]string) []string {
	hostEnv := os.Environ()
	out := make([]string, 0, len(hostEnv)+len(env))
	out = append(out, hostEnv...)
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func firstNonNil(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
