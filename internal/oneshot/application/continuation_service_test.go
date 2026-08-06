package application

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

type continuationReplay struct {
	payloadSHA string
	task       domain.TaskSnapshot
	delivery   domain.DeliverySnapshot
}

type continuationMemoryRepository struct {
	mu         sync.Mutex
	task       domain.TaskSnapshot
	context    domain.RuntimeContextSnapshot
	deliveries []domain.DeliverySnapshot
	replays    map[string]continuationReplay
}

func (r *continuationMemoryRepository) GetTask(_ context.Context, owner domain.Owner, id string) (domain.TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.task.ID != id || r.task.PrincipalKind != owner.Kind || r.task.PrincipalID != owner.ID {
		return domain.TaskSnapshot{}, domain.NewDomainError(domain.ErrorTaskNotFound, "Task not found", nil)
	}
	return r.task, nil
}

func (r *continuationMemoryRepository) GetRuntimeContext(_ context.Context, owner domain.Owner, id string) (domain.RuntimeContextSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.context.ID != id || r.context.PrincipalKind != owner.Kind || r.context.PrincipalID != owner.ID {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextNotFound, "RuntimeContext not found", nil)
	}
	return r.context, nil
}

func (r *continuationMemoryRepository) FindContinueReplay(_ context.Context, owner domain.Owner, canonicalPath, idempotencyKey, payloadSHA string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replays == nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, nil
	}
	replay, ok := r.replays[string(owner.Kind)+"\x00"+owner.ID+"\x00"+canonicalPath+"\x00"+idempotencyKey]
	if !ok {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, nil
	}
	if replay.payloadSHA != payloadSHA {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorIdempotencyConflict, "Idempotency-Key payload conflict", nil)
	}
	return replay.task, replay.delivery, true, nil
}

func (r *continuationMemoryRepository) CreateContinueDelivery(_ context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedVersion int64, delivery domain.DeliverySnapshot, canonicalPath string, _ *time.Time) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replays == nil {
		r.replays = make(map[string]continuationReplay)
	}
	scope := string(owner.Kind) + "\x00" + owner.ID + "\x00" + canonicalPath + "\x00" + delivery.IdempotencyKey
	if replay, ok := r.replays[scope]; ok {
		if replay.payloadSHA != delivery.PayloadSHA256 {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorIdempotencyConflict, "Idempotency-Key payload conflict", nil)
		}
		return replay.task, replay.delivery, false, nil
	}
	if r.task.PrincipalKind != owner.Kind || r.task.PrincipalID != owner.ID || r.task.Version != expectedVersion {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorRunConflict, "Task version conflict", nil)
	}
	if task.Version != expectedVersion+1 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.InvalidRequestf("updated Task version mismatch")
	}
	r.task = task
	r.deliveries = append(r.deliveries, delivery)
	r.replays[scope] = continuationReplay{payloadSHA: delivery.PayloadSHA256, task: task, delivery: delivery}
	return task, delivery, true, nil
}

type nonResumeAdapter struct{}

func (nonResumeAdapter) ProviderID() string             { return "non-resume" }
func (nonResumeAdapter) AdapterVersion() string         { return "1.0.0" }
func (nonResumeAdapter) MinimumProviderVersion() string { return "0.0.0" }
func (nonResumeAdapter) Enabled() bool                  { return true }
func (nonResumeAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{SupportsNonInteractive: true, Cancellation: true}
}
func (nonResumeAdapter) BuildCommand(context.Context, adapter.ExecutionInput) (adapter.CommandSpec, error) {
	return adapter.CommandSpec{}, nil
}
func (nonResumeAdapter) NormalizeOutput(context.Context, adapter.OutputChunk) ([]adapter.NormalizedOutputEvent, error) {
	return nil, nil
}

func completedContinuationFixture(t *testing.T, providerID string) (*continuationMemoryRepository, ContinueTaskCommand) {
	t.Helper()
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "owner-1"}
	tmpDir := filepath.Clean(t.TempDir())
	contextAggregate, err := domain.NewRuntimeContext(domain.RuntimeContextArgs{
		Owner: owner, ProjectID: "project-1", ProviderID: providerID,
		ProviderContextID: "provider-context-1", WorkspacePath: tmpDir,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	contextSnapshot := contextAggregate.Snapshot()
	task, err := domain.NewTask(domain.TaskArgs{
		Owner: owner, ProjectID: "project-1", ProviderID: providerID,
		Source: domain.Source{Kind: domain.SourceAPI}, Prompt: "initial prompt",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	taskSnapshot := task.Snapshot()
	taskSnapshot.Status = domain.TaskCompleted
	taskSnapshot.RuntimeContextID = &contextSnapshot.ID
	taskSnapshot.UpdatedAt = now.Add(time.Minute)
	restored, err := domain.RestoreTask(taskSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	repository := &continuationMemoryRepository{task: restored.Snapshot(), context: contextSnapshot, replays: make(map[string]continuationReplay)}
	command := ContinueTaskCommand{
		Owner: owner, ProjectID: "project-1", TaskID: taskSnapshot.ID,
		ProviderID: providerID, WorkspacePath: tmpDir,
		PromptDelta: "continue from the previous result", IdempotencyKey: "continue-key", MaxAttempts: 3,
	}
	return repository, command
}

func TestContinuationServiceCreatesNewContinueDeliveryForCodexAndClaude(t *testing.T) {
	for _, providerID := range []string{adapter.CodexProviderID, adapter.ClaudeProviderID} {
		t.Run(providerID, func(t *testing.T) {
			repository, command := completedContinuationFixture(t, providerID)
			registry := adapter.NewRegistry(
				adapter.NewCodexAdapter(adapter.CodexConfig{Enabled: true}),
				adapter.NewClaudeAdapter(adapter.ClaudeConfig{Enabled: true}),
			)
			service, err := NewContinuationService(repository, registry)
			if err != nil {
				t.Fatal(err)
			}
			service.now = func() time.Time { return time.Date(2026, 7, 28, 9, 5, 0, 0, time.UTC) }
			result, err := service.Continue(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Created || result.Task.Status != domain.TaskQueued || result.Delivery.Operation != domain.DeliveryContinue {
				t.Fatalf("result=%+v", result)
			}
			if result.Delivery.Input.PromptDelta != command.PromptDelta || result.Delivery.Input.Options["workspace_path"] != command.WorkspacePath {
				t.Fatalf("delivery input=%+v", result.Delivery.Input)
			}
			if result.Task.RuntimeContextID == nil || *result.Task.RuntimeContextID != repository.context.ID {
				t.Fatalf("runtime context lost: %+v", result.Task)
			}
		})
	}
}

func TestContinuationServiceRejectsUnsupportedProviderAndMissingIdempotency(t *testing.T) {
	repository, command := completedContinuationFixture(t, "non-resume")
	service, err := NewContinuationService(repository, adapter.NewRegistry(nonResumeAdapter{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Continue(context.Background(), command); !domain.HasCode(err, domain.ErrorResumeUnsupported) {
		t.Fatalf("unsupported resume err=%v", err)
	}

	repository, command = completedContinuationFixture(t, adapter.CodexProviderID)
	service, err = NewContinuationService(repository, adapter.NewRegistry(adapter.NewCodexAdapter(adapter.CodexConfig{Enabled: true})))
	if err != nil {
		t.Fatal(err)
	}
	command.IdempotencyKey = ""
	if _, err := service.Continue(context.Background(), command); !domain.HasCode(err, domain.ErrorIdempotencyRequired) {
		t.Fatalf("missing idempotency err=%v", err)
	}
}

func TestContinuationServiceEnforcesExactOwnerProjectProviderWorkspaceAndActiveContext(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*continuationMemoryRepository, *ContinueTaskCommand)
		code   domain.ErrorCode
	}{
		{name: "owner", mutate: func(_ *continuationMemoryRepository, command *ContinueTaskCommand) { command.Owner.ID = "other" }, code: domain.ErrorTaskNotFound},
		{name: "project", mutate: func(_ *continuationMemoryRepository, command *ContinueTaskCommand) { command.ProjectID = "other" }, code: domain.ErrorForbidden},
		{name: "provider", mutate: func(_ *continuationMemoryRepository, command *ContinueTaskCommand) {
			command.ProviderID = adapter.ClaudeProviderID
		}, code: domain.ErrorContextOwnerMismatch},
		{name: "workspace", mutate: func(_ *continuationMemoryRepository, command *ContinueTaskCommand) {
			command.WorkspacePath = filepath.Join(filepath.Dir(command.WorkspacePath), "other")
		}, code: domain.ErrorContextOwnerMismatch},
		{name: "busy-context", mutate: func(repository *continuationMemoryRepository, _ *ContinueTaskCommand) {
			repository.context.Status = domain.ContextBusy
		}, code: domain.ErrorRunConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, command := completedContinuationFixture(t, adapter.CodexProviderID)
			test.mutate(repository, &command)
			service, err := NewContinuationService(repository, adapter.NewRegistry(
				adapter.NewCodexAdapter(adapter.CodexConfig{Enabled: true}),
				adapter.NewClaudeAdapter(adapter.ClaudeConfig{Enabled: true}),
			))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Continue(context.Background(), command); !domain.HasCode(err, test.code) {
				t.Fatalf("err=%v want=%s", err, test.code)
			}
		})
	}
}

func TestContinuationServiceIdempotentReplayAndPayloadConflict(t *testing.T) {
	repository, command := completedContinuationFixture(t, adapter.CodexProviderID)
	service, err := NewContinuationService(repository, adapter.NewRegistry(adapter.NewCodexAdapter(adapter.CodexConfig{Enabled: true})))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Continue(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Continue(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || second.Created || first.Task.ID != second.Task.ID || first.Delivery.ID != second.Delivery.ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	command.PromptDelta = "different follow-up"
	if _, err := service.Continue(context.Background(), command); !domain.HasCode(err, domain.ErrorIdempotencyConflict) {
		t.Fatalf("payload conflict err=%v", err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.deliveries) != 1 {
		t.Fatalf("deliveries=%d", len(repository.deliveries))
	}
}

func TestContinuationServiceConcurrentSameKeyReplaysOneQueuedCycle(t *testing.T) {
	repository, command := completedContinuationFixture(t, adapter.CodexProviderID)
	service, err := NewContinuationService(repository, adapter.NewRegistry(adapter.NewCodexAdapter(adapter.CodexConfig{Enabled: true})))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan ContinueTaskResult, 2)
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			result, err := service.Continue(context.Background(), command)
			results <- result
			errors <- err
		}()
	}
	close(start)
	var firstDelivery string
	var created int
	for i := 0; i < 2; i++ {
		result := <-results
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if result.Created {
			created++
		}
		if firstDelivery == "" {
			firstDelivery = result.Delivery.ID
		} else if result.Delivery.ID != firstDelivery {
			t.Fatalf("replay returned different Delivery: %s vs %s", firstDelivery, result.Delivery.ID)
		}
	}
	if created != 1 {
		t.Fatalf("created count=%d", created)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.deliveries) != 1 {
		t.Fatalf("deliveries=%d", len(repository.deliveries))
	}
}

func TestContinuationServiceConcurrentDistinctKeysAllowOnlyOneQueuedCycle(t *testing.T) {
	repository, command := completedContinuationFixture(t, adapter.CodexProviderID)
	service, err := NewContinuationService(repository, adapter.NewRegistry(adapter.NewCodexAdapter(adapter.CodexConfig{Enabled: true})))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		commandCopy := command
		commandCopy.IdempotencyKey = command.IdempotencyKey + string(rune('a'+i))
		go func(command ContinueTaskCommand) {
			<-start
			_, err := service.Continue(context.Background(), command)
			errors <- err
		}(commandCopy)
	}
	close(start)
	var success, failure int
	for i := 0; i < 2; i++ {
		if err := <-errors; err == nil {
			success++
		} else if domain.HasCode(err, domain.ErrorRunConflict) || domain.HasCode(err, domain.ErrorInvalidTransition) {
			failure++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 || failure != 1 {
		t.Fatalf("success=%d failure=%d", success, failure)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.deliveries) != 1 {
		t.Fatalf("deliveries=%d", len(repository.deliveries))
	}
}
