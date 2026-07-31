package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/application"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/saga"
)

type fakeRunRepository struct {
	mu         sync.Mutex
	queue      *queue.MemoryQueue
	tasks      map[string]domain.TaskSnapshot
	deliveries map[string]domain.DeliverySnapshot
	runs       map[string]domain.RunSnapshot
	contexts   map[string]domain.RuntimeContextSnapshot
	artifacts  []domain.ArtifactSnapshot
	records    []domain.StreamRecordSnapshot
	events     []domain.StandardEventSnapshot
	sagas      map[string]saga.State

	failCreate      error
	failGetRun      error
	failUpdate      error
	failFinalize    error
	failLoadCursor  error
	failAppend      error
	failRecordStage map[saga.Stage]error
	corruptCreate   string
}

func newFakeRunRepository(executionQueue *queue.MemoryQueue) *fakeRunRepository {
	return &fakeRunRepository{
		queue:      executionQueue,
		tasks:      map[string]domain.TaskSnapshot{},
		deliveries: map[string]domain.DeliverySnapshot{},
		runs:       map[string]domain.RunSnapshot{},
		contexts:   map[string]domain.RuntimeContextSnapshot{},
		sagas:      map[string]saga.State{},
	}
}

func (r *fakeRunRepository) CreateRunWithSaga(_ context.Context, _ domain.Owner, task domain.TaskSnapshot, expectedVersion int64, delivery domain.DeliverySnapshot, run domain.RunSnapshot, initial saga.State) (domain.TaskSnapshot, domain.DeliverySnapshot, domain.RunSnapshot, error) {
	if r.failCreate != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, r.failCreate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if task.Version != expectedVersion+1 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("task version mismatch")
	}
	if err := initial.Validate(); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if initial.Stage != saga.StageRunCreated || initial.RunID != run.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("invalid initial Saga state")
	}
	r.tasks[task.ID] = task
	r.deliveries[delivery.ID] = delivery
	r.runs[run.ID] = run
	r.sagas[run.ID] = initial
	returnedTask, returnedDelivery, returnedRun := task, delivery, run
	switch r.corruptCreate {
	case "task":
		returnedTask.Version = 0
	case "delivery":
		returnedDelivery.ID = "invalid"
	case "run":
		returnedRun.ID = "invalid"
	}
	return returnedTask, returnedDelivery, returnedRun, nil
}

func (r *fakeRunRepository) GetTask(_ context.Context, owner domain.Owner, taskID string) (domain.TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	task, ok := r.tasks[taskID]
	if !ok || task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID {
		return domain.TaskSnapshot{}, domain.NewDomainError(domain.ErrorTaskNotFound, "Task not found", nil)
	}
	return task, nil
}

func (r *fakeRunRepository) GetRuntimeContext(_ context.Context, owner domain.Owner, contextID string) (domain.RuntimeContextSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	runtimeContext, ok := r.contexts[contextID]
	if !ok || runtimeContext.PrincipalKind != owner.Kind || runtimeContext.PrincipalID != owner.ID {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextNotFound, "RuntimeContext not found", nil)
	}
	return runtimeContext, nil
}

func (r *fakeRunRepository) UpdateRuntimeContext(_ context.Context, owner domain.Owner, runtimeContext domain.RuntimeContextSnapshot, expectedVersion int64) (domain.RuntimeContextSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.contexts[runtimeContext.ID]
	if !ok || current.PrincipalKind != owner.Kind || current.PrincipalID != owner.ID {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextNotFound, "RuntimeContext not found", nil)
	}
	if current.Version != expectedVersion || runtimeContext.Version != expectedVersion+1 {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "RuntimeContext version conflict", nil)
	}
	r.contexts[runtimeContext.ID] = runtimeContext
	return runtimeContext, nil
}

func (r *fakeRunRepository) CreateDeliveryWithTaskUpdate(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedVersion int64, delivery domain.DeliverySnapshot) (domain.TaskSnapshot, domain.DeliverySnapshot, error) {
	r.mu.Lock()
	current, ok := r.tasks[task.ID]
	if !ok || current.PrincipalKind != owner.Kind || current.PrincipalID != owner.ID || current.Version != expectedVersion || task.Version != expectedVersion+1 {
		r.mu.Unlock()
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Task version conflict", nil)
	}
	r.tasks[task.ID] = task
	r.deliveries[delivery.ID] = delivery
	r.mu.Unlock()
	result, err := r.queue.EnqueueContinue(ctx, task, delivery, "/api/v1/oneshot/tasks/"+task.ID+"/continue")
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	return result.Task, result.Delivery, nil
}

func (r *fakeRunRepository) FindContinueReplay(_ context.Context, _ domain.Owner, _, _, _ string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, nil
}

func (r *fakeRunRepository) CreateContinueDelivery(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedVersion int64, delivery domain.DeliverySnapshot, canonicalPath string, _ *time.Time) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	updatedTask, createdDelivery, err := r.CreateDeliveryWithTaskUpdate(ctx, owner, task, expectedVersion, delivery)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	if canonicalPath != application.ContinueCanonicalPath(task.ID) {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.InvalidRequestf("unexpected continue canonical path")
	}
	return updatedTask, createdDelivery, true, nil
}
func (r *fakeRunRepository) CreateContinueRunWithSaga(_ context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, delivery domain.DeliverySnapshot, run domain.RunSnapshot, runtimeContext domain.RuntimeContextSnapshot, expectedContextVersion int64, initial saga.State) (domain.TaskSnapshot, domain.DeliverySnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	currentTask, ok := r.tasks[task.ID]
	currentContext, contextOK := r.contexts[runtimeContext.ID]
	if !ok || !contextOK || currentTask.Version != expectedTaskVersion || currentContext.Version != expectedContextVersion {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "continue version conflict", nil)
	}
	if task.Version != expectedTaskVersion+1 || runtimeContext.Version != expectedContextVersion+1 || runtimeContext.Status != domain.ContextBusy {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("invalid continue transition")
	}
	if task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID || runtimeContext.PrincipalKind != owner.Kind || runtimeContext.PrincipalID != owner.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "continue owner mismatch", nil)
	}
	r.tasks[task.ID] = task
	r.deliveries[delivery.ID] = delivery
	r.runs[run.ID] = run
	r.contexts[runtimeContext.ID] = runtimeContext
	r.sagas[run.ID] = initial
	return task, delivery, run, runtimeContext, nil
}

func (r *fakeRunRepository) FinalizeRunWithTaskAndContext(_ context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, run domain.RunSnapshot, runtimeContext domain.RuntimeContextSnapshot, expectedContextVersion int64, createContext bool) (domain.TaskSnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, error) {
	r.mu.Lock()
	currentTask, ok := r.tasks[task.ID]
	if !ok || currentTask.Version != expectedTaskVersion || task.Version != expectedTaskVersion+1 {
		r.mu.Unlock()
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Task version conflict", nil)
	}
	if createContext {
		if _, exists := r.contexts[runtimeContext.ID]; exists || runtimeContext.Version != 1 {
			r.mu.Unlock()
			return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "RuntimeContext conflict", nil)
		}
	} else {
		currentContext, exists := r.contexts[runtimeContext.ID]
		if !exists || currentContext.Version != expectedContextVersion || runtimeContext.Version != expectedContextVersion+1 {
			r.mu.Unlock()
			return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "RuntimeContext version conflict", nil)
		}
	}
	if task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID || runtimeContext.PrincipalKind != owner.Kind || runtimeContext.PrincipalID != owner.ID || runtimeContext.Status != domain.ContextActive {
		r.mu.Unlock()
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "RuntimeContext owner mismatch", nil)
	}
	r.tasks[task.ID] = task
	r.runs[run.ID] = run
	r.contexts[runtimeContext.ID] = runtimeContext
	r.mu.Unlock()
	if err := r.queue.AttachTerminalRun(run.DeliveryID, run.ID, run.Status); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	return task, run, runtimeContext, nil
}

func (r *fakeRunRepository) GetRun(_ context.Context, _ domain.Owner, runID string) (domain.RunSnapshot, error) {
	if r.failGetRun != nil {
		return domain.RunSnapshot{}, r.failGetRun
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return domain.RunSnapshot{}, domain.NewDomainError(domain.ErrorRunNotFound, "Run not found", nil)
	}
	return run, nil
}

func (r *fakeRunRepository) RecordSagaState(_ context.Context, _ domain.Owner, state saga.State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if r.failRecordStage != nil {
		if err := r.failRecordStage[state.Stage]; err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.sagas[state.RunID] = state
	r.mu.Unlock()
	return nil
}

func (r *fakeRunRepository) GetSagaState(_ context.Context, _ domain.Owner, runID string) (saga.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.sagas[runID]
	if !ok {
		return saga.State{}, domain.NewDomainError(domain.ErrorRunNotFound, "Run saga not found", nil)
	}
	return state, nil
}

func (r *fakeRunRepository) UpdateRun(_ context.Context, _ domain.Owner, run domain.RunSnapshot) (domain.RunSnapshot, error) {
	if r.failUpdate != nil {
		return domain.RunSnapshot{}, r.failUpdate
	}
	r.mu.Lock()
	r.runs[run.ID] = run
	r.mu.Unlock()
	if run.Status.Terminal() {
		if err := r.queue.AttachTerminalRun(run.DeliveryID, run.ID, run.Status); err != nil {
			return domain.RunSnapshot{}, err
		}
	}
	return run, nil
}

func (r *fakeRunRepository) FinalizeRunWithTask(_ context.Context, _ domain.Owner, task domain.TaskSnapshot, expectedVersion int64, run domain.RunSnapshot) (domain.TaskSnapshot, domain.RunSnapshot, error) {
	if r.failFinalize != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, r.failFinalize
	}
	r.mu.Lock()
	current, ok := r.tasks[task.ID]
	if !ok || current.Version != expectedVersion || task.Version != expectedVersion+1 {
		r.mu.Unlock()
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "task version conflict", nil)
	}
	if !run.Status.Terminal() || string(task.Status) != string(run.Status) {
		r.mu.Unlock()
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("terminal outcome mismatch")
	}
	r.tasks[task.ID] = task
	r.runs[run.ID] = run
	r.mu.Unlock()
	if err := r.queue.AttachTerminalRun(run.DeliveryID, run.ID, run.Status); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	return task, run, nil
}

func (r *fakeRunRepository) LoadOutputCursor(_ context.Context, _ domain.Owner, runID string) (int64, int64, int64, int64, error) {
	if r.failLoadCursor != nil {
		return 0, 0, 0, 0, r.failLoadCursor
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var streamSequence, eventSequence, stdoutOffset, stderrOffset int64
	for _, record := range r.records {
		if record.RunID != runID {
			continue
		}
		if record.Sequence > streamSequence {
			streamSequence = record.Sequence
		}
		next := record.ByteOffset + record.ByteLength
		if record.Stream == domain.StreamStdout && next > stdoutOffset {
			stdoutOffset = next
		}
		if record.Stream == domain.StreamStderr && next > stderrOffset {
			stderrOffset = next
		}
	}
	for _, event := range r.events {
		if event.RunID == runID && event.Sequence > eventSequence {
			eventSequence = event.Sequence
		}
	}
	return streamSequence, eventSequence, stdoutOffset, stderrOffset, nil
}

func (r *fakeRunRepository) AppendOutput(_ context.Context, _ domain.Owner, runID string, artifacts []domain.ArtifactSnapshot, records []domain.StreamRecordSnapshot, events []domain.StandardEventSnapshot) error {
	if r.failAppend != nil {
		return r.failAppend
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range records {
		if record.RunID != runID {
			return domain.InvalidRequestf("output Run mismatch")
		}
	}
	r.artifacts = append(r.artifacts, artifacts...)
	r.records = append(r.records, records...)
	r.events = append(r.events, events...)
	return nil
}

func (r *fakeRunRepository) snapshots() (domain.TaskSnapshot, domain.RunSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var task domain.TaskSnapshot
	for _, item := range r.tasks {
		task = item
	}
	var run domain.RunSnapshot
	for _, item := range r.runs {
		run = item
	}
	return task, run
}

func shellFixtureCommands(t *testing.T) map[string]adapter.CommandSpec {
	t.Helper()
	cwd := t.TempDir()
	missingCWD := filepath.Join(t.TempDir(), "missing")
	return map[string]adapter.CommandSpec{
		"success": {
			Executable: "/bin/sh",
			Args:       []string{fixturePath(t, "success.sh")},
			Dir:        cwd,
		},
		"nonzero": {
			Executable: "/bin/sh",
			Args:       []string{fixturePath(t, "nonzero.sh")},
			Dir:        cwd,
		},
		"interleaved": {
			Executable: "/bin/sh",
			Args:       []string{fixturePath(t, "interleaved.sh")},
			Dir:        cwd,
		},
		"missing-command": {
			Executable: filepath.Join(t.TempDir(), "missing-command"),
			Dir:        cwd,
		},
		"missing-cwd": {
			Executable: "/bin/sh",
			Args:       []string{fixturePath(t, "success.sh")},
			Dir:        missingCWD,
		},
	}
}

func enqueueShellTask(t *testing.T, executionQueue *queue.MemoryQueue, commandName string) (domain.TaskSnapshot, domain.DeliverySnapshot) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Second)
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "executor-test-owner"}
	task, err := domain.NewTask(domain.TaskArgs{
		Owner:      owner,
		ProjectID:  "executor-test-project",
		ProviderID: adapter.ShellProviderID,
		Source:     domain.Source{Kind: domain.SourceAPI, ClientRequestID: "executor-test-request"},
		Prompt:     "execute fixture",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{
		TaskID:      task.Snapshot().ID,
		Operation:   domain.DeliveryNew,
		RequestedBy: owner,
		Input: domain.DeliveryInput{
			AttachmentRefs: []string{},
			Options:        map[string]any{shellCommandOption: commandName},
		},
		IdempotencyKey: "executor-test-key-" + commandName,
		PayloadSHA256:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MaxAttempts:    1,
		AvailableAt:    now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.QueueInitialDelivery(delivery.Snapshot(), now); err != nil {
		t.Fatal(err)
	}
	result, err := executionQueue.Enqueue(context.Background(), queue.EnqueueRequest{
		Task: task.Snapshot(), Delivery: delivery.Snapshot(), Method: "POST",
		CanonicalPath: "/api/v1/oneshot/tasks", IdempotencyKey: delivery.Snapshot().IdempotencyKey,
		PayloadSHA256: delivery.Snapshot().PayloadSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Task, result.Delivery
}

func TestWorkerRunServiceExecutorChain(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	for _, test := range []struct {
		name       string
		command    string
		taskStatus domain.TaskStatus
		runStatus  domain.RunStatus
		exitCode   int
	}{
		{name: "success", command: "success", taskStatus: domain.TaskCompleted, runStatus: domain.RunCompleted, exitCode: 0},
		{name: "nonzero", command: "nonzero", taskStatus: domain.TaskFailed, runStatus: domain.RunFailed, exitCode: 7},
		{name: "command-not-found", command: "missing-command", taskStatus: domain.TaskFailed, runStatus: domain.RunFailed, exitCode: 0},
		{name: "cwd-not-found", command: "missing-cwd", taskStatus: domain.TaskFailed, runStatus: domain.RunFailed, exitCode: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			executionQueue := queue.NewMemoryQueue(nil)
			_, initialDelivery := enqueueShellTask(t, executionQueue, test.command)
			repository := newFakeRunRepository(executionQueue)
			shellAdapter := adapter.NewShellAdapter(adapter.ShellConfig{
				Enabled:  true,
				Commands: shellFixtureCommands(t),
			})
			storage, storageErr := NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
			if storageErr != nil {
				t.Fatal(storageErr)
			}
			service, err := NewRunService(repository, adapter.NewRegistry(shellAdapter), NewProcessExecutor(), WithArtifactStorage(storage))
			if err != nil {
				t.Fatal(err)
			}
			worker, err := queue.NewWorker(executionQueue, service, "executor-test-worker", queue.WithWorkerClaimLimit(1))
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.DrainOnce(context.Background()); err != nil {
				t.Fatal(err)
			}

			delivery, ok := executionQueue.GetDelivery(initialDelivery.ID)
			if !ok || delivery.Status != domain.DeliveryAcknowledged {
				t.Fatalf("delivery = %+v, exists=%v", delivery, ok)
			}
			task, run := repository.snapshots()
			if task.Status != test.taskStatus || run.Status != test.runStatus {
				t.Fatalf("task/run status = %s/%s", task.Status, run.Status)
			}
			if run.StartedAt == nil && test.command != "missing-command" && test.command != "missing-cwd" {
				t.Fatal("started process did not persist started_at")
			}
			if run.FinishedAt == nil {
				t.Fatal("terminal Run did not persist finished_at")
			}
			if run.Status == domain.RunCompleted || (run.Status == domain.RunFailed && run.StartedAt != nil) {
				if run.ExitCode == nil || *run.ExitCode != test.exitCode {
					t.Fatalf("exit code = %v, want %d", run.ExitCode, test.exitCode)
				}
			}
		})
	}
}

func TestRunServiceCapturesInterleavedOutputAndFinalManifest(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	executionQueue := queue.NewMemoryQueue(nil)
	_, initialDelivery := enqueueShellTask(t, executionQueue, "interleaved")
	repository := newFakeRunRepository(executionQueue)
	storage, err := NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	shellAdapter := adapter.NewShellAdapter(adapter.ShellConfig{Enabled: true, Commands: shellFixtureCommands(t)})
	service, err := NewRunService(repository, adapter.NewRegistry(shellAdapter), NewProcessExecutor(), WithArtifactStorage(storage), WithOutputChunkSize(1024))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := queue.NewWorker(executionQueue, service, "output-test-worker", queue.WithWorkerClaimLimit(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	delivery, ok := executionQueue.GetDelivery(initialDelivery.ID)
	if !ok || delivery.Status != domain.DeliveryAcknowledged {
		t.Fatalf("delivery = %+v, exists=%v", delivery, ok)
	}

	repository.mu.Lock()
	records := append([]domain.StreamRecordSnapshot(nil), repository.records...)
	events := append([]domain.StandardEventSnapshot(nil), repository.events...)
	artifacts := append([]domain.ArtifactSnapshot(nil), repository.artifacts...)
	repository.mu.Unlock()
	wantStreams := []domain.StreamKind{domain.StreamStdout, domain.StreamStderr, domain.StreamStdout, domain.StreamStderr}
	if len(records) != len(wantStreams) || len(events) != len(wantStreams) {
		t.Fatalf("records/events = %d/%d", len(records), len(events))
	}
	for index, record := range records {
		if record.Sequence != int64(index+1) || record.Stream != wantStreams[index] {
			t.Fatalf("record[%d] = %+v", index, record)
		}
	}
	finalCount := 0
	for _, artifact := range artifacts {
		if artifact.Kind == domain.ArtifactFinalResult {
			finalCount++
		}
	}
	if finalCount != 1 || len(artifacts) != len(records)+1 {
		t.Fatalf("artifacts=%d final=%d", len(artifacts), finalCount)
	}
}

func TestNewWorkerWhenDisabledDoesNotStartWorker(t *testing.T) {
	worker, err := NewWorkerWhenEnabled(false, nil, nil, "")
	if err != nil || worker != nil {
		t.Fatalf("disabled worker = %v, err=%v", worker, err)
	}
}

func TestRunServiceRejectsUnsupportedProviderBeforeRunCreation(t *testing.T) {
	executionQueue := queue.NewMemoryQueue(nil)
	task, delivery := enqueueShellTask(t, executionQueue, "success")
	task.ProviderID = "unsupported-provider"
	claim := queue.Claim{Task: task, Delivery: delivery}
	repository := newFakeRunRepository(executionQueue)
	storage, storageErr := NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
	if storageErr != nil {
		t.Fatal(storageErr)
	}
	service, err := NewRunService(repository, adapter.NewRegistry(), NewProcessExecutor(), WithArtifactStorage(storage))
	if err != nil {
		t.Fatal(err)
	}
	outcome := service.Process(context.Background(), claim)
	if outcome.Action != queue.ActionDeadLetter || outcome.Code != domain.ErrorUnsupportedProvider {
		t.Fatalf("outcome = %+v", outcome)
	}
	_, run := repository.snapshots()
	if run.ID != "" {
		t.Fatalf("unsupported provider created Run: %+v", run)
	}
}

func TestRunServiceFailsClosedWhenPersistedRunSnapshotsAreInvalid(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	for _, corrupt := range []string{"task", "delivery", "run"} {
		t.Run(corrupt, func(t *testing.T) {
			executionQueue := queue.NewMemoryQueue(nil)
			enqueueShellTask(t, executionQueue, "success")
			claims, err := executionQueue.ClaimDue(context.Background(), "corruption-test-worker", 1, time.Minute)
			if err != nil || len(claims) != 1 {
				t.Fatalf("claim due: claims=%d err=%v", len(claims), err)
			}
			claim := claims[0]
			task, delivery := claim.Task, claim.Delivery
			repository := newFakeRunRepository(executionQueue)
			repository.corruptCreate = corrupt
			storage, err := NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
			if err != nil {
				t.Fatal(err)
			}
			provider := adapter.NewShellAdapter(adapter.ShellConfig{Enabled: true, Commands: shellFixtureCommands(t)})
			service, err := NewRunService(repository, adapter.NewRegistry(provider), NewProcessExecutor(), WithArtifactStorage(storage))
			if err != nil {
				t.Fatal(err)
			}
			outcome := service.Process(context.Background(), queue.Claim{Task: task, Delivery: delivery})
			if outcome.Action != queue.ActionRecover {
				t.Fatalf("outcome=%+v want recover", outcome)
			}
			repository.mu.Lock()
			defer repository.mu.Unlock()
			if len(repository.sagas) != 1 {
				t.Fatalf("saga count=%d want=1", len(repository.sagas))
			}
			for _, state := range repository.sagas {
				if state.FailureStage == nil || state.PrimaryErrorCode == nil {
					t.Fatalf("invalid persisted snapshot was not recorded in Saga: %+v", state)
				}
			}
		})
	}
}

type providerFixtureCatalog struct {
	metadata map[string]adapter.ProviderMetadata
}

func (c providerFixtureCatalog) OneShotProvider(_ context.Context, providerID string) (adapter.ProviderMetadata, error) {
	metadata, ok := c.metadata[providerID]
	if !ok {
		return adapter.ProviderMetadata{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "provider metadata not found", nil)
	}
	return metadata, nil
}

func fakeProviderExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-provider")
	script := `#!/bin/sh
set -eu
prompt=''
IFS= read -r prompt || true
printf '%s:%s\n' "$$" "$prompt" >> pids.log
resume=0
for arg in "$@"; do
  case "$arg" in
    resume|--resume) resume=1 ;;
  esac
done
if [ "${FAKE_PROVIDER:-}" = "codex" ]; then
  printf '%s\n' '{"type":"thread.started","thread_id":"provider-context-001"}'
  if [ "$resume" -eq 1 ]; then
    printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"codex resumed"}}'
  else
    printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"codex initial"}}'
  fi
else
  printf '%s\n' '{"type":"system","subtype":"init","session_id":"provider-context-001"}'
  if [ "$resume" -eq 1 ]; then
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"claude resumed","session_id":"provider-context-001"}'
  else
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"claude initial","session_id":"provider-context-001"}'
  fi
fi
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func enqueueProviderTask(t *testing.T, executionQueue *queue.MemoryQueue, providerID, workspace string) (domain.TaskSnapshot, domain.DeliverySnapshot) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Second)
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "provider-owner"}
	task, err := domain.NewTask(domain.TaskArgs{
		Owner: owner, ProjectID: "provider-project", ProviderID: providerID,
		Source: domain.Source{Kind: domain.SourceAPI, ClientRequestID: "provider-request"},
		Prompt: "initial provider prompt",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{
		TaskID: task.Snapshot().ID, Operation: domain.DeliveryNew, RequestedBy: owner,
		Input:          domain.DeliveryInput{Options: map[string]any{"workspace_path": workspace}},
		IdempotencyKey: "provider-initial-key", PayloadSHA256: strings.Repeat("c", 64), MaxAttempts: 1, AvailableAt: now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.QueueInitialDelivery(delivery.Snapshot(), now); err != nil {
		t.Fatal(err)
	}
	result, err := executionQueue.Enqueue(context.Background(), queue.EnqueueRequest{
		Task: task.Snapshot(), Delivery: delivery.Snapshot(), Method: "POST",
		CanonicalPath: "/api/v1/oneshot/tasks", IdempotencyKey: delivery.Snapshot().IdempotencyKey,
		PayloadSHA256: delivery.Snapshot().PayloadSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Task, result.Delivery
}

func TestProviderInitialAndContinueRunsUsePersistedRuntimeContextAndNewProcess(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fake provider")
	}
	for _, test := range []struct {
		name       string
		providerID string
		provider   adapter.OneShotAdapter
		envValue   string
	}{
		{name: "codex", providerID: adapter.CodexProviderID, provider: adapter.NewCodexAdapter(adapter.CodexConfig{Enabled: true}), envValue: "codex"},
		{name: "claude", providerID: adapter.ClaudeProviderID, provider: adapter.NewClaudeAdapter(adapter.ClaudeConfig{Enabled: true}), envValue: "claude"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			executable := fakeProviderExecutable(t)
			catalog := providerFixtureCatalog{metadata: map[string]adapter.ProviderMetadata{
				test.providerID: {
					ID: test.providerID, DisplayName: test.name, Version: "9.0.0", Executable: executable, Enabled: true,
					Environment: map[string]adapter.EnvironmentValue{"FAKE_PROVIDER": {Value: test.envValue}},
				},
			}}
			registry, err := adapter.NewConfiguredRegistry(true, catalog, nil, test.provider)
			if err != nil {
				t.Fatal(err)
			}
			executionQueue := queue.NewMemoryQueue(nil)
			initialTask, _ := enqueueProviderTask(t, executionQueue, test.providerID, workspace)
			repository := newFakeRunRepository(executionQueue)
			storage, err := NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewRunService(repository, registry, NewProcessExecutor(), WithArtifactStorage(storage))
			if err != nil {
				t.Fatal(err)
			}
			worker, err := queue.NewWorker(executionQueue, service, "provider-worker", queue.WithWorkerClaimLimit(1))
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.DrainOnce(context.Background()); err != nil {
				t.Fatal(err)
			}

			persistedTask, err := repository.GetTask(context.Background(), domain.Owner{Kind: initialTask.PrincipalKind, ID: initialTask.PrincipalID}, initialTask.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persistedTask.Status != domain.TaskCompleted || persistedTask.RuntimeContextID == nil {
				t.Fatalf("initial Task=%+v", persistedTask)
			}
			persistedContext, err := repository.GetRuntimeContext(context.Background(), domain.Owner{Kind: initialTask.PrincipalKind, ID: initialTask.PrincipalID}, *persistedTask.RuntimeContextID)
			if err != nil {
				t.Fatal(err)
			}
			if persistedContext.ProviderContextID != "provider-context-001" || persistedContext.Status != domain.ContextActive || persistedContext.WorkspacePath != workspace {
				t.Fatalf("initial RuntimeContext=%+v", persistedContext)
			}

			continuation, err := application.NewContinuationService(repository, registry)
			if err != nil {
				t.Fatal(err)
			}
			continueResult, err := continuation.Continue(context.Background(), application.ContinueTaskCommand{
				Owner:     domain.Owner{Kind: initialTask.PrincipalKind, ID: initialTask.PrincipalID},
				ProjectID: initialTask.ProjectID, TaskID: initialTask.ID, ProviderID: test.providerID,
				WorkspacePath: workspace, PromptDelta: "follow-up provider prompt", IdempotencyKey: "provider-continue-key", MaxAttempts: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if continueResult.Delivery.Operation != domain.DeliveryContinue {
				t.Fatalf("continue Delivery=%+v", continueResult.Delivery)
			}
			if err := worker.DrainOnce(context.Background()); err != nil {
				t.Fatal(err)
			}

			finalTask, err := repository.GetTask(context.Background(), domain.Owner{Kind: initialTask.PrincipalKind, ID: initialTask.PrincipalID}, initialTask.ID)
			if err != nil {
				t.Fatal(err)
			}
			finalContext, err := repository.GetRuntimeContext(context.Background(), domain.Owner{Kind: initialTask.PrincipalKind, ID: initialTask.PrincipalID}, *finalTask.RuntimeContextID)
			if err != nil {
				t.Fatal(err)
			}
			if finalTask.Status != domain.TaskCompleted || finalContext.ID != persistedContext.ID || finalContext.Status != domain.ContextActive || finalContext.Version <= persistedContext.Version {
				t.Fatalf("final Task/Context=%+v %+v", finalTask, finalContext)
			}
			repository.mu.Lock()
			var runIDs []string
			for runID := range repository.runs {
				runIDs = append(runIDs, runID)
			}
			repository.mu.Unlock()
			if len(runIDs) != 2 || runIDs[0] == runIDs[1] {
				t.Fatalf("expected two separate Runs, got %v", runIDs)
			}
			raw, err := os.ReadFile(filepath.Join(workspace, "pids.log"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != 2 || !strings.Contains(lines[0], "initial provider prompt") || !strings.Contains(lines[1], "follow-up provider prompt") {
				t.Fatalf("process evidence=%q", string(raw))
			}
			firstPID := strings.SplitN(lines[0], ":", 2)[0]
			secondPID := strings.SplitN(lines[1], ":", 2)[0]
			if firstPID == secondPID {
				t.Fatalf("continue reused process pid=%s evidence=%q", firstPID, string(raw))
			}
		})
	}
}

func fakeResumeFailureExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-resume-failure")
	script := `#!/bin/sh
set -eu
prompt=''
IFS= read -r prompt || true
resume=0
for arg in "$@"; do
  case "$arg" in resume|--resume) resume=1 ;; esac
done
if [ "$resume" -eq 1 ]; then
  printf '%s\n' '{"type":"error","message":"resume thread not found"}'
  exit 9
fi
printf '%s\n' '{"type":"thread.started","thread_id":"stable-context-001"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"initial complete"}}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResumeFailureKeepsOriginalContextAndDoesNotCreateReplacement(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fake provider")
	}
	workspace := t.TempDir()
	provider := adapter.NewCodexAdapter(adapter.CodexConfig{Enabled: true})
	registry, err := adapter.NewConfiguredRegistry(true, providerFixtureCatalog{metadata: map[string]adapter.ProviderMetadata{
		adapter.CodexProviderID: {ID: adapter.CodexProviderID, Version: "9.0.0", Executable: fakeResumeFailureExecutable(t), Enabled: true},
	}}, nil, provider)
	if err != nil {
		t.Fatal(err)
	}
	executionQueue := queue.NewMemoryQueue(nil)
	initialTask, _ := enqueueProviderTask(t, executionQueue, adapter.CodexProviderID, workspace)
	repository := newFakeRunRepository(executionQueue)
	storage, err := NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRunService(repository, registry, NewProcessExecutor(), WithArtifactStorage(storage))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := queue.NewWorker(executionQueue, service, "resume-failure-worker", queue.WithWorkerClaimLimit(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner := domain.Owner{Kind: initialTask.PrincipalKind, ID: initialTask.PrincipalID}
	initialResult, err := repository.GetTask(context.Background(), owner, initialTask.ID)
	if err != nil || initialResult.RuntimeContextID == nil {
		t.Fatalf("initial result=%+v err=%v", initialResult, err)
	}
	originalContextID := *initialResult.RuntimeContextID

	continuation, err := application.NewContinuationService(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := continuation.Continue(context.Background(), application.ContinueTaskCommand{
		Owner: owner, ProjectID: initialTask.ProjectID, TaskID: initialTask.ID,
		ProviderID: adapter.CodexProviderID, WorkspacePath: workspace,
		PromptDelta: "resume must fail", IdempotencyKey: "resume-failure-key", MaxAttempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := worker.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	finalTask, err := repository.GetTask(context.Background(), owner, initialTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalContext, err := repository.GetRuntimeContext(context.Background(), owner, originalContextID)
	if err != nil {
		t.Fatal(err)
	}
	if finalTask.Status != domain.TaskFailed || finalTask.RuntimeContextID == nil || *finalTask.RuntimeContextID != originalContextID {
		t.Fatalf("final Task=%+v", finalTask)
	}
	if finalContext.Status != domain.ContextActive || finalContext.ProviderContextID != "stable-context-001" {
		t.Fatalf("final Context=%+v", finalContext)
	}
	repository.mu.Lock()
	contextCount := len(repository.contexts)
	var resumeRun domain.RunSnapshot
	for _, run := range repository.runs {
		if run.RuntimeContextID != nil {
			resumeRun = run
		}
	}
	repository.mu.Unlock()
	if contextCount != 1 {
		t.Fatalf("resume failure created replacement contexts: %d", contextCount)
	}
	if resumeRun.Status != domain.RunFailed || resumeRun.ErrorCode == nil || *resumeRun.ErrorCode != string(domain.ErrorResumeFailed) {
		t.Fatalf("resume Run=%+v", resumeRun)
	}
}
