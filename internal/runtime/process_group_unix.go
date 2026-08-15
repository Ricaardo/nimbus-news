//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package runtime

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	terminateSignal = syscall.SIGTERM
	killSignal      = syscall.SIGKILL
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func processGroupID(process *os.Process) int {
	if process == nil {
		return 0
	}
	return process.Pid
}

func signalProcessGroup(process *os.Process, groupID int, signal os.Signal) error {
	if process == nil {
		return nil
	}
	unixSignal, ok := signal.(syscall.Signal)
	if !ok {
		return process.Signal(signal)
	}
	return syscall.Kill(-groupID, unixSignal)
}

func processGroupExists(groupID int) bool {
	if groupID <= 0 {
		return false
	}
	err := syscall.Kill(-groupID, 0)
	return err == nil || err == syscall.EPERM
}

func processGroupNeedsKill(groupID int) bool { return processGroupExists(groupID) }

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
