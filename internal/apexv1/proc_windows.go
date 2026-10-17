//go:build windows

package apexv1

import (
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// checkGroup is a running check's job object (D58 2.2, 2.4): a timeout
// terminates every process in it, its accounting is the check's CPU time,
// and it is created KILL_ON_JOB_CLOSE, so the check dies when ape's handle
// closes — ape ending, however it ends. Best effort: a process the check
// starts before it is assigned to the job (a few microseconds after the
// start) escapes it, and its leftovers are killed when it ends, not left
// running as on Linux and macOS.
type checkGroup struct {
	job windows.Handle
	pid int
}

func prepareCheck(c *exec.Cmd, _ string) (*checkGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	return &checkGroup{job: job}, nil
}

func (g *checkGroup) started(c *exec.Cmd) {
	g.pid = c.Process.Pid
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(g.pid)) //nolint:gosec // the pid ape just started
	if err != nil {
		return
	}
	_ = windows.AssignProcessToJobObject(g.job, h)
	_ = windows.CloseHandle(h)
}

// done closes the job: whatever the check left running ends with it.
func (g *checkGroup) done(bool) { _ = windows.CloseHandle(g.job) }

// kill terminates the job, and the started check c itself, which may not
// be in the job yet.
func (g *checkGroup) kill(c *exec.Cmd) error {
	err := windows.TerminateJobObject(g.job, 1)
	if c.Process != nil {
		_ = c.Process.Kill()
	}
	return err
}

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// cpu is the job's user+kernel time, every process it ever held included.
func (g *checkGroup) cpu() (time.Duration, bool) {
	var a jobAccounting
	if err := windows.QueryInformationJobObject(g.job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&a)), uint32(unsafe.Sizeof(a)), nil); err != nil {
		return 0, false
	}
	return time.Duration(a.TotalUserTime+a.TotalKernelTime) * 100, true
}

// processAlive reports whether a process with pid is still running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // a pid ape recorded
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}
