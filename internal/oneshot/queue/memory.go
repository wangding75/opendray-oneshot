package queue

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

type memoryReplay struct {
	payloadSHA string
	task       domain.TaskSnapshot
	delivery   domain.DeliverySnapshot
}

type memoryRun struct {
	id     string
	status domain.RunStatus
}

// MemoryQueue mirrors PostgreSQL queue semantics for focused race tests.
type MemoryQueue struct {
	mu         sync.Mutex
	tasks      map[string]domain.TaskSnapshot
	deliveries map[string]domain.DeliverySnapshot
	runs       map[string]memoryRun
	replays    map[string]memoryReplay
	sources    map[string]string
	now        func() time.Time
	audit      AuditSink
}

func NewMemoryQueue(audit AuditSink) *MemoryQueue {
	if audit == nil {
		audit = nopAuditSink{}
	}
	return &MemoryQueue{
		tasks:      make(map[string]domain.TaskSnapshot),
		deliveries: make(map[string]domain.DeliverySnapshot),
		runs:       make(map[string]memoryRun),
		replays:    make(map[string]memoryReplay),
		sources:    make(map[string]string),
		now:        func() time.Time { return time.Now().UTC() },
		audit:      audit,
	}
}

// SetClock is intended for deterministic queue tests.
func (q *MemoryQueue) SetClock(clock func() time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.now = clock
}

func replayScope(request EnqueueRequest) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s",
		request.Task.PrincipalKind, request.Task.PrincipalID,
		strings.ToUpper(request.Method), request.CanonicalPath, request.IdempotencyKey)
}

func sourceScope(source domain.Source) string {
	if source.ChannelID == "" || source.SourceMessageID == "" {
		return ""
	}
	return source.ChannelID + "\x00" + source.SourceMessageID
}

func (q *MemoryQueue) Enqueue(ctx context.Context, request EnqueueRequest) (EnqueueResult, error) {
	if err := validateEnqueue(request); err != nil {
		return EnqueueResult{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	scope := replayScope(request)
	if replay, ok := q.replays[scope]; ok {
		if replay.payloadSHA != request.PayloadSHA256 {
			return EnqueueResult{}, domain.NewDomainError(domain.ErrorIdempotencyConflict, "Idempotency-Key was reused with a different payload", nil)
		}
		return EnqueueResult{Task: replay.task, Delivery: replay.delivery, Created: false}, nil
	}
	if source := sourceScope(request.Task.Source); source != "" {
		if taskID, ok := q.sources[source]; ok {
			return EnqueueResult{}, domain.NewDomainError(domain.ErrorIdempotencyConflict, "Source message already owns Task "+taskID, nil)
		}
		q.sources[source] = request.Task.ID
	}
	q.tasks[request.Task.ID] = request.Task
	q.deliveries[request.Delivery.ID] = request.Delivery
	q.replays[scope] = memoryReplay{payloadSHA: request.PayloadSHA256, task: request.Task, delivery: request.Delivery}
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.queued", DeliveryID: request.Delivery.ID, TaskID: request.Task.ID, OccurredAt: request.Delivery.UpdatedAt})
	return EnqueueResult{Task: request.Task, Delivery: request.Delivery, Created: true}, nil
}

func (q *MemoryQueue) ClaimDue(ctx context.Context, workerID string, limit int, lease time.Duration) ([]Claim, error) {
	if strings.TrimSpace(workerID) == "" {
		return nil, domain.InvalidRequestf("worker_id is required")
	}
	if limit <= 0 {
		limit = DefaultClaimLimit
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now().UTC()

	ids := make([]string, 0, len(q.deliveries))
	for id := range q.deliveries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := q.deliveries[ids[i]], q.deliveries[ids[j]]
		if a.AvailableAt.Equal(b.AvailableAt) {
			if a.CreatedAt.Equal(b.CreatedAt) {
				return a.ID < b.ID
			}
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.AvailableAt.Before(b.AvailableAt)
	})

	claims := make([]Claim, 0, limit)
	for _, id := range ids {
		if len(claims) >= limit {
			break
		}
		delivery := q.deliveries[id]
		task := q.tasks[delivery.TaskID]
		if task.Status != domain.TaskQueued {
			continue
		}
		// A Delivery with an attached Run is owned by the execution Saga. Even
		// after its worker lease expires, the dispatch queue must neither rerun
		// nor silently acknowledge it; the crash Reconciler releases credentials,
		// closes the Saga, and calls AcknowledgeRecovered explicitly.
		if delivery.RunID != nil {
			continue
		}
		eligible := (delivery.Status == domain.DeliveryPending || delivery.Status == domain.DeliveryRetryWait) && !delivery.AvailableAt.After(now)
		if delivery.Status == domain.DeliveryReserved && delivery.LeaseUntil != nil && !delivery.LeaseUntil.After(now) && delivery.RunID == nil {
			eligible = true
		}
		if !eligible || delivery.RunID != nil {
			continue
		}
		if delivery.Attempt >= delivery.MaxAttempts {
			delivery.Status = domain.DeliveryDeadLetter
			delivery.LeaseOwner = nil
			delivery.LeaseUntil = nil
			code := string(domain.ErrorDeliveryExhausted)
			delivery.LastErrorCode = &code
			delivery.UpdatedAt = now
			q.deliveries[id] = delivery
			task.Status = domain.TaskFailed
			task.Version++
			task.UpdatedAt = now
			q.tasks[task.ID] = task
			continue
		}
		// Lease expiry is an internal transactional reconciliation. Reset the
		// persisted shape before applying the frozen retry_wait -> reserved edge.
		if delivery.Status == domain.DeliveryReserved {
			delivery.Status = domain.DeliveryRetryWait
			delivery.LeaseOwner = nil
			delivery.LeaseUntil = nil
		}
		aggregate, err := domain.RestoreDelivery(delivery)
		if err != nil {
			return nil, err
		}
		if err := aggregate.Reserve(workerID, now.Add(lease), now); err != nil {
			return nil, err
		}
		delivery = aggregate.Snapshot()
		q.deliveries[id] = delivery
		claims = append(claims, Claim{Task: task, Delivery: delivery})
		q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.reserved", DeliveryID: id, TaskID: task.ID, WorkerID: workerID, Attempt: delivery.Attempt, OccurredAt: now})
	}
	return claims, nil
}

func (q *MemoryQueue) RenewLease(ctx context.Context, deliveryID, workerID string, lease time.Duration) (domain.DeliverySnapshot, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delivery, ok := q.deliveries[deliveryID]
	if !ok {
		return domain.DeliverySnapshot{}, ErrNotFound
	}
	now := q.now().UTC()
	if delivery.Status != domain.DeliveryReserved || delivery.LeaseOwner == nil || *delivery.LeaseOwner != workerID || delivery.LeaseUntil == nil || !delivery.LeaseUntil.After(now) {
		return domain.DeliverySnapshot{}, &LeaseLostError{DeliveryID: deliveryID, WorkerID: workerID}
	}
	until := now.Add(lease)
	delivery.LeaseUntil = &until
	delivery.UpdatedAt = now
	q.deliveries[deliveryID] = delivery
	return delivery, nil
}

func (q *MemoryQueue) Ack(ctx context.Context, deliveryID, workerID string) (domain.DeliverySnapshot, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delivery, task, err := q.liveClaim(deliveryID, workerID)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if delivery.RunID == nil {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("acknowledged Delivery requires run_id")
	}
	run, ok := q.runs[*delivery.RunID]
	if !ok || !run.status.Terminal() {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Run outcome is not durable", nil)
	}
	aggregate, err := domain.RestoreDelivery(delivery)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := aggregate.Acknowledge(q.now().UTC()); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	out := aggregate.Snapshot()
	q.deliveries[deliveryID] = out
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.acknowledged", DeliveryID: deliveryID, TaskID: task.ID, WorkerID: workerID, Attempt: out.Attempt, OccurredAt: out.UpdatedAt})
	return out, nil
}

func (q *MemoryQueue) Nack(ctx context.Context, deliveryID, workerID string, code domain.ErrorCode, policy RetryPolicy) (domain.DeliverySnapshot, error) {
	if !domain.IsKnownErrorCode(code) || !domain.IsRetryableCode(code) {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("nack requires a retryable One-shot error code")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delivery, task, err := q.liveClaim(deliveryID, workerID)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if delivery.RunID != nil {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("Delivery with run_id cannot be automatically retried")
	}
	now := q.now().UTC()
	aggregate, err := domain.RestoreDelivery(delivery)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if delivery.Attempt >= delivery.MaxAttempts {
		if err := aggregate.DeadLetter(domain.ErrorDeliveryExhausted, now); err != nil {
			return domain.DeliverySnapshot{}, err
		}
		out := aggregate.Snapshot()
		q.deliveries[deliveryID] = out
		task.Status = domain.TaskFailed
		task.Version++
		task.UpdatedAt = now
		q.tasks[task.ID] = task
		return out, nil
	}
	available := now.Add(policy.Delay(delivery.Attempt))
	if err := aggregate.Nack(code, available, now); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	out := aggregate.Snapshot()
	q.deliveries[deliveryID] = out
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.retry_scheduled", DeliveryID: deliveryID, TaskID: task.ID, WorkerID: workerID, Attempt: out.Attempt, Code: code, AvailableAt: &available, OccurredAt: out.UpdatedAt})
	return out, nil
}

func (q *MemoryQueue) DeadLetter(ctx context.Context, deliveryID, workerID string, code domain.ErrorCode) (domain.DeliverySnapshot, error) {
	if !domain.IsKnownErrorCode(code) {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("dead-letter requires a known One-shot error code")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delivery, task, err := q.liveClaim(deliveryID, workerID)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	aggregate, err := domain.RestoreDelivery(delivery)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	now := q.now().UTC()
	if err := aggregate.DeadLetter(code, now); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	out := aggregate.Snapshot()
	q.deliveries[deliveryID] = out
	if task.Status == domain.TaskQueued {
		task.Status = domain.TaskFailed
		task.Version++
		task.UpdatedAt = now
		q.tasks[task.ID] = task
	}
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.dead_letter", DeliveryID: deliveryID, TaskID: task.ID, WorkerID: workerID, Attempt: out.Attempt, Code: code, OccurredAt: out.UpdatedAt})
	return out, nil
}

func (q *MemoryQueue) Cancel(ctx context.Context, deliveryID string, owner domain.Owner, workerID string) (domain.DeliverySnapshot, error) {
	if err := owner.Validate(); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delivery, ok := q.deliveries[deliveryID]
	if !ok {
		return domain.DeliverySnapshot{}, ErrNotFound
	}
	task := q.tasks[delivery.TaskID]
	if task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorForbidden, "Delivery owner mismatch", nil)
	}
	if delivery.Status == domain.DeliveryCancelled {
		return delivery, nil
	}
	if delivery.Status == domain.DeliveryReserved {
		if delivery.RunID != nil {
			return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorCancelFailed, "Reserved Delivery already owns a Run", nil)
		}
		leaseLive := delivery.LeaseUntil != nil && delivery.LeaseUntil.After(q.now().UTC())
		if leaseLive && (delivery.LeaseOwner == nil || *delivery.LeaseOwner != workerID) {
			return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorCancelFailed, "Live reserved Delivery is owned by another worker", nil)
		}
	}
	aggregate, err := domain.RestoreDelivery(delivery)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := aggregate.Cancel(true, q.now().UTC()); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	out := aggregate.Snapshot()
	q.deliveries[deliveryID] = out
	if task.Status == domain.TaskQueued {
		task.Status = domain.TaskCancelled
		task.Version++
		task.UpdatedAt = out.UpdatedAt
		q.tasks[task.ID] = task
	}
	return out, nil
}

func (q *MemoryQueue) liveClaim(deliveryID, workerID string) (domain.DeliverySnapshot, domain.TaskSnapshot, error) {
	delivery, ok := q.deliveries[deliveryID]
	if !ok {
		return domain.DeliverySnapshot{}, domain.TaskSnapshot{}, ErrNotFound
	}
	now := q.now().UTC()
	if delivery.Status != domain.DeliveryReserved || delivery.LeaseOwner == nil || *delivery.LeaseOwner != workerID || delivery.LeaseUntil == nil || !delivery.LeaseUntil.After(now) {
		return domain.DeliverySnapshot{}, domain.TaskSnapshot{}, &LeaseLostError{DeliveryID: deliveryID, WorkerID: workerID}
	}
	return delivery, q.tasks[delivery.TaskID], nil
}

// AttachTerminalRun is a test/executor seam: it atomically records the sole Run
// identity and terminal status before Ack is attempted.
func (q *MemoryQueue) AttachTerminalRun(deliveryID, runID string, status domain.RunStatus) error {
	if !status.Terminal() {
		return domain.InvalidRequestf("attached Run must be terminal")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delivery, ok := q.deliveries[deliveryID]
	if !ok {
		return ErrNotFound
	}
	if delivery.RunID != nil && *delivery.RunID != runID {
		return domain.NewDomainError(domain.ErrorRunConflict, "Delivery already owns another Run", nil)
	}
	delivery.RunID = &runID
	q.deliveries[deliveryID] = delivery
	q.runs[runID] = memoryRun{id: runID, status: status}
	return nil
}

func (q *MemoryQueue) GetDelivery(deliveryID string) (domain.DeliverySnapshot, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.deliveries[deliveryID]
	return item, ok
}

func (q *MemoryQueue) GetTask(taskID string) (domain.TaskSnapshot, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.tasks[taskID]
	return item, ok
}

// AcknowledgeRecovered completes a Delivery after a terminal Run was recovered
// or an earlier ACK failed. It does not require the expired worker lease.
func (q *MemoryQueue) AcknowledgeRecovered(ctx context.Context, deliveryID, runID string) (domain.DeliverySnapshot, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delivery, ok := q.deliveries[deliveryID]
	if !ok {
		return domain.DeliverySnapshot{}, ErrNotFound
	}
	if delivery.Status == domain.DeliveryAcknowledged {
		return delivery, nil
	}
	if delivery.RunID == nil || *delivery.RunID != runID {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Delivery is not attached to recovered Run", nil)
	}
	run, ok := q.runs[runID]
	if !ok || !run.status.Terminal() {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Recovered Run outcome is not terminal", nil)
	}
	now := q.now().UTC()
	delivery.Status = domain.DeliveryAcknowledged
	delivery.LeaseOwner = nil
	delivery.LeaseUntil = nil
	delivery.LastErrorCode = nil
	delivery.UpdatedAt = now
	q.deliveries[deliveryID] = delivery
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.acknowledged", DeliveryID: deliveryID, TaskID: delivery.TaskID, Attempt: delivery.Attempt, OccurredAt: now})
	return delivery, nil
}

// EnqueueContinue inserts a pending continue Delivery for an existing Task.
// It mirrors the store-backed continuation transaction for focused executor
// integration tests and keeps idempotency scoped to the canonical continue path.
func (q *MemoryQueue) EnqueueContinue(ctx context.Context, task domain.TaskSnapshot, delivery domain.DeliverySnapshot, canonicalPath string) (EnqueueResult, error) {
	if _, err := domain.RestoreTask(task); err != nil {
		return EnqueueResult{}, err
	}
	if _, err := domain.RestoreDelivery(delivery); err != nil {
		return EnqueueResult{}, err
	}
	if task.Status != domain.TaskQueued || delivery.Status != domain.DeliveryPending || delivery.Operation != domain.DeliveryContinue || delivery.TaskID != task.ID {
		return EnqueueResult{}, domain.InvalidRequestf("continue enqueue requires queued Task and pending continue Delivery")
	}
	if delivery.RequestedByKind != task.PrincipalKind || delivery.RequestedByID != task.PrincipalID {
		return EnqueueResult{}, domain.NewDomainError(domain.ErrorForbidden, "Task and Delivery owner mismatch", nil)
	}
	request := EnqueueRequest{
		Task: task, Delivery: delivery, Method: "POST", CanonicalPath: canonicalPath,
		IdempotencyKey: delivery.IdempotencyKey, PayloadSHA256: delivery.PayloadSHA256,
	}
	if strings.TrimSpace(canonicalPath) == "" || strings.TrimSpace(delivery.IdempotencyKey) == "" || len(delivery.PayloadSHA256) != 64 {
		return EnqueueResult{}, domain.InvalidRequestf("continue canonical path, idempotency key, and payload hash are required")
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	scope := replayScope(request)
	if replay, ok := q.replays[scope]; ok {
		if replay.payloadSHA != request.PayloadSHA256 {
			return EnqueueResult{}, domain.NewDomainError(domain.ErrorIdempotencyConflict, "Idempotency-Key was reused with a different payload", nil)
		}
		return EnqueueResult{Task: replay.task, Delivery: replay.delivery, Created: false}, nil
	}
	q.tasks[task.ID] = task
	q.deliveries[delivery.ID] = delivery
	q.replays[scope] = memoryReplay{payloadSHA: delivery.PayloadSHA256, task: task, delivery: delivery}
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.queued", DeliveryID: delivery.ID, TaskID: task.ID, OccurredAt: delivery.UpdatedAt})
	return EnqueueResult{Task: task, Delivery: delivery, Created: true}, nil
}
