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

// runExec spawns argv with the supplied environment and bridges its stdio
// onto the framed channel: stdout/stderr chunks become MsgStdout / MsgStderr
// frames, stdin frames feed the child's stdin, and the final exit status
// becomes a MsgExit frame. encMu serializes Encoder writes across the two
// framedWriters and any direct sendLocked calls.
//
// Argv must be non-empty. An empty argv is a protocol error and is reported
// back as MsgError with no MsgExit (the child never started).
//
// env is merged on top of the agent's inherited os.Environ so the child
// keeps PATH / HOME / etc. — caller keys win on collisions. An empty env
// inherits unchanged.
//
// stdinFrames receives client-sent MsgStdin / MsgStdinClose frames. Closing
// the channel is treated as MsgStdinClose; the caller is responsible for
// closing it once the connection is shutting down so the child's stdin
// pipe is drained and the child can observe EOF.
func runExec(parentCtx context.Context, argv []string, env map[string]string, stdinFrames <-chan Message, enc *Encoder, encMu *sync.Mutex) error {
	if len(argv) == 0 {
		return sendErrorf(enc, encMu, "exec: argv is empty")
	}

	// Inner ctx: lets us kill the child if the wire encoder dies
	// mid-stream (e.g. host-side network drop). exec.CommandContext
	// terminates the child when ctx fires.
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv comes from a trusted vsock peer
	if len(env) > 0 {
		cmd.Env = mergeEnv(env)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return sendErrorf(enc, encMu, "exec: open stdin pipe: %v", err)
	}
	// Setting cmd.Stdout/Stderr (instead of using StdoutPipe/StderrPipe) makes
	// cmd.Wait drain the child's output before returning. With the pipe-based
	// API, Wait closes the parent read fd as soon as the child exits, racing
	// any pump goroutine still draining the kernel pipe buffer.
	stdoutW := newFramedWriter(MsgStdout, enc, encMu, cancel)
	stderrW := newFramedWriter(MsgStderr, enc, encMu, cancel)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return sendErrorf(enc, encMu, "exec: start %s: %v", argv[0], err)
	}
	if err := sendLocked(enc, encMu, Message{Type: MsgStarted, PID: cmd.Process.Pid}); err != nil {
		// Encoder broken (host gone / network drop). Cancel so
		// CommandContext kills the child instead of running it to
		// completion against a dead conn.
		cancel()
	}

	stdinDone := make(chan struct{})
	go pumpStdin(stdinPipe, stdinFrames, stdinDone)

	waitErr := cmd.Wait()
	<-stdinDone

	// If the framedWriter lost the conn mid-stream, surface that in
	// preference to the child's exit status — the client cannot have
	// received MsgExit anyway.
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
		// MsgError is terminal: client.go treats it as a return,
		// no MsgExit will follow (and none should).
		return sendErrorf(enc, encMu, "exec: wait %s: %v", argv[0], waitErr)
	}

	return sendLocked(enc, encMu, Message{Type: MsgExit, ExitCode: exitCode})
}

// pumpStdin drains client stdin frames into the child's stdin pipe. A
// MsgStdinClose frame, a closed channel, or a write error all close the
// pipe and signal done. Write errors are intentionally not propagated
// back to the client — a child that closes stdin early is normal (e.g.
// `head -1`).
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

// mergeEnv layers caller-supplied env vars over the agent's inherited
// os.Environ. Caller keys win on collision — same precedence as docker
// run --env. Children that opt out can pass a sentinel like
// `--env PATH= --env HOME=` to clear specific inherited keys.
func mergeEnv(env map[string]string) []string {
	out := append(os.Environ(), make([]string, 0, len(env))...)
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
