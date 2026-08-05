//go:build windows

package executor

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
)

// The Windows tests drive the process-tree supervisor through its public
// ProcessSupervisor.Start/Process API using this test binary itself as a
// controlled child helper program (no third-party CLI required). TestMain
// dispatches sub-invocations based on the B2F1_SLAVE environment variable.

func TestMain(m *testing.M) {
	flag.Parse()
	switch os.Getenv("B2F1_SLAVE") {
	case "echo":
		fmt.Fprintf(os.Stdout, "B2F1_ECHO_OK\n")
		fmt.Fprintf(os.Stdout, "B2F1_ECHO_ARGS:%s\n", strings.Join(flag.Args(), "|"))
		os.Exit(0)
	case "parent":
		fmt.Fprintf(os.Stdout, "PARENT_STARTED:%d\n", os.Getpid())
		_ = os.Stdout.Sync()
		child := exec.Command(exePath())
		child.Env = append(os.Environ(), "B2F1_SLAVE=child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(99)
		}
		fmt.Fprintf(os.Stdout, "CHILD_STARTED:%d\n", child.Process.Pid)
		_ = os.Stdout.Sync()
		// Hold the tree until the supervisor's Job Object kills it. A busy
		// wait is required: an empty select would trip the Go runtime deadlock
		// detector and abort with a non-zero code.
		for {
			time.Sleep(time.Hour)
		}
	case "child":
		fmt.Fprintf(os.Stdout, "CHILD_READY:%d\n", os.Getpid())
		_ = os.Stdout.Sync()
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

func exePath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return os.Args[0]
}

// TestHelperBinary asserts the expected connection is present on this host.
func testHelperBinary() string { return exePath() }

type winLockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *winLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *winLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func waitForToken(t *testing.T, buffer *winLockedBuffer, token string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buffer.String(), token) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output did not contain %q: %q", token, buffer.String())
}

func parsePID(t *testing.T, output, prefix string) int {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			var pid int
			if _, err := fmt.Sscanf(line[len(prefix):], "%d", &pid); err != nil {
				t.Fatalf("parse %s: %v", line, err)
			}
			return pid
		}
	}
	t.Fatalf("missing %s in %q", prefix, output)
	return 0
}

func waitPIDGoneWindows(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if !windowsProcessAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d remained alive", pid)
}

func waitPIDAliveWindows(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if windowsProcessAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d did not become live", pid)
}

func windowsSpec(dir string, extraArgs ...string) adapter.CommandSpec {
	return adapter.CommandSpec{
		Executable: exePath(),
		Args:       extraArgs,
		Dir:        dir,
	}
}

func getTestDir(t *testing.T) string {
	return t.TempDir()
}

// TestWindowsSupervisorNormalStart covers 7.1: a controlled helper process
// starts, writes fixed output, returns exit 0, Wait succeeds, and the
// supervisor no longer reports an unsupported provider.
func TestWindowsSupervisorNormalStart(t *testing.T) {
	var output winLockedBuffer
	supervisor := NewProcessSupervisor(WithTerminationGrace(200 * time.Millisecond))
	cap := supervisor.Capabilities()
	if !cap.ProcessTree {
		t.Fatalf("expected ProcessTree support on Windows, got %+v", cap)
	}
	spec := windowsSpec(t.TempDir())
	spec.Environment = map[string]adapter.EnvironmentValue{"B2F1_SLAVE": {Value: "echo"}}
	process, err := supervisor.Start(context.Background(), spec, &output, &output)
	if err != nil {
		t.Fatalf("Start returned unsupported/error: %v", err)
	}
	result := process.Wait()
	if result.Err != nil || result.ExitCode != 0 || !result.CleanupResolved {
		t.Fatalf("result = %+v, output=%q", result, output.String())
	}
	if !strings.Contains(output.String(), "B2F1_ECHO_OK") {
		t.Fatalf("stdout missing fixed output: %q", output.String())
	}
	if process.IsAlive() {
		t.Fatal("process reported alive after normal completion")
	}
}

// TestWindowsSupervisorPathWithSpaces covers 7.2: working directory containing
// spaces plus an argument containing spaces; argv must not be shell-split.
func TestWindowsSupervisorPathWithSpaces(t *testing.T) {
	spaceDir := filepath.Join(os.TempDir(), "b2f1 spaced dir nested")
	if err := os.MkdirAll(spaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(spaceDir)

	var output winLockedBuffer
	spec := adapter.CommandSpec{
		Executable:  exePath(),
		Args:        []string{"alpha beta gamma"},
		Dir:         spaceDir,
		Environment: map[string]adapter.EnvironmentValue{"B2F1_SLAVE": {Value: "echo"}},
	}
	process, err := NewProcessSupervisor().Start(context.Background(), spec, &output, &output)
	if err != nil {
		t.Fatalf("Start with spaces failed: %v", err)
	}
	result := process.Wait()
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := output.String(); !strings.Contains(got, "B2F1_ECHO_ARGS:alpha beta gamma") {
		t.Fatalf("argv was split: %q", got)
	}
}

// TestWindowsSupervisorChildTreeCancel covers 7.3: root spawns a child; cancel
// terminates the whole tree and leaves an unrelated process alive.
func TestWindowsSupervisorChildTreeCancel(t *testing.T) {
	var output winLockedBuffer
	spec := windowsSpec(t.TempDir())
	spec.Environment = map[string]adapter.EnvironmentValue{"B2F1_SLAVE": {Value: "parent"}}
	process, err := NewProcessSupervisor(WithTerminationGrace(150*time.Millisecond)).Start(
		context.Background(), spec, &output, &output,
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForToken(t, &output, "PARENT_STARTED:")
	waitForToken(t, &output, "CHILD_STARTED:")
	parentPID := parsePID(t, output.String(), "PARENT_STARTED:")
	childPID := parsePID(t, output.String(), "CHILD_STARTED:")
	waitPIDAliveWindows(t, childPID)

	// Unrelated live process that must survive the cancellation.
	var unrelatedOut winLockedBuffer
	unrelated := windowsSpec(t.TempDir())
	unrelated.Environment = map[string]adapter.EnvironmentValue{"B2F1_SLAVE": {Value: "child"}}
	unrelatedProc, err := NewProcessSupervisor().Start(context.Background(), unrelated, &unrelatedOut, &unrelatedOut)
	if err != nil {
		t.Fatal(err)
	}
	waitForToken(t, &unrelatedOut, "CHILD_READY:")
	unrelatedPID := parsePID(t, unrelatedOut.String(), "CHILD_READY:")

	if err := process.TerminateTree(150 * time.Millisecond); err != nil {
		t.Fatalf("TerminateTree: %v", err)
	}
	result := process.Wait()
	if !result.Cancelled || !result.CleanupResolved {
		t.Fatalf("cancelled result = %+v, output=%q", result, output.String())
	}
	waitPIDGoneWindows(t, parentPID)
	waitPIDGoneWindows(t, childPID)
	if process.IsAlive() {
		t.Fatal("tree still alive after cancellation")
	}
	if !windowsProcessAlive(unrelatedPID) {
		t.Fatal("unrelated process was killed by another task cancellation")
	}
	if err := unrelatedProc.TerminateTree(150 * time.Millisecond); err != nil {
		t.Fatalf("cleanup of unrelated process: %v", err)
	}
	_ = unrelatedProc.Wait()
}

// TestWindowsSupervisorTwoTaskIsolation covers 7.4: two independent trees share
// no Job Object; cancelling A leaves B running.
func TestWindowsSupervisorTwoTaskIsolation(t *testing.T) {
	var outA, outB winLockedBuffer
	spec := func(out *winLockedBuffer) (*SupervisedProcess, int, int) {
		s := windowsSpec(getTestDir(t))
		s.Environment = map[string]adapter.EnvironmentValue{"B2F1_SLAVE": {Value: "parent"}}
		p, err := NewProcessSupervisor(WithTerminationGrace(150*time.Millisecond)).Start(context.Background(), s, out, out)
		if err != nil {
			t.Fatal(err)
		}
		waitForToken(t, out, "PARENT_STARTED:")
		waitForToken(t, out, "CHILD_STARTED:")
		return p, parsePID(t, out.String(), "PARENT_STARTED:"), parsePID(t, out.String(), "CHILD_STARTED:")
	}

	procA, aRoot, aChild := spec(&outA)
	procB, bRoot, bChild := spec(&outB)
	waitPIDAliveWindows(t, aChild)
	waitPIDAliveWindows(t, bChild)

	if err := procA.TerminateTree(150 * time.Millisecond); err != nil {
		t.Fatalf("cancel A: %v", err)
	}
	_ = procA.Wait()
	waitPIDGoneWindows(t, aRoot)
	waitPIDGoneWindows(t, aChild)

	if !windowsProcessAlive(bRoot) || !windowsProcessAlive(bChild) {
		t.Fatal("task B tree was affected by cancelling task A")
	}
	if err := procB.TerminateTree(150 * time.Millisecond); err != nil {
		t.Fatalf("cleanup B: %v", err)
	}
	_ = procB.Wait()
	waitPIDGoneWindows(t, bRoot)
	waitPIDGoneWindows(t, bChild)
}

// TestWindowsSupervisorStartFailureCleanup covers 7.5: a non-existent
// executable fails Start without a panic, leaves no visible process, and
// repeated cleanup is idempotent.
func TestWindowsSupervisorStartFailureCleanup(t *testing.T) {
	spec := windowsSpec(t.TempDir())
	spec.Executable = filepath.Join(t.TempDir(), "does-not-exist-"+fmt.Sprint(time.Now().UnixNano())+".exe")
	var output winLockedBuffer
	process, err := NewProcessSupervisor().Start(context.Background(), spec, &output, &output)
	if err == nil {
		_ = process.TerminateTree(50 * time.Millisecond)
		_ = process.Wait()
		t.Fatal("Start succeeded for a non-existent executable")
	}

	// Repeated cleanup (even on a never-started process) must not panic.
	closeWindowsJob(999991)
	closeWindowsJob(999991)
	if processTreeAlive(999991) {
		t.Fatal("phantom process flagged alive")
	}
}

// TestWindowsSupervisorNormalCompletionCleanup covers 7.6: a short process
// completes, Wait succeeds, and cleanup is safe to call afterwards (no second
// kill, no invalid-handle error, no panic).
func TestWindowsSupervisorNormalCompletionCleanup(t *testing.T) {
	var output winLockedBuffer
	spec := windowsSpec(t.TempDir())
	spec.Environment = map[string]adapter.EnvironmentValue{"B2F1_SLAVE": {Value: "echo"}}
	process, err := NewProcessSupervisor().Start(context.Background(), spec, &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	pid := process.PID()
	result := process.Wait()
	if result.Err != nil || result.ExitCode != 0 || !result.CleanupResolved {
		t.Fatalf("result = %+v", result)
	}
	// Job Object handle must already be closed and the registry drained.
	if processTreeAlive(pid) {
		t.Fatalf("tree %d still flagged alive after completion", pid)
	}
	if process.IsAlive() {
		t.Fatalf("process %d reported alive after completion", pid)
	}
	// TerminateTree/KillTree after completion are idempotent, no panics.
	if err := process.TerminateTree(50 * time.Millisecond); err != nil {
		t.Fatalf("post-completion TerminateTree: %v", err)
	}
	if err := process.KillTree(); err != nil {
		t.Fatalf("post-completion KillTree: %v", err)
	}
}
