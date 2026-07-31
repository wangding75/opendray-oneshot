// Package recovery reconciles crash-interrupted One-shot execution Sagas.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/executor"
	"github.com/opendray/opendray-v2/internal/oneshot/saga"
)

// Repository is the minimum durable recovery boundary.
type Repository interface {
	ListRecoverableRuns(context.Context, int) ([]saga.RecoveryItem, error)
	FinalizeRunWithTask(context.Context, domain.Owner, domain.TaskSnapshot, int64, domain.RunSnapshot) (domain.TaskSnapshot, domain.RunSnapshot, error)
	GetRuntimeContext(context.Context, domain.Owner, string) (domain.RuntimeContextSnapshot, error)
	UpdateRuntimeContext(context.Context, domain.Owner, domain.RuntimeContextSnapshot, int64) (domain.RuntimeContextSnapshot, error)
	FinalizeRunWithTaskAndContext(context.Context, domain.Owner, domain.TaskSnapshot, int64, domain.RunSnapshot, domain.RuntimeContextSnapshot, int64, bool) (domain.TaskSnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, error)
	RecordSagaState(context.Context, domain.Owner, saga.State) error
}

// QueueRecovery acknowledges terminal execution Deliveries without trusting a
// crashed worker lease.
type QueueRecovery interface {
	AcknowledgeRecovered(context.Context, string, string) (domain.DeliverySnapshot, error)
}

// CredentialReleaser releases opaque persisted leases after restart.
type CredentialReleaser interface {
	ReleaseCredential(context.Context, string) error
}

// Reconciler finalizes interrupted Runs, releases credentials, and acknowledges
// terminal Deliveries without ever starting a second provider process.
type Reconciler struct {
	repository  Repository
	queue       QueueRecovery
	credentials CredentialReleaser
	supervisor  *executor.ProcessSupervisor
	limit       int
	grace       time.Duration
	now         func() time.Time
}

type Option func(*Reconciler)

func WithLimit(limit int) Option {
	return func(reconciler *Reconciler) {
		if limit > 0 {
			reconciler.limit = limit
		}
	}
}

func WithGracePeriod(grace time.Duration) Option {
	return func(reconciler *Reconciler) {
		if grace > 0 {
			reconciler.grace = grace
		}
	}
}

func New(repository Repository, queue QueueRecovery, credentials CredentialReleaser, supervisor *executor.ProcessSupervisor, options ...Option) (*Reconciler, error) {
	if repository == nil || queue == nil || supervisor == nil {
		return nil, domain.InvalidRequestf("recovery repository, queue, and process supervisor are required")
	}
	reconciler := &Reconciler{
		repository: repository, queue: queue, credentials: credentials,
		supervisor: supervisor, limit: 100, grace: 2 * time.Second,
		now: func() time.Time { return time.Now().UTC() },
	}
	for _, option := range options {
		option(reconciler)
	}
	return reconciler, nil
}

// RunOnce performs startup/periodic reconciliation. It processes every item so
// one corrupt record cannot hide other recoverable Runs.
func (r *Reconciler) RunOnce(ctx context.Context) error {
	items, err := r.repository.ListRecoverableRuns(ctx, r.limit)
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		if err := r.reconcile(ctx, item); err != nil {
			failures = append(failures, fmt.Errorf("run %s: %w", item.Run.ID, err))
		}
	}
	return errors.Join(failures...)
}

// Run performs mandatory startup reconciliation, then periodically reconciles
// until shutdown. A reconciliation error is returned to the application
// lifecycle so the normal queue worker is not started or left running while
// crash state is unresolved.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error {
	if err := r.RunOnce(ctx); err != nil {
		return err
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func (r *Reconciler) reconcile(ctx context.Context, item saga.RecoveryItem) error {
	state := item.Saga
	if item.Run.Status.Terminal() {
		if err := r.releaseTerminalRuntimeContext(ctx, item); err != nil {
			return r.recordFailure(ctx, item.Owner, &state, "runtime_context_release", err, nil)
		}
		if err := r.releaseCredential(ctx, item.Owner, &state); err != nil {
			return err
		}
		if _, err := r.queue.AcknowledgeRecovered(ctx, item.Delivery.ID, item.Run.ID); err != nil {
			return r.recordFailure(ctx, item.Owner, &state, "acknowledge_recovered", err, nil)
		}
		state.Stage = saga.StageAcknowledged
		state.UpdatedAt = r.now().UTC()
		return r.repository.RecordSagaState(ctx, item.Owner, state)
	}

	task, err := domain.RestoreTask(item.Task)
	if err != nil {
		return err
	}
	run, err := domain.RestoreRun(item.Run)
	if err != nil {
		return err
	}
	expectedTaskVersion := task.Snapshot().Version

	pid := 0
	if item.Run.PID != nil {
		pid = *item.Run.PID
	} else if state.PID != nil {
		pid = *state.PID
	}
	if pid > 0 && r.supervisor.IsTreeAlive(pid) {
		if err := r.supervisor.TerminateExistingTree(ctx, pid, r.grace); err != nil {
			return r.recordFailure(ctx, item.Owner, &state, "process_cleanup", nil, err)
		}
	}

	finishedAt := r.now().UTC()
	runtimeContext, expectedContextVersion, err := r.releasableRuntimeContext(ctx, item, finishedAt)
	if err != nil {
		return r.recordFailure(ctx, item.Owner, &state, "runtime_context_recovery", err, nil)
	}
	if err := recoverRun(task, run, state, finishedAt, runtimeContext); err != nil {
		return r.recordFailure(ctx, item.Owner, &state, "terminal_recovery", err, nil)
	}
	if runtimeContext == nil {
		if _, _, err := r.repository.FinalizeRunWithTask(ctx, item.Owner, task.Snapshot(), expectedTaskVersion, run.Snapshot()); err != nil {
			return r.recordFailure(ctx, item.Owner, &state, "terminal_persist", err, nil)
		}
	} else if _, _, _, err := r.repository.FinalizeRunWithTaskAndContext(ctx, item.Owner, task.Snapshot(), expectedTaskVersion, run.Snapshot(), *runtimeContext, expectedContextVersion, false); err != nil {
		return r.recordFailure(ctx, item.Owner, &state, "terminal_persist", err, nil)
	}
	state.Stage = saga.StageTerminalPersisted
	state.FailureStage = stringPointer("gateway_recovery")
	state.PrimaryErrorCode = stringPointer(string(domain.ErrorInternal))
	state.PrimaryErrorMessage = stringPointer("gateway restarted before execution Saga completed")
	state.UpdatedAt = finishedAt
	if err := r.repository.RecordSagaState(ctx, item.Owner, state); err != nil {
		return err
	}
	if err := r.releaseCredential(ctx, item.Owner, &state); err != nil {
		return err
	}
	if _, err := r.queue.AcknowledgeRecovered(ctx, item.Delivery.ID, item.Run.ID); err != nil {
		return r.recordFailure(ctx, item.Owner, &state, "acknowledge_recovered", err, nil)
	}
	state.Stage = saga.StageAcknowledged
	state.UpdatedAt = r.now().UTC()
	return r.repository.RecordSagaState(ctx, item.Owner, state)
}

func recoverRun(task *domain.Task, run *domain.Run, state saga.State, finishedAt time.Time, runtimeContext *domain.RuntimeContextSnapshot) error {
	snapshot := run.Snapshot()
	switch snapshot.Status {
	case domain.RunCreated:
		if err := run.Start(); err != nil {
			return err
		}
		if err := run.StartFailed(domain.ErrorInternal, "gateway restarted before Run start", finishedAt); err != nil {
			return err
		}
		return task.MarkRunFailed(run.Snapshot(), runtimeContext, finishedAt)
	case domain.RunStarting:
		if err := run.StartFailed(domain.ErrorInternal, "gateway restarted during Run start", finishedAt); err != nil {
			return err
		}
		return task.MarkRunFailed(run.Snapshot(), runtimeContext, finishedAt)
	case domain.RunRunning:
		if err := run.SupervisionFailed(domain.ErrorInternal, "gateway restarted while provider process was running", true, finishedAt); err != nil {
			return err
		}
		return task.MarkRunFailed(run.Snapshot(), runtimeContext, finishedAt)
	case domain.RunCollectingOutput:
		if !state.Stage.AtLeast(saga.StageOutputCommitted) {
			if err := run.FinalizeFailure(domain.ErrorOutputPersistFailed, "gateway restarted before output commit completed", true, finishedAt); err != nil {
				return err
			}
			return task.MarkRunFailed(run.Snapshot(), runtimeContext, finishedAt)
		}
		result := adapter.ExecutionResult{CleanupResolved: true, FinishedAt: finishedAt}
		if state.ExitCode != nil {
			result.ExitCode = *state.ExitCode
		} else if snapshot.ExitCode != nil {
			result.ExitCode = *snapshot.ExitCode
		}
		result.Cancelled = state.ResultCancelled
		result.TimedOut = state.ResultTimedOut
		if state.ResultErrorCode != nil {
			code := domain.ErrorCode(*state.ResultErrorCode)
			message := "recovered provider execution failure"
			if state.ResultErrorMessage != nil {
				message = *state.ResultErrorMessage
			}
			result.Err = domain.NewDomainError(code, message, nil)
		}
		return finalizeRecoveredResult(task, run, result, runtimeContext)
	default:
		return domain.InvalidRequestf("Run %s is not recoverable from status %s", snapshot.ID, snapshot.Status)
	}
}

func finalizeRecoveredResult(task *domain.Task, run *domain.Run, result adapter.ExecutionResult, runtimeContext *domain.RuntimeContextSnapshot) error {
	if result.TimedOut {
		if err := run.FinalizeTimeout(true, result.FinishedAt); err != nil {
			return err
		}
		return task.MarkRunTimedOut(run.Snapshot(), runtimeContext, result.FinishedAt)
	}
	if result.Cancelled {
		if err := run.FinalizeCancel(true, result.FinishedAt); err != nil {
			return err
		}
		return task.MarkRunCancelled(run.Snapshot(), runtimeContext, result.FinishedAt)
	}
	if result.Err == nil && result.ExitCode == 0 {
		if err := run.FinalizeSuccess(true, result.FinishedAt); err != nil {
			return err
		}
		return task.MarkRunCompleted(run.Snapshot(), runtimeContext, result.FinishedAt)
	}
	code := domain.ErrorExecutionFailed
	message := fmt.Sprintf("recovered provider exit code %d", result.ExitCode)
	if result.Err != nil {
		if actual, ok := domain.CodeOf(result.Err); ok {
			code = actual
		}
		message = result.Err.Error()
	}
	if err := run.FinalizeFailure(code, message, true, result.FinishedAt); err != nil {
		return err
	}
	return task.MarkRunFailed(run.Snapshot(), runtimeContext, result.FinishedAt)
}

func (r *Reconciler) releasableRuntimeContext(ctx context.Context, item saga.RecoveryItem, finishedAt time.Time) (*domain.RuntimeContextSnapshot, int64, error) {
	if item.Run.RuntimeContextID == nil {
		return nil, 0, nil
	}
	persisted, err := r.repository.GetRuntimeContext(ctx, item.Owner, *item.Run.RuntimeContextID)
	if err != nil {
		return nil, 0, err
	}
	if persisted.ID != *item.Run.RuntimeContextID || item.Task.RuntimeContextID == nil || *item.Task.RuntimeContextID != persisted.ID ||
		persisted.ProjectID != item.Task.ProjectID || persisted.ProviderID != item.Run.ProviderID {
		return nil, 0, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "recovery RuntimeContext identity mismatch", nil)
	}
	runtimeContext, err := domain.RestoreRuntimeContext(persisted)
	if err != nil {
		return nil, 0, err
	}
	if persisted.Status != domain.ContextBusy {
		return nil, 0, domain.NewDomainError(domain.ErrorRunConflict, "recoverable Run RuntimeContext is not busy", nil)
	}
	expectedVersion := persisted.Version
	if err := runtimeContext.Release(expectedVersion, finishedAt); err != nil {
		return nil, 0, err
	}
	snapshot := runtimeContext.Snapshot()
	return &snapshot, expectedVersion, nil
}

func (r *Reconciler) releaseTerminalRuntimeContext(ctx context.Context, item saga.RecoveryItem) error {
	if item.Run.RuntimeContextID == nil {
		return nil
	}
	persisted, err := r.repository.GetRuntimeContext(ctx, item.Owner, *item.Run.RuntimeContextID)
	if err != nil {
		return err
	}
	if persisted.ID != *item.Run.RuntimeContextID || item.Task.RuntimeContextID == nil || *item.Task.RuntimeContextID != persisted.ID ||
		persisted.ProjectID != item.Task.ProjectID || persisted.ProviderID != item.Run.ProviderID {
		return domain.NewDomainError(domain.ErrorContextOwnerMismatch, "terminal recovery RuntimeContext identity mismatch", nil)
	}
	if persisted.Status != domain.ContextBusy {
		return nil
	}
	runtimeContext, err := domain.RestoreRuntimeContext(persisted)
	if err != nil {
		return err
	}
	expectedVersion := persisted.Version
	if err := runtimeContext.Release(expectedVersion, r.now().UTC()); err != nil {
		return err
	}
	_, err = r.repository.UpdateRuntimeContext(ctx, item.Owner, runtimeContext.Snapshot(), expectedVersion)
	return err
}

func (r *Reconciler) releaseCredential(ctx context.Context, owner domain.Owner, state *saga.State) error {
	if state.CredentialLeaseID == nil || *state.CredentialLeaseID == "" || r.credentials == nil {
		return nil
	}
	if err := r.credentials.ReleaseCredential(ctx, *state.CredentialLeaseID); err != nil {
		return r.recordFailure(ctx, owner, state, "credential_release", nil, err)
	}
	state.CredentialLeaseID = nil
	state.Stage = saga.StageCredentialReleased
	state.UpdatedAt = r.now().UTC()
	return r.repository.RecordSagaState(ctx, owner, *state)
}

func (r *Reconciler) recordFailure(ctx context.Context, owner domain.Owner, state *saga.State, stage string, primary, compensation error) error {
	state.FailureStage = stringPointer(stage)
	if primary != nil {
		code := domain.ErrorInternal
		if actual, ok := domain.CodeOf(primary); ok {
			code = actual
		}
		state.PrimaryErrorCode = stringPointer(string(code))
		state.PrimaryErrorMessage = stringPointer(primary.Error())
	}
	if compensation != nil {
		state.CompensationError = stringPointer(compensation.Error())
	}
	state.UpdatedAt = r.now().UTC()
	if err := r.repository.RecordSagaState(ctx, owner, *state); err != nil {
		return errors.Join(primary, compensation, err)
	}
	return errors.Join(primary, compensation)
}

func stringPointer(value string) *string { return &value }
