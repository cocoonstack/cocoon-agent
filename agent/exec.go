package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// execWaitDelay stops a daemonized grandchild that inherits the pipes from pinning the session after exit.
const execWaitDelay = 2 * time.Second

// processController hooks the platform's child-lifecycle steps into runExec.
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

// runExec runs argv to completion, framing stdout/stderr/exit onto enc; caller env keys override os.Environ.
func runExec(parentCtx context.Context, argv []string, env map[string]string, stdinFrames <-chan Message, enc *Encoder) error {
	if len(argv) == 0 {
		return enc.SendErrorf("exec: argv is empty")
	}

	// inner ctx so an encoder failure kills the child instead of leaving it against a dead conn
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
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return enc.SendErrorf("exec: open stdin pipe: %v", err)
	}
	// cmd.Stdout/Stderr, not the pipe API: Wait drains them, while pipe fds close at child exit and race the last read
	stdoutW := newFramedWriter(MsgStdout, enc, cancel)
	stderrW := newFramedWriter(MsgStderr, enc, cancel)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		return enc.SendErrorf("exec: start %s: %v", argv[0], err)
	}
	if err := procCtl.AfterStart(cmd); err != nil {
		cancel()
		// cancel cannot reach a child not yet in the Windows Job Object, so Kill is required
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return enc.SendErrorf("exec: assign process %s: %v", argv[0], err)
	}
	if err := enc.Encode(Message{Type: MsgStarted, PID: cmd.Process.Pid}); err != nil {
		// the wire is dead: reap the child and report the encoder error, not the MsgExit failure it causes
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("send started frame: %w", err)
	}

	stdinDone := make(chan struct{})
	go pumpStdin(ctx, stdinPipe, stdinFrames, stdinDone)

	waitErr := cmd.Wait()
	// cancel unblocks the stdin pump when the client never sent MsgStdinClose
	cancel()
	<-stdinDone

	if errors.Is(context.Cause(ctx), errTerminalFrameSent) {
		return nil
	}

	// an encoder error outranks the child's exit: the client never received MsgExit
	if encErr := errors.Join(stdoutW.err(), stderrW.err()); encErr != nil {
		return fmt.Errorf("write child output: %w", encErr)
	}

	exitCode := 0
	switch exitErr, isExit := errors.AsType[*exec.ExitError](waitErr); {
	case waitErr == nil:
	case isExit:
		exitCode = exitErr.ExitCode()
	case errors.Is(waitErr, exec.ErrWaitDelay):
		// Pipes were abandoned to a background child; the command itself exited.
		exitCode = cmd.ProcessState.ExitCode()
	default:
		return enc.SendErrorf("exec: wait %s: %v", argv[0], waitErr)
	}

	return enc.Encode(Message{Type: MsgExit, ExitCode: exitCode})
}

// pumpStdin feeds stdin frames to the child; a write error is silent because a child closing stdin early is normal.
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
