// Package queue implements the durable One-shot execution Delivery queue.
//
// It is intentionally separate from channel notification delivery and from
// interactive PTY Session routing. PostgreSQL database time is authoritative
// for all lease and retry transitions.
package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const (
	DefaultClaimLimit   = 16
	DefaultLease        = 45 * time.Second
	DefaultPollInterval = 500 * time.Millisecond
)

// ErrNotFound is returned when a queue Delivery no longer exists.
var ErrNotFound = errors.New("oneshot queue delivery not found")

// LeaseLostError means another worker owns the Delivery or the lease expired.
type LeaseLostError struct {
	DeliveryID string
	WorkerID   string
}

func (e *LeaseLostError) Error() string {
	return fmt.Sprintf("oneshot queue lease lost: delivery=%s worker=%s", e.DeliveryID, e.WorkerID)
}

// Claim is the immutable execution input reserved by one worker.
type Claim struct {
	Task     domain.TaskSnapshot
	Delivery domain.DeliverySnapshot
}

// EnqueueRequest is an already domain-validated Task and initial Delivery plus
// the request identity used for API/Telegram deduplication.
type EnqueueRequest struct {
	Task           domain.TaskSnapshot
	Delivery       domain.DeliverySnapshot
	Method         string
	CanonicalPath  string
	IdempotencyKey string
	PayloadSHA256  string
	ExpiresAt      *time.Time
}

// EnqueueResult returns the original resources for idempotent replays.
type EnqueueResult struct {
	Task     domain.TaskSnapshot
	Delivery domain.DeliverySnapshot
	Created  bool
}

// Repository is the application-facing durable queue contract.
type Repository interface {
	Enqueue(ctx context.Context, request EnqueueRequest) (EnqueueResult, error)
	ClaimDue(ctx context.Context, workerID string, limit int, lease time.Duration) ([]Claim, error)
	RenewLease(ctx context.Context, deliveryID, workerID string, lease time.Duration) (domain.DeliverySnapshot, error)
	Ack(ctx context.Context, deliveryID, workerID string) (domain.DeliverySnapshot, error)
	Nack(ctx context.Context, deliveryID, workerID string, code domain.ErrorCode, policy RetryPolicy) (domain.DeliverySnapshot, error)
	DeadLetter(ctx context.Context, deliveryID, workerID string, code domain.ErrorCode) (domain.DeliverySnapshot, error)
	Cancel(ctx context.Context, deliveryID string, owner domain.Owner, workerID string) (domain.DeliverySnapshot, error)
	AcknowledgeRecovered(ctx context.Context, deliveryID, runID string) (domain.DeliverySnapshot, error)
}

// Processor performs one claimed Delivery. It must not create a second Run for
// a Delivery that already owns one. The returned Action determines the durable
// queue transition.
type Processor interface {
	Process(ctx context.Context, claim Claim) Outcome
}

// AckObserver is called only after the durable queue ACK succeeds.
type AckObserver interface {
	Acked(context.Context, Claim) error
}

// Action is the queue transition requested by a Processor.
type Action string

const (
	ActionAck        Action = "ack"
	ActionRetry      Action = "retry"
	ActionDeadLetter Action = "dead_letter"
	// ActionRecover leaves the durable lease untouched for the crash reconciler.
	ActionRecover Action = "recover"
)

// Outcome is a Processor result. Retry and dead-letter actions require a
// stable One-shot error code.
type Outcome struct {
	Action Action
	Code   domain.ErrorCode
}

func (o Outcome) Validate() error {
	switch o.Action {
	case ActionAck, ActionRecover:
		return nil
	case ActionRetry:
		if !domain.IsKnownErrorCode(o.Code) || !domain.IsRetryableCode(o.Code) {
			return domain.InvalidRequestf("retry outcome requires a retryable One-shot error code")
		}
		return nil
	case ActionDeadLetter:
		if !domain.IsKnownErrorCode(o.Code) {
			return domain.InvalidRequestf("dead-letter outcome requires a known One-shot error code")
		}
		return nil
	default:
		return domain.InvalidRequestf("invalid queue outcome action %q", o.Action)
	}
}
