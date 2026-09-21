//go:build linux

package agent

import (
	"os/exec"
	"syscall"
)

// setupProcess gives the child its own pgid and kills the whole group on cancel; a normal exit leaves a daemonized grandchild alone.
func setupProcess(cmd *exec.Cmd) (processController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return processController{}, nil
}
