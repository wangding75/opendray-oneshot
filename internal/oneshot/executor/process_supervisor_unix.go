//go:build !windows && !linux

package executor

import (
	"errors"
	"os/exec"
	"runtime"
	"syscall"
)

func platformSupervisionCapabilities() SupervisionCapabilities {
	return SupervisionCapabilities{Platform: runtime.GOOS, ProcessTree: true, GracefulTermination: true}
}

func configureProcessTree(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// adoptProcessTree binds a running child to a platform tree resource. The other
// Unix build manages the tree through the process group configured in
// configureProcessTree, so there is nothing additional to bind.
func adoptProcessTree(_ *exec.Cmd) error { return nil }

func terminateProcessTree(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killProcessTree(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processTreeAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
