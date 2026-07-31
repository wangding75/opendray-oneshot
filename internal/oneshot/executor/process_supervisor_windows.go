//go:build windows

package executor

import (
	"errors"
	"os/exec"
	"runtime"
)

var errWindowsProcessTreeUnsupported = errors.New("native Windows process-tree supervision requires a Job Object and is not supported in this build; use WSL2/Linux")

func platformSupervisionCapabilities() SupervisionCapabilities {
	return SupervisionCapabilities{
		Platform: runtime.GOOS, ProcessTree: false, GracefulTermination: false,
		UnsupportedReason: errWindowsProcessTreeUnsupported.Error(),
	}
}

func configureProcessTree(_ *exec.Cmd) error { return errWindowsProcessTreeUnsupported }
func terminateProcessTree(_ int) error       { return errWindowsProcessTreeUnsupported }
func killProcessTree(_ int) error            { return errWindowsProcessTreeUnsupported }
func processTreeAlive(_ int) bool            { return false }
