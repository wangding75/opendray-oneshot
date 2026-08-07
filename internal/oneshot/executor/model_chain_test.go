package executor

import (
	"context"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
)

type mockModelChainAdapter struct {
	capturedModel string
}

func (a *mockModelChainAdapter) ProviderID() string             { return "mock-model-provider" }
func (a *mockModelChainAdapter) AdapterVersion() string         { return "1.0.0" }
func (a *mockModelChainAdapter) MinimumProviderVersion() string { return "1.0.0" }
func (a *mockModelChainAdapter) Enabled() bool                  { return true }
func (a *mockModelChainAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{SupportsNonInteractive: true}
}
func (a *mockModelChainAdapter) DefaultModel() string { return "default-mock-model" }

func (a *mockModelChainAdapter) BuildCommand(ctx context.Context, input adapter.ExecutionInput) (adapter.CommandSpec, error) {
	a.capturedModel = input.Run.Model
	return adapter.CommandSpec{}, domain.NewDomainError(domain.ErrorInvalidRequest, "verification: model="+input.Run.Model, nil)
}

func (a *mockModelChainAdapter) NormalizeOutput(ctx context.Context, chunk adapter.OutputChunk) ([]adapter.NormalizedOutputEvent, error) {
	return nil, nil
}

func TestModelSnapshotPreservedEndToEnd(t *testing.T) {
	now := time.Now().UTC()
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "test-owner"}

	// 1. Create task with a specific model
	task, err := domain.NewTask(domain.TaskArgs{
		Owner:      owner,
		ProjectID:  "test-project",
		ProviderID: "mock-model-provider",
		Model:      "frozen-model-xyz",
		Source:     domain.Source{Kind: domain.SourceAPI, ClientRequestID: "req-xyz"},
		Prompt:     "test prompt",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Create delivery
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{
		TaskID: task.Snapshot().ID, Operation: domain.DeliveryNew, RequestedBy: owner,
		Input: domain.DeliveryInput{AttachmentRefs: []string{}, Options: map[string]any{}},
		IdempotencyKey: "test-idempotency-key", PayloadSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		MaxAttempts: 1, AvailableAt: now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Queue and reserve delivery
	if err := task.QueueInitialDelivery(delivery.Snapshot(), now); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Reserve("test-worker", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}

	// 4. Construct claim (worker input)
	claim := queue.Claim{
		Task:     task.Snapshot(),
		Delivery: delivery.Snapshot(),
	}

	// 5. Initialize RunService with our mock adapter
	executionQueue := queue.NewMemoryQueue(nil)
	repository := newFakeRunRepository(executionQueue)
	mockAdapter := &mockModelChainAdapter{}
	registry := adapter.NewRegistry(mockAdapter)

	storage, err := NewFileArtifactStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	service, err := NewRunService(repository, registry, NewProcessExecutor(), WithArtifactStorage(storage))
	if err != nil {
		t.Fatal(err)
	}

	// 6. Process the claim
	outcome := service.Process(context.Background(), claim)

	// 7. Verify model propagation
	if mockAdapter.capturedModel != "frozen-model-xyz" {
		t.Fatalf("expected model 'frozen-model-xyz' to be captured, got %q", mockAdapter.capturedModel)
	}

	// Verify that the outcome action matches expected crash recovery (ActionRecover)
	if outcome.Action != queue.ActionRecover {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
}
