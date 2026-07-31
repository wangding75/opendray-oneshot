//go:build linux

package executor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func platformSupervisionCapabilities() SupervisionCapabilities {
	return SupervisionCapabilities{Platform: runtime.GOOS, ProcessTree: true, GracefulTermination: true}
}

func configureProcessTree(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

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

// processTreeAlive ignores zombie-only process groups. A zombie cannot execute
// work and may remain visible until the container's init process reaps it; it
// must not turn successful whole-tree cleanup into a false cancellation error.
func processTreeAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	entries, err := filepath.Glob("/proc/[0-9]*/stat")
	if err == nil && len(entries) > 0 {
		for _, path := range entries {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			state, processGroup, ok := linuxStatStateAndGroup(string(data))
			if ok && processGroup == pid && state != "Z" {
				return true
			}
		}
		return false
	}
	fallback := syscall.Kill(-pid, 0)
	return fallback == nil || errors.Is(fallback, syscall.EPERM)
}

func linuxStatStateAndGroup(stat string) (string, int, bool) {
	// /proc/<pid>/stat: pid (comm, which may contain spaces) state ppid pgrp ...
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 || closeParen+2 >= len(stat) {
		return "", 0, false
	}
	fields := strings.Fields(stat[closeParen+2:])
	if len(fields) < 3 {
		return "", 0, false
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, false
	}
	return fields[0], group, true
}
