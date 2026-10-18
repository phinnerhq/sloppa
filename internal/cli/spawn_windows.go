//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// command runs a shell command line as Node does: `cmd.exe /d /s /c "<line>"`, the line verbatim,
// since it's escaped for cmd.exe already.
func command(s spawnCommand) *exec.Cmd {
	if !s.shell {
		return exec.Command(s.command, s.args...)
	}
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.Command(shell)
	argv0 := shell
	if strings.ContainsAny(argv0, " \t") {
		argv0 = `"` + argv0 + `"`
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: fmt.Sprintf(`%s /d /s /c "%s"`, argv0, s.command)}
	return cmd
}

// prepare starts the dev server suspended, so it's in its job before it can start anything.
func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

// processTree is the dev server's job object, which everything it starts joins: terminating the job
// ends them all, and so does this command's own end, however it ends, since the job is closed with
// it. Without a job, as where jobs can't nest on old Windows, the tree is killed with
// `taskkill /T /F` instead.
type processTree struct {
	job windows.Handle
	pid int
}

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

func attach(cmd *exec.Cmd) (processTree, error) {
	t := processTree{pid: cmd.Process.Pid}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(t.pid))
	if err != nil {
		cmd.Process.Kill()
		return t, fmt.Errorf("opening its process: %w", err)
	}
	defer windows.CloseHandle(process)
	if job, err := killOnCloseJob(); err == nil {
		if windows.AssignProcessToJobObject(job, process) == nil {
			t.job = job
		} else {
			windows.CloseHandle(job)
		}
	}
	if status, _, _ := ntResumeProcess.Call(uintptr(process)); status != 0 {
		cmd.Process.Kill()
		t.release()
		return t, fmt.Errorf("resuming it: %w", windows.NTStatus(status))
	}
	return t, nil
}

func killOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// signal can't send a signal on Windows. Ctrl-C is the exception: the console sends it to the dev
// server too, so it's worth waiting for the dev server to act on it.
func (t processTree) signal(sig os.Signal) bool { return sig == os.Interrupt }

// alive is about the dev server alone: kill takes care of whatever it left behind.
func (t processTree) alive() bool { return false }

func (t processTree) kill() {
	if t.job != 0 {
		windows.TerminateJobObject(t.job, 1)
		return
	}
	exec.Command("taskkill", "/pid", strconv.Itoa(t.pid), "/T", "/F").Run()
}

func (t processTree) release() {
	if t.job != 0 {
		windows.CloseHandle(t.job)
	}
}
