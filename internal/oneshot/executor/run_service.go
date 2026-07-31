package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/saga"
)

const shellCommandOption = "shell_command"

// ErrInjectedCrash is used only by deterministic fault-injection tests. The
// processor intentionally skips compensation so Reconciler sees crash state.
var ErrInjectedCrash = errors.New("oneshot injected gateway crash")

// RunRepository is the persistence boundary required by the execution Saga.
// internal/oneshot/store.Store satisfies this interface.
type RunRepository interface {
	CreateRunWithSaga(context.Context, domain.Owner, domain.TaskSnapshot, int64, domain.DeliverySnapshot, domain.RunSnapshot, saga.State) (domain.TaskSnapshot, domain.DeliverySnapshot, domain.RunSnapshot, error)
	GetRun(context.Context, domain.Owner, string) (domain.RunSnapshot, error)
	UpdateRun(context.Context, domain.Owner, domain.RunSnapshot) (domain.RunSnapshot, error)
	FinalizeRunWithTask(context.Context, domain.Owner, domain.TaskSnapshot, int64, domain.RunSnapshot) (domain.TaskSnapshot, domain.RunSnapshot, error)
	LoadOutputCursor(context.Context, domain.Owner, string) (int64, int64, int64, int64, error)
	AppendOutput(context.Context, domain.Owner, string, []domain.ArtifactSnapshot, []domain.StreamRecordSnapshot, []domain.StandardEventSnapshot) error
	RecordSagaState(context.Context, domain.Owner, saga.State) error
	GetSagaState(context.Context, domain.Owner, string) (saga.State, error)
}

// RuntimeContextRepository is required only for continue/resume capable Runs.
// Keeping it separate preserves compatibility for non-resumable test and Shell repositories.
type RuntimeContextRepository interface {
	GetRuntimeContext(context.Context, domain.Owner, string) (domain.RuntimeContextSnapshot, error)
	UpdateRuntimeContext(context.Context, domain.Owner, domain.RuntimeContextSnapshot, int64) (domain.RuntimeContextSnapshot, error)
	CreateContinueRunWithSaga(context.Context, domain.Owner, domain.TaskSnapshot, int64, domain.DeliverySnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, int64, saga.State) (domain.TaskSnapshot, domain.DeliverySnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, error)
	FinalizeRunWithTaskAndContext(context.Context, domain.Owner, domain.TaskSnapshot, int64, domain.RunSnapshot, domain.RuntimeContextSnapshot, int64, bool) (domain.TaskSnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, error)
}

// FaultInjector can stop execution immediately after a durable Saga checkpoint.
type FaultInjector interface {
	Checkpoint(saga.Stage) error
}

type FaultInjectorFunc func(saga.Stage) error

func (f FaultInjectorFunc) Checkpoint(stage saga.Stage) error { return f(stage) }

// RunNotificationSink is the future OD-OS-19 outbox seam. Notification failure
// is compensation/audit work and must never re-execute a completed provider Run.
type RunNotificationSink interface {
	EnqueueRunTerminal(context.Context, domain.Owner, domain.TaskSnapshot, domain.RunSnapshot) error
}

// RunService is the queue Processor implementing the explicit crash-consistent
// Delivery -> Run -> process -> output -> terminal -> ACK Saga.
type RunService struct {
	repository    RunRepository
	registry      *adapter.Registry
	executor      *ProcessExecutor
	storage       ArtifactStorage
	chunkSize     int
	gracePeriod   time.Duration
	now           func() time.Time
	faults        FaultInjector
	notifications RunNotificationSink

	activeMu sync.RWMutex
	active   map[string]*Process
	ackRuns  sync.Map // delivery id -> run id
}

type RunServiceOption func(*RunService)

func WithArtifactStorage(storage ArtifactStorage) RunServiceOption {
	return func(service *RunService) { service.storage = storage }
}

func WithOutputChunkSize(size int) RunServiceOption {
	return func(service *RunService) { service.chunkSize = size }
}

func WithRunTerminationGrace(grace time.Duration) RunServiceOption {
	return func(service *RunService) {
		if grace > 0 {
			service.gracePeriod = grace
		}
	}
}

func WithRunFaultInjector(injector FaultInjector) RunServiceOption {
	return func(service *RunService) { service.faults = injector }
}

func WithRunNotificationSink(sink RunNotificationSink) RunServiceOption {
	return func(service *RunService) { service.notifications = sink }
}

func NewRunService(repository RunRepository, registry *adapter.Registry, processExecutor *ProcessExecutor, options ...RunServiceOption) (*RunService, error) {
	if repository == nil || registry == nil || processExecutor == nil {
		return nil, domain.InvalidRequestf("Run repository, adapter registry, and process executor are required")
	}
	service := &RunService{
		repository:  repository,
		registry:    registry,
		executor:    processExecutor,
		chunkSize:   defaultOutputChunkSize,
		gracePeriod: defaultTerminationGrace,
		now:         func() time.Time { return time.Now().UTC() },
		active:      make(map[string]*Process),
	}
	for _, option := range options {
		option(service)
	}
	if service.storage == nil {
		return nil, domain.InvalidRequestf("RunService artifact storage is required")
	}
	return service, nil
}

// Process implements queue.Processor. Once a Run is attached, failures are
// represented by the Run/Saga and the Delivery is never used to start a second
// provider process.
func (s *RunService) Process(ctx context.Context, claim queue.Claim) queue.Outcome {
	owner := domain.Owner{Kind: claim.Task.PrincipalKind, ID: claim.Task.PrincipalID}
	contextRepository, hasContextRepository := s.repository.(RuntimeContextRepository)
	if claim.Delivery.RunID != nil {
		return s.outcomeForAttachedRun(ctx, owner, claim.Delivery)
	}

	resolved, err := s.registry.ResolveProvider(ctx, claim.Task.ProviderID)
	if err != nil {
		return outcomeForPreRunError(err)
	}

	task, err := domain.RestoreTask(claim.Task)
	if err != nil {
		return queue.Outcome{Action: queue.ActionDeadLetter, Code: domain.ErrorInvalidRequest}
	}
	delivery, err := domain.RestoreDelivery(claim.Delivery)
	if err != nil {
		return queue.Outcome{Action: queue.ActionDeadLetter, Code: domain.ErrorInvalidRequest}
	}
	createdAt := s.now().UTC()
	var runtimeContext *domain.RuntimeContext
	var contextExpectedVersion int64
	var runContextSnapshot *domain.RuntimeContextSnapshot
	if delivery.Snapshot().Operation == domain.DeliveryContinue {
		if !hasContextRepository {
			return outcomeForPreRunError(domain.NewDomainError(domain.ErrorResumeUnsupported, "Run repository does not support RuntimeContext", nil))
		}
		if task.Snapshot().RuntimeContextID == nil {
			return outcomeForPreRunError(domain.NewDomainError(domain.ErrorContextNotFound, "continue Task has no RuntimeContext", nil))
		}
		persistedContext, contextErr := contextRepository.GetRuntimeContext(ctx, owner, *task.Snapshot().RuntimeContextID)
		if contextErr != nil {
			return outcomeForPreRunError(contextErr)
		}
		runtimeContext, contextErr = domain.RestoreRuntimeContext(persistedContext)
		if contextErr != nil {
			return outcomeForPreRunError(contextErr)
		}
		contextExpectedVersion = persistedContext.Version
		if contextErr = runtimeContext.Acquire(owner, task.Snapshot().ProjectID, task.Snapshot().ProviderID, contextExpectedVersion, createdAt); contextErr != nil {
			return outcomeForPreRunError(contextErr)
		}
		snapshot := runtimeContext.Snapshot()
		runContextSnapshot = &snapshot
	}
	run, err := domain.NewRun(task.Snapshot(), delivery.Snapshot(), runContextSnapshot, createdAt)
	if err != nil {
		return outcomeForPreRunError(err)
	}
	if err := run.Start(); err != nil {
		return outcomeForPreRunError(err)
	}
	if err := delivery.AttachRun(run.Snapshot().ID, createdAt); err != nil {
		return outcomeForPreRunError(err)
	}
	previousTaskVersion := task.Snapshot().Version
	if err := task.StartRun(delivery.Snapshot(), run.Snapshot(), createdAt); err != nil {
		return outcomeForPreRunError(err)
	}
	state := saga.State{
		RunID: run.Snapshot().ID, TaskID: task.Snapshot().ID,
		DeliveryID: delivery.Snapshot().ID, Stage: saga.StageRunCreated,
		UpdatedAt: s.now().UTC(),
	}
	var persistedTask domain.TaskSnapshot
	var persistedDelivery domain.DeliverySnapshot
	var persistedRun domain.RunSnapshot
	var persistedContext domain.RuntimeContextSnapshot
	if runtimeContext != nil {
		persistedTask, persistedDelivery, persistedRun, persistedContext, err = contextRepository.CreateContinueRunWithSaga(
			ctx, owner, task.Snapshot(), previousTaskVersion, delivery.Snapshot(), run.Snapshot(), runtimeContext.Snapshot(), contextExpectedVersion, state,
		)
	} else {
		persistedTask, persistedDelivery, persistedRun, err = s.repository.CreateRunWithSaga(
			ctx, owner, task.Snapshot(), previousTaskVersion, delivery.Snapshot(), run.Snapshot(), state,
		)
	}
	if err != nil {
		return outcomeForPreRunError(err)
	}
	if runtimeContext != nil {
		runtimeContext, err = domain.RestoreRuntimeContext(persistedContext)
		if err != nil {
			s.recordFailure(context.WithoutCancel(ctx), owner, &state, saga.StageRunCreated, err, nil)
			return queue.Outcome{Action: queue.ActionRecover}
		}
	}
	task, err = domain.RestoreTask(persistedTask)
	if err != nil {
		s.recordFailure(context.WithoutCancel(ctx), owner, &state, saga.StageRunCreated, err, nil)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	delivery, err = domain.RestoreDelivery(persistedDelivery)
	if err != nil {
		s.recordFailure(context.WithoutCancel(ctx), owner, &state, saga.StageRunCreated, err, nil)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	run, err = domain.RestoreRun(persistedRun)
	if err != nil {
		s.recordFailure(context.WithoutCancel(ctx), owner, &state, saga.StageRunCreated, err, nil)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	s.ackRuns.Store(delivery.Snapshot().ID, run.Snapshot().ID)
	if runtimeContext != nil {
		defer s.releaseBusyContextBestEffort(context.WithoutCancel(ctx), owner, runtimeContext)
	}
	if extractor, ok := resolved.Adapter.(adapter.RuntimeContextExtractor); ok {
		defer extractor.ForgetRun(run.Snapshot().ID)
	}

	if s.faults != nil {
		if err := s.faults.Checkpoint(saga.StageRunCreated); err != nil {
			if errors.Is(err, ErrInjectedCrash) {
				return queue.Outcome{Action: queue.ActionRecover}
			}
			return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageRunCreated, err, "", runtimeContext)
		}
	}

	lease, err := s.registry.AcquireCredential(ctx, adapter.CredentialRequest{
		ProviderID: task.Snapshot().ProviderID, ProjectID: task.Snapshot().ProjectID,
		Owner: owner, RunID: run.Snapshot().ID,
	})
	if err != nil {
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageCredentialAcquired, err, "", runtimeContext)
	}
	if lease.ID != "" {
		state.CredentialLeaseID = stringPointer(lease.ID)
	}
	if err := s.checkpoint(context.WithoutCancel(ctx), owner, &state, saga.StageCredentialAcquired); err != nil {
		if errors.Is(err, ErrInjectedCrash) {
			return queue.Outcome{Action: queue.ActionRecover}
		}
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageCredentialAcquired, err, lease.ID, runtimeContext)
	}

	var executionContext *domain.RuntimeContextSnapshot
	if runtimeContext != nil {
		snapshot := runtimeContext.Snapshot()
		executionContext = &snapshot
	}
	command, err := resolved.Adapter.BuildCommand(ctx, adapter.ExecutionInput{
		Task: task.Snapshot(), Delivery: delivery.Snapshot(), Run: run.Snapshot(), RuntimeContext: executionContext,
		CommandName: commandNameFromDelivery(claim.Delivery), Prompt: claim.Task.Prompt,
		Environment: environmentFromDelivery(claim.Delivery),
	})
	if err != nil {
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageCommandBuilt, err, lease.ID, runtimeContext)
	}
	command = mergeSharedCommand(command, resolved.Metadata, lease)
	if err := s.checkpoint(context.WithoutCancel(ctx), owner, &state, saga.StageCommandBuilt); err != nil {
		if errors.Is(err, ErrInjectedCrash) {
			return queue.Outcome{Action: queue.ActionRecover}
		}
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageCommandBuilt, err, lease.ID, runtimeContext)
	}

	timeout, err := timeoutFromDelivery(claim.Delivery)
	if err != nil {
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageCommandBuilt, err, lease.ID, runtimeContext)
	}
	if timeout > 0 && !resolved.Capabilities.Cancellation {
		err := domain.NewDomainError(domain.ErrorUnsupportedProvider, "provider does not support cancellable One-shot execution", nil)
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageCommandBuilt, err, lease.ID, runtimeContext)
	}
	processCtx := ctx
	cancelProcess := func() {}
	if timeout > 0 {
		processCtx, cancelProcess = context.WithTimeout(ctx, timeout)
	}
	defer cancelProcess()

	outputCtx := context.WithoutCancel(ctx)
	collector, err := NewOutputCollector(outputCtx, OutputCollectorConfig{
		Owner: owner, Task: task.Snapshot(), Run: run.Snapshot(), Repository: s.repository,
		Storage: s.storage, Adapter: resolved.Adapter, ChunkSize: s.chunkSize, Now: s.now,
	})
	if err != nil {
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageCommandBuilt, err, lease.ID, runtimeContext)
	}
	process, err := s.executor.StartWithOutput(processCtx, command,
		&streamWriter{collector: collector, stream: domain.StreamStdout, ctx: outputCtx},
		&streamWriter{collector: collector, stream: domain.StreamStderr, ctx: outputCtx},
	)
	if err != nil {
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageProcessStarted, err, lease.ID, runtimeContext)
	}
	s.registerActive(run.Snapshot().ID, process)
	defer s.unregisterActive(run.Snapshot().ID, process)
	state.PID = intPointer(process.PID())
	if err := run.ProcessStarted(process.PID(), process.StartedAt()); err != nil {
		_ = process.TerminateTree(s.gracePeriod)
		_ = process.Wait()
		return s.failStartingRun(ctx, owner, task, run, persistedTask.Version, &state, saga.StageProcessStarted, err, lease.ID, runtimeContext)
	}
	if err := s.checkpoint(outputCtx, owner, &state, saga.StageProcessStarted); err != nil {
		if errors.Is(err, ErrInjectedCrash) {
			return queue.Outcome{Action: queue.ActionRecover}
		}
		return s.compensateRunning(ctx, owner, task, run, persistedTask.Version, collector, process, &state, saga.StageProcessStarted, err, lease.ID, runtimeContext)
	}
	if _, err := s.repository.UpdateRun(outputCtx, owner, run.Snapshot()); err != nil {
		return s.compensateRunning(ctx, owner, task, run, persistedTask.Version, collector, process, &state, saga.StageRunningPersisted, err, lease.ID, runtimeContext)
	}
	if err := s.checkpoint(outputCtx, owner, &state, saga.StageRunningPersisted); err != nil {
		if errors.Is(err, ErrInjectedCrash) {
			return queue.Outcome{Action: queue.ActionRecover}
		}
		return s.compensateRunning(ctx, owner, task, run, persistedTask.Version, collector, process, &state, saga.StageRunningPersisted, err, lease.ID, runtimeContext)
	}

	result := process.Wait()
	if interpreter, ok := resolved.Adapter.(adapter.ResultInterpreter); ok {
		result = interpreter.InterpretResult(outputCtx, run.Snapshot().ID, result)
	}
	state.ExitCode = intPointer(result.ExitCode)
	state.ResultCancelled = result.Cancelled
	state.ResultTimedOut = result.TimedOut
	if result.Err != nil {
		code := codeFromError(result.Err, domain.ErrorExecutionFailed)
		state.ResultErrorCode = stringPointer(string(code))
		state.ResultErrorMessage = stringPointer(result.Err.Error())
	}
	if err := run.ProcessExited(result.ExitCode); err != nil {
		return s.failAfterExit(outputCtx, owner, task, run, persistedTask.Version, &state, saga.StageProcessExited, err, lease.ID, runtimeContext)
	}
	if _, err := s.repository.UpdateRun(outputCtx, owner, run.Snapshot()); err != nil {
		s.recordFailure(outputCtx, owner, &state, saga.StageProcessExited, err, nil)
		_ = s.releaseCredential(outputCtx, owner, &state, lease.ID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	if err := s.checkpoint(outputCtx, owner, &state, saga.StageProcessExited); err != nil {
		if errors.Is(err, ErrInjectedCrash) {
			return queue.Outcome{Action: queue.ActionRecover}
		}
		return s.failAfterExit(outputCtx, owner, task, run, persistedTask.Version, &state, saga.StageProcessExited, err, lease.ID, runtimeContext)
	}

	var outcomeContext *domain.RuntimeContextSnapshot
	createOutcomeContext := false
	contextFinalExpectedVersion := int64(0)
	succeeded := result.Err == nil && result.ExitCode == 0 && !result.Cancelled && !result.TimedOut
	if runtimeContext != nil {
		preparedContext, expectedVersion, prepareErr := prepareTerminalRuntimeContext(runtimeContext, result.FinishedAt)
		contextFinalExpectedVersion = expectedVersion
		if prepareErr != nil {
			result.Err = prepareErr
			succeeded = false
		} else {
			outcomeContext = preparedContext
		}
	} else if succeeded && resolved.Capabilities.SupportsResume {
		extractor, ok := resolved.Adapter.(adapter.RuntimeContextExtractor)
		if !ok {
			result.Err = domain.NewDomainError(domain.ErrorResumeFailed, "provider adapter did not expose RuntimeContext evidence", nil)
			succeeded = false
		} else if evidence, found, evidenceErr := extractor.RuntimeContextEvidence(outputCtx, run.Snapshot().ID); evidenceErr != nil {
			result.Err = domain.NewDomainError(domain.ErrorResumeFailed, "provider RuntimeContext extraction failed", evidenceErr)
			succeeded = false
		} else if !found || strings.TrimSpace(evidence.ProviderContextID) == "" {
			result.Err = domain.NewDomainError(domain.ErrorResumeFailed, "provider did not return a resumable context id", nil)
			succeeded = false
		} else {
			createdContext, contextErr := domain.NewRuntimeContext(domain.RuntimeContextArgs{
				Owner: owner, ProjectID: task.Snapshot().ProjectID, ProviderID: task.Snapshot().ProviderID,
				ProviderContextID: evidence.ProviderContextID, WorkspacePath: command.Dir,
			}, result.FinishedAt)
			if contextErr != nil {
				result.Err = contextErr
				succeeded = false
			} else {
				snapshot := createdContext.Snapshot()
				outcomeContext = &snapshot
				createOutcomeContext = true
			}
		}
	}
	if _, err := collector.Finalize(outputCtx, FinalOutput{
		ExitCode: result.ExitCode, Succeeded: succeeded, FinishedAt: result.FinishedAt,
	}); err != nil {
		return s.failAfterExit(outputCtx, owner, task, run, persistedTask.Version, &state, saga.StageOutputCommitted, err, lease.ID, runtimeContext)
	}
	if err := s.checkpoint(outputCtx, owner, &state, saga.StageOutputCommitted); err != nil {
		if errors.Is(err, ErrInjectedCrash) {
			return queue.Outcome{Action: queue.ActionRecover}
		}
		return s.failAfterExit(outputCtx, owner, task, run, persistedTask.Version, &state, saga.StageOutputCommitted, err, lease.ID, runtimeContext)
	}

	if err := finalizeResult(task, run, result, outcomeContext); err != nil {
		return s.failAfterExit(outputCtx, owner, task, run, persistedTask.Version, &state, saga.StageTerminalPersisted, err, lease.ID, runtimeContext)
	}
	if outcomeContext != nil {
		if _, _, persistedContext, finalizeErr := contextRepository.FinalizeRunWithTaskAndContext(outputCtx, owner, task.Snapshot(), persistedTask.Version, run.Snapshot(), *outcomeContext, contextFinalExpectedVersion, createOutcomeContext); finalizeErr != nil {
			s.recordFailure(outputCtx, owner, &state, saga.StageTerminalPersisted, finalizeErr, nil)
			_ = s.releaseCredential(outputCtx, owner, &state, lease.ID, finalizeErr)
			return queue.Outcome{Action: queue.ActionRecover}
		} else if runtimeContext != nil {
			_, finalizeErr = domain.RestoreRuntimeContext(persistedContext)
			if finalizeErr != nil {
				s.recordFailure(outputCtx, owner, &state, saga.StageTerminalPersisted, finalizeErr, nil)
				return queue.Outcome{Action: queue.ActionRecover}
			}
		}
	} else if _, _, err := s.repository.FinalizeRunWithTask(outputCtx, owner, task.Snapshot(), persistedTask.Version, run.Snapshot()); err != nil {
		s.recordFailure(outputCtx, owner, &state, saga.StageTerminalPersisted, err, nil)
		_ = s.releaseCredential(outputCtx, owner, &state, lease.ID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	if err := s.checkpoint(outputCtx, owner, &state, saga.StageTerminalPersisted); err != nil {
		if errors.Is(err, ErrInjectedCrash) {
			return queue.Outcome{Action: queue.ActionRecover}
		}
		s.recordFailure(outputCtx, owner, &state, saga.StageTerminalPersisted, err, nil)
		return queue.Outcome{Action: queue.ActionRecover}
	}

	if s.notifications != nil {
		if err := s.notifications.EnqueueRunTerminal(outputCtx, owner, task.Snapshot(), run.Snapshot()); err != nil {
			s.recordFailure(outputCtx, owner, &state, saga.StageTerminalPersisted, nil, err)
		}
	}
	if err := s.releaseCredential(outputCtx, owner, &state, lease.ID, nil); err != nil {
		// The provider Run is already durable and must not execute again. The
		// reconciler will retry opaque lease release from the Saga record.
		return queue.Outcome{Action: queue.ActionAck}
	}
	return queue.Outcome{Action: queue.ActionAck}
}

func (s *RunService) outcomeForAttachedRun(ctx context.Context, owner domain.Owner, delivery domain.DeliverySnapshot) queue.Outcome {
	if delivery.RunID == nil {
		return queue.Outcome{Action: queue.ActionRecover}
	}
	run, err := s.repository.GetRun(ctx, owner, *delivery.RunID)
	if err != nil {
		return queue.Outcome{Action: queue.ActionRecover}
	}
	if run.Status.Terminal() {
		s.ackRuns.Store(delivery.ID, run.ID)
		return queue.Outcome{Action: queue.ActionAck}
	}
	return queue.Outcome{Action: queue.ActionRecover}
}

func (s *RunService) checkpoint(ctx context.Context, owner domain.Owner, state *saga.State, stage saga.Stage) error {
	state.Stage = stage
	state.UpdatedAt = s.now().UTC()
	if err := s.repository.RecordSagaState(ctx, owner, *state); err != nil {
		return err
	}
	if s.faults != nil {
		if err := s.faults.Checkpoint(stage); err != nil {
			return err
		}
	}
	return nil
}

func (s *RunService) recordFailure(ctx context.Context, owner domain.Owner, state *saga.State, stage saga.Stage, primary, compensation error) {
	failureStage := string(stage)
	state.FailureStage = &failureStage
	if primary != nil {
		code := codeFromError(primary, domain.ErrorInternal)
		state.PrimaryErrorCode = stringPointer(string(code))
		state.PrimaryErrorMessage = stringPointer(primary.Error())
	}
	if compensation != nil {
		state.CompensationError = stringPointer(compensation.Error())
	}
	state.UpdatedAt = s.now().UTC()
	_ = s.repository.RecordSagaState(ctx, owner, *state)
}

func (s *RunService) failStartingRun(ctx context.Context, owner domain.Owner, task *domain.Task, run *domain.Run, expectedTaskVersion int64, state *saga.State, stage saga.Stage, cause error, leaseID string, runtimeContext *domain.RuntimeContext) queue.Outcome {
	cleanupCtx := context.WithoutCancel(ctx)
	code := codeFromError(cause, domain.ErrorExecutionFailed)
	if code == domain.ErrorTimeout {
		code = domain.ErrorExecutionFailed
	}
	finishedAt := s.now().UTC()
	terminalPersisted := true
	if err := run.StartFailed(code, cause.Error(), finishedAt); err != nil {
		terminalPersisted = false
		s.recordFailure(cleanupCtx, owner, state, stage, cause, err)
	} else if outcomeContext, contextVersion, contextErr := prepareTerminalRuntimeContext(runtimeContext, finishedAt); contextErr != nil {
		terminalPersisted = false
		s.recordFailure(cleanupCtx, owner, state, stage, cause, contextErr)
	} else if err := task.MarkRunFailed(run.Snapshot(), outcomeContext, finishedAt); err != nil {
		terminalPersisted = false
		s.recordFailure(cleanupCtx, owner, state, stage, cause, err)
	} else if err := s.persistTerminalWithRuntimeContext(cleanupCtx, owner, task.Snapshot(), expectedTaskVersion, run.Snapshot(), outcomeContext, contextVersion); err != nil {
		terminalPersisted = false
		s.recordFailure(cleanupCtx, owner, state, stage, cause, err)
	} else {
		s.recordFailure(cleanupCtx, owner, state, stage, cause, nil)
		_ = s.checkpoint(cleanupCtx, owner, state, saga.StageTerminalPersisted)
	}
	_ = s.releaseCredential(cleanupCtx, owner, state, leaseID, cause)
	if !terminalPersisted {
		return queue.Outcome{Action: queue.ActionRecover}
	}
	return queue.Outcome{Action: queue.ActionAck}
}

func (s *RunService) compensateRunning(ctx context.Context, owner domain.Owner, task *domain.Task, run *domain.Run, expectedTaskVersion int64, collector *OutputCollector, process *Process, state *saga.State, stage saga.Stage, cause error, leaseID string, runtimeContext *domain.RuntimeContext) queue.Outcome {
	cleanupCtx := context.WithoutCancel(ctx)
	terminationErr := process.TerminateTree(s.gracePeriod)
	result := process.Wait()
	state.ExitCode = intPointer(result.ExitCode)
	state.ResultCancelled = result.Cancelled
	state.ResultTimedOut = result.TimedOut
	state.PID = intPointer(process.PID())
	if terminationErr != nil {
		s.recordFailure(cleanupCtx, owner, state, stage, cause, terminationErr)
	} else {
		s.recordFailure(cleanupCtx, owner, state, stage, cause, nil)
	}
	if err := run.ProcessExited(result.ExitCode); err != nil {
		_ = s.releaseCredential(cleanupCtx, owner, state, leaseID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	_, _ = s.repository.UpdateRun(cleanupCtx, owner, run.Snapshot())
	_, finalizeErr := collector.Finalize(cleanupCtx, FinalOutput{ExitCode: result.ExitCode, Succeeded: false, FinishedAt: result.FinishedAt})
	message := cause.Error()
	code := codeFromError(cause, domain.ErrorExecutionFailed)
	if terminationErr != nil {
		code = domain.ErrorCancelFailed
		message = terminationErr.Error()
	}
	if finalizeErr != nil {
		code = domain.ErrorOutputPersistFailed
		message = finalizeErr.Error()
	}
	if err := run.FinalizeFailure(code, message, true, result.FinishedAt); err != nil {
		_ = s.releaseCredential(cleanupCtx, owner, state, leaseID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	outcomeContext, contextVersion, contextErr := prepareTerminalRuntimeContext(runtimeContext, result.FinishedAt)
	if contextErr != nil {
		_ = s.releaseCredential(cleanupCtx, owner, state, leaseID, contextErr)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	if err := task.MarkRunFailed(run.Snapshot(), outcomeContext, result.FinishedAt); err != nil {
		_ = s.releaseCredential(cleanupCtx, owner, state, leaseID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	if err := s.persistTerminalWithRuntimeContext(cleanupCtx, owner, task.Snapshot(), expectedTaskVersion, run.Snapshot(), outcomeContext, contextVersion); err != nil {
		_ = s.releaseCredential(cleanupCtx, owner, state, leaseID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	_ = s.checkpoint(cleanupCtx, owner, state, saga.StageTerminalPersisted)
	_ = s.releaseCredential(cleanupCtx, owner, state, leaseID, nil)
	return queue.Outcome{Action: queue.ActionAck}
}

func (s *RunService) failAfterExit(ctx context.Context, owner domain.Owner, task *domain.Task, run *domain.Run, expectedTaskVersion int64, state *saga.State, stage saga.Stage, cause error, leaseID string, runtimeContext *domain.RuntimeContext) queue.Outcome {
	finishedAt := s.now().UTC()
	if state.ExitCode != nil {
		finishedAt = s.now().UTC()
	}
	code := codeFromError(cause, domain.ErrorExecutionFailed)
	if code == domain.ErrorTimeout {
		code = domain.ErrorOutputPersistFailed
	}
	if err := run.FinalizeFailure(code, cause.Error(), true, finishedAt); err != nil {
		s.recordFailure(ctx, owner, state, stage, cause, err)
		_ = s.releaseCredential(ctx, owner, state, leaseID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	outcomeContext, contextVersion, contextErr := prepareTerminalRuntimeContext(runtimeContext, finishedAt)
	if contextErr != nil {
		s.recordFailure(ctx, owner, state, stage, cause, contextErr)
		_ = s.releaseCredential(ctx, owner, state, leaseID, contextErr)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	if err := task.MarkRunFailed(run.Snapshot(), outcomeContext, finishedAt); err != nil {
		s.recordFailure(ctx, owner, state, stage, cause, err)
		_ = s.releaseCredential(ctx, owner, state, leaseID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	if err := s.persistTerminalWithRuntimeContext(ctx, owner, task.Snapshot(), expectedTaskVersion, run.Snapshot(), outcomeContext, contextVersion); err != nil {
		s.recordFailure(ctx, owner, state, stage, cause, err)
		_ = s.releaseCredential(ctx, owner, state, leaseID, err)
		return queue.Outcome{Action: queue.ActionRecover}
	}
	s.recordFailure(ctx, owner, state, stage, cause, nil)
	_ = s.checkpoint(ctx, owner, state, saga.StageTerminalPersisted)
	_ = s.releaseCredential(ctx, owner, state, leaseID, nil)
	return queue.Outcome{Action: queue.ActionAck}
}

func prepareTerminalRuntimeContext(runtimeContext *domain.RuntimeContext, finishedAt time.Time) (*domain.RuntimeContextSnapshot, int64, error) {
	if runtimeContext == nil {
		return nil, 0, nil
	}
	current := runtimeContext.Snapshot()
	clone, err := domain.RestoreRuntimeContext(current)
	if err != nil {
		return nil, 0, err
	}
	expectedVersion := current.Version
	if current.Status == domain.ContextBusy {
		if err := clone.Release(expectedVersion, finishedAt); err != nil {
			return nil, 0, err
		}
	} else if current.Status != domain.ContextActive {
		return nil, 0, domain.NewDomainError(domain.ErrorRunConflict, "RuntimeContext is not releasable", nil)
	} else {
		// An active in-memory snapshot can only result from preparation before a
		// later output/finalization failure. Its persisted predecessor is busy.
		expectedVersion = current.Version - 1
	}
	snapshot := clone.Snapshot()
	return &snapshot, expectedVersion, nil
}

func (s *RunService) persistTerminalWithRuntimeContext(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, run domain.RunSnapshot, runtimeContext *domain.RuntimeContextSnapshot, expectedContextVersion int64) error {
	if runtimeContext == nil {
		_, _, err := s.repository.FinalizeRunWithTask(ctx, owner, task, expectedTaskVersion, run)
		return err
	}
	repository, ok := s.repository.(RuntimeContextRepository)
	if !ok {
		return domain.NewDomainError(domain.ErrorResumeUnsupported, "Run repository does not support RuntimeContext finalization", nil)
	}
	_, _, _, err := repository.FinalizeRunWithTaskAndContext(ctx, owner, task, expectedTaskVersion, run, *runtimeContext, expectedContextVersion, false)
	return err
}

func finalizeResult(task *domain.Task, run *domain.Run, result adapter.ExecutionResult, runtimeContext *domain.RuntimeContextSnapshot) error {
	finishedAt := result.FinishedAt
	if result.TimedOut {
		if !result.CleanupResolved {
			return domain.NewDomainError(domain.ErrorCancelFailed, "timeout cleanup was not resolved", result.Err)
		}
		if err := run.FinalizeTimeout(true, finishedAt); err != nil {
			return err
		}
		return task.MarkRunTimedOut(run.Snapshot(), runtimeContext, finishedAt)
	}
	if result.Cancelled {
		if !result.CleanupResolved {
			return domain.NewDomainError(domain.ErrorCancelFailed, "cancellation cleanup was not resolved", result.Err)
		}
		if err := run.FinalizeCancel(true, finishedAt); err != nil {
			return err
		}
		return task.MarkRunCancelled(run.Snapshot(), runtimeContext, finishedAt)
	}
	if result.Err == nil && result.ExitCode == 0 {
		if err := run.FinalizeSuccess(true, finishedAt); err != nil {
			return err
		}
		return task.MarkRunCompleted(run.Snapshot(), runtimeContext, finishedAt)
	}
	code := codeFromError(result.Err, domain.ErrorExecutionFailed)
	message := fmt.Sprintf("One-shot child exited with code %d", result.ExitCode)
	if result.Err != nil {
		message = result.Err.Error()
	}
	if err := run.FinalizeFailure(code, message, true, finishedAt); err != nil {
		return err
	}
	return task.MarkRunFailed(run.Snapshot(), runtimeContext, finishedAt)
}

func (s *RunService) releaseCredential(ctx context.Context, owner domain.Owner, state *saga.State, leaseID string, primary error) error {
	if strings.TrimSpace(leaseID) == "" {
		if state.Stage.AtLeast(saga.StageTerminalPersisted) {
			_ = s.checkpoint(ctx, owner, state, saga.StageCredentialReleased)
		}
		return nil
	}
	if err := s.registry.ReleaseCredential(ctx, leaseID); err != nil {
		s.recordFailure(ctx, owner, state, state.Stage, primary, err)
		return err
	}
	state.CredentialLeaseID = nil
	if err := s.checkpoint(ctx, owner, state, saga.StageCredentialReleased); err != nil {
		s.recordFailure(ctx, owner, state, saga.StageCredentialReleased, primary, err)
		return err
	}
	return nil
}

func (s *RunService) releaseBusyContextBestEffort(ctx context.Context, owner domain.Owner, runtimeContext *domain.RuntimeContext) {
	if runtimeContext == nil || runtimeContext.Snapshot().Status != domain.ContextBusy {
		return
	}
	expected := runtimeContext.Snapshot().Version
	if err := runtimeContext.Release(expected, s.now().UTC()); err != nil {
		return
	}
	if repository, ok := s.repository.(RuntimeContextRepository); ok {
		_, _ = repository.UpdateRuntimeContext(ctx, owner, runtimeContext.Snapshot(), expected)
	}
}

// CancelActiveRun only reports success after ProcessSupervisor confirms the
// process tree is absent. The normal Process path then drains output and writes
// the cancelled terminal state.
func (s *RunService) CancelActiveRun(ctx context.Context, runID string) error {
	s.activeMu.RLock()
	process := s.active[runID]
	s.activeMu.RUnlock()
	if process == nil {
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- process.TerminateTree(s.gracePeriod) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		return err
	}
}

func (s *RunService) registerActive(runID string, process *Process) {
	s.activeMu.Lock()
	s.active[runID] = process
	s.activeMu.Unlock()
}

func (s *RunService) unregisterActive(runID string, process *Process) {
	s.activeMu.Lock()
	if s.active[runID] == process {
		delete(s.active, runID)
	}
	s.activeMu.Unlock()
}

// Acked implements queue.AckObserver and durably closes the Saga only after the
// execution Delivery ACK succeeds.
func (s *RunService) Acked(ctx context.Context, claim queue.Claim) error {
	value, ok := s.ackRuns.LoadAndDelete(claim.Delivery.ID)
	if !ok {
		return nil
	}
	runID, _ := value.(string)
	owner := domain.Owner{Kind: claim.Task.PrincipalKind, ID: claim.Task.PrincipalID}
	state, err := s.repository.GetSagaState(context.WithoutCancel(ctx), owner, runID)
	if err != nil {
		return err
	}
	return s.checkpoint(context.WithoutCancel(ctx), owner, &state, saga.StageAcknowledged)
}

func commandNameFromDelivery(delivery domain.DeliverySnapshot) string {
	value := delivery.Input.Options[shellCommandOption]
	name, _ := value.(string)
	return strings.TrimSpace(name)
}

func environmentFromDelivery(delivery domain.DeliverySnapshot) map[string]string {
	value, ok := delivery.Input.Options["environment"]
	if !ok {
		return nil
	}
	out := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		for key, item := range typed {
			out[key] = item
		}
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok {
				out[key] = text
			}
		}
	}
	return out
}

func timeoutFromDelivery(delivery domain.DeliverySnapshot) (time.Duration, error) {
	value, ok := delivery.Input.Options["timeout_seconds"]
	if !ok || value == nil {
		return 0, nil
	}
	var seconds int64
	switch typed := value.(type) {
	case int:
		seconds = int64(typed)
	case int64:
		seconds = typed
	case float64:
		if typed != float64(int64(typed)) {
			return 0, domain.InvalidRequestf("timeout_seconds must be an integer")
		}
		seconds = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, domain.InvalidRequestf("timeout_seconds must be an integer")
		}
		seconds = parsed
	default:
		return 0, domain.InvalidRequestf("timeout_seconds must be an integer")
	}
	if seconds <= 0 || seconds > 86400 {
		return 0, domain.InvalidRequestf("timeout_seconds must be between 1 and 86400")
	}
	return time.Duration(seconds) * time.Second, nil
}

func mergeSharedCommand(command adapter.CommandSpec, metadata adapter.ProviderMetadata, lease adapter.CredentialLease) adapter.CommandSpec {
	out := command
	if metadata.Executable != "" {
		out.Executable = metadata.Executable
	}
	if out.Environment == nil {
		out.Environment = make(map[string]adapter.EnvironmentValue)
	}
	for key, value := range metadata.Environment {
		out.Environment[key] = value
	}
	for key, value := range lease.Environment {
		out.Environment[key] = value
	}
	return out
}

func outcomeForPreRunError(err error) queue.Outcome {
	code := codeFromError(err, domain.ErrorInternal)
	if domain.IsRetryableCode(code) {
		return queue.Outcome{Action: queue.ActionRetry, Code: code}
	}
	return queue.Outcome{Action: queue.ActionDeadLetter, Code: code}
}

func codeFromError(err error, fallback domain.ErrorCode) domain.ErrorCode {
	if err != nil {
		if code, ok := domain.CodeOf(err); ok {
			return code
		}
	}
	return fallback
}

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }

// NewWorkerWhenEnabled prevents a disabled One-shot subsystem from starting a
// polling worker. A nil worker is an intentional disabled result.
func NewWorkerWhenEnabled(enabled bool, repository queue.Repository, processor queue.Processor, workerID string, options ...queue.WorkerOption) (*queue.Worker, error) {
	if !enabled {
		return nil, nil
	}
	return queue.NewWorker(repository, processor, workerID, options...)
}
