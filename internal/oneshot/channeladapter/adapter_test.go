package channeladapter

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/oneshot/application"
	attachmentservice "github.com/opendray/opendray-v2/internal/oneshot/attachment"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
	"github.com/opendray/opendray-v2/internal/oneshot/workspacepolicy"
)

type adapterRepoFixture struct {
	binding       *store.ChannelBinding
	resolvedReply string
	task          domain.TaskSnapshot
	runtime       domain.RuntimeContextSnapshot
}

func (r *adapterRepoFixture) GetTask(_ context.Context, _ domain.Owner, taskID string) (domain.TaskSnapshot, error) {
	if r.task.ID != "" && r.task.ID == taskID {
		return r.task, nil
	}
	return domain.TaskSnapshot{}, domain.NewDomainError(domain.ErrorTaskNotFound, "not found", nil)
}
func (r *adapterRepoFixture) ListTasksFiltered(context.Context, domain.Owner, store.TaskListFilter) (store.Page[domain.TaskSnapshot], error) {
	return store.Page[domain.TaskSnapshot]{}, nil
}
func (r *adapterRepoFixture) GetRuntimeContext(_ context.Context, _ domain.Owner, contextID string) (domain.RuntimeContextSnapshot, error) {
	if r.runtime.ID != "" && r.runtime.ID == contextID {
		return r.runtime, nil
	}
	return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextNotFound, "not found", nil)
}
func (r *adapterRepoFixture) UpsertChannelBinding(_ context.Context, value store.ChannelBinding) (store.ChannelBinding, error) {
	r.binding = &value
	return value, nil
}
func (r *adapterRepoFixture) ResolveChannelBinding(_ context.Context, owner domain.Owner, channelID, conversationID, threadID, sourceMessageID string, _ time.Time) (store.ChannelBinding, error) {
	r.resolvedReply = sourceMessageID
	if r.binding == nil || r.binding.Owner != owner || r.binding.ChannelID != channelID || r.binding.ConversationID != conversationID || r.binding.ThreadID != threadID || r.binding.SourceMessageID == nil || *r.binding.SourceMessageID != sourceMessageID {
		return store.ChannelBinding{}, domain.NewDomainError(domain.ErrorTaskNotFound, "not found", nil)
	}
	return *r.binding, nil
}

type telegramChannelFixture struct{}

func (telegramChannelFixture) Kind() string                                       { return "telegram" }
func (telegramChannelFixture) ID() string                                         { return "telegram-main" }
func (telegramChannelFixture) Start(context.Context, channel.InboundFunc) error   { return nil }
func (telegramChannelFixture) Stop(context.Context) error                         { return nil }
func (telegramChannelFixture) Send(context.Context, channel.ChannelMessage) error { return nil }

func TestOrdinaryTelegramTextWithoutOneShotBindingFallsThroughToPTY(t *testing.T) {
	adapter, err := New(Config{Enabled: true}, nil, nil, nil, &adapterRepoFixture{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{
		Channel: telegramChannelFixture{}, Message: channel.ChannelMessage{ChannelID: "telegram-main", ConversationID: "chat-1", Author: "user-1", Text: "plain text"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != channel.DispatchNotHandled {
		t.Fatalf("ordinary text was claimed by One-shot: %s", result.Status)
	}
}

func TestSlashCommandsRemainOwnedByCommandRegistry(t *testing.T) {
	adapter, err := New(Config{Enabled: true}, nil, nil, nil, &adapterRepoFixture{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{
		Channel: telegramChannelFixture{}, Message: channel.ChannelMessage{ChannelID: "telegram-main", ConversationID: "chat-1", Author: "user-1", Text: "/run test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != channel.DispatchNotHandled {
		t.Fatalf("command bypassed command registry: %s", result.Status)
	}
}

func TestTelegramOwnerUsesStableNumericUserID(t *testing.T) {
	owner, err := ownerFor(channel.ChannelMessage{Author: "@mutable", ConversationID: "chat-1", Metadata: map[string]any{"tg_user_id": "10001"}})
	if err != nil {
		t.Fatal(err)
	}
	if owner.Kind != domain.PrincipalAdmin || owner.ID != "10001" {
		t.Fatalf("unstable Telegram owner identity: %+v", owner)
	}
}

func TestOnlyExactOneShotNotificationReplyIsClaimed(t *testing.T) {
	replyID := "telegram-out-42"
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "10001"}
	repo := &adapterRepoFixture{binding: &store.ChannelBinding{
		Owner: owner, ChannelID: "telegram-main", ConversationID: "chat-1", ThreadID: "thread-1",
		SourceMessageID: &replyID, TaskID: "task-1", Kind: "notification",
	}}
	adapter, err := New(Config{Enabled: true}, nil, nil, nil, repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := channel.ChannelMessage{
		ChannelID: "telegram-main", ConversationID: "chat-1", ThreadID: "thread-1", Author: "@mutable", Text: "follow up",
		Metadata: map[string]any{"tg_user_id": "10001", "reply_to_outbound_msg_id": "pty-out-7"},
	}
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{Channel: telegramChannelFixture{}, Message: base})
	if err != nil || result.Status != channel.DispatchNotHandled {
		t.Fatalf("PTY reply was intercepted by One-shot: result=%+v err=%v", result, err)
	}
	base.Metadata["reply_to_outbound_msg_id"] = replyID
	result, err = adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{Channel: telegramChannelFixture{}, Message: base})
	if result.Status != channel.DispatchHandled || !domain.HasCode(err, domain.ErrorDisabled) {
		t.Fatalf("exact One-shot notification reply was not claimed: result=%+v err=%v", result, err)
	}
	if repo.resolvedReply != replyID {
		t.Fatalf("reply binding lookup did not use exact outbound message id: %q", repo.resolvedReply)
	}
}

type attachmentStagerFixture struct {
	request attachmentservice.StageRequest
	calls   int
}

func (s *attachmentStagerFixture) Stage(_ context.Context, request attachmentservice.StageRequest) (domain.StagedAttachmentSnapshot, error) {
	s.request = request
	s.calls++
	return domain.StagedAttachmentSnapshot{ID: "oat_test123", PrincipalKind: request.Owner.Kind, PrincipalID: request.Owner.ID, ProjectID: request.ProjectID, SourceKind: request.SourceKind, SourceRef: request.SourceRef, Name: request.Name, DetectedMIME: "text/plain", SizeBytes: 5, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StorageKey: "attachments/te/oat_test123/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: domain.StagedAttachmentReady, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, nil
}

type attachmentTelegramFixture struct{ telegramChannelFixture }

func (attachmentTelegramFixture) OpenAttachment(context.Context, channel.Attachment) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("hello")), nil
}

type providerFixture struct{ attach bool }

func (p providerFixture) ListCapabilities(context.Context) ([]ProviderOption, error) {
	return []ProviderOption{{ID: "future", Enabled: true, CanAttach: p.attach}}, nil
}

func TestTelegramAttachmentsAreStagedBeforeDeliveryReference(t *testing.T) {
	repo := &adapterRepoFixture{}
	stager := &attachmentStagerFixture{}
	adapter, err := New(Config{Enabled: true}, nil, nil, nil, repo, providerFixture{attach: true}, nil, WithAttachmentStager(stager))
	if err != nil {
		t.Fatal(err)
	}
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "10001"}
	refs, err := adapter.stageAttachmentRefs(context.Background(), attachmentTelegramFixture{}, owner, "project-1", channel.ChannelMessage{Attachments: []channel.Attachment{{ID: "tg-file-1", Name: "notes.txt", MIMEType: "text/plain"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != "oat_test123" || stager.request.SourceRef != "tg-file-1" || stager.request.SourceKind != domain.SourceTelegram {
		t.Fatalf("unexpected staged refs=%v request=%+v", refs, stager.request)
	}
}

func TestProviderWithoutOneShotAttachmentCapabilityIsRejected(t *testing.T) {
	adapter, err := New(Config{Enabled: true}, nil, nil, nil, &adapterRepoFixture{}, providerFixture{attach: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.validateAttachmentCapability(context.Background(), "future", 1); err == nil {
		t.Fatal("attachment capability bypassed")
	}
}

type continuationFixture struct {
	result application.ContinueTaskResult
}

func (f continuationFixture) Continue(context.Context, application.ContinueTaskCommand) (application.ContinueTaskResult, error) {
	return f.result, nil
}

type failingSendTelegram struct{ telegramChannelFixture }

func (failingSendTelegram) Send(context.Context, channel.ChannelMessage) error {
	return errors.New("telegram unavailable")
}

func TestContinuationAcknowledgementSendFailureIsReturned(t *testing.T) {
	replyID := "telegram-out-99"
	contextID := "orc_01J00000000000000000000000"
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "10001"}
	task := domain.TaskSnapshot{
		ID: "otk_01J00000000000000000000000", PrincipalKind: owner.Kind, PrincipalID: owner.ID,
		ProjectID: "project-1", ProviderID: "future", Status: domain.TaskWaitingInput,
		RuntimeContextID: &contextID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Version: 1,
	}
	runtime := domain.RuntimeContextSnapshot{
		ID: contextID, PrincipalKind: owner.Kind, PrincipalID: owner.ID, ProjectID: task.ProjectID,
		ProviderID: task.ProviderID, ProviderContextID: "provider-context", WorkspacePath: "/tmp/workspace",
		Status: domain.ContextActive, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	delivery := domain.DeliverySnapshot{ID: "odl_01J00000000000000000000000", TaskID: task.ID}
	repo := &adapterRepoFixture{
		task: task, runtime: runtime,
		binding: &store.ChannelBinding{Owner: owner, ChannelID: "telegram-main", ConversationID: "chat-1", ThreadID: "thread-1", SourceMessageID: &replyID, TaskID: task.ID, Kind: "notification"},
	}
	adapter, err := New(Config{Enabled: true}, nil, continuationFixture{result: application.ContinueTaskResult{Task: task, Delivery: delivery, Created: true}}, nil, repo, providerFixture{attach: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Dispatch(context.Background(), channel.InboundDispatchRequest{
		Channel: failingSendTelegram{},
		Message: channel.ChannelMessage{ChannelID: "telegram-main", ConversationID: "chat-1", ThreadID: "thread-1", Author: "@mutable", Text: "continue please", SourceMessageID: "incoming-1", Metadata: map[string]any{"tg_user_id": "10001", "reply_to_outbound_msg_id": replyID}},
	})
	if result.Status != channel.DispatchHandled || err == nil || !strings.Contains(err.Error(), "continuation acknowledgement") {
		t.Fatalf("send failure was hidden: result=%+v err=%v", result, err)
	}
}

func TestRunCommandRejectsWorkspaceOutsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	creator := application.NewDispatchService(queue.NewMemoryQueue(nil), application.WithWorkspacePolicy(policy, root))
	adapter, err := New(Config{Enabled: true, DefaultProjectID: "project-1", DefaultProvider: "future", DefaultWorkspace: root}, creator, nil, nil, &adapterRepoFixture{}, providerFixture{attach: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	card, err := adapter.runCommand(context.Background(), channel.CommandContext{
		Channel: telegramChannelFixture{},
		Args:    []string{"--workspace", filepath.Join(t.TempDir(), "outside"), "--", "hello"},
		Message: channel.ChannelMessage{ChannelID: "telegram-main", ConversationID: "chat-1", Author: "10001", SourceMessageID: "msg-1", Metadata: map[string]any{"tg_user_id": "10001"}},
	})
	if err == nil || card != nil {
		t.Fatalf("outside workspace accepted: card=%+v err=%v", card, err)
	}
}

func TestRunCommandCreatesTaskWithinAllowedWorkspace(t *testing.T) {
	root := t.TempDir()
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	creator := application.NewDispatchService(queue.NewMemoryQueue(nil), application.WithWorkspacePolicy(policy, root))
	adapter, err := New(Config{Enabled: true, DefaultProjectID: "project-1", DefaultProvider: "future", DefaultWorkspace: root}, creator, nil, nil, &adapterRepoFixture{}, providerFixture{attach: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	card, err := adapter.runCommand(context.Background(), channel.CommandContext{
		Channel: telegramChannelFixture{},
		Args:    []string{"--workspace", root, "--", "hello"},
		Message: channel.ChannelMessage{ChannelID: "telegram-main", ConversationID: "chat-1", Author: "10001", SourceMessageID: "msg-2", Metadata: map[string]any{"tg_user_id": "10001"}},
	})
	if err != nil || card == nil {
		t.Fatalf("card=%+v err=%v", card, err)
	}
}
