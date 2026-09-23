//go:build windows

package agent

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsProcessController owns the Job Object of one runExec session; cmd.Cancel and runExec's deferred close both release it.
type windowsProcessController struct {
	job   windows.Handle
	close func()
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
	return resumeInitialThread(pid)
}

func (c *windowsProcessController) cancel() error {
	c.close()
	return nil
}

// setupProcess creates a kill-on-close Job Object so the child's whole process tree dies with the session.
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

	ctl := &windowsProcessController{job: job, close: sync.OnceFunc(func() { _ = windows.CloseHandle(job) })}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	cmd.Cancel = ctl.cancel

	return processController{
		afterStart: ctl.assign,
		close:      ctl.close,
	}, nil
}

func resumeInitialThread(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot threads: %w", err)
	}
	defer windows.CloseHandle(snap) //nolint:errcheck

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	err = windows.Thread32First(snap, &entry)
	for err == nil && entry.OwnerProcessID != pid {
		err = windows.Thread32Next(snap, &entry)
	}
	if err != nil {
		return fmt.Errorf("find thread of process %d: %w", pid, err)
	}

	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
	if err != nil {
		return fmt.Errorf("open thread: %w", err)
	}
	defer windows.CloseHandle(thread) //nolint:errcheck

	if _, err := windows.ResumeThread(thread); err != nil {
		return fmt.Errorf("resume thread: %w", err)
	}
	return nil
}
