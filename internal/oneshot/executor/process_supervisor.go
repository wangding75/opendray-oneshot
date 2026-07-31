package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const defaultTerminationGrace = 2 * time.Second

// SupervisionCapabilities describe host-level process-tree support.
type SupervisionCapabilities struct {
	Platform            string `json:"platform"`
	ProcessTree         bool   `json:"process_tree"`
	GracefulTermination bool   `json:"graceful_termination"`
	UnsupportedReason   string `json:"unsupported_reason,omitempty"`
}

type terminationReason string

const (
	terminationNone    terminationReason = ""
	terminationCancel  terminationReason = "cancel"
	terminationTimeout terminationReason = "timeout"
	terminationKill    terminationReason = "kill"
)

// ProcessSupervisor starts ordinary child processes in an isolated OS process
// tree and owns cancellation/timeout cleanup. It is independent from PTY and
// Interactive Session termination code.
type ProcessSupervisor struct {
	now         func() time.Time
	gracePeriod time.Duration
}

type ProcessSupervisorOption func(*ProcessSupervisor)

func WithTerminationGrace(grace time.Duration) ProcessSupervisorOption {
	return func(supervisor *ProcessSupervisor) {
		if grace > 0 {
			supervisor.gracePeriod = grace
		}
	}
}

func NewProcessSupervisor(options ...ProcessSupervisorOption) *ProcessSupervisor {
	supervisor := &ProcessSupervisor{
		now:         func() time.Time { return time.Now().UTC() },
		gracePeriod: defaultTerminationGrace,
	}
	for _, option := range options {
		option(supervisor)
	}
	return supervisor
}

func (s *ProcessSupervisor) Capabilities() SupervisionCapabilities {
	return platformSupervisionCapabilities()
}

// SupervisedProcess is a started process tree. Exactly one goroutine calls
// cmd.Wait; public Wait calls are idempotent and only observe the durable
// result after stdout/stderr copying has drained.
type SupervisedProcess struct {
	ctx       context.Context
	cmd       *exec.Cmd
	pid       int
	startedAt time.Time
	now       func() time.Time

	done       chan struct{}
	outputDone chan error

	resultMu sync.RWMutex
	result   adapter.ExecutionResult

	actionMu      sync.Mutex
	terminationMu sync.Mutex
	reason        terminationReason
	cleanupErr    error
}

func (s *ProcessSupervisor) Start(ctx context.Context, spec adapter.CommandSpec, stdout, stderr io.Writer) (*SupervisedProcess, error) {
	if s == nil {
		return nil, domain.NewDomainError(domain.ErrorProviderUnavailable, "One-shot process supervisor is unavailable", nil)
	}
	capability := s.Capabilities()
	if !capability.ProcessTree {
		return nil, domain.NewDomainError(domain.ErrorUnsupportedProvider, capability.UnsupportedReason, nil)
	}
	if err := validateCommandSpec(spec); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	info, err := os.Stat(spec.Dir)
	if err != nil {
		return nil, domain.NewDomainError(domain.ErrorExecutionFailed, "working directory is unavailable", err)
	}
	if !info.IsDir() {
		return nil, domain.NewDomainError(domain.ErrorExecutionFailed, "working directory is not a directory", nil)
	}
	if _, err := os.Stat(spec.Executable); err != nil {
		return nil, domain.NewDomainError(domain.ErrorProviderUnavailable, "One-shot executable is unavailable", err)
	}

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, domain.NewDomainError(domain.ErrorExecutionFailed, "failed to create stdout pipe", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, domain.NewDomainError(domain.ErrorExecutionFailed, "failed to create stderr pipe", err)
	}
	closePipes := func() {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
	}

	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.ProcessEnvironment()
	// Use explicit *os.File pipes rather than os/exec's internal writer pipes.
	// cmd.Wait can then return when the leader exits even when a background
	// descendant inherited stdout/stderr. The supervisor kills the remaining
	// process group before waiting for our copy goroutines to observe EOF.
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	if err := configureProcessTree(cmd); err != nil {
		closePipes()
		return nil, domain.NewDomainError(domain.ErrorUnsupportedProvider, "failed to configure process-tree supervision", err)
	}
	startedAt := s.now().UTC()
	if err := cmd.Start(); err != nil {
		closePipes()
		return nil, domain.NewDomainError(domain.ErrorExecutionFailed, "failed to start One-shot child process", err)
	}
	// The parent must not keep child-side descriptors open.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	process := &SupervisedProcess{
		ctx: ctx, cmd: cmd, pid: cmd.Process.Pid, startedAt: startedAt,
		now: s.now, done: make(chan struct{}), outputDone: make(chan error, 2),
	}
	go process.copyOutput(stdoutReader, stdout)
	go process.copyOutput(stderrReader, stderr)
	go process.reap()
	go process.watchContext(s.gracePeriod)
	return process, nil
}

func (p *SupervisedProcess) watchContext(grace time.Duration) {
	select {
	case <-p.done:
		return
	case <-p.ctx.Done():
		reason := terminationCancel
		if errors.Is(p.ctx.Err(), context.DeadlineExceeded) {
			reason = terminationTimeout
		}
		_ = p.terminate(reason, grace)
	}
}

func (p *SupervisedProcess) copyOutput(reader *os.File, target io.Writer) {
	_, err := io.Copy(target, reader)
	closeErr := reader.Close()
	if err == nil {
		err = closeErr
	}
	p.outputDone <- err
}

func (p *SupervisedProcess) reap() {
	err := p.cmd.Wait()
	finishedAt := p.now().UTC()

	// A provider may let its launcher exit before descendants. The process group
	// remains authoritative; force-clean any surviving descendants so a natural
	// parent exit cannot leak background work.
	cleanupResolved := true
	if processTreeAlive(p.pid) {
		if killErr := killProcessTree(p.pid); killErr != nil {
			cleanupResolved = false
			p.setCleanupError(killErr)
		} else if !waitTreeGone(p.pid, 500*time.Millisecond) {
			cleanupResolved = false
			p.setCleanupError(errors.New("process tree remained alive after SIGKILL"))
		}
	}

	// The group is now absent, so inherited stdout/stderr descriptors are closed.
	// Wait for both copy loops before publishing the terminal result.
	var outputErr error
	for i := 0; i < 2; i++ {
		if copyErr := <-p.outputDone; copyErr != nil {
			outputErr = errors.Join(outputErr, copyErr)
		}
	}

	exitCode := -1
	if p.cmd.ProcessState != nil {
		exitCode = p.cmd.ProcessState.ExitCode()
	}
	result := adapter.ExecutionResult{
		PID: p.pid, ExitCode: exitCode, StartedAt: p.startedAt,
		FinishedAt: finishedAt, CleanupResolved: cleanupResolved,
	}

	p.terminationMu.Lock()
	reason := p.reason
	cleanupErr := p.cleanupErr
	p.terminationMu.Unlock()

	switch reason {
	case terminationTimeout:
		result.TimedOut = true
		if cleanupErr != nil || !cleanupResolved {
			result.Err = domain.NewDomainError(domain.ErrorCancelFailed, "timed-out process tree cleanup failed", cleanupErr)
		} else {
			result.Err = domain.NewDomainError(domain.ErrorTimeout, "One-shot child process timed out", context.DeadlineExceeded)
		}
	case terminationCancel, terminationKill:
		result.Cancelled = true
		if cleanupErr != nil || !cleanupResolved {
			result.Err = domain.NewDomainError(domain.ErrorCancelFailed, "cancelled process tree cleanup failed", cleanupErr)
		} else {
			result.Err = context.Canceled
		}
	default:
		switch {
		case outputErr != nil:
			result.Err = domain.NewDomainError(domain.ErrorOutputPersistFailed, "failed to drain One-shot process output", outputErr)
		case err != nil:
			var exitErr *exec.ExitError
			switch {
			case errors.As(err, &exitErr):
				result.Err = domain.NewDomainError(domain.ErrorExecutionFailed, fmt.Sprintf("One-shot child exited with code %d", exitCode), nil)
			default:
				result.Err = domain.NewDomainError(domain.ErrorExecutionFailed, "One-shot child process failed", err)
			}
		}
	}

	p.resultMu.Lock()
	p.result = result
	p.resultMu.Unlock()
	close(p.done)
}

func (p *SupervisedProcess) setCleanupError(err error) {
	if err == nil {
		return
	}
	p.terminationMu.Lock()
	if p.cleanupErr == nil {
		p.cleanupErr = err
	}
	p.terminationMu.Unlock()
}

func (p *SupervisedProcess) PID() int {
	if p == nil {
		return 0
	}
	return p.pid
}

func (p *SupervisedProcess) StartedAt() time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.startedAt
}

// IsAlive reports whether the process group still contains any process.
func (p *SupervisedProcess) IsAlive() bool {
	if p == nil || p.pid <= 0 {
		return false
	}
	return processTreeAlive(p.pid)
}

// TerminateTree performs graceful TERM followed by KILL after grace. It waits
// until the entire tree is absent, so callers may persist cancelled only after
// this method returns nil.
func (p *SupervisedProcess) TerminateTree(grace time.Duration) error {
	if grace <= 0 {
		grace = defaultTerminationGrace
	}
	return p.terminate(terminationCancel, grace)
}

// KillTree immediately terminates the process group and waits for reaping.
func (p *SupervisedProcess) KillTree() error {
	return p.terminate(terminationKill, 0)
}

func (p *SupervisedProcess) terminate(reason terminationReason, grace time.Duration) error {
	if p == nil || p.pid <= 0 {
		return nil
	}
	p.actionMu.Lock()
	defer p.actionMu.Unlock()
	select {
	case <-p.done:
		p.terminationMu.Lock()
		err := p.cleanupErr
		p.terminationMu.Unlock()
		return err
	default:
	}
	p.terminationMu.Lock()
	if p.reason == terminationNone || reason == terminationTimeout {
		p.reason = reason
	}
	p.terminationMu.Unlock()

	if grace > 0 {
		if err := terminateProcessTree(p.pid); err != nil {
			p.setCleanupError(err)
		} else {
			timer := time.NewTimer(grace)
			select {
			case <-p.done:
				timer.Stop()
				p.terminationMu.Lock()
				err := p.cleanupErr
				p.terminationMu.Unlock()
				return err
			case <-timer.C:
			}
		}
	}
	if processTreeAlive(p.pid) {
		if err := killProcessTree(p.pid); err != nil {
			p.setCleanupError(err)
			return err
		}
	}
	select {
	case <-p.done:
		p.terminationMu.Lock()
		err := p.cleanupErr
		p.terminationMu.Unlock()
		return err
	case <-time.After(2 * time.Second):
		err := errors.New("process tree did not terminate within cleanup deadline")
		p.setCleanupError(err)
		return err
	}
}

// Wait is idempotent and returns only after stdout/stderr have drained.
func (p *SupervisedProcess) Wait() adapter.ExecutionResult {
	if p == nil || p.done == nil {
		return adapter.ExecutionResult{Err: domain.NewDomainError(domain.ErrorInternal, "One-shot process handle is unavailable", nil)}
	}
	<-p.done
	p.resultMu.RLock()
	defer p.resultMu.RUnlock()
	return p.result
}

func waitTreeGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processTreeAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !processTreeAlive(pid)
}

// IsTreeAlive reports whether an externally persisted process group is still
// present. It is used by startup crash recovery and does not adopt PTY state.
func (s *ProcessSupervisor) IsTreeAlive(pid int) bool {
	if s == nil || pid <= 0 || !s.Capabilities().ProcessTree {
		return false
	}
	return processTreeAlive(pid)
}

// TerminateExistingTree cleans a process group left by a crashed gateway. The
// process is not our child, so the supervisor polls OS liveness rather than
// calling Wait.
func (s *ProcessSupervisor) TerminateExistingTree(ctx context.Context, pid int, grace time.Duration) error {
	if s == nil || pid <= 0 {
		return nil
	}
	if !s.Capabilities().ProcessTree {
		return domain.NewDomainError(domain.ErrorUnsupportedProvider, s.Capabilities().UnsupportedReason, nil)
	}
	if !processTreeAlive(pid) {
		return nil
	}
	if grace <= 0 {
		grace = s.gracePeriod
	}
	if err := terminateProcessTree(pid); err != nil {
		return domain.NewDomainError(domain.ErrorCancelFailed, "failed to terminate recovered process tree", err)
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !processTreeAlive(pid) {
				return nil
			}
		case <-deadline.C:
			if err := killProcessTree(pid); err != nil {
				return domain.NewDomainError(domain.ErrorCancelFailed, "failed to kill recovered process tree", err)
			}
			if !waitTreeGone(pid, 2*time.Second) {
				return domain.NewDomainError(domain.ErrorCancelFailed, "recovered process tree remained alive", nil)
			}
			return nil
		}
	}
}
