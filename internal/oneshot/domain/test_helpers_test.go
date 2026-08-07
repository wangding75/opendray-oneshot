package domain

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)

func testOwner() Owner { return Owner{Kind: PrincipalAdmin, ID: "operator"} }

func testSource() Source {
	return Source{
		Kind:            SourceTelegram,
		ChannelID:       "telegram-main",
		SourceMessageID: "msg-1",
		ReplyAddress: &ReplyAddress{
			ChannelID:      "telegram-main",
			ConversationID: "chat-1",
			MessageID:      "msg-1",
			Metadata:       map[string]string{"locale": "zh-CN"},
		},
		Metadata: map[string]string{"client": "test"},
	}
}

func mustTask(t *testing.T) *Task {
	t.Helper()
	task, err := NewTask(TaskArgs{
		Owner:      testOwner(),
		ProjectID:  "prj_demo",
		ProviderID: "codex",
		Model:      "default-model",
		Source:     testSource(),
		Prompt:     "Fix the focused test.",
	}, testNow)
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	return task
}

func mustDelivery(t *testing.T, task TaskSnapshot, operation DeliveryOperation, at time.Time) *Delivery {
	t.Helper()
	input := DeliveryInput{Options: map[string]any{"timeout_seconds": 60}}
	if operation == DeliveryContinue {
		input.PromptDelta = "Continue with the next step."
	}
	delivery, err := NewDelivery(DeliveryArgs{
		TaskID:         task.ID,
		Operation:      operation,
		RequestedBy:    Owner{Kind: task.PrincipalKind, ID: task.PrincipalID},
		Input:          input,
		IdempotencyKey: "idem-" + operation.String(),
		PayloadSHA256:  strings.Repeat("a", 64),
		MaxAttempts:    3,
		AvailableAt:    at,
	}, at)
	if err != nil {
		t.Fatalf("NewDelivery: %v", err)
	}
	return delivery
}

func mustContext(t *testing.T, owner Owner, projectID, providerID string, at time.Time) *RuntimeContext {
	t.Helper()
	workspacePath := "/srv/opendray/workspaces/demo"
	if filepath.Separator == '\\' {
		workspacePath = `C:\srv\opendray\workspaces\demo`
	}
	context, err := NewRuntimeContext(RuntimeContextArgs{
		Owner:             owner,
		ProjectID:         projectID,
		ProviderID:        providerID,
		ProviderContextID: "provider-context-1",
		WorkspacePath:     workspacePath,
	}, at)
	if err != nil {
		t.Fatalf("NewRuntimeContext: %v", err)
	}
	return context
}

func mustReservedDelivery(t *testing.T, task TaskSnapshot, operation DeliveryOperation, at time.Time) *Delivery {
	t.Helper()
	delivery := mustDelivery(t, task, operation, at)
	if err := delivery.Reserve("worker-1", at.Add(time.Minute), at); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return delivery
}

func mustRun(t *testing.T, task TaskSnapshot, delivery *Delivery, context *RuntimeContextSnapshot, at time.Time) *Run {
	t.Helper()
	run, err := NewRun(task, delivery.Snapshot(), context, at)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if err := delivery.AttachRun(run.Snapshot().ID, at); err != nil {
		t.Fatalf("AttachRun: %v", err)
	}
	return run
}

func requireCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", code)
	}
	if !HasCode(err, code) {
		actual, _ := CodeOf(err)
		t.Fatalf("expected %s, got %s: %v", code, actual, err)
	}
}
