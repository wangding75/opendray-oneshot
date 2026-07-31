package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const continueTaskPathPrefix = "/api/v1/oneshot/tasks/"

// ContinuationRepository is the minimal persistence boundary for creating a
// new continue Delivery. Store implements it without exposing SQL to the
// application layer.
type ContinuationRepository interface {
	GetTask(context.Context, domain.Owner, string) (domain.TaskSnapshot, error)
	GetRuntimeContext(context.Context, domain.Owner, string) (domain.RuntimeContextSnapshot, error)
	FindContinueReplay(context.Context, domain.Owner, string, string, string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error)
	CreateContinueDelivery(context.Context, domain.Owner, domain.TaskSnapshot, int64, domain.DeliverySnapshot, string, *time.Time) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error)
}

type ContinueTaskCommand struct {
	Owner          domain.Owner
	ProjectID      string
	TaskID         string
	ProviderID     string
	WorkspacePath  string
	PromptDelta    string
	AttachmentRefs []string
	Options        map[string]any
	IdempotencyKey string
	MaxAttempts    int
	ExpiresAt      *time.Time
}

type ContinueTaskResult struct {
	Task     domain.TaskSnapshot
	Delivery domain.DeliverySnapshot
	Created  bool
}

type ContinuationService struct {
	repository ContinuationRepository
	registry   *adapter.Registry
	now        func() time.Time
}

func NewContinuationService(repository ContinuationRepository, registry *adapter.Registry) (*ContinuationService, error) {
	if repository == nil || registry == nil {
		return nil, domain.InvalidRequestf("continuation repository and adapter registry are required")
	}
	return &ContinuationService{repository: repository, registry: registry, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Continue validates exact ownership and context identity before creating a
// new Delivery. The queue worker later creates a new Run and child process.
func (s *ContinuationService) Continue(ctx context.Context, command ContinueTaskCommand) (ContinueTaskResult, error) {
	if err := command.Owner.Validate(); err != nil {
		return ContinueTaskResult{}, err
	}
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.TaskID) == "" || strings.TrimSpace(command.ProviderID) == "" {
		return ContinueTaskResult{}, domain.InvalidRequestf("project_id, task_id, and provider_id are required")
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" {
		return ContinueTaskResult{}, domain.NewDomainError(domain.ErrorIdempotencyRequired, "Idempotency-Key is required", nil)
	}
	payloadSHA, err := CanonicalContinuePayloadSHA256(command)
	if err != nil {
		return ContinueTaskResult{}, err
	}
	canonicalPath := ContinueCanonicalPath(command.TaskID)
	replayTask, replayDelivery, replayed, err := s.repository.FindContinueReplay(ctx, command.Owner, canonicalPath, strings.TrimSpace(command.IdempotencyKey), payloadSHA)
	if err != nil {
		return ContinueTaskResult{}, err
	}
	if replayed {
		return ContinueTaskResult{Task: replayTask, Delivery: replayDelivery, Created: false}, nil
	}
	descriptor, err := s.registry.Describe(ctx, command.ProviderID)
	if err != nil {
		return ContinueTaskResult{}, err
	}
	if !descriptor.Capabilities.SupportsResume {
		return ContinueTaskResult{}, domain.NewDomainError(domain.ErrorResumeUnsupported, "provider does not support One-shot resume", nil)
	}
	persistedTask, err := s.repository.GetTask(ctx, command.Owner, command.TaskID)
	if err != nil {
		return ContinueTaskResult{}, err
	}
	task, err := domain.RestoreTask(persistedTask)
	if err != nil {
		return ContinueTaskResult{}, err
	}
	if err := task.Authorize(command.Owner, command.ProjectID); err != nil {
		return ContinueTaskResult{}, err
	}
	if persistedTask.ProviderID != command.ProviderID {
		return ContinueTaskResult{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "provider does not match Task RuntimeContext", nil)
	}
	if persistedTask.RuntimeContextID == nil {
		return ContinueTaskResult{}, domain.NewDomainError(domain.ErrorContextNotFound, "Task has no RuntimeContext", nil)
	}
	persistedContext, err := s.repository.GetRuntimeContext(ctx, command.Owner, *persistedTask.RuntimeContextID)
	if err != nil {
		return ContinueTaskResult{}, err
	}
	if persistedContext.ProjectID != command.ProjectID || persistedContext.ProviderID != command.ProviderID ||
		persistedContext.PrincipalKind != command.Owner.Kind || persistedContext.PrincipalID != command.Owner.ID {
		return ContinueTaskResult{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "RuntimeContext owner, project, or provider mismatch", nil)
	}
	if persistedContext.Status != domain.ContextActive {
		return ContinueTaskResult{}, domain.NewDomainError(domain.ErrorRunConflict, "RuntimeContext is not active", nil)
	}
	workspace := strings.TrimSpace(command.WorkspacePath)
	if workspace == "" || workspace != persistedContext.WorkspacePath {
		return ContinueTaskResult{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "workspace does not match RuntimeContext", nil)
	}
	maxAttempts := command.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	options := cloneOptions(command.Options)
	options["workspace_path"] = workspace
	input := domain.DeliveryInput{PromptDelta: command.PromptDelta, AttachmentRefs: append([]string(nil), command.AttachmentRefs...), Options: options}
	now := s.now().UTC()
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{TaskID: task.Snapshot().ID, Operation: domain.DeliveryContinue, RequestedBy: command.Owner, Input: input, IdempotencyKey: strings.TrimSpace(command.IdempotencyKey), PayloadSHA256: payloadSHA, MaxAttempts: maxAttempts, AvailableAt: now}, now)
	if err != nil {
		return ContinueTaskResult{}, err
	}
	previousVersion := task.Snapshot().Version
	if err := task.QueueContinue(delivery.Snapshot(), persistedContext, now); err != nil {
		return s.continueReplayAfterRace(ctx, command, canonicalPath, payloadSHA, err)
	}
	updatedTask, createdDelivery, created, err := s.repository.CreateContinueDelivery(ctx, command.Owner, task.Snapshot(), previousVersion, delivery.Snapshot(), canonicalPath, command.ExpiresAt)
	if err != nil {
		return s.continueReplayAfterRace(ctx, command, canonicalPath, payloadSHA, err)
	}
	return ContinueTaskResult{Task: updatedTask, Delivery: createdDelivery, Created: created}, nil
}

func (s *ContinuationService) continueReplayAfterRace(ctx context.Context, command ContinueTaskCommand, canonicalPath, payloadSHA string, cause error) (ContinueTaskResult, error) {
	replayTask, replayDelivery, replayed, err := s.repository.FindContinueReplay(ctx, command.Owner, canonicalPath, strings.TrimSpace(command.IdempotencyKey), payloadSHA)
	if err != nil {
		return ContinueTaskResult{}, err
	}
	if replayed {
		return ContinueTaskResult{Task: replayTask, Delivery: replayDelivery, Created: false}, nil
	}
	return ContinueTaskResult{}, cause
}

func CanonicalContinuePayloadSHA256(command ContinueTaskCommand) (string, error) {
	canonical := struct {
		Owner          domain.Owner   `json:"owner"`
		ProjectID      string         `json:"project_id"`
		TaskID         string         `json:"task_id"`
		ProviderID     string         `json:"provider_id"`
		WorkspacePath  string         `json:"workspace_path"`
		PromptDelta    string         `json:"prompt_delta"`
		AttachmentRefs []string       `json:"attachment_refs"`
		Options        map[string]any `json:"options"`
		MaxAttempts    int            `json:"max_attempts"`
	}{command.Owner, command.ProjectID, command.TaskID, command.ProviderID, command.WorkspacePath, command.PromptDelta, command.AttachmentRefs, command.Options, command.MaxAttempts}
	if canonical.MaxAttempts <= 0 {
		canonical.MaxAttempts = 3
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", domain.InvalidRequestf("continue payload is not JSON-compatible: %v", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func cloneOptions(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func ContinueCanonicalPath(taskID string) string {
	return continueTaskPathPrefix + strings.TrimSpace(taskID) + "/continue"
}
