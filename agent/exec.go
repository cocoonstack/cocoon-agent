package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"strings"
	"time"
)

// execWaitDelay bounds how long Wait blocks on the child's stdout/stderr
// pipes after exit: a daemonized grandchild inherits them and would pin
// the session (and its conn) until it dies.
const execWaitDelay = 2 * time.Second

// processController hooks platform-specific child-lifecycle steps into
// runExec: AfterStart runs once cmd.Process exists (Windows assigns the
// child to its Job Object); Close releases any held kernel resource.
type processController struct {
	afterStart func(*exec.Cmd) error
	close      func()
}

func (c processController) AfterStart(cmd *exec.Cmd) error {
	if c.afterStart == nil {
		return nil
	}
	return c.afterStart(cmd)
}

func (c processController) Close() {
	if c.close != nil {
		c.close()
	}
}

// runExec runs argv to completion, framing stdout/stderr/exit onto enc.
// Empty argv → MsgError with no MsgExit; env is merged on top of os.Environ
// with caller keys winning.
func runExec(parentCtx context.Context, argv []string, env map[string]string, stdinFrames <-chan Message, enc *Encoder) error {
	if len(argv) == 0 {
		return enc.SendErrorf("exec: argv is empty")
	}

	// Inner ctx so an encoder failure can kill the child via
	// exec.CommandContext instead of letting it run against a dead conn.
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv from trusted vsock peer
	cmd.WaitDelay = execWaitDelay
	procCtl, err := setupProcess(cmd)
	if err != nil {
		return enc.SendErrorf("exec: setup process %s: %v", argv[0], err)
	}
	defer procCtl.Close()
	if len(env) > 0 {
		cmd.Env = mergeEnv(env)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return enc.SendErrorf("exec: open stdin pipe: %v", err)
	}
	// cmd.Stdout/Stderr (vs StdoutPipe/StderrPipe) lets cmd.Wait drain
	// before returning. The pipe-based API closes the parent read fd as
	// soon as the child exits, racing the pump's last read.
	stdoutW := newFramedWriter(MsgStdout, enc, cancel)
	stderrW := newFramedWriter(MsgStderr, enc, cancel)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return enc.SendErrorf("exec: start %s: %v", argv[0], err)
	}
	if err := procCtl.AfterStart(cmd); err != nil {
		cancel()
		// Child isn't in the Windows Job Object yet, so cancel doesn't reach
		// it via CloseHandle(job); explicit Kill is required.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdinPipe.Close()
		return enc.SendErrorf("exec: assign process %s: %v", argv[0], err)
	}
	if err := enc.Encode(Message{Type: MsgStarted, PID: cmd.Process.Pid}); err != nil {
		// Wire is dead; kill+reap to avoid a zombie, then surface the
		// original encoder error rather than masking it with the inevitable
		// downstream MsgExit failure.
		cancel()
		_ = cmd.Wait()
		_ = stdinPipe.Close()
		return fmt.Errorf("send started frame: %w", err)
	}

	stdinDone := make(chan struct{})
	go pumpStdin(ctx, stdinPipe, stdinFrames, stdinDone)

	waitErr := cmd.Wait()
	// Cancel so the stdin pump unblocks if the client never sent
	// MsgStdinClose — otherwise <-stdinDone hangs forever.
	cancel()
	<-stdinDone

	if errors.Is(context.Cause(ctx), errTerminalFrameSent) {
		return nil
	}

	// Surface any encoder error in preference to the child's exit —
	// the client never received MsgExit anyway.
	if encErr := errors.Join(stdoutW.err(), stderrW.err()); encErr != nil {
		return fmt.Errorf("write child output: %w", encErr)
	}

	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exitErr):
		exitCode = exitErr.ExitCode()
	case errors.Is(waitErr, exec.ErrWaitDelay):
		// Pipes were abandoned to a background child; the command itself exited.
		exitCode = cmd.ProcessState.ExitCode()
	default:
		return enc.SendErrorf("exec: wait %s: %v", argv[0], waitErr)
	}

	return enc.Encode(Message{Type: MsgExit, ExitCode: exitCode})
}

// pumpStdin drains stdin frames into the child's pipe; returns on
// MsgStdinClose, channel close, ctx cancel, or write error. Write errors
// are silent — child closing stdin early is normal (e.g. `head -1`).
func pumpStdin(ctx context.Context, w io.WriteCloser, frames <-chan Message, done chan<- struct{}) {
	defer close(done)
	defer w.Close() //nolint:errcheck
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok || frame.Type == MsgStdinClose {
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
}

// mergeEnv layers caller env over os.Environ. Dedup is required: libc getenv
// returns the first match, so duplicate keys would shadow caller overrides.
func mergeEnv(callerEnv map[string]string) []string {
	host := os.Environ()
	merged := make(map[string]string, len(host)+len(callerEnv))
	for _, kv := range host {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, dup := merged[k]; dup {
			continue // first occurrence wins, matching libc getenv
		}
		merged[k] = v
	}
	maps.Copy(merged, callerEnv)
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}
