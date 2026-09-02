//go:build linux

package agent

import (
	"os/exec"
	"syscall"
)

// setupProcess gives the child its own pgid and kills the whole group on cancel, so background workers do not outlive the session.
func setupProcess(cmd *exec.Cmd) (processController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return processController{}, nil
}
