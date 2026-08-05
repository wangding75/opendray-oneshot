//go:build windows

package executor

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows process-tree supervision uses a dedicated, per One-shot Task Job
// Object configured with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. Killing that Job
// terminates the root process and every descendant that joined it, while never
// affecting other tasks, PTY sessions, or the OpenDray process itself.
//
// The child is created CREATE_SUSPENDED so it cannot spawn descendants before
// it is assigned to the Job; after assignment its threads are resumed. Every
// process the agent later spawns inherits the Job membership, so cancelling or
// timing out a run closes the Job Object and reaps the whole tree.

var (
	errWindowsProcessTree = errors.New("native Windows process-tree supervision requires a Job Object and is not supported in this build; use WSL2/Linux")
	windowsJobs           sync.Map // pid -> *windowsJobRegistry
)

const stillActiveWindows = 259

type windowsJob struct {
	handle windows.Handle // 0 once closed
}

// jobForLookup keeps processTreeAlive happy for live processes: a registered
// job handle left open keeps the tree reported alive so the common reaper
// closes it even after the root has exited (KILL_ON_JOB_CLOSE flushes any
// surviving descendant and frees the handle).

func platformSupervisionCapabilities() SupervisionCapabilities {
	return SupervisionCapabilities{
		Platform: runtime.GOOS, ProcessTree: true, GracefulTermination: true,
	}
}

func configureProcessTree(cmd *exec.Cmd) error {
	// CREATE_NEW_PROCESS_GROUP gives the child an independent console process
	// group; CREATE_SUSPENDED keeps it from forking descendants until the Job
	// assignment below completes.
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
	return nil
}

// adoptProcessTree binds the running child to a fresh Job Object and resumes it.
// It is invoked by the common supervisor immediately after cmd.Start succeeds.
func adoptProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("One-shot child process handle is unavailable")
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return errors.New("One-shot child process has no PID")
	}

	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false, uint32(pid),
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			// The process already exited before we could adopt it. Nothing
			// to manage; the common path will reap the (already finished) result.
			return nil
		}
		return fmt.Errorf("failed to open One-shot child process: %w", err)
	}
	defer windows.CloseHandle(processHandle)

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("failed to create Windows Job Object: %w", err)
	}

	cleanupJob := func() {
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
	}

	// Configure KILL_ON_JOB_CLOSE: closing the last Job handle terminates all
	// processes associated with it.
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		cleanupJob()
		return fmt.Errorf("failed to configure Windows Job Object: %w", err)
	}

	if err := windows.AssignProcessToJobObject(job, processHandle); err != nil {
		cleanupJob()
		return fmt.Errorf("failed to assign One-shot child process to Job Object: %w", err)
	}

	windowsJobs.Store(pid, &windowsJob{handle: job})

	// The process was created suspended; resume its threads so it can run.
	if err := resumeSuspendedThreads(pid); err != nil {
		closeWindowsJob(pid)
		return fmt.Errorf("failed to resume suspended One-shot child process: %w", err)
	}
	return nil
}

// processTreeAlive reports whether the process tree still needs cleanup. For a
// live supervised task it returns true while the Job Object handle is still
// open (so the common reaper always closes it and flushes descendants), and
// otherwise asks the OS whether the root process is still running.
func processTreeAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if v, ok := windowsJobs.Load(pid); ok {
		reg := v.(*windowsJob)
		return reg.handle != 0
	}
	return windowsProcessAlive(pid)
}

// terminateProcessTree gracefully terminates the tree. Windows provides no
// portable SIGTERM for arbitrary non-console children, so this degrades to
// closing the Job Object, which terminates the root and all descendants.
func terminateProcessTree(pid int) error {
	return killProcessTree(pid)
}

// killProcessTree terminates the whole tree by closing its Job Object. If no
// Job was registered (e.g. recovered gateway process), it falls back to
// terminating the root process directly.
func killProcessTree(pid int) error {
	if !closeWindowsJob(pid) {
		// No Job Object registered: best effort to stop the root process.
		processHandle, err := windows.OpenProcess(
			windows.PROCESS_TERMINATE, false, uint32(pid),
		)
		if err != nil {
			if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
				return nil // process already gone
			}
			return err
		}
		defer windows.CloseHandle(processHandle)
		return windows.TerminateProcess(processHandle, 1)
	}
	return nil
}

// windowsProcessAlive reports whether the root process is still running.
func windowsProcessAlive(pid int) bool {
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid),
	)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(processHandle)
	var code uint32
	if err := windows.GetExitCodeProcess(processHandle, &code); err != nil {
		return false
	}
	return code == stillActiveWindows
}

// closeWindowsJob closes the registered Job Object for pid exactly once. It
// returns true if a Job Object was closed (KILL_ON_JOB_CLOSE terminates the
// tree).
func closeWindowsJob(pid int) bool {
	v, ok := windowsJobs.LoadAndDelete(pid)
	if !ok {
		return false
	}
	reg := v.(*windowsJob)
	if reg.handle == 0 {
		return false
	}
	_ = windows.CloseHandle(reg.handle)
	reg.handle = 0
	return true
}

// resumeSuspendedThreads resumes all threads of pid that were created
// suspended (the primary thread from CREATE_SUSPENDED and any threads it had
// spawned before suspension). It fails only if no thread could be resumed.
func resumeSuspendedThreads(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	resumed := 0
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return fmt.Errorf("failed to enumerate threads: %w", err)
	}
	for {
		if entry.OwnerProcessID == uint32(pid) {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr == nil {
				_, _ = windows.ResumeThread(thread)
				_ = windows.CloseHandle(thread)
				resumed++
			}
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			break
		}
	}
	if resumed == 0 {
		return errors.New("no threads found to resume for suspended One-shot child process")
	}
	return nil
}
