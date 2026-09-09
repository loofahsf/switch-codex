//go:build unix

package scheduler

import (
	"os/exec"
	"sync"
	"syscall"
)

func prepareProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func attachProcess(cmd *exec.Cmd) (func(), func(), error) {
	var once sync.Once
	kill := func() {
		once.Do(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Process.Kill() })
	}
	return kill, kill, nil
}
