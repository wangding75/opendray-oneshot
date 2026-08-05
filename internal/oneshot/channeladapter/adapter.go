// Package channeladapter integrates explicit Telegram commands with the
// independent One-shot execution domain. Plain text remains a PTY fallback
// unless it replies inside an exact One-shot channel binding.
package channeladapter

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/oneshot/application"
	attachmentservice "github.com/opendray/opendray-v2/internal/oneshot/attachment"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
)

const InboundPriority = 100

type TaskCreator interface {
	CreateTask(context.Context, application.CreateTaskCommand) (application.CreateTaskResult, error)
}
type TaskContinuer interface {
	Continue(context.Context, application.ContinueTaskCommand) (application.ContinueTaskResult, error)
}
type TaskController interface {
	CancelTask(context.Context, application.CancelTaskCommand) (application.CancelTaskResult, error)
	RetryTask(context.Context, application.RetryTaskCommand) (application.RetryTaskResult, error)
}
type Repository interface {
	GetTask(context.Context, domain.Owner, string) (domain.TaskSnapshot, error)
	ListTasksFiltered(context.Context, domain.Owner, store.TaskListFilter) (store.Page[domain.TaskSnapshot], error)
	GetRuntimeContext(context.Context, domain.Owner, string) (domain.RuntimeContextSnapshot, error)
	UpsertChannelBinding(context.Context, store.ChannelBinding) (store.ChannelBinding, error)
	ResolveChannelBinding(context.Context, domain.Owner, string, string, string, string, time.Time) (store.ChannelBinding, error)
}
type ProviderCatalog interface {
	ListCapabilities(context.Context) ([]ProviderOption, error)
}
type ProviderOption struct {
	ID          string
	DisplayName string
	Enabled     bool
	CanResume   bool
	CanAttach   bool
}

type Config struct {
	Enabled          bool
	DefaultProjectID string
	DefaultProvider  string
	DefaultWorkspace string
	BindingTTL       time.Duration
}

type AttachmentStager interface {
	Stage(context.Context, attachmentservice.StageRequest) (domain.StagedAttachmentSnapshot, error)
}

type Option func(*Adapter)

func WithAttachmentStager(value AttachmentStager) Option {
	return func(adapter *Adapter) { adapter.attachments = value }
}

type Adapter struct {
	config      Config
	creator     TaskCreator
	continuer   TaskContinuer
	controller  TaskController
	repository  Repository
	providers   ProviderCatalog
	attachments AttachmentStager
	log         *slog.Logger
	now         func() time.Time
}

func New(config Config, creator TaskCreator, continuer TaskContinuer, controller TaskController, repository Repository, providers ProviderCatalog, log *slog.Logger, options ...Option) (*Adapter, error) {
	if repository == nil {
		return nil, domain.InvalidRequestf("One-shot channel repository is required")
	}
	if log == nil {
		log = slog.Default()
	}
	if config.BindingTTL <= 0 {
		config.BindingTTL = 30 * 24 * time.Hour
	}
	adapter := &Adapter{config: config, creator: creator, continuer: continuer, controller: controller, repository: repository, providers: providers, log: log.With("component", "oneshot-channel"), now: func() time.Time { return time.Now().UTC() }}
	for _, option := range options {
		if option != nil {
			option(adapter)
		}
	}
	return adapter, nil
}

func (a *Adapter) RegisterCommands(hub *channel.Hub) {
	if a == nil || hub == nil {
		return
	}
	hub.RegisterCommand(channel.Command{Name: "run", Description: "Run a non-interactive One-shot task", Source: "oneshot", CardHandler: a.runCommand})
	hub.RegisterCommand(channel.Command{Name: "tasks", Description: "List One-shot tasks", Source: "oneshot", CardHandler: a.tasksCommand})
	hub.RegisterCommand(channel.Command{Name: "task", Description: "Show one One-shot task", Source: "oneshot", CardHandler: a.taskCommand})
	hub.RegisterCommand(channel.Command{Name: "continue", Description: "Continue a resumable One-shot task", Source: "oneshot", CardHandler: a.continueCommand})
	hub.RegisterCommand(channel.Command{Name: "cancel", Description: "Cancel a One-shot task", Source: "oneshot", CardHandler: a.cancelCommand})
	hub.RegisterCommand(channel.Command{Name: "retry", Description: "Retry a failed One-shot task", Source: "oneshot", CardHandler: a.retryCommand})
}

// Dispatch claims only non-command Telegram replies in an exact One-shot binding.
// Returning not_handled preserves the existing PTY Session fallback.
func (a *Adapter) Dispatch(ctx context.Context, req channel.InboundDispatchRequest) (channel.InboundDispatchResult, error) {
	if a == nil || !a.config.Enabled || req.Channel == nil || req.Channel.Kind() != "telegram" {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	if _, _, ok := channel.ParseCommand(req.Message.Text); ok {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	replyTo := telegramReplyTo(req.Message)
	if replyTo == "" {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	owner, err := ownerFor(req.Message)
	if err != nil {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	binding, err := a.repository.ResolveChannelBinding(ctx, owner, req.Message.ChannelID, req.Message.ConversationID, req.Message.ThreadID, replyTo, a.now())
	if err != nil {
		if domain.HasCode(err, domain.ErrorTaskNotFound) {
			return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
		}
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, err
	}
	if binding.Kind != "task" && binding.Kind != "notification" && binding.Kind != "continue" {
		return channel.InboundDispatchResult{Status: channel.DispatchNotHandled}, nil
	}
	if a.continuer == nil {
		return channel.InboundDispatchResult{Status: channel.DispatchHandled}, domain.NewDomainError(domain.ErrorDisabled, "One-shot continuation is unavailable", nil)
	}
	task, runtime, err := a.continuationContext(ctx, owner, binding.TaskID)
	if err != nil {
		return channel.InboundDispatchResult{Status: channel.DispatchHandled}, err
	}
	if err := a.validateAttachmentCapability(ctx, task.ProviderID, len(req.Message.Attachments)); err != nil {
		return channel.InboundDispatchResult{Status: channel.DispatchHandled}, err
	}
	refs, err := a.stageAttachmentRefs(ctx, req.Channel, owner, task.ProjectID, req.Message)
	if err != nil {
		return channel.InboundDispatchResult{Status: channel.DispatchHandled}, err
	}
	result, err := a.continuer.Continue(ctx, application.ContinueTaskCommand{
		Owner: owner, ProjectID: task.ProjectID, TaskID: task.ID, ProviderID: task.ProviderID,
		WorkspacePath: runtime.WorkspacePath, PromptDelta: strings.TrimSpace(req.Message.Text),
		AttachmentRefs: refs, Options: map[string]any{"channel_reply": true},
		IdempotencyKey: telegramKey(req.Message),
	})
	if err != nil {
		return channel.InboundDispatchResult{Status: channel.DispatchHandled}, err
	}
	if err := a.bind(ctx, owner, req.Message, result.Task.ID, "continue"); err != nil {
		return channel.InboundDispatchResult{Status: channel.DispatchHandled}, err
	}
	a.rememberNotificationPreference(ctx, owner, result.Task.ProjectID, req.Message)
	a.log.InfoContext(ctx, "One-shot cross-device continuation accepted", "principal_kind", owner.Kind, "principal_id", owner.ID, "project_id", result.Task.ProjectID, "task_id", result.Task.ID, "source", "telegram_reply", "reply_channel_id", req.Message.ChannelID, "reply_conversation_id", req.Message.ConversationID)
	if sendErr := req.Channel.Send(ctx, channel.ChannelMessage{ChannelID: req.Message.ChannelID, ConversationID: req.Message.ConversationID, ThreadID: req.Message.ThreadID, Direction: channel.DirectionOutbound, Text: fmt.Sprintf("Continued %s (%s)", result.Task.ID, result.Delivery.ID), ReplyCtx: req.Message.ReplyCtx, Timestamp: a.now()}); sendErr != nil {
		return channel.InboundDispatchResult{Status: channel.DispatchHandled}, fmt.Errorf("send One-shot continuation acknowledgement: %w", sendErr)
	}
	return channel.InboundDispatchResult{Status: channel.DispatchHandled}, nil
}

type runArgs struct{ ProjectID, ProviderID, Workspace, Prompt string }

func (a *Adapter) runCommand(ctx context.Context, cc channel.CommandContext) (*channel.Card, error) {
	if !a.config.Enabled {
		return errorCard("One-shot disabled", "One-shot execution is disabled"), nil
	}
	if cc.Channel == nil || cc.Channel.Kind() != "telegram" {
		return errorCard("Unsupported channel", "/run is currently enabled only for Telegram"), nil
	}
	if a.creator == nil {
		return nil, domain.NewDomainError(domain.ErrorDisabled, "One-shot task creation is unavailable", nil)
	}
	parsed, err := a.parseRunArgs(ctx, cc.Args)
	if err != nil {
		return a.runHelp(ctx, err.Error()), nil
	}
	owner, err := ownerFor(cc.Message)
	if err != nil {
		return nil, err
	}
	source := sourceFor(cc.Message)
	if err := a.validateAttachmentCapability(ctx, parsed.ProviderID, len(cc.Message.Attachments)); err != nil {
		return nil, err
	}
	refs, err := a.stageAttachmentRefs(ctx, cc.Channel, owner, parsed.ProjectID, cc.Message)
	if err != nil {
		return nil, err
	}
	result, err := a.creator.CreateTask(ctx, application.CreateTaskCommand{
		Owner: owner, ProjectID: parsed.ProjectID, ProviderID: parsed.ProviderID,
		WorkspacePath: parsed.Workspace, Source: source, Prompt: parsed.Prompt,
		Input:          domain.DeliveryInput{AttachmentRefs: refs},
		IdempotencyKey: telegramKey(cc.Message),
	})
	if err != nil {
		return nil, err
	}
	if err := a.bind(ctx, owner, cc.Message, result.Task.ID, "task"); err != nil {
		return nil, err
	}
	a.rememberNotificationPreference(ctx, owner, result.Task.ProjectID, cc.Message)
	return taskCard(result.Task, fmt.Sprintf("Queued delivery %s", result.Delivery.ID)), nil
}

func (a *Adapter) tasksCommand(ctx context.Context, cc channel.CommandContext) (*channel.Card, error) {
	owner, err := ownerFor(cc.Message)
	if err != nil {
		return nil, err
	}
	projectID := a.config.DefaultProjectID
	if len(cc.Args) > 0 {
		projectID = strings.TrimSpace(cc.Args[0])
	}
	page, err := a.repository.ListTasksFiltered(ctx, owner, store.TaskListFilter{ProjectID: projectID, Page: store.PageRequest{Limit: 10}})
	if err != nil {
		return nil, err
	}
	elements := []channel.CardElement{}
	for _, task := range page.Items {
		elements = append(elements, channel.CardListItem{Text: fmt.Sprintf("%s · %s · %s", task.ID, task.Status, task.ProviderID), Button: channel.ButtonOption{Text: "Open", Value: "cmd:/task " + task.ID}})
	}
	if len(elements) == 0 {
		elements = append(elements, channel.CardMarkdown{Content: "No One-shot tasks found."})
	}
	return &channel.Card{Header: &channel.CardHeader{Title: "One-shot tasks"}, Elements: elements}, nil
}

func (a *Adapter) taskCommand(ctx context.Context, cc channel.CommandContext) (*channel.Card, error) {
	if len(cc.Args) < 1 {
		return errorCard("Usage", "/task <task_id>"), nil
	}
	owner, err := ownerFor(cc.Message)
	if err != nil {
		return nil, err
	}
	task, err := a.repository.GetTask(ctx, owner, cc.Args[0])
	if err != nil {
		return nil, err
	}
	if err := a.bind(ctx, owner, cc.Message, task.ID, "task"); err != nil {
		return nil, err
	}
	return taskCard(task, "Reply to a One-shot result notification to continue this task, or use /continue explicitly."), nil
}

func (a *Adapter) continueCommand(ctx context.Context, cc channel.CommandContext) (*channel.Card, error) {
	if len(cc.Args) < 2 {
		return errorCard("Usage", "/continue <task_id> <prompt>"), nil
	}
	if a.continuer == nil {
		return nil, domain.NewDomainError(domain.ErrorDisabled, "One-shot continuation is unavailable", nil)
	}
	owner, err := ownerFor(cc.Message)
	if err != nil {
		return nil, err
	}
	task, runtime, err := a.continuationContext(ctx, owner, cc.Args[0])
	if err != nil {
		return nil, err
	}
	if err := a.validateAttachmentCapability(ctx, task.ProviderID, len(cc.Message.Attachments)); err != nil {
		return nil, err
	}
	refs, err := a.stageAttachmentRefs(ctx, cc.Channel, owner, task.ProjectID, cc.Message)
	if err != nil {
		return nil, err
	}
	result, err := a.continuer.Continue(ctx, application.ContinueTaskCommand{Owner: owner, ProjectID: task.ProjectID, TaskID: task.ID, ProviderID: task.ProviderID, WorkspacePath: runtime.WorkspacePath, PromptDelta: strings.Join(cc.Args[1:], " "), AttachmentRefs: refs, Options: map[string]any{"telegram_command": true}, IdempotencyKey: telegramKey(cc.Message)})
	if err != nil {
		return nil, err
	}
	if err := a.bind(ctx, owner, cc.Message, task.ID, "continue"); err != nil {
		return nil, err
	}
	a.rememberNotificationPreference(ctx, owner, result.Task.ProjectID, cc.Message)
	return taskCard(result.Task, "Continuation queued: "+result.Delivery.ID), nil
}

func (a *Adapter) cancelCommand(ctx context.Context, cc channel.CommandContext) (*channel.Card, error) {
	if len(cc.Args) < 1 {
		return errorCard("Usage", "/cancel <task_id> [project_id]"), nil
	}
	if a.controller == nil {
		return nil, domain.NewDomainError(domain.ErrorDisabled, "One-shot control is unavailable", nil)
	}
	owner, err := ownerFor(cc.Message)
	if err != nil {
		return nil, err
	}
	task, err := a.repository.GetTask(ctx, owner, cc.Args[0])
	if err != nil {
		return nil, err
	}
	projectID := task.ProjectID
	if len(cc.Args) > 1 {
		projectID = cc.Args[1]
	}
	result, err := a.controller.CancelTask(ctx, application.CancelTaskCommand{Owner: owner, ProjectID: projectID, TaskID: task.ID})
	if err != nil {
		return nil, err
	}
	a.rememberNotificationPreference(ctx, owner, result.Task.ProjectID, cc.Message)
	return taskCard(result.Task, "Cancellation accepted"), nil
}

func (a *Adapter) retryCommand(ctx context.Context, cc channel.CommandContext) (*channel.Card, error) {
	if len(cc.Args) < 1 {
		return errorCard("Usage", "/retry <task_id> [project_id]"), nil
	}
	if a.controller == nil {
		return nil, domain.NewDomainError(domain.ErrorDisabled, "One-shot control is unavailable", nil)
	}
	owner, err := ownerFor(cc.Message)
	if err != nil {
		return nil, err
	}
	task, err := a.repository.GetTask(ctx, owner, cc.Args[0])
	if err != nil {
		return nil, err
	}
	projectID := task.ProjectID
	if len(cc.Args) > 1 {
		projectID = cc.Args[1]
	}
	if err := a.validateAttachmentCapability(ctx, task.ProviderID, len(cc.Message.Attachments)); err != nil {
		return nil, err
	}
	refs, err := a.stageAttachmentRefs(ctx, cc.Channel, owner, projectID, cc.Message)
	if err != nil {
		return nil, err
	}
	result, err := a.controller.RetryTask(ctx, application.RetryTaskCommand{Owner: owner, ProjectID: projectID, TaskID: task.ID, Input: domain.DeliveryInput{AttachmentRefs: refs, Options: map[string]any{"telegram_command": true}}, IdempotencyKey: telegramKey(cc.Message)})
	if err != nil {
		return nil, err
	}
	if err := a.bind(ctx, owner, cc.Message, task.ID, "task"); err != nil {
		return nil, err
	}
	a.rememberNotificationPreference(ctx, owner, result.Task.ProjectID, cc.Message)
	return taskCard(result.Task, "Retry queued: "+result.Delivery.ID), nil
}

func (a *Adapter) continuationContext(ctx context.Context, owner domain.Owner, taskID string) (domain.TaskSnapshot, domain.RuntimeContextSnapshot, error) {
	task, err := a.repository.GetTask(ctx, owner, taskID)
	if err != nil {
		return domain.TaskSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if task.RuntimeContextID == nil {
		return domain.TaskSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextNotFound, "Task has no RuntimeContext", nil)
	}
	runtime, err := a.repository.GetRuntimeContext(ctx, owner, *task.RuntimeContextID)
	return task, runtime, err
}

func (a *Adapter) bind(ctx context.Context, owner domain.Owner, msg channel.ChannelMessage, taskID, kind string) error {
	expires := a.now().Add(a.config.BindingTTL)
	var sourceMessageID *string
	if value := strings.TrimSpace(msg.SourceMessageID); value != "" {
		sourceMessageID = &value
	}
	_, err := a.repository.UpsertChannelBinding(ctx, store.ChannelBinding{Owner: owner, ChannelID: msg.ChannelID, ConversationID: msg.ConversationID, ThreadID: msg.ThreadID, SourceMessageID: sourceMessageID, TaskID: taskID, Kind: kind, ExpiresAt: &expires})
	return err
}

func telegramReplyTo(msg channel.ChannelMessage) string {
	if msg.Metadata == nil {
		return ""
	}
	switch value := msg.Metadata["reply_to_outbound_msg_id"].(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}

func ownerFor(msg channel.ChannelMessage) (domain.Owner, error) {
	id := ""
	if msg.Metadata != nil {
		if stable, ok := msg.Metadata["tg_user_id"].(string); ok {
			id = strings.TrimSpace(stable)
		}
	}
	if id == "" {
		id = strings.TrimSpace(msg.Author)
	}
	if id == "" {
		id = strings.TrimSpace(msg.ConversationID)
	}
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: id}
	return owner, owner.Validate()
}
func sourceFor(msg channel.ChannelMessage) domain.Source {
	metadata := map[string]string{"conversation_id": msg.ConversationID, "thread_id": msg.ThreadID, "author": msg.Author}
	return domain.Source{Kind: domain.SourceTelegram, ChannelID: msg.ChannelID, SourceMessageID: msg.SourceMessageID, ReplyAddress: &domain.ReplyAddress{ChannelID: msg.ChannelID, ConversationID: msg.ConversationID, ThreadID: msg.ThreadID, MessageID: msg.SourceMessageID}, Metadata: metadata}
}
func telegramKey(msg channel.ChannelMessage) string {
	return strings.Join([]string{"telegram", msg.ChannelID, msg.ConversationID, msg.ThreadID, msg.SourceMessageID}, ":")
}
func (a *Adapter) validateAttachmentCapability(ctx context.Context, providerID string, count int) error {
	if count == 0 {
		return nil
	}
	if a.providers == nil {
		return domain.NewDomainError(domain.ErrorProviderUnavailable, "Provider attachment capability is unavailable", nil)
	}
	items, err := a.providers.ListCapabilities(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == providerID && item.Enabled {
			if !item.CanAttach {
				return domain.InvalidRequestf("provider %s does not support One-shot attachments", providerID)
			}
			return nil
		}
	}
	return domain.NewDomainError(domain.ErrorUnsupportedProvider, "provider is not enabled for One-shot", nil)
}

func (a *Adapter) stageAttachmentRefs(ctx context.Context, transport channel.Channel, owner domain.Owner, projectID string, msg channel.ChannelMessage) ([]string, error) {
	if len(msg.Attachments) == 0 {
		return nil, nil
	}
	if a.attachments == nil {
		return nil, domain.NewDomainError(domain.ErrorArtifactUnavailable, "One-shot attachment staging is unavailable", nil)
	}
	opener, ok := transport.(channel.AttachmentOpener)
	if !ok {
		return nil, domain.InvalidRequestf("channel transport cannot securely open attachments")
	}
	refs := make([]string, 0, len(msg.Attachments))
	seen := map[string]struct{}{}
	for _, item := range msg.Attachments {
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		reader, err := opener.OpenAttachment(ctx, item)
		if err != nil {
			return nil, err
		}
		snapshot, stageErr := a.attachments.Stage(ctx, attachmentservice.StageRequest{Owner: owner, ProjectID: projectID, SourceKind: domain.SourceTelegram, SourceRef: item.ID, Name: item.Name, DeclaredMIME: item.MIMEType, Reader: reader})
		closeErr := reader.Close()
		if stageErr != nil {
			return nil, stageErr
		}
		if closeErr != nil && closeErr != io.EOF {
			return nil, closeErr
		}
		refs = append(refs, snapshot.ID)
	}
	return refs, nil
}

type notificationPreferenceWriter interface {
	UpsertNotificationPreference(context.Context, store.NotificationPreference) (store.NotificationPreference, error)
}

func (a *Adapter) rememberNotificationPreference(ctx context.Context, owner domain.Owner, projectID string, msg channel.ChannelMessage) {
	writer, ok := a.repository.(notificationPreferenceWriter)
	if !ok || strings.TrimSpace(projectID) == "" {
		return
	}
	_, err := writer.UpsertNotificationPreference(ctx, store.NotificationPreference{Owner: owner, ProjectID: projectID, ChannelID: msg.ChannelID, ConversationID: msg.ConversationID, ThreadID: msg.ThreadID, MessageID: msg.SourceMessageID, Metadata: map[string]string{"source": "telegram"}, Enabled: true, UpdatedAt: a.now()})
	if err != nil {
		a.log.WarnContext(ctx, "persist Telegram notification preference failed", "principal_kind", owner.Kind, "principal_id", owner.ID, "project_id", projectID, "source", "telegram", "reply_channel_id", msg.ChannelID, "reply_conversation_id", msg.ConversationID, "err", err)
		return
	}
	a.log.InfoContext(ctx, "One-shot cross-device notification preference updated", "principal_kind", owner.Kind, "principal_id", owner.ID, "project_id", projectID, "source", "telegram", "reply_channel_id", msg.ChannelID, "reply_conversation_id", msg.ConversationID)
}

func (a *Adapter) parseRunArgs(ctx context.Context, args []string) (runArgs, error) {
	out := runArgs{ProjectID: a.config.DefaultProjectID, ProviderID: a.config.DefaultProvider, Workspace: a.config.DefaultWorkspace}
	prompt := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			i++
			if i >= len(args) {
				return out, fmt.Errorf("--project requires a value")
			}
			out.ProjectID = args[i]
		case "--provider":
			i++
			if i >= len(args) {
				return out, fmt.Errorf("--provider requires a value")
			}
			out.ProviderID = args[i]
		case "--workspace":
			i++
			if i >= len(args) {
				return out, fmt.Errorf("--workspace requires a value")
			}
			out.Workspace = args[i]
		case "--":
			prompt = append(prompt, args[i+1:]...)
			i = len(args)
		default:
			prompt = append(prompt, args[i])
		}
	}
	out.ProjectID = strings.TrimSpace(out.ProjectID)
	out.ProviderID = strings.TrimSpace(out.ProviderID)
	out.Workspace = strings.TrimSpace(out.Workspace)
	out.Prompt = strings.TrimSpace(strings.Join(prompt, " "))
	if out.ProjectID == "" || out.ProviderID == "" || out.Workspace == "" || out.Prompt == "" {
		return out, fmt.Errorf("project, provider, workspace and prompt are required")
	}
	if a.providers != nil {
		options, err := a.providers.ListCapabilities(ctx)
		if err != nil {
			return out, err
		}
		found := false
		for _, option := range options {
			if option.ID == out.ProviderID && option.Enabled {
				found = true
				break
			}
		}
		if !found {
			return out, fmt.Errorf("provider %s is not enabled for One-shot", out.ProviderID)
		}
	}
	return out, nil
}

func (a *Adapter) runHelp(ctx context.Context, reason string) *channel.Card {
	content := "Usage: /run --project <id> --provider <id> --workspace <absolute path> -- <prompt>"
	if reason != "" {
		content = reason + "\n\n" + content
	}
	elements := []channel.CardElement{channel.CardMarkdown{Content: content}}
	if a.providers != nil {
		if options, err := a.providers.ListCapabilities(ctx); err == nil {
			sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
			buttons := []channel.ButtonOption{}
			for _, option := range options {
				if option.Enabled {
					label := option.DisplayName
					if label == "" {
						label = option.ID
					}
					buttons = append(buttons, channel.ButtonOption{Text: label, Value: "cmd:/run --provider " + option.ID})
				}
			}
			if len(buttons) > 0 {
				elements = append(elements, channel.CardActions{Buttons: [][]channel.ButtonOption{buttons}})
			}
		}
	}
	return &channel.Card{Header: &channel.CardHeader{Title: "Run One-shot task"}, Elements: elements}
}
func taskCard(task domain.TaskSnapshot, note string) *channel.Card {
	buttons := []channel.ButtonOption{{Text: "Refresh", Value: "cmd:/task " + task.ID}, {Text: "Cancel", Value: "cmd:/cancel " + task.ID, Style: "danger"}}
	if task.Status == domain.TaskFailed || task.Status == domain.TaskTimedOut {
		buttons = append(buttons, channel.ButtonOption{Text: "Retry", Value: "cmd:/retry " + task.ID, Style: "primary"})
	}
	return &channel.Card{Header: &channel.CardHeader{Title: "One-shot task"}, Elements: []channel.CardElement{channel.CardMarkdown{Content: fmt.Sprintf("Task: `%s`\nProject: `%s`\nProvider: `%s`\nStatus: **%s**", task.ID, task.ProjectID, task.ProviderID, task.Status)}, channel.CardNote{Text: note}, channel.CardActions{Buttons: [][]channel.ButtonOption{buttons}}}}
}
func errorCard(title, body string) *channel.Card {
	return &channel.Card{Header: &channel.CardHeader{Title: title, Color: "red"}, Elements: []channel.CardElement{channel.CardMarkdown{Content: body}}}
}
