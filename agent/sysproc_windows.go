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

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

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

	closeJob := sync.OnceFunc(func() { _ = windows.CloseHandle(job) })
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	cmd.Cancel = func() error { closeJob(); return nil }

	return processController{
		afterStart: func(cmd *exec.Cmd) error { return assignToJob(job, cmd) },
		close:      closeJob,
	}, nil
}

func assignToJob(job windows.Handle, cmd *exec.Cmd) error {
	pid := uint32(cmd.Process.Pid) //nolint:gosec // the OS hands out PIDs as DWORDs; the int round-trip can't overflow
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, pid)
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(proc) //nolint:errcheck

	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		return fmt.Errorf("assign process to job object: %w", err)
	}
	return resumeProcess(proc)
}

func resumeProcess(proc windows.Handle) error {
	if err := ntResumeProcess.Find(); err != nil {
		return err
	}
	if status, _, _ := ntResumeProcess.Call(uintptr(proc)); status != 0 {
		return fmt.Errorf("resume process: NTSTATUS 0x%x", status)
	}
	return nil
}
