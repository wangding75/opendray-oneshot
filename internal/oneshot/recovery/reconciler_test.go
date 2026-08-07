package recovery

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/executor"
	"github.com/opendray/opendray-v2/internal/oneshot/saga"
)

type recoveryRepository struct {
	mu               sync.Mutex
	items            []saga.RecoveryItem
	states           map[string]saga.State
	contexts         map[string]domain.RuntimeContextSnapshot
	finalized        int
	contextFinalized int
	failList         error
	failFinal        error
	failState        error
}

func newRecoveryRepository(items ...saga.RecoveryItem) *recoveryRepository {
	states := make(map[string]saga.State, len(items))
	for _, item := range items {
		states[item.Run.ID] = item.Saga
	}
	return &recoveryRepository{items: items, states: states, contexts: make(map[string]domain.RuntimeContextSnapshot)}
}

func (r *recoveryRepository) ListRecoverableRuns(_ context.Context, limit int) ([]saga.RecoveryItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failList != nil {
		return nil, r.failList
	}
	if limit <= 0 || limit > len(r.items) {
		limit = len(r.items)
	}
	out := make([]saga.RecoveryItem, 0, limit)
	for _, item := range r.items[:limit] {
		item.Saga = r.states[item.Run.ID]
		out = append(out, item)
	}
	return out, nil
}

func (r *recoveryRepository) FinalizeRunWithTask(_ context.Context, _ domain.Owner, task domain.TaskSnapshot, _ int64, run domain.RunSnapshot) (domain.TaskSnapshot, domain.RunSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failFinal != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, r.failFinal
	}
	r.finalized++
	for index := range r.items {
		if r.items[index].Run.ID == run.ID {
			r.items[index].Task = task
			r.items[index].Run = run
		}
	}
	return task, run, nil
}

func (r *recoveryRepository) GetRuntimeContext(_ context.Context, owner domain.Owner, id string) (domain.RuntimeContextSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	contextSnapshot, ok := r.contexts[id]
	if !ok {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextNotFound, "RuntimeContext not found", nil)
	}
	if contextSnapshot.PrincipalKind != owner.Kind || contextSnapshot.PrincipalID != owner.ID {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "RuntimeContext owner mismatch", nil)
	}
	return contextSnapshot, nil
}

func (r *recoveryRepository) UpdateRuntimeContext(_ context.Context, owner domain.Owner, snapshot domain.RuntimeContextSnapshot, expectedVersion int64) (domain.RuntimeContextSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.contexts[snapshot.ID]
	if !ok || current.Version != expectedVersion {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "RuntimeContext version conflict", nil)
	}
	if current.PrincipalKind != owner.Kind || current.PrincipalID != owner.ID {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "RuntimeContext owner mismatch", nil)
	}
	r.contexts[snapshot.ID] = snapshot
	return snapshot, nil
}

func (r *recoveryRepository) FinalizeRunWithTaskAndContext(_ context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, run domain.RunSnapshot, runtimeContext domain.RuntimeContextSnapshot, expectedContextVersion int64, createContext bool) (domain.TaskSnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, error) {
	if createContext {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, errors.New("recovery must not create RuntimeContext")
	}
	if _, _, err := r.FinalizeRunWithTask(context.Background(), owner, task, expectedTaskVersion, run); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	persisted, err := r.UpdateRuntimeContext(context.Background(), owner, runtimeContext, expectedContextVersion)
	if err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	r.mu.Lock()
	r.contextFinalized++
	r.mu.Unlock()
	return task, run, persisted, nil
}

func (r *recoveryRepository) RecordSagaState(_ context.Context, _ domain.Owner, state saga.State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failState != nil {
		return r.failState
	}
	r.states[state.RunID] = state
	return nil
}

func (r *recoveryRepository) state(runID string) saga.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.states[runID]
}

type recoveryQueue struct {
	mu       sync.Mutex
	acked    []string
	failures int
}

func (q *recoveryQueue) AcknowledgeRecovered(_ context.Context, deliveryID, runID string) (domain.DeliverySnapshot, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failures > 0 {
		q.failures--
		return domain.DeliverySnapshot{}, errors.New("injected recovered ACK failure")
	}
	q.acked = append(q.acked, deliveryID+":"+runID)
	return domain.DeliverySnapshot{ID: deliveryID, RunID: &runID, Status: domain.DeliveryAcknowledged}, nil
}

type recoveryCredentials struct {
	mu       sync.Mutex
	released []string
	failures int
}

func (c *recoveryCredentials) ReleaseCredential(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failures > 0 {
		c.failures--
		return errors.New("injected credential release failure")
	}
	c.released = append(c.released, id)
	return nil
}

func recoveryItem(t *testing.T, status domain.RunStatus, stage saga.Stage) saga.RecoveryItem {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "recovery-owner"}
	task, err := domain.NewTask(domain.TaskArgs{
		Owner: owner, ProjectID: "recovery-project", ProviderID: "shell-oneshot-fixture",
		Model: "shell",
		Source: domain.Source{Kind: domain.SourceAPI, ClientRequestID: "recovery-request"}, Prompt: "recover",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{
		TaskID: task.Snapshot().ID, Operation: domain.DeliveryNew, RequestedBy: owner,
		Input:          domain.DeliveryInput{AttachmentRefs: []string{}, Options: map[string]any{}},
		IdempotencyKey: "recovery-key", PayloadSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MaxAttempts: 3, AvailableAt: now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.QueueInitialDelivery(delivery.Snapshot(), now); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Reserve("crashed-worker", now.Add(time.Hour), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(task.Snapshot(), delivery.Snapshot(), nil, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	if err := delivery.AttachRun(run.Snapshot().ID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := task.StartRun(delivery.Snapshot(), run.Snapshot(), now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}

	switch status {
	case domain.RunStarting:
	case domain.RunRunning:
		if err := run.ProcessStarted(999999, now.Add(4*time.Second)); err != nil {
			t.Fatal(err)
		}
	case domain.RunCollectingOutput, domain.RunCompleted:
		if err := run.ProcessStarted(999999, now.Add(4*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := run.ProcessExited(0); err != nil {
			t.Fatal(err)
		}
		if status == domain.RunCompleted {
			finished := now.Add(5 * time.Second)
			if err := run.FinalizeSuccess(true, finished); err != nil {
				t.Fatal(err)
			}
			if err := task.MarkRunCompleted(run.Snapshot(), nil, finished); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("unsupported fixture status %s", status)
	}

	leaseID := "recovery-credential-lease"
	state := saga.State{
		RunID: run.Snapshot().ID, TaskID: task.Snapshot().ID, DeliveryID: delivery.Snapshot().ID,
		Stage: stage, CredentialLeaseID: &leaseID, UpdatedAt: now.Add(6 * time.Second),
	}
	if stage.AtLeast(saga.StageProcessStarted) {
		pid := 999999
		state.PID = &pid
	}
	if stage.AtLeast(saga.StageProcessExited) {
		exitCode := 0
		state.ExitCode = &exitCode
	}
	return saga.RecoveryItem{
		Owner: owner, Task: task.Snapshot(), Delivery: delivery.Snapshot(), Run: run.Snapshot(), Saga: state,
	}
}

func attachBusyRuntimeContext(t *testing.T, item *saga.RecoveryItem) domain.RuntimeContextSnapshot {
	t.Helper()
	createdAt := item.Run.CreatedAt.Add(-time.Second)
	workspacePath := "/tmp/opendray-recovery-workspace"
	if filepath.Separator == '\\' {
		workspacePath = `C:\tmp\opendray-recovery-workspace`
	}
	runtimeContext, err := domain.NewRuntimeContext(domain.RuntimeContextArgs{
		Owner:             item.Owner,
		ProjectID:         item.Task.ProjectID,
		ProviderID:        item.Run.ProviderID,
		ProviderContextID: "provider-context-recovery",
		WorkspacePath:     workspacePath,
	}, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeContext.Snapshot()
	if err := runtimeContext.Acquire(item.Owner, item.Task.ProjectID, item.Run.ProviderID, active.Version, item.Run.CreatedAt); err != nil {
		t.Fatal(err)
	}
	snapshot := runtimeContext.Snapshot()
	item.Task.RuntimeContextID = stringPointerForTest(snapshot.ID)
	item.Run.RuntimeContextID = stringPointerForTest(snapshot.ID)
	return snapshot
}

func stringPointerForTest(value string) *string { return &value }

func TestReconcilerAcknowledgesTerminalRunWithoutRerun(t *testing.T) {
	item := recoveryItem(t, domain.RunCompleted, saga.StageTerminalPersisted)
	repository := newRecoveryRepository(item)
	queue := &recoveryQueue{}
	credentials := &recoveryCredentials{}
	reconciler, err := New(repository, queue, credentials, executor.NewProcessSupervisor())
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.finalized != 0 {
		t.Fatalf("terminal Run was finalized again: %d", repository.finalized)
	}
	state := repository.state(item.Run.ID)
	if state.Stage != saga.StageAcknowledged || state.CredentialLeaseID != nil {
		t.Fatalf("state = %+v", state)
	}
	credentials.mu.Lock()
	if len(credentials.released) != 1 || credentials.released[0] != "recovery-credential-lease" {
		t.Fatalf("released = %+v", credentials.released)
	}
	credentials.mu.Unlock()
	queue.mu.Lock()
	if len(queue.acked) != 1 {
		t.Fatalf("acked = %+v", queue.acked)
	}
	queue.mu.Unlock()
}

func TestReconcilerFinalizesCommittedOutputAndReleasesCredential(t *testing.T) {
	item := recoveryItem(t, domain.RunCollectingOutput, saga.StageOutputCommitted)
	repository := newRecoveryRepository(item)
	queue := &recoveryQueue{}
	credentials := &recoveryCredentials{}
	reconciler, err := New(repository, queue, credentials, executor.NewProcessSupervisor())
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.finalized != 1 {
		t.Fatalf("finalized = %d", repository.finalized)
	}
	repository.mu.Lock()
	got := repository.items[0]
	repository.mu.Unlock()
	if got.Run.Status != domain.RunCompleted || got.Task.Status != domain.TaskCompleted {
		t.Fatalf("task/run = %s/%s", got.Task.Status, got.Run.Status)
	}
	if state := repository.state(item.Run.ID); state.Stage != saga.StageAcknowledged {
		t.Fatalf("state = %+v", state)
	}
}

func TestReconcilerMarksInterruptedRunningRunFailed(t *testing.T) {
	item := recoveryItem(t, domain.RunRunning, saga.StageRunningPersisted)
	repository := newRecoveryRepository(item)
	queue := &recoveryQueue{}
	reconciler, err := New(repository, queue, nil, executor.NewProcessSupervisor())
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	got := repository.items[0]
	repository.mu.Unlock()
	if got.Run.Status != domain.RunFailed || got.Task.Status != domain.TaskFailed {
		t.Fatalf("task/run = %s/%s", got.Task.Status, got.Run.Status)
	}
	state := repository.state(item.Run.ID)
	if state.FailureStage == nil || *state.FailureStage != "gateway_recovery" || state.PrimaryErrorCode == nil {
		t.Fatalf("recovery audit = %+v", state)
	}
}

func TestReconcilerPersistsACKAndCompensationFailuresThenRetries(t *testing.T) {
	item := recoveryItem(t, domain.RunCompleted, saga.StageTerminalPersisted)
	repository := newRecoveryRepository(item)
	queue := &recoveryQueue{failures: 1}
	credentials := &recoveryCredentials{failures: 1}
	reconciler, err := New(repository, queue, credentials, executor.NewProcessSupervisor())
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background()); err == nil {
		t.Fatal("credential compensation failure was not returned")
	}
	state := repository.state(item.Run.ID)
	if state.FailureStage == nil || *state.FailureStage != "credential_release" || state.CompensationError == nil {
		t.Fatalf("credential failure audit = %+v", state)
	}
	if err := reconciler.RunOnce(context.Background()); err == nil {
		t.Fatal("recovered ACK failure was not returned")
	}
	state = repository.state(item.Run.ID)
	if state.FailureStage == nil || *state.FailureStage != "acknowledge_recovered" || state.PrimaryErrorCode == nil {
		t.Fatalf("ACK failure audit = %+v", state)
	}
	if err := reconciler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state = repository.state(item.Run.ID); state.Stage != saga.StageAcknowledged {
		t.Fatalf("state after retry = %+v", state)
	}
}

func TestReconcilerAtomicallyReleasesBusyRuntimeContextForInterruptedContinueRun(t *testing.T) {
	item := recoveryItem(t, domain.RunRunning, saga.StageRunningPersisted)
	contextSnapshot := attachBusyRuntimeContext(t, &item)
	repository := newRecoveryRepository(item)
	repository.contexts[contextSnapshot.ID] = contextSnapshot
	reconciler, err := New(repository, &recoveryQueue{}, nil, executor.NewProcessSupervisor())
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	persistedContext := repository.contexts[contextSnapshot.ID]
	finalized := repository.contextFinalized
	got := repository.items[0]
	repository.mu.Unlock()
	if finalized != 1 {
		t.Fatalf("context finalization count = %d", finalized)
	}
	if persistedContext.Status != domain.ContextActive || persistedContext.Version != contextSnapshot.Version+1 {
		t.Fatalf("persisted context = %+v", persistedContext)
	}
	if got.Run.Status != domain.RunFailed || got.Task.Status != domain.TaskFailed || got.Task.RuntimeContextID == nil || *got.Task.RuntimeContextID != contextSnapshot.ID {
		t.Fatalf("recovered task/run = %+v / %+v", got.Task, got.Run)
	}
}

func TestReconcilerReleasesBusyRuntimeContextBeforeAcknowledgingTerminalRun(t *testing.T) {
	item := recoveryItem(t, domain.RunCompleted, saga.StageTerminalPersisted)
	contextSnapshot := attachBusyRuntimeContext(t, &item)
	repository := newRecoveryRepository(item)
	repository.contexts[contextSnapshot.ID] = contextSnapshot
	queue := &recoveryQueue{}
	reconciler, err := New(repository, queue, nil, executor.NewProcessSupervisor())
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	persistedContext := repository.contexts[contextSnapshot.ID]
	repository.mu.Unlock()
	if persistedContext.Status != domain.ContextActive || persistedContext.Version != contextSnapshot.Version+1 {
		t.Fatalf("persisted context = %+v", persistedContext)
	}
	if repository.finalized != 0 || repository.contextFinalized != 0 {
		t.Fatalf("terminal Run was re-finalized: run=%d context=%d", repository.finalized, repository.contextFinalized)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.acked) != 1 {
		t.Fatalf("acked = %+v", queue.acked)
	}
}

func TestReconcilerRunFailsClosedWhenStartupRecoveryFails(t *testing.T) {
	repository := newRecoveryRepository()
	repository.failList = errors.New("injected startup recovery failure")
	reconciler, err := New(repository, &recoveryQueue{}, &recoveryCredentials{}, executor.NewProcessSupervisor())
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Run(context.Background(), time.Hour); err == nil || err.Error() != "injected startup recovery failure" {
		t.Fatalf("Run error = %v", err)
	}
}
