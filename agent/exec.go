package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
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
// stdinFrames receives client-sent MsgStdin / MsgStdinClose frames. Closing
// the channel is treated as MsgStdinClose; it is the caller's responsibility
// to close it once the connection is shutting down so the child's stdin
// pipe is drained and the child can observe EOF.
func runExec(ctx context.Context, argv []string, env map[string]string, stdinFrames <-chan Message, enc *Encoder, encMu *sync.Mutex) error {
	if len(argv) == 0 {
		return sendErrorf(enc, encMu, "exec: argv is empty")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv comes from a trusted vsock peer
	if len(env) > 0 {
		cmd.Env = mapToEnv(env)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return sendErrorf(enc, encMu, "exec: open stdin pipe: %v", err)
	}
	// Setting cmd.Stdout/Stderr (instead of using StdoutPipe/StderrPipe) makes
	// cmd.Wait drain the child's output before returning. With the pipe-based
	// API, Wait closes the parent read fd as soon as the child exits, racing
	// any pump goroutine still draining the kernel pipe buffer.
	cmd.Stdout = &framedWriter{msgType: MsgStdout, enc: enc, mu: encMu}
	cmd.Stderr = &framedWriter{msgType: MsgStderr, enc: enc, mu: encMu}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return sendErrorf(enc, encMu, "exec: start %s: %v", argv[0], err)
	}
	if err := sendLocked(enc, encMu, Message{Type: MsgStarted, PID: cmd.Process.Pid}); err != nil {
		// Client gone; let cmd.Wait return on context-cancel.
		_ = stdinPipe.Close()
	}

	stdinDone := make(chan struct{})
	go pumpStdin(stdinPipe, stdinFrames, stdinDone)

	waitErr := cmd.Wait()
	<-stdinDone

	exitCode := 0
	switch {
	case waitErr == nil:
	case errors.As(waitErr, new(*exec.ExitError)):
		var exitErr *exec.ExitError
		_ = errors.As(waitErr, &exitErr)
		exitCode = exitErr.ExitCode()
	default:
		return sendErrorf(enc, encMu, "exec: wait %s: %v", argv[0], waitErr)
	}

	return sendLocked(enc, encMu, Message{Type: MsgExit, ExitCode: exitCode})
}

// framedWriter wraps a stdout/stderr io.Writer interface so cmd.Wait can
// drive it directly. Each Write becomes one MsgStdout/MsgStderr frame.
type framedWriter struct {
	msgType string
	enc     *Encoder
	mu      *sync.Mutex
}

func (w *framedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	payload := make([]byte, len(p))
	copy(payload, p)
	w.mu.Lock()
	err := w.enc.Encode(Message{Type: w.msgType, Data: payload})
	w.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// pumpStdin drains client stdin frames into the child's stdin pipe. A
// MsgStdinClose frame, a closed channel, or a write error all close the
// pipe and signal done. We deliberately do not propagate write errors
// back to the client: a child that closes stdin early is normal (e.g.
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

func mapToEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
