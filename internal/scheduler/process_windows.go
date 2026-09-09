package scheduler

import (
	"errors"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
func attachProcess(cmd *exec.Cmd) (func(), func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, errors.New("无法创建 CLI 进程组")
	}
	fail := func() (func(), func(), error) {
		windows.CloseHandle(job)
		return nil, nil, errors.New("无法管理 CLI 进程组")
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fail()
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail()
	}
	defer windows.CloseHandle(handle)
	if err = windows.AssignProcessToJobObject(job, handle); err != nil {
		return fail()
	}
	var killOnce, closeOnce sync.Once
	kill := func() { killOnce.Do(func() { _ = windows.TerminateJobObject(job, 1); _ = cmd.Process.Kill() }) }
	cleanup := func() { kill(); closeOnce.Do(func() { _ = windows.CloseHandle(job) }) }
	return kill, cleanup, nil
}
