//go:build !windows

package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
)

func supervisedShellSpec(t *testing.T, script string) adapter.CommandSpec {
	t.Helper()
	return adapter.CommandSpec{
		Executable: "/bin/sh",
		Args:       []string{"-c", script},
		Dir:        t.TempDir(),
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func waitForOutput(t *testing.T, buffer *lockedBuffer, token string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buffer.String(), token) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output did not contain %q: %q", token, buffer.String())
}

func processGone(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

func waitPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if processGone(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remained alive", pid)
}

func parsePIDLine(t *testing.T, output, prefix string) int {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
			if err != nil {
				t.Fatalf("parse %s line %q: %v", prefix, line, err)
			}
			return pid
		}
	}
	t.Fatalf("missing %s in %q", prefix, output)
	return 0
}

func TestProcessSupervisorTerminatesMultiLevelTreeAndPreservesOutput(t *testing.T) {
	var output lockedBuffer
	supervisor := NewProcessSupervisor(WithTerminationGrace(100 * time.Millisecond))
	script := `
set -eu
(
  trap '' TERM
  (trap '' TERM; sleep 30) &
  grandchild=$!
  echo grandchild:$grandchild
  wait $grandchild
) &
child=$!
echo child:$child
echo ready
wait $child
`
	process, err := supervisor.Start(context.Background(), supervisedShellSpec(t, script), &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &output, "ready")
	childPID := parsePIDLine(t, output.String(), "child:")
	grandchildPID := parsePIDLine(t, output.String(), "grandchild:")

	if err := process.TerminateTree(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	result := process.Wait()
	if !result.Cancelled || !result.CleanupResolved {
		t.Fatalf("result = %+v", result)
	}
	if process.IsAlive() {
		t.Fatal("process group remained alive after cancellation")
	}
	waitPIDGone(t, childPID)
	waitPIDGone(t, grandchildPID)
	if !strings.Contains(output.String(), "ready") {
		t.Fatalf("pre-cancel output was lost: %q", output.String())
	}

	// Cancellation is idempotent after the tree has already been reaped.
	if err := process.TerminateTree(10 * time.Millisecond); err != nil {
		t.Fatalf("repeated cancellation failed: %v", err)
	}
}

func TestProcessSupervisorTimeoutUsesSameTreeCleanup(t *testing.T) {
	var output lockedBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	supervisor := NewProcessSupervisor(WithTerminationGrace(40 * time.Millisecond))
	script := `trap '' TERM; (trap '' TERM; sleep 30) & child=$!; echo child:$child; echo before-timeout; wait $child`
	process, err := supervisor.Start(ctx, supervisedShellSpec(t, script), &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &output, "before-timeout")
	childPID := parsePIDLine(t, output.String(), "child:")
	result := process.Wait()
	if !result.TimedOut || result.Cancelled || !result.CleanupResolved {
		t.Fatalf("timeout result = %+v", result)
	}
	if process.IsAlive() {
		t.Fatal("process group remained alive after timeout")
	}
	waitPIDGone(t, childPID)
	if !strings.Contains(output.String(), "before-timeout") {
		t.Fatalf("pre-timeout output was lost: %q", output.String())
	}
}

func TestProcessSupervisorTERMIgnoreFallsBackToKILL(t *testing.T) {
	var output lockedBuffer
	grace := 80 * time.Millisecond
	process, err := NewProcessSupervisor().Start(
		context.Background(),
		supervisedShellSpec(t, `trap '' TERM; echo ignoring-term; while :; do sleep 1; done`),
		&output,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &output, "ignoring-term")
	started := time.Now()
	if err := process.TerminateTree(grace); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	result := process.Wait()
	if elapsed < grace {
		t.Fatalf("TERM grace was not observed: %s < %s", elapsed, grace)
	}
	if !result.Cancelled || !result.CleanupResolved {
		t.Fatalf("result = %+v", result)
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected forced non-zero exit, got %+v", result)
	}
}

func TestProcessSupervisorNaturalExitDrainsOutput(t *testing.T) {
	var stdout, stderr lockedBuffer
	process, err := NewProcessSupervisor().Start(
		context.Background(),
		supervisedShellSpec(t, `printf 'stdout-tail'; printf 'stderr-tail' >&2`),
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := process.Wait()
	if result.Err != nil || result.ExitCode != 0 || !result.CleanupResolved {
		t.Fatalf("result = %+v", result)
	}
	if got := stdout.String(); got != "stdout-tail" {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); got != "stderr-tail" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestTerminateExistingTreeIsIdempotent(t *testing.T) {
	var output lockedBuffer
	supervisor := NewProcessSupervisor(WithTerminationGrace(20 * time.Millisecond))
	process, err := supervisor.Start(context.Background(), supervisedShellSpec(t, `trap '' TERM; echo ready; sleep 30`), &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &output, "ready")
	pid := process.PID()
	if err := supervisor.TerminateExistingTree(context.Background(), pid, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	_ = process.Wait()
	if err := supervisor.TerminateExistingTree(context.Background(), pid, 20*time.Millisecond); err != nil {
		t.Fatalf("second recovery cleanup: %v", err)
	}
	if supervisor.IsTreeAlive(pid) {
		t.Fatalf("tree %d still alive", pid)
	}
	_ = fmt.Sprintf("%d", pid) // keep pid visible in race diagnostics
}

func TestProcessSupervisorNaturalLeaderExitCleansBackgroundDescendant(t *testing.T) {
	var output lockedBuffer
	process, err := NewProcessSupervisor(WithTerminationGrace(30*time.Millisecond)).Start(
		context.Background(),
		supervisedShellSpec(t, `(trap '' TERM; sleep 30) & child=$!; echo child:$child; echo leader-exit; exit 0`),
		&output,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &output, "leader-exit")
	childPID := parsePIDLine(t, output.String(), "child:")
	resultCh := make(chan adapter.ExecutionResult, 1)
	go func() { resultCh <- process.Wait() }()
	select {
	case result := <-resultCh:
		if result.Err != nil || result.ExitCode != 0 || !result.CleanupResolved {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait blocked on a background descendant holding output pipes")
	}
	waitPIDGone(t, childPID)
	if process.IsAlive() {
		t.Fatal("background descendant remained after leader exit")
	}
}

func TestProcessSupervisorFeedsProviderPromptThroughStdin(t *testing.T) {
	var stdout lockedBuffer
	spec := supervisedShellSpec(t, `IFS= read -r prompt; printf '{"type":"result","prompt":"%s"}\n' "$prompt"`)
	spec.Stdin = []byte("prompt sent outside argv\n")
	process, err := NewProcessSupervisor().Start(context.Background(), spec, &stdout, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	result := process.Wait()
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	if got := stdout.String(); !strings.Contains(got, `"prompt":"prompt sent outside argv"`) {
		t.Fatalf("stdout=%q", got)
	}
}

func TestFakeProviderCLICancellationRetainsStructuredOutput(t *testing.T) {
	var stdout lockedBuffer
	spec := supervisedShellSpec(t, `echo '{"type":"thread.started","thread_id":"fake-context"}'; echo '{"type":"item.completed","item":{"type":"agent_message","text":"before cancel"}}'; trap '' TERM; sleep 30`)
	process, err := NewProcessSupervisor(WithTerminationGrace(30*time.Millisecond)).Start(context.Background(), spec, &stdout, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &stdout, "before cancel")
	if err := process.TerminateTree(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	result := process.Wait()
	if !result.Cancelled || !result.CleanupResolved {
		t.Fatalf("result=%+v", result)
	}
	output := stdout.String()
	if !strings.Contains(output, "fake-context") || !strings.Contains(output, "before cancel") {
		t.Fatalf("structured output lost: %q", output)
	}
}
