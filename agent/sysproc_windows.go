//go:build windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setupProcess assigns the child to a kill-on-close Job Object so ctx
// cancellation and client disconnect tear down the whole process tree, not
// just the immediate child process.
func setupProcess(cmd *exec.Cmd) (processController, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return processController{}, fmt.Errorf("create job object: %w", err)
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), //nolint:gosec // Windows API requires a pointer to JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return processController{}, fmt.Errorf("set job object limits: %w", err)
	}

	ctl := &windowsProcessController{job: job}
	cmd.Cancel = ctl.cancel

	return processController{
		afterStart: ctl.assign,
		close:      ctl.close,
	}, nil
}

type windowsProcessController struct {
	job  windows.Handle
	once sync.Once
}

func (c *windowsProcessController) assign(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	const maxWindowsProcessID = int64(^uint32(0))

	pid64 := int64(cmd.Process.Pid)
	if pid64 <= 0 || pid64 > maxWindowsProcessID {
		return fmt.Errorf("process id %d is outside uint32 range", cmd.Process.Pid)
	}
	pid := uint32(pid64) //nolint:gosec // Checked above before passing to the Windows API.
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(proc) //nolint:errcheck

	if err := windows.AssignProcessToJobObject(c.job, proc); err != nil {
		return fmt.Errorf("assign process to job object: %w", err)
	}
	return nil
}

func (c *windowsProcessController) cancel() error {
	c.close()
	return nil
}

func (c *windowsProcessController) close() {
	c.once.Do(func() {
		_ = windows.CloseHandle(c.job)
	})
}
