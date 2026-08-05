// Package application coordinates One-shot use cases without importing PTY or
// interactive Session packages.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/workspacepolicy"
)

const createTaskPath = "/api/v1/oneshot/tasks"

// DispatchService durably creates the Task and its initial execution Delivery.
type DispatchService struct {
	repository       queue.Repository
	now              func() time.Time
	policy           *workspacepolicy.Policy
	defaultWorkspace string
}

type DispatchServiceOption func(*DispatchService)

func WithWorkspacePolicy(policy *workspacepolicy.Policy, defaultWorkspace string) DispatchServiceOption {
	return func(service *DispatchService) {
		service.policy = policy
		service.defaultWorkspace = strings.TrimSpace(defaultWorkspace)
	}
}

func NewDispatchService(repository queue.Repository, options ...DispatchServiceOption) *DispatchService {
	service := &DispatchService{
		repository: repository,
		now:        func() time.Time { return time.Now().UTC() },
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// CreateTaskCommand is the canonical create input shared by API, Telegram,
// mobile, and web entrypoints.
type CreateTaskCommand struct {
	Owner          domain.Owner
	ProjectID      string
	ProviderID     string
	WorkspacePath  string
	Source         domain.Source
	Prompt         string
	Input          domain.DeliveryInput
	IdempotencyKey string
	MaxAttempts    int
	ExpiresAt      *time.Time
}

// CreateTaskResult identifies whether the request created or replayed the
// original resources.
type CreateTaskResult struct {
	Task     domain.TaskSnapshot
	Delivery domain.DeliverySnapshot
	Created  bool
}

// CreateTask applies domain validation before the transaction and delegates
// all deduplication and persistence to the queue repository.
func (s *DispatchService) CreateTask(ctx context.Context, command CreateTaskCommand) (CreateTaskResult, error) {
	if s == nil || s.repository == nil {
		return CreateTaskResult{}, domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot dispatch service is unavailable", nil)
	}
	if command.MaxAttempts <= 0 {
		command.MaxAttempts = 3
	}
	key, err := requestIdempotencyKey(command.Source, command.IdempotencyKey)
	if err != nil {
		return CreateTaskResult{}, err
	}
	workspace, err := s.workspacePath(command.WorkspacePath)
	if err != nil {
		return CreateTaskResult{}, err
	}
	command.Input.Options = cloneCreateOptions(command.Input.Options)
	command.Input.Options["workspace_path"] = workspace
	payloadSHA, err := CanonicalCreatePayloadSHA256(command)
	if err != nil {
		return CreateTaskResult{}, err
	}

	now := s.now().UTC()
	task, err := domain.NewTask(domain.TaskArgs{
		Owner: command.Owner, ProjectID: command.ProjectID, ProviderID: command.ProviderID,
		Source: command.Source, Prompt: command.Prompt,
	}, now)
	if err != nil {
		return CreateTaskResult{}, err
	}
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{
		TaskID: task.Snapshot().ID, Operation: domain.DeliveryNew,
		RequestedBy: command.Owner, Input: command.Input,
		IdempotencyKey: key, PayloadSHA256: payloadSHA,
		MaxAttempts: command.MaxAttempts, AvailableAt: now,
	}, now)
	if err != nil {
		return CreateTaskResult{}, err
	}
	if err := task.QueueInitialDelivery(delivery.Snapshot(), now); err != nil {
		return CreateTaskResult{}, err
	}

	result, err := s.repository.Enqueue(ctx, queue.EnqueueRequest{
		Task: task.Snapshot(), Delivery: delivery.Snapshot(), Method: "POST",
		CanonicalPath: createTaskPath, IdempotencyKey: key,
		PayloadSHA256: payloadSHA, ExpiresAt: command.ExpiresAt,
	})
	if err != nil {
		return CreateTaskResult{}, err
	}
	return CreateTaskResult{Task: result.Task, Delivery: result.Delivery, Created: result.Created}, nil
}

func (s *DispatchService) workspacePath(requested string) (string, error) {
	workspace := strings.TrimSpace(requested)
	if workspace == "" {
		workspace = strings.TrimSpace(s.defaultWorkspace)
	}
	if workspace == "" {
		return "", domain.InvalidRequestf("no default One-shot workspace is configured")
	}
	if s.policy == nil || !s.policy.HasRoots() {
		return "", domain.InvalidRequestf("no allowed One-shot workspace roots are configured")
	}
	return s.policy.NormalizeAndValidate(workspace)
}

func cloneCreateOptions(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func requestIdempotencyKey(source domain.Source, supplied string) (string, error) {
	if source.Kind == domain.SourceTelegram {
		if strings.TrimSpace(source.ChannelID) == "" || strings.TrimSpace(source.SourceMessageID) == "" {
			return "", domain.InvalidRequestf("Telegram source requires channel_id and source_message_id")
		}
		digest := sha256.Sum256([]byte(source.ChannelID + "\x00" + source.SourceMessageID))
		return "telegram:" + hex.EncodeToString(digest[:]), nil
	}
	if strings.TrimSpace(supplied) == "" {
		return "", domain.NewDomainError(domain.ErrorIdempotencyRequired, "Idempotency-Key is required", nil)
	}
	return strings.TrimSpace(supplied), nil
}

// CanonicalCreatePayloadSHA256 binds the idempotency key to immutable request
// data and attachment references. encoding/json sorts map keys deterministically.
func CanonicalCreatePayloadSHA256(command CreateTaskCommand) (string, error) {
	canonical := struct {
		Owner       domain.Owner         `json:"owner"`
		ProjectID   string               `json:"project_id"`
		ProviderID  string               `json:"provider_id"`
		Source      domain.Source        `json:"source"`
		Prompt      string               `json:"prompt"`
		Input       domain.DeliveryInput `json:"input"`
		MaxAttempts int                  `json:"max_attempts"`
	}{
		Owner: command.Owner, ProjectID: command.ProjectID, ProviderID: command.ProviderID,
		Source: command.Source, Prompt: command.Prompt, Input: command.Input,
		MaxAttempts: command.MaxAttempts,
	}
	if canonical.MaxAttempts <= 0 {
		canonical.MaxAttempts = 3
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", domain.InvalidRequestf("create Task payload is not JSON-compatible: %v", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
