//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package runtime

import (
	"os"
	"os/exec"
)

var (
	terminateSignal os.Signal = os.Interrupt
	killSignal      os.Signal = os.Kill
)

func configureProcessGroup(_ *exec.Cmd) {}

func processGroupID(process *os.Process) int {
	if process == nil {
		return 0
	}
	return process.Pid
}

func signalProcessGroup(process *os.Process, _ int, signal os.Signal) error {
	if process == nil {
		return nil
	}
	if signal == killSignal {
		return process.Kill()
	}
	return process.Signal(signal)
}

// Other platforms have no portable process-group probe. Conservatively issue
// the final direct-process kill after the grace period, then rely on Kill's
// return rather than pretending a group can be probed.
func processGroupNeedsKill(groupID int) bool { return groupID > 0 }
func processGroupExists(_ int) bool          { return false }

func processExists(_ int) bool { return false }
