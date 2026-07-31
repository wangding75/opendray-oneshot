package application

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
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
	repository := queue.NewMemoryQueue(nil)
	service := NewDispatchService(repository)
	fixed := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixed }
	command := baseCommand()

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
	repository := queue.NewMemoryQueue(nil)
	service := NewDispatchService(repository)
	command := baseCommand()
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
	service := NewDispatchService(queue.NewMemoryQueue(nil))
	command := baseCommand()
	command.IdempotencyKey = ""
	_, err := service.CreateTask(context.Background(), command)
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
