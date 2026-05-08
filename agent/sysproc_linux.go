//go:build linux

package agent

import (
	"os"
	"os/exec"
	"syscall"
)

// setupProcess puts the child in its own pgid and overrides
// exec.CommandContext's default cancel (which only SIGKILLs the immediate
// child) with a pgkill so background workers like `sh -c 'sleep 100 &'`
// don't survive ctx cancellation as root-owned orphans.
func setupProcess(cmd *exec.Cmd) (processController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return processController{}, nil
}
