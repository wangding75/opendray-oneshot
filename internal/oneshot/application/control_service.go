package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
)

const retryTaskPathSuffix = "/retry"

// ControlRepository is the persistence boundary for REST/channel control actions.
type ControlRepository interface {
	GetTask(context.Context, domain.Owner, string) (domain.TaskSnapshot, error)
	GetRun(context.Context, domain.Owner, string) (domain.RunSnapshot, error)
	ListDeliveries(context.Context, domain.Owner, string, store.PageRequest) (store.Page[domain.DeliverySnapshot], error)
	UpdateTask(context.Context, domain.Owner, domain.TaskSnapshot, int64) (domain.TaskSnapshot, error)
	FindRetryReplay(context.Context, domain.Owner, string, string, string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error)
	CreateRetryDelivery(context.Context, domain.Owner, domain.TaskSnapshot, int64, domain.DeliverySnapshot, string, *time.Time) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error)
}

// DeliveryCanceller cancels a queued/reserved delivery in the execution queue.
type DeliveryCanceller interface {
	Cancel(context.Context, string, domain.Owner, string) (domain.DeliverySnapshot, error)
}

// ActiveRunCanceller stops a live Run tracked by the current worker.
type ActiveRunCanceller interface {
	CancelActiveRun(context.Context, string) error
}

// ExistingTreeTerminator handles a recovered Run whose process is no longer in the worker map.
type ExistingTreeTerminator interface {
	TerminateExistingTree(context.Context, int, time.Duration) error
}

type ControlService struct {
	repository ControlRepository
	queue      DeliveryCanceller
	active     ActiveRunCanceller
	terminator ExistingTreeTerminator
	workerID   string
	grace      time.Duration
	now        func() time.Time
}

func NewControlService(repository ControlRepository, queue DeliveryCanceller, active ActiveRunCanceller, terminator ExistingTreeTerminator, workerID string, grace time.Duration) (*ControlService, error) {
	if repository == nil || queue == nil {
		return nil, domain.InvalidRequestf("control repository and queue are required")
	}
	if strings.TrimSpace(workerID) == "" {
		workerID = "oneshot-control"
	}
	if grace <= 0 {
		grace = 5 * time.Second
	}
	return &ControlService{repository: repository, queue: queue, active: active, terminator: terminator, workerID: workerID, grace: grace, now: func() time.Time { return time.Now().UTC() }}, nil
}

type CancelTaskCommand struct {
	Owner     domain.Owner
	ProjectID string
	TaskID    string
}

type CancelTaskResult struct {
	Task     domain.TaskSnapshot      `json:"task"`
	Delivery *domain.DeliverySnapshot `json:"delivery,omitempty"`
	Run      *domain.RunSnapshot      `json:"run,omitempty"`
	Noop     bool                     `json:"noop"`
}

// CancelTask is idempotent and reports success only after an active process tree is absent.
func (s *ControlService) CancelTask(ctx context.Context, command CancelTaskCommand) (CancelTaskResult, error) {
	persisted, err := s.repository.GetTask(ctx, command.Owner, command.TaskID)
	if err != nil {
		return CancelTaskResult{}, err
	}
	task, err := domain.RestoreTask(persisted)
	if err != nil {
		return CancelTaskResult{}, err
	}
	if err := task.Authorize(command.Owner, command.ProjectID); err != nil {
		return CancelTaskResult{}, err
	}
	if persisted.Status == domain.TaskCancelled {
		return CancelTaskResult{Task: persisted, Noop: true}, nil
	}
	if persisted.Status == domain.TaskRunning && persisted.CurrentRunID != nil {
		run, runErr := s.repository.GetRun(ctx, command.Owner, *persisted.CurrentRunID)
		if runErr != nil {
			return CancelTaskResult{}, runErr
		}
		if s.active != nil {
			if err := s.active.CancelActiveRun(ctx, run.ID); err != nil {
				return CancelTaskResult{}, domain.NewDomainError(domain.ErrorCancelFailed, "terminate active Run", err)
			}
		}
		if run.PID != nil && s.terminator != nil {
			if err := s.terminator.TerminateExistingTree(ctx, *run.PID, s.grace); err != nil {
				return CancelTaskResult{}, domain.NewDomainError(domain.ErrorCancelFailed, "terminate recovered process tree", err)
			}
		}
		return CancelTaskResult{Task: persisted, Run: &run}, nil
	}

	page, err := s.repository.ListDeliveries(ctx, command.Owner, command.TaskID, store.PageRequest{Limit: 200})
	if err != nil {
		return CancelTaskResult{}, err
	}
	var cancelled *domain.DeliverySnapshot
	for _, item := range page.Items {
		switch item.Status {
		case domain.DeliveryPending, domain.DeliveryRetryWait, domain.DeliveryReserved:
			out, cancelErr := s.queue.Cancel(ctx, item.ID, command.Owner, s.workerID)
			if cancelErr != nil {
				return CancelTaskResult{}, cancelErr
			}
			cancelled = &out
		}
	}
	expected := persisted.Version
	if err := task.CancelQuiescent(s.now()); err != nil {
		return CancelTaskResult{}, err
	}
	updated, err := s.repository.UpdateTask(ctx, command.Owner, task.Snapshot(), expected)
	if err != nil {
		return CancelTaskResult{}, err
	}
	return CancelTaskResult{Task: updated, Delivery: cancelled}, nil
}

type RetryTaskCommand struct {
	Owner          domain.Owner
	ProjectID      string
	TaskID         string
	Input          domain.DeliveryInput
	IdempotencyKey string
	MaxAttempts    int
	ExpiresAt      *time.Time
}

type RetryTaskResult struct {
	Task     domain.TaskSnapshot     `json:"task"`
	Delivery domain.DeliverySnapshot `json:"delivery"`
	Created  bool                    `json:"created"`
}

func (s *ControlService) RetryTask(ctx context.Context, command RetryTaskCommand) (RetryTaskResult, error) {
	if err := command.Owner.Validate(); err != nil {
		return RetryTaskResult{}, err
	}
	key := strings.TrimSpace(command.IdempotencyKey)
	if key == "" {
		return RetryTaskResult{}, domain.NewDomainError(domain.ErrorIdempotencyRequired, "Idempotency-Key is required", nil)
	}
	payloadSHA, err := CanonicalRetryPayloadSHA256(command)
	if err != nil {
		return RetryTaskResult{}, err
	}
	path := strings.TrimSuffix(ContinueCanonicalPath(command.TaskID), "/continue") + retryTaskPathSuffix
	replayTask, replayDelivery, replayed, err := s.repository.FindRetryReplay(ctx, command.Owner, path, key, payloadSHA)
	if err != nil {
		return RetryTaskResult{}, err
	}
	if replayed {
		return RetryTaskResult{Task: replayTask, Delivery: replayDelivery, Created: false}, nil
	}

	persisted, err := s.repository.GetTask(ctx, command.Owner, command.TaskID)
	if err != nil {
		return RetryTaskResult{}, err
	}
	task, err := domain.RestoreTask(persisted)
	if err != nil {
		return RetryTaskResult{}, err
	}
	if err := task.Authorize(command.Owner, command.ProjectID); err != nil {
		return RetryTaskResult{}, err
	}
	maxAttempts := command.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	now := s.now()
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{TaskID: persisted.ID, Operation: domain.DeliveryRetry, RequestedBy: command.Owner, Input: command.Input, IdempotencyKey: key, PayloadSHA256: payloadSHA, MaxAttempts: maxAttempts, AvailableAt: now}, now)
	if err != nil {
		return RetryTaskResult{}, err
	}
	expected := persisted.Version
	if err := task.QueueRetry(delivery.Snapshot(), now); err != nil {
		return s.retryReplayAfterRace(ctx, command, path, payloadSHA, err)
	}
	updatedTask, createdDelivery, created, err := s.repository.CreateRetryDelivery(ctx, command.Owner, task.Snapshot(), expected, delivery.Snapshot(), path, command.ExpiresAt)
	if err != nil {
		return s.retryReplayAfterRace(ctx, command, path, payloadSHA, err)
	}
	return RetryTaskResult{Task: updatedTask, Delivery: createdDelivery, Created: created}, nil
}

func (s *ControlService) retryReplayAfterRace(ctx context.Context, command RetryTaskCommand, canonicalPath, payloadSHA string, cause error) (RetryTaskResult, error) {
	replayTask, replayDelivery, replayed, err := s.repository.FindRetryReplay(ctx, command.Owner, canonicalPath, strings.TrimSpace(command.IdempotencyKey), payloadSHA)
	if err != nil {
		return RetryTaskResult{}, err
	}
	if replayed {
		return RetryTaskResult{Task: replayTask, Delivery: replayDelivery, Created: false}, nil
	}
	return RetryTaskResult{}, cause
}

func CanonicalRetryPayloadSHA256(command RetryTaskCommand) (string, error) {
	canonical := struct {
		Owner       domain.Owner         `json:"owner"`
		ProjectID   string               `json:"project_id"`
		TaskID      string               `json:"task_id"`
		Input       domain.DeliveryInput `json:"input"`
		MaxAttempts int                  `json:"max_attempts"`
	}{command.Owner, command.ProjectID, command.TaskID, command.Input, command.MaxAttempts}
	if canonical.MaxAttempts <= 0 {
		canonical.MaxAttempts = 3
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", domain.InvalidRequestf("retry payload is not JSON-compatible: %v", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
