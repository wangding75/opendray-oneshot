package application

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/workspacepolicy"
)

func baseCommand() CreateTaskCommand {
	return CreateTaskCommand{
		Owner:      domain.Owner{Kind: domain.PrincipalAdmin, ID: "dispatch-owner"},
		ProjectID:  "dispatch-project",
		ProviderID: "dispatch-provider",
		Source:     domain.Source{Kind: domain.SourceAPI, ClientRequestID: "request-1"},
		Prompt:     "implement the next task",
		Input: domain.DeliveryInput{
			AttachmentRefs: []string{"artifact://requirements"},
			Options:        map[string]any{"reasoning": "high", "sandbox": true},
		},
		IdempotencyKey: "dispatch-key",
		MaxAttempts:    3,
	}
}

func TestDispatchServiceIdempotentReplayAndConflict(t *testing.T) {
	root := t.TempDir()
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	repository := queue.NewMemoryQueue(nil)
	service := NewDispatchService(repository, WithWorkspacePolicy(policy, root))
	fixed := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixed }
	command := baseCommand()
	command.WorkspacePath = root

	first, err := service.CreateTask(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || second.Created {
		t.Fatalf("created flags=%v/%v", first.Created, second.Created)
	}
	if first.Task.ID != second.Task.ID || first.Delivery.ID != second.Delivery.ID {
		t.Fatalf("replay returned different resources: %+v %+v", first, second)
	}

	command.Prompt = "different immutable payload"
	_, err = service.CreateTask(context.Background(), command)
	if !domain.HasCode(err, domain.ErrorIdempotencyConflict) {
		t.Fatalf("payload conflict err=%v", err)
	}
}

func TestDispatchServiceTelegramDerivesStableKey(t *testing.T) {
	root := t.TempDir()
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	repository := queue.NewMemoryQueue(nil)
	service := NewDispatchService(repository, WithWorkspacePolicy(policy, root))
	command := baseCommand()
	command.WorkspacePath = root
	command.Source = domain.Source{
		Kind: domain.SourceTelegram, ChannelID: "telegram-main", SourceMessageID: "update-100",
	}
	command.IdempotencyKey = ""

	first, err := service.CreateTask(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Task.ID != second.Task.ID || second.Created {
		t.Fatalf("Telegram replay=%+v %+v", first, second)
	}

	command.Prompt = "mutated duplicate update"
	_, err = service.CreateTask(context.Background(), command)
	if !domain.HasCode(err, domain.ErrorIdempotencyConflict) {
		t.Fatalf("Telegram payload conflict err=%v", err)
	}
}

func TestDispatchServiceRequiresAPIIdempotencyKey(t *testing.T) {
	root := t.TempDir()
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	service := NewDispatchService(queue.NewMemoryQueue(nil), WithWorkspacePolicy(policy, root))
	command := baseCommand()
	command.WorkspacePath = root
	command.IdempotencyKey = ""
	_, err = service.CreateTask(context.Background(), command)
	if !domain.HasCode(err, domain.ErrorIdempotencyRequired) {
		t.Fatalf("missing key err=%v", err)
	}
}

func TestCanonicalCreatePayloadSHA256IsStableAndAttachmentBound(t *testing.T) {
	first := baseCommand()
	second := baseCommand()
	second.Input.Options = map[string]any{"sandbox": true, "reasoning": "high"}

	firstHash, err := CanonicalCreatePayloadSHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := CanonicalCreatePayloadSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash || len(firstHash) != 64 {
		t.Fatalf("canonical hashes=%q/%q", firstHash, secondHash)
	}
	second.Input.AttachmentRefs = append(second.Input.AttachmentRefs, "artifact://extra")
	changed, err := CanonicalCreatePayloadSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if changed == firstHash {
		t.Fatal("attachment references were not bound into payload hash")
	}
	if strings.ToLower(changed) != changed {
		t.Fatalf("hash is not lowercase: %s", changed)
	}
}

func TestDispatchServiceCanonicalizesWorkspaceBeforePersistence(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	repository := queue.NewMemoryQueue(nil)
	service := NewDispatchService(repository, WithWorkspacePolicy(policy, root))
	command := baseCommand()
	command.WorkspacePath = filepath.Join(child, ".")
	result, err := service.CreateTask(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Delivery.Input.Options["workspace_path"]; got != filepath.Clean(child) {
		t.Fatalf("workspace_path=%v", got)
	}
}

func TestDispatchServiceRejectsWorkspaceOutsideAllowedRootsWithoutConsumingIdempotencyKey(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	repository := queue.NewMemoryQueue(nil)
	service := NewDispatchService(repository, WithWorkspacePolicy(policy, root))
	command := baseCommand()
	command.WorkspacePath = outside
	if _, err := service.CreateTask(context.Background(), command); err == nil {
		t.Fatal("outside workspace accepted")
	}
	command.WorkspacePath = root
	result, err := service.CreateTask(context.Background(), command)
	if err != nil {
		t.Fatalf("valid replay after invalid request should succeed: %v", err)
	}
	if !result.Created {
		t.Fatal("valid request was blocked by prior invalid request")
	}
}

func TestDispatchServiceRequiresConfiguredWorkspacePolicy(t *testing.T) {
	repository := queue.NewMemoryQueue(nil)
	policy, err := workspacepolicy.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewDispatchService(repository, WithWorkspacePolicy(policy, ""))
	command := baseCommand()
	command.WorkspacePath = t.TempDir()
	if _, err := service.CreateTask(context.Background(), command); !domain.HasCode(err, domain.ErrorInvalidRequest) {
		t.Fatalf("err=%v", err)
	}
}
