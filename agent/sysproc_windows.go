//go:build windows

package agent

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsProcessController owns the Job Object that backs one runExec
// session. cmd.Cancel and processController.close both fire close():
// Cancel covers ctx-cancel during cmd.Wait, Close (via runExec's defer)
// covers the paths Cancel never reaches — normal exit, cmd.Start failure.
type windowsProcessController struct {
	job  windows.Handle
	once sync.Once
}

func (c *windowsProcessController) assign(cmd *exec.Cmd) error {
	pid := uint32(cmd.Process.Pid) //nolint:gosec // the OS hands out PIDs as DWORDs; the int round-trip can't overflow
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

// setupProcess creates a kill-on-close Job Object so the child's whole
// process tree dies when the session ends — background workers spawned
// by the child don't outlive runExec.
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
		uintptr(unsafe.Pointer(&info)), //nolint:gosec // SetInformationJobObject requires a JOBOBJECT_EXTENDED_LIMIT_INFORMATION pointer
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
