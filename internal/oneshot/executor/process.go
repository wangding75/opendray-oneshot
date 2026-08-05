// Package executor runs provider commands as ordinary child processes.
// It deliberately does not import PTY or Session packages.
package executor

import (
	"context"
	"io"
	"path/filepath"
	"strings"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/workspacepolicy"
)

// ProcessExecutor is the provider-neutral facade over ProcessSupervisor.
type ProcessExecutor struct {
	stdout     io.Writer
	stderr     io.Writer
	supervisor *ProcessSupervisor
	policy     *workspacepolicy.Policy
}

type ProcessExecutorOption func(*ProcessExecutor)

func WithOutputWriters(stdout, stderr io.Writer) ProcessExecutorOption {
	return func(executor *ProcessExecutor) {
		executor.stdout = stdout
		executor.stderr = stderr
	}
}

func WithProcessSupervisor(supervisor *ProcessSupervisor) ProcessExecutorOption {
	return func(executor *ProcessExecutor) {
		if supervisor != nil {
			executor.supervisor = supervisor
		}
	}
}

func WithWorkspacePolicy(policy *workspacepolicy.Policy) ProcessExecutorOption {
	return func(executor *ProcessExecutor) {
		executor.policy = policy
	}
}

func NewProcessExecutor(options ...ProcessExecutorOption) *ProcessExecutor {
	executor := &ProcessExecutor{stdout: io.Discard, stderr: io.Discard, supervisor: NewProcessSupervisor()}
	for _, option := range options {
		option(executor)
	}
	if executor.stdout == nil {
		executor.stdout = io.Discard
	}
	if executor.stderr == nil {
		executor.stderr = io.Discard
	}
	if executor.supervisor == nil {
		executor.supervisor = NewProcessSupervisor()
	}
	return executor
}

// Process remains the stable executor handle name.
type Process = SupervisedProcess

func (e *ProcessExecutor) Start(ctx context.Context, spec adapter.CommandSpec) (*Process, error) {
	return e.StartWithOutput(ctx, spec, e.stdout, e.stderr)
}

// StartWithOutput starts one process with Run-scoped output writers.
func (e *ProcessExecutor) StartWithOutput(ctx context.Context, spec adapter.CommandSpec, stdout, stderr io.Writer) (*Process, error) {
	if e == nil || e.supervisor == nil {
		return nil, domain.NewDomainError(domain.ErrorProviderUnavailable, "One-shot process executor is unavailable", nil)
	}
	if e.policy != nil {
		if !e.policy.HasRoots() {
			return nil, domain.InvalidRequestf("no allowed One-shot workspace roots are configured")
		}
		canonicalDir, err := e.policy.NormalizeAndValidate(spec.Dir)
		if err != nil {
			return nil, err
		}
		spec.Dir = canonicalDir
	}
	return e.supervisor.Start(ctx, spec, stdout, stderr)
}

func validateCommandSpec(spec adapter.CommandSpec) error {
	if strings.TrimSpace(spec.Executable) == "" || !filepath.IsAbs(spec.Executable) {
		return domain.InvalidRequestf("One-shot executable must be an absolute path")
	}
	if strings.TrimSpace(spec.Dir) == "" || !filepath.IsAbs(spec.Dir) {
		return domain.InvalidRequestf("One-shot cwd must be an absolute path")
	}
	for key := range spec.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return domain.InvalidRequestf("invalid environment variable name")
		}
	}
	return nil
}
