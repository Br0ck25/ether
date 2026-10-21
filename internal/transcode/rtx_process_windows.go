//go:build windows

package transcode

import (
	"fmt"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// StartRTX owns the entire native NGX process tree. NGX starts updater
// children; killing NVEnc alone leaves those processes and directory handles
// alive. Suspend before assigning the job so no child can escape assignment.
// This affects only a process Ether starts, never an existing user's process.
func (ExecRunner) StartRTX(c Command) (Process, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("RTX Video job: %w", err)
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	limits.BasicLimitInformation.ActiveProcessLimit = 32
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("RTX Video job limits: %w", err)
	}
	resumeProcess := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")
	if err := resumeProcess.Find(); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("RTX Video suspended launch unavailable: %w", err)
	}
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	cmd.WaitDelay = waitDelay
	hideWindow(cmd)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	fail := func(err error) (Process, error) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME|windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(fmt.Errorf("RTX Video process handle: %w", err))
	}
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(h)
		return fail(fmt.Errorf("RTX Video job assignment: %w", err))
	}
	status, _, _ := resumeProcess.Call(uintptr(h))
	if status != 0 {
		windows.CloseHandle(h)
		return fail(fmt.Errorf("RTX Video process resume status: %#x", status))
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	treeStopped := make(chan struct{})
	go func() {
		// Observe parent exit independently of exec.Wait's pipe draining: NGX
		// children can inherit those pipes. Terminating the job releases them.
		_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
		_ = windows.TerminateJobObject(job, 1)
		// Job termination is asynchronous. Wait for all owned descendants
		// to release their handles before publishing Done.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			var accounting struct {
				User, Kernel, PeriodUser, PeriodKernel int64
				PageFaults, Total, Active, Terminated  uint32
			}
			if err := windows.QueryInformationJobObject(job, 1, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil || accounting.Active == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		windows.CloseHandle(h)
		close(treeStopped)
	}()
	go func() { p.err = cmd.Wait(); <-treeStopped; windows.CloseHandle(job); close(p.done) }()
	return p, nil
}
