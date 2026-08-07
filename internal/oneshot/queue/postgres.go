package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	oneshootstore "github.com/opendray/opendray-v2/internal/oneshot/store"
)

const deliveryColumns = `
d.id,d.task_id,d.operation,d.requested_by_kind,d.requested_by_id,d.input,
d.idempotency_key,d.payload_sha256,d.status,d.attempt,d.max_attempts,
d.available_at,d.lease_owner,d.lease_until,d.run_id,d.last_error_code,d.created_at,d.updated_at`

const taskColumns = `
t.id,t.principal_kind,t.principal_id,t.project_id,t.provider_id,t.model,t.source,t.prompt,
t.status,t.current_run_id,t.runtime_context_id,t.version,t.created_at,t.updated_at`

// PostgresQueue is the production reliable One-shot execution queue.
type PostgresQueue struct {
	pool  *pgxpool.Pool
	audit AuditSink
}

// NewPostgresQueue creates a queue using the shared application pool.
func NewPostgresQueue(pool *pgxpool.Pool, audit AuditSink) *PostgresQueue {
	if audit == nil {
		audit = nopAuditSink{}
	}
	return &PostgresQueue{pool: pool, audit: audit}
}

func (q *PostgresQueue) available() error {
	if q == nil || q.pool == nil {
		return domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot queue is unavailable", nil)
	}
	return nil
}

func validateEnqueue(request EnqueueRequest) error {
	if _, err := domain.RestoreTask(request.Task); err != nil {
		return err
	}
	if _, err := domain.RestoreDelivery(request.Delivery); err != nil {
		return err
	}
	if request.Task.Status != domain.TaskQueued || request.Delivery.Status != domain.DeliveryPending {
		return domain.InvalidRequestf("enqueue requires queued Task and pending Delivery")
	}
	if request.Delivery.Operation != domain.DeliveryNew || request.Delivery.TaskID != request.Task.ID {
		return domain.InvalidRequestf("enqueue requires the Task initial Delivery")
	}
	if request.Delivery.RequestedByKind != request.Task.PrincipalKind || request.Delivery.RequestedByID != request.Task.PrincipalID {
		return domain.NewDomainError(domain.ErrorForbidden, "Task and Delivery owner mismatch", nil)
	}
	if strings.TrimSpace(request.Method) == "" || strings.TrimSpace(request.CanonicalPath) == "" {
		return domain.InvalidRequestf("method and canonical path are required")
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return domain.NewDomainError(domain.ErrorIdempotencyRequired, "Idempotency-Key is required", nil)
	}
	if len(request.PayloadSHA256) != 64 {
		return domain.InvalidRequestf("payload_sha256 must contain 64 lowercase hex characters")
	}
	for _, ch := range request.PayloadSHA256 {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return domain.InvalidRequestf("payload_sha256 must contain 64 lowercase hex characters")
		}
	}
	return nil
}

// Enqueue creates Task, initial Delivery, and replay metadata in one serializable
// transaction. Same scope/key/payload returns the original resources.
func (q *PostgresQueue) Enqueue(ctx context.Context, request EnqueueRequest) (EnqueueResult, error) {
	if err := q.available(); err != nil {
		return EnqueueResult{}, err
	}
	if err := validateEnqueue(request); err != nil {
		return EnqueueResult{}, err
	}

	tx, err := q.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return EnqueueResult{}, queueError("begin enqueue transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	owner := domain.Owner{Kind: request.Task.PrincipalKind, ID: request.Task.PrincipalID}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	path := strings.TrimSpace(request.CanonicalPath)
	key := strings.TrimSpace(request.IdempotencyKey)

	// Expired keys are not valid replay identities and must not permanently
	// block reuse of the same external key.
	if _, err := tx.Exec(ctx, `DELETE FROM oneshot_idempotency_keys
WHERE principal_kind=$1 AND principal_id=$2 AND method=$3 AND canonical_path=$4
  AND idempotency_key=$5 AND expires_at IS NOT NULL AND expires_at<=clock_timestamp()`,
		owner.Kind, owner.ID, method, path, key); err != nil {
		return EnqueueResult{}, queueError("remove expired idempotency key", err)
	}

	var inserted int
	err = tx.QueryRow(ctx, `INSERT INTO oneshot_idempotency_keys (
    principal_kind,principal_id,method,canonical_path,idempotency_key,payload_sha256,expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (principal_kind,principal_id,method,canonical_path,idempotency_key) DO NOTHING
RETURNING 1`, owner.Kind, owner.ID, method, path, key, request.PayloadSHA256, request.ExpiresAt).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		result, replayErr := loadReplay(ctx, tx, owner, method, path, key, request.PayloadSHA256)
		if replayErr != nil {
			return EnqueueResult{}, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return EnqueueResult{}, queueError("commit idempotent replay", err)
		}
		return result, nil
	}
	if err != nil {
		return EnqueueResult{}, queueError("insert idempotency key", err)
	}

	persistedTask, err := insertTask(ctx, tx, request.Task)
	if err != nil {
		return EnqueueResult{}, err
	}
	persistedDelivery, err := insertDelivery(ctx, tx, request.Delivery)
	if err != nil {
		return EnqueueResult{}, err
	}
	if err := oneshootstore.BindDeliveryAttachments(ctx, tx, owner, persistedTask.ProjectID, persistedTask.ID, persistedDelivery.ID, persistedDelivery.Input.AttachmentRefs); err != nil {
		return EnqueueResult{}, err
	}
	if err := insertInitialTaskLifecycle(ctx, tx, persistedTask); err != nil {
		return EnqueueResult{}, err
	}
	response, _ := json.Marshal(struct {
		Task     domain.TaskSnapshot     `json:"task"`
		Delivery domain.DeliverySnapshot `json:"delivery"`
	}{Task: persistedTask, Delivery: persistedDelivery})
	result, err := tx.Exec(ctx, `UPDATE oneshot_idempotency_keys SET
    resource_kind='task',resource_id=$6,response_status=202,response_body=$7::jsonb
WHERE principal_kind=$1 AND principal_id=$2 AND method=$3 AND canonical_path=$4
  AND idempotency_key=$5`, owner.Kind, owner.ID, method, path, key, persistedTask.ID, response)
	if err != nil {
		return EnqueueResult{}, queueError("complete idempotency key", err)
	}
	if result.RowsAffected() != 1 {
		return EnqueueResult{}, domain.NewDomainError(domain.ErrorQueueUnavailable, "Idempotency record disappeared during enqueue", nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return EnqueueResult{}, queueError("commit enqueue transaction", err)
	}

	q.audit.RecordQueueEvent(ctx, AuditEvent{
		Type: "oneshot.delivery.queued", DeliveryID: persistedDelivery.ID,
		TaskID: persistedTask.ID, Attempt: persistedDelivery.Attempt,
		OccurredAt: persistedDelivery.UpdatedAt,
	})
	return EnqueueResult{Task: persistedTask, Delivery: persistedDelivery, Created: true}, nil
}

func loadReplay(ctx context.Context, tx pgx.Tx, owner domain.Owner, method, path, key, payloadSHA string) (EnqueueResult, error) {
	var storedHash string
	var resourceKind, resourceID *string
	var responseRaw []byte
	err := tx.QueryRow(ctx, `SELECT payload_sha256,resource_kind,resource_id,response_body
FROM oneshot_idempotency_keys
WHERE principal_kind=$1 AND principal_id=$2 AND method=$3 AND canonical_path=$4
  AND idempotency_key=$5
FOR UPDATE`, owner.Kind, owner.ID, method, path, key).Scan(&storedHash, &resourceKind, &resourceID, &responseRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnqueueResult{}, domain.NewDomainError(domain.ErrorQueueUnavailable, "Idempotency replay record is unavailable", nil)
	}
	if err != nil {
		return EnqueueResult{}, queueError("load idempotency replay", err)
	}
	if storedHash != payloadSHA {
		return EnqueueResult{}, domain.NewDomainError(domain.ErrorIdempotencyConflict, "Idempotency-Key was reused with a different payload", nil)
	}
	if resourceKind == nil || resourceID == nil || *resourceKind != "task" {
		return EnqueueResult{}, domain.NewDomainError(domain.ErrorQueueUnavailable, "Idempotency request has no committed Task", nil)
	}
	if len(responseRaw) > 0 && string(responseRaw) != "null" {
		var original struct {
			Task     domain.TaskSnapshot     `json:"task"`
			Delivery domain.DeliverySnapshot `json:"delivery"`
		}
		if err := json.Unmarshal(responseRaw, &original); err != nil {
			return EnqueueResult{}, queueError("decode idempotency response", err)
		}
		if original.Task.ID == *resourceID {
			if _, err := domain.RestoreTask(original.Task); err == nil {
				if _, err := domain.RestoreDelivery(original.Delivery); err == nil {
					return EnqueueResult{Task: original.Task, Delivery: original.Delivery, Created: false}, nil
				}
			}
		}
	}
	task, err := selectTask(ctx, tx, owner, *resourceID)
	if err != nil {
		return EnqueueResult{}, err
	}
	delivery, err := selectInitialDelivery(ctx, tx, owner, task.ID)
	if err != nil {
		return EnqueueResult{}, err
	}
	return EnqueueResult{Task: task, Delivery: delivery, Created: false}, nil
}

func insertTask(ctx context.Context, tx pgx.Tx, snapshot domain.TaskSnapshot) (domain.TaskSnapshot, error) {
	source, err := json.Marshal(snapshot.Source)
	if err != nil {
		return domain.TaskSnapshot{}, domain.InvalidRequestf("task source is not JSON-compatible")
	}
	out, err := scanTask(tx.QueryRow(ctx, `INSERT INTO oneshot_tasks (
    id,principal_kind,principal_id,project_id,provider_id,model,source,source_kind,
    source_channel_id,source_message_id,prompt,status,current_run_id,
    runtime_context_id,version,created_at,updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
RETURNING `+strings.ReplaceAll(taskColumns, "t.", ""),
		snapshot.ID, snapshot.PrincipalKind, snapshot.PrincipalID, snapshot.ProjectID,
		snapshot.ProviderID, snapshot.Model, source, snapshot.Source.Kind, nullableString(snapshot.Source.ChannelID),
		nullableString(snapshot.Source.SourceMessageID), snapshot.Prompt, snapshot.Status,
		snapshot.CurrentRunID, snapshot.RuntimeContextID, snapshot.Version,
		snapshot.CreatedAt.UTC(), snapshot.UpdatedAt.UTC()))
	if err != nil {
		return domain.TaskSnapshot{}, mapWriteError("insert queued Task", err)
	}
	return out, nil
}

func insertInitialTaskLifecycle(ctx context.Context, tx pgx.Tx, snapshot domain.TaskSnapshot) error {
	created := snapshot
	created.Status = domain.TaskPending
	created.CurrentRunID = nil
	created.RuntimeContextID = nil
	created.Version = 1
	created.UpdatedAt = created.CreatedAt
	for _, event := range []struct {
		status  domain.TaskStatus
		version int64
		when    time.Time
		task    domain.TaskSnapshot
	}{
		{status: domain.TaskPending, version: 1, when: created.CreatedAt, task: created},
		{status: snapshot.Status, version: snapshot.Version, when: snapshot.UpdatedAt, task: snapshot},
	} {
		payload, err := json.Marshal(map[string]any{
			"task_id": event.task.ID, "status": event.status,
			"principal_kind": event.task.PrincipalKind, "principal_id": event.task.PrincipalID,
			"project_id": event.task.ProjectID, "provider_id": event.task.ProviderID,
			"source": event.task.Source, "version": event.version,
		})
		if err != nil {
			return domain.InvalidRequestf("Task lifecycle payload is not JSON-compatible")
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO oneshot_lifecycle_events (
    principal_kind,principal_id,project_id,task_id,run_id,aggregate_kind,
    aggregate_id,sequence,topic,data,occurred_at
) VALUES ($1,$2,$3,$4,NULL,'task',$4,$5,$6,$7,$8)
ON CONFLICT (aggregate_kind,aggregate_id,sequence) DO NOTHING`,
			event.task.PrincipalKind, event.task.PrincipalID, event.task.ProjectID,
			event.task.ID, event.version, "oneshot.task."+string(event.status), payload, event.when.UTC()); err != nil {
			return mapWriteError("insert queued Task lifecycle", err)
		}
	}
	return nil
}

func insertDelivery(ctx context.Context, tx pgx.Tx, snapshot domain.DeliverySnapshot) (domain.DeliverySnapshot, error) {
	input, err := json.Marshal(snapshot.Input)
	if err != nil {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("delivery input is not JSON-compatible")
	}
	out, err := scanDelivery(tx.QueryRow(ctx, `INSERT INTO oneshot_deliveries (
    id,task_id,operation,requested_by_kind,requested_by_id,input,idempotency_key,
    payload_sha256,status,attempt,max_attempts,available_at,lease_owner,lease_until,
    run_id,last_error_code,created_at,updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
RETURNING `+strings.ReplaceAll(deliveryColumns, "d.", ""),
		snapshot.ID, snapshot.TaskID, snapshot.Operation, snapshot.RequestedByKind,
		snapshot.RequestedByID, input, snapshot.IdempotencyKey, snapshot.PayloadSHA256,
		snapshot.Status, snapshot.Attempt, snapshot.MaxAttempts, snapshot.AvailableAt.UTC(),
		snapshot.LeaseOwner, snapshot.LeaseUntil, snapshot.RunID, snapshot.LastErrorCode,
		snapshot.CreatedAt.UTC(), snapshot.UpdatedAt.UTC()))
	if err != nil {
		return domain.DeliverySnapshot{}, mapWriteError("insert queued Delivery", err)
	}
	return out, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ClaimDue atomically dead-letters exhausted unstarted rows and reserves
// runnable Deliveries using FOR UPDATE SKIP LOCKED. Attached Runs are owned by
// the execution Saga Reconciler.
func (q *PostgresQueue) ClaimDue(ctx context.Context, workerID string, limit int, lease time.Duration) ([]Claim, error) {
	if err := q.available(); err != nil {
		return nil, err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil, domain.InvalidRequestf("worker_id is required")
	}
	if limit <= 0 {
		limit = DefaultClaimLimit
	}
	if limit > 256 {
		limit = 256
	}
	if lease <= 0 {
		lease = DefaultLease
	}

	tx, err := q.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, queueError("begin claim transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Deliveries with an attached Run are excluded from dispatch. Terminal
	// Run acknowledgement is owned by the execution Saga Reconciler so ACK
	// failure, credential release, and StageAcknowledged remain one auditable
	// recovery path.
	dead, err := deadLetterExhausted(ctx, tx, limit)
	if err != nil {
		return nil, err
	}

	leaseMillis := lease.Milliseconds()
	if leaseMillis < 1 {
		leaseMillis = 1
	}
	rows, err := tx.Query(ctx, `WITH due AS (
    SELECT d.id
    FROM oneshot_deliveries d
    JOIN oneshot_tasks t ON t.id=d.task_id
    WHERE t.status='queued'
      AND d.run_id IS NULL
      AND d.attempt < d.max_attempts
      AND (
        (d.status IN ('pending','retry_wait') AND d.available_at<=clock_timestamp())
        OR (d.status='reserved' AND d.lease_until<=clock_timestamp())
      )
    ORDER BY d.available_at,d.created_at,d.id
    FOR UPDATE OF d SKIP LOCKED
    LIMIT $1
), claimed AS (
    UPDATE oneshot_deliveries d SET
      status='reserved',attempt=d.attempt+1,lease_owner=$2,
      lease_until=clock_timestamp()+($3::bigint * interval '1 millisecond'),
      updated_at=clock_timestamp()
    FROM due WHERE d.id=due.id
    RETURNING d.*
)
SELECT `+taskColumns+`,`+strings.ReplaceAll(deliveryColumns, "d.", "c.")+`
FROM claimed c JOIN oneshot_tasks t ON t.id=c.task_id
ORDER BY c.available_at,c.created_at,c.id`, limit, workerID, leaseMillis)
	if err != nil {
		return nil, queueError("claim due Deliveries", err)
	}
	claims := make([]Claim, 0, limit)
	for rows.Next() {
		task, delivery, scanErr := scanClaim(rows)
		if scanErr != nil {
			rows.Close()
			return nil, queueError("scan claimed Delivery", scanErr)
		}
		claims = append(claims, Claim{Task: task, Delivery: delivery})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, queueError("iterate claimed Deliveries", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, queueError("commit claim transaction", err)
	}

	for _, item := range dead {
		q.audit.RecordQueueEvent(ctx, item)
	}
	for _, claim := range claims {
		q.audit.RecordQueueEvent(ctx, AuditEvent{
			Type: "oneshot.delivery.reserved", DeliveryID: claim.Delivery.ID,
			TaskID: claim.Task.ID, WorkerID: workerID, Attempt: claim.Delivery.Attempt,
			OccurredAt: claim.Delivery.UpdatedAt,
		})
	}
	return claims, nil
}

func deadLetterExhausted(ctx context.Context, tx pgx.Tx, limit int) ([]AuditEvent, error) {
	rows, err := tx.Query(ctx, `WITH exhausted AS (
    SELECT d.id,d.task_id
    FROM oneshot_deliveries d JOIN oneshot_tasks t ON t.id=d.task_id
    WHERE t.status='queued' AND d.run_id IS NULL AND d.attempt>=d.max_attempts
      AND ((d.status IN ('pending','retry_wait') AND d.available_at<=clock_timestamp())
        OR (d.status='reserved' AND d.lease_until<=clock_timestamp()))
    ORDER BY d.available_at,d.id
    FOR UPDATE OF d SKIP LOCKED
    LIMIT $1
), updated AS (
    UPDATE oneshot_deliveries d SET status='dead_letter',lease_owner=NULL,lease_until=NULL,
      last_error_code='oneshot.delivery_exhausted',updated_at=clock_timestamp()
    FROM exhausted x WHERE d.id=x.id
    RETURNING d.id,d.task_id,d.attempt,d.updated_at
), task_update AS (
    UPDATE oneshot_tasks t SET status='failed',current_run_id=NULL,
      version=t.version+1,updated_at=clock_timestamp()
    FROM updated u WHERE t.id=u.task_id AND t.status='queued'
    RETURNING t.id
)
SELECT id,task_id,attempt,updated_at FROM updated`, limit)
	if err != nil {
		return nil, queueError("dead-letter exhausted Deliveries", err)
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		if err := rows.Scan(&event.DeliveryID, &event.TaskID, &event.Attempt, &event.OccurredAt); err != nil {
			return nil, err
		}
		event.Type = "oneshot.delivery.dead_letter"
		event.Code = domain.ErrorDeliveryExhausted
		events = append(events, event)
	}
	return events, rows.Err()
}

func (q *PostgresQueue) RenewLease(ctx context.Context, deliveryID, workerID string, lease time.Duration) (domain.DeliverySnapshot, error) {
	if err := q.available(); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if strings.TrimSpace(workerID) == "" || lease <= 0 {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("worker_id and positive lease are required")
	}
	leaseMillis := lease.Milliseconds()
	if leaseMillis < 1 {
		leaseMillis = 1
	}
	out, err := scanDelivery(q.pool.QueryRow(ctx, `UPDATE oneshot_deliveries d SET
    lease_until=clock_timestamp()+($3::bigint * interval '1 millisecond'),updated_at=clock_timestamp()
WHERE d.id=$1 AND d.status='reserved' AND d.lease_owner=$2 AND d.lease_until>clock_timestamp()
RETURNING `+deliveryColumns, deliveryID, workerID, leaseMillis))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeliverySnapshot{}, q.leaseError(ctx, deliveryID, workerID)
	}
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("renew Delivery lease", err)
	}
	return out, nil
}

func (q *PostgresQueue) Ack(ctx context.Context, deliveryID, workerID string) (domain.DeliverySnapshot, error) {
	if err := q.available(); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	out, err := scanDelivery(q.pool.QueryRow(ctx, `UPDATE oneshot_deliveries d SET
    status='acknowledged',lease_owner=NULL,lease_until=NULL,last_error_code=NULL,
    updated_at=clock_timestamp()
WHERE d.id=$1 AND d.status='reserved' AND d.lease_owner=$2
  AND d.lease_until>clock_timestamp() AND d.run_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM oneshot_runs r WHERE r.id=d.run_id
      AND r.status IN ('waiting_input','completed','failed','cancelled','timed_out'))
RETURNING `+deliveryColumns, deliveryID, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeliverySnapshot{}, q.leaseError(ctx, deliveryID, workerID)
	}
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("ack Delivery", err)
	}
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.acknowledged", DeliveryID: out.ID, TaskID: out.TaskID, WorkerID: workerID, Attempt: out.Attempt, OccurredAt: out.UpdatedAt})
	return out, nil
}

func (q *PostgresQueue) Nack(ctx context.Context, deliveryID, workerID string, code domain.ErrorCode, policy RetryPolicy) (domain.DeliverySnapshot, error) {
	if !domain.IsKnownErrorCode(code) || !domain.IsRetryableCode(code) {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("nack requires a retryable One-shot error code")
	}
	if err := q.available(); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	tx, err := q.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("begin nack transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	claim, leaseLive, err := lockClaim(ctx, tx, deliveryID)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := requireLiveLease(claim.Delivery, workerID, leaseLive); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if claim.Delivery.RunID != nil {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("Delivery with run_id cannot be automatically retried")
	}

	if claim.Delivery.Attempt >= claim.Delivery.MaxAttempts {
		out, err := deadLetterLocked(ctx, tx, claim, workerID, domain.ErrorDeliveryExhausted)
		if err != nil {
			return domain.DeliverySnapshot{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.DeliverySnapshot{}, queueError("commit exhausted nack", err)
		}
		q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.dead_letter", DeliveryID: out.ID, TaskID: out.TaskID, WorkerID: workerID, Attempt: out.Attempt, Code: domain.ErrorDeliveryExhausted, OccurredAt: out.UpdatedAt})
		return out, nil
	}

	delay := policy.Delay(claim.Delivery.Attempt)
	out, err := scanDelivery(tx.QueryRow(ctx, `UPDATE oneshot_deliveries d SET
    status='retry_wait',available_at=clock_timestamp()+($4::bigint * interval '1 millisecond'),
    lease_owner=NULL,lease_until=NULL,last_error_code=$3,updated_at=clock_timestamp()
WHERE d.id=$1 AND d.status='reserved' AND d.lease_owner=$2
RETURNING `+deliveryColumns, deliveryID, workerID, code, delay.Milliseconds()))
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("nack Delivery", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeliverySnapshot{}, queueError("commit nack transaction", err)
	}
	available := out.AvailableAt
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.retry_scheduled", DeliveryID: out.ID, TaskID: out.TaskID, WorkerID: workerID, Attempt: out.Attempt, Code: code, AvailableAt: &available, OccurredAt: out.UpdatedAt})
	return out, nil
}

func (q *PostgresQueue) DeadLetter(ctx context.Context, deliveryID, workerID string, code domain.ErrorCode) (domain.DeliverySnapshot, error) {
	if !domain.IsKnownErrorCode(code) {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("dead-letter requires a known One-shot error code")
	}
	if err := q.available(); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	tx, err := q.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("begin dead-letter transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	claim, leaseLive, err := lockClaim(ctx, tx, deliveryID)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := requireLiveLease(claim.Delivery, workerID, leaseLive); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if claim.Delivery.RunID != nil {
		return domain.DeliverySnapshot{}, domain.InvalidRequestf("Delivery with run_id cannot be dead-lettered by the dispatch queue")
	}
	out, err := deadLetterLocked(ctx, tx, claim, workerID, code)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeliverySnapshot{}, queueError("commit dead-letter transaction", err)
	}
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.dead_letter", DeliveryID: out.ID, TaskID: out.TaskID, WorkerID: workerID, Attempt: out.Attempt, Code: code, OccurredAt: out.UpdatedAt})
	return out, nil
}

func deadLetterLocked(ctx context.Context, tx pgx.Tx, claim Claim, workerID string, code domain.ErrorCode) (domain.DeliverySnapshot, error) {
	out, err := scanDelivery(tx.QueryRow(ctx, `UPDATE oneshot_deliveries d SET
    status='dead_letter',lease_owner=NULL,lease_until=NULL,last_error_code=$3,
    updated_at=clock_timestamp()
WHERE d.id=$1 AND d.status='reserved' AND d.lease_owner=$2
RETURNING `+deliveryColumns, claim.Delivery.ID, workerID, code))
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("dead-letter Delivery", err)
	}
	if claim.Task.Status == domain.TaskQueued {
		result, updateErr := tx.Exec(ctx, `UPDATE oneshot_tasks SET status='failed',current_run_id=NULL,
    version=version+1,updated_at=clock_timestamp()
WHERE id=$1 AND status='queued'`, claim.Task.ID)
		if updateErr != nil {
			return domain.DeliverySnapshot{}, queueError("fail Task after dead-letter", updateErr)
		}
		if result.RowsAffected() != 1 {
			return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Task state changed during dead-letter", nil)
		}
	}
	return out, nil
}

func (q *PostgresQueue) Cancel(ctx context.Context, deliveryID string, owner domain.Owner, workerID string) (domain.DeliverySnapshot, error) {
	if err := owner.Validate(); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := q.available(); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	tx, err := q.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("begin cancel transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	claim, leaseLive, err := lockClaim(ctx, tx, deliveryID)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if claim.Task.PrincipalKind != owner.Kind || claim.Task.PrincipalID != owner.ID {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorForbidden, "Delivery owner mismatch", nil)
	}
	if claim.Delivery.Status == domain.DeliveryCancelled {
		if err := tx.Commit(ctx); err != nil {
			return domain.DeliverySnapshot{}, queueError("commit cancel replay", err)
		}
		return claim.Delivery, nil
	}
	if claim.Delivery.Status == domain.DeliveryAcknowledged || claim.Delivery.Status == domain.DeliveryDeadLetter {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorInvalidTransition, "Terminal Delivery cannot be cancelled", nil)
	}
	if claim.Delivery.Status == domain.DeliveryReserved {
		if claim.Delivery.RunID != nil {
			return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorCancelFailed, "Reserved Delivery already owns a Run", nil)
		}
		if leaseLive && (workerID == "" || claim.Delivery.LeaseOwner == nil || *claim.Delivery.LeaseOwner != workerID) {
			return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorCancelFailed, "Live reserved Delivery is owned by another worker", nil)
		}
	}
	out, err := scanDelivery(tx.QueryRow(ctx, `UPDATE oneshot_deliveries d SET
    status='cancelled',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE d.id=$1 AND d.status IN ('pending','retry_wait','reserved')
RETURNING `+deliveryColumns, deliveryID))
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("cancel Delivery", err)
	}
	if claim.Task.Status == domain.TaskQueued {
		if _, err := tx.Exec(ctx, `UPDATE oneshot_tasks SET status='cancelled',current_run_id=NULL,
    version=version+1,updated_at=clock_timestamp()
WHERE id=$1 AND status='queued'`, claim.Task.ID); err != nil {
			return domain.DeliverySnapshot{}, queueError("cancel queued Task", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeliverySnapshot{}, queueError("commit cancel transaction", err)
	}
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.cancelled", DeliveryID: out.ID, TaskID: out.TaskID, WorkerID: workerID, Attempt: out.Attempt, OccurredAt: out.UpdatedAt})
	return out, nil
}

func lockClaim(ctx context.Context, tx pgx.Tx, deliveryID string) (Claim, bool, error) {
	row := tx.QueryRow(ctx, `SELECT `+taskColumns+`,`+deliveryColumns+`,
       COALESCE(d.lease_until>clock_timestamp(),false)
FROM oneshot_deliveries d JOIN oneshot_tasks t ON t.id=d.task_id
WHERE d.id=$1
FOR UPDATE OF d,t`, deliveryID)
	var task domain.TaskSnapshot
	var sourceRaw []byte
	var delivery domain.DeliverySnapshot
	var inputRaw []byte
	var leaseLive bool
	if err := row.Scan(
		&task.ID, &task.PrincipalKind, &task.PrincipalID, &task.ProjectID, &task.ProviderID,
		&sourceRaw, &task.Prompt, &task.Status, &task.CurrentRunID, &task.RuntimeContextID,
		&task.Version, &task.CreatedAt, &task.UpdatedAt,
		&delivery.ID, &delivery.TaskID, &delivery.Operation, &delivery.RequestedByKind,
		&delivery.RequestedByID, &inputRaw, &delivery.IdempotencyKey, &delivery.PayloadSHA256,
		&delivery.Status, &delivery.Attempt, &delivery.MaxAttempts, &delivery.AvailableAt,
		&delivery.LeaseOwner, &delivery.LeaseUntil, &delivery.RunID, &delivery.LastErrorCode,
		&delivery.CreatedAt, &delivery.UpdatedAt, &leaseLive,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Claim{}, false, ErrNotFound
		}
		return Claim{}, false, queueError("lock Delivery", err)
	}
	if err := json.Unmarshal(sourceRaw, &task.Source); err != nil {
		return Claim{}, false, queueError("decode Task source", err)
	}
	if err := json.Unmarshal(inputRaw, &delivery.Input); err != nil {
		return Claim{}, false, queueError("decode Delivery input", err)
	}
	restoredTask, err := domain.RestoreTask(task)
	if err != nil {
		return Claim{}, false, err
	}
	restoredDelivery, err := domain.RestoreDelivery(delivery)
	if err != nil {
		return Claim{}, false, err
	}
	return Claim{Task: restoredTask.Snapshot(), Delivery: restoredDelivery.Snapshot()}, leaseLive, nil
}

func requireLiveLease(delivery domain.DeliverySnapshot, workerID string, leaseLive bool) error {
	if delivery.Status != domain.DeliveryReserved || delivery.LeaseOwner == nil || *delivery.LeaseOwner != workerID || delivery.LeaseUntil == nil || !leaseLive {
		return &LeaseLostError{DeliveryID: delivery.ID, WorkerID: workerID}
	}
	return nil
}

func (q *PostgresQueue) leaseError(ctx context.Context, deliveryID, workerID string) error {
	var exists bool
	if err := q.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM oneshot_deliveries WHERE id=$1)`, deliveryID).Scan(&exists); err != nil {
		return queueError("inspect lost lease", err)
	}
	if !exists {
		return ErrNotFound
	}
	return &LeaseLostError{DeliveryID: deliveryID, WorkerID: workerID}
}

func selectTask(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, owner domain.Owner, taskID string) (domain.TaskSnapshot, error) {
	out, err := scanTask(q.QueryRow(ctx, `SELECT `+taskColumns+` FROM oneshot_tasks t
WHERE t.id=$1 AND t.principal_kind=$2 AND t.principal_id=$3`, taskID, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TaskSnapshot{}, domain.NewDomainError(domain.ErrorTaskNotFound, "Task not found", nil)
	}
	if err != nil {
		return domain.TaskSnapshot{}, queueError("select Task", err)
	}
	return out, nil
}

func selectInitialDelivery(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, owner domain.Owner, taskID string) (domain.DeliverySnapshot, error) {
	out, err := scanDelivery(q.QueryRow(ctx, `SELECT `+deliveryColumns+`
FROM oneshot_deliveries d JOIN oneshot_tasks t ON t.id=d.task_id
WHERE d.task_id=$1 AND d.operation='new' AND t.principal_kind=$2 AND t.principal_id=$3
ORDER BY d.created_at,d.id LIMIT 1`, taskID, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorQueueUnavailable, "Initial Delivery not found", nil)
	}
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("select initial Delivery", err)
	}
	return out, nil
}

type scanner interface{ Scan(...any) error }

func scanClaim(row scanner) (domain.TaskSnapshot, domain.DeliverySnapshot, error) {
	var task domain.TaskSnapshot
	var sourceRaw []byte
	var delivery domain.DeliverySnapshot
	var inputRaw []byte
	if err := row.Scan(
		&task.ID, &task.PrincipalKind, &task.PrincipalID, &task.ProjectID, &task.ProviderID,
		&task.Model, &sourceRaw, &task.Prompt, &task.Status, &task.CurrentRunID, &task.RuntimeContextID,
		&task.Version, &task.CreatedAt, &task.UpdatedAt,
		&delivery.ID, &delivery.TaskID, &delivery.Operation, &delivery.RequestedByKind,
		&delivery.RequestedByID, &inputRaw, &delivery.IdempotencyKey, &delivery.PayloadSHA256,
		&delivery.Status, &delivery.Attempt, &delivery.MaxAttempts, &delivery.AvailableAt,
		&delivery.LeaseOwner, &delivery.LeaseUntil, &delivery.RunID, &delivery.LastErrorCode,
		&delivery.CreatedAt, &delivery.UpdatedAt,
	); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := json.Unmarshal(sourceRaw, &task.Source); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, fmt.Errorf("decode Task source: %w", err)
	}
	if err := json.Unmarshal(inputRaw, &delivery.Input); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, fmt.Errorf("decode Delivery input: %w", err)
	}
	restoredTask, err := domain.RestoreTask(task)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	restoredDelivery, err := domain.RestoreDelivery(delivery)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	return restoredTask.Snapshot(), restoredDelivery.Snapshot(), nil
}

func scanTask(row scanner) (domain.TaskSnapshot, error) {
	var task domain.TaskSnapshot
	var sourceRaw []byte
	if err := row.Scan(&task.ID, &task.PrincipalKind, &task.PrincipalID, &task.ProjectID,
		&task.ProviderID, &task.Model, &sourceRaw, &task.Prompt, &task.Status, &task.CurrentRunID,
		&task.RuntimeContextID, &task.Version, &task.CreatedAt, &task.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, err
	}
	if err := json.Unmarshal(sourceRaw, &task.Source); err != nil {
		return domain.TaskSnapshot{}, err
	}
	restored, err := domain.RestoreTask(task)
	if err != nil {
		return domain.TaskSnapshot{}, err
	}
	return restored.Snapshot(), nil
}

func scanDelivery(row scanner) (domain.DeliverySnapshot, error) {
	var delivery domain.DeliverySnapshot
	var inputRaw []byte
	if err := row.Scan(&delivery.ID, &delivery.TaskID, &delivery.Operation,
		&delivery.RequestedByKind, &delivery.RequestedByID, &inputRaw,
		&delivery.IdempotencyKey, &delivery.PayloadSHA256, &delivery.Status,
		&delivery.Attempt, &delivery.MaxAttempts, &delivery.AvailableAt,
		&delivery.LeaseOwner, &delivery.LeaseUntil, &delivery.RunID,
		&delivery.LastErrorCode, &delivery.CreatedAt, &delivery.UpdatedAt); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := json.Unmarshal(inputRaw, &delivery.Input); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	restored, err := domain.RestoreDelivery(delivery)
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	return restored.Snapshot(), nil
}

func mapWriteError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if strings.Contains(pgErr.ConstraintName, "idempotency") || strings.Contains(pgErr.ConstraintName, "channel_source") {
				return domain.NewDomainError(domain.ErrorIdempotencyConflict, "Idempotency-Key or source message already exists", err)
			}
			if strings.Contains(pgErr.ConstraintName, "one_active_per_task") || strings.Contains(pgErr.ConstraintName, "run_id") {
				return domain.NewDomainError(domain.ErrorRunConflict, "Run already exists", err)
			}
			return domain.NewDomainError(domain.ErrorInvalidRequest, "Unique constraint rejected queue data", err)
		case "40001", "40P01", "55P03":
			return domain.NewDomainError(domain.ErrorQueueUnavailable, "Queue concurrency conflict", err)
		case "23503", "23514", "23502", "22P02":
			return domain.NewDomainError(domain.ErrorInvalidRequest, "Database constraint rejected queue data", err)
		}
	}
	return queueError(operation, err)
}

func queueError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot queue operation failed: "+operation, fmt.Errorf("%s: %w", operation, err))
}

// AcknowledgeRecovered completes a Delivery whose attached Run is terminal,
// regardless of the previous worker lease. It is used only by crash recovery
// and is idempotent for an already acknowledged Delivery.
func (q *PostgresQueue) AcknowledgeRecovered(ctx context.Context, deliveryID, runID string) (domain.DeliverySnapshot, error) {
	if err := q.available(); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	out, err := scanDelivery(q.pool.QueryRow(ctx, `
UPDATE oneshot_deliveries d SET
    status='acknowledged',lease_owner=NULL,lease_until=NULL,last_error_code=NULL,
    updated_at=clock_timestamp()
FROM oneshot_runs r
WHERE d.id=$1 AND d.run_id=$2 AND r.id=$2 AND r.delivery_id=d.id
  AND r.status IN ('waiting_input','completed','failed','cancelled','timed_out')
  AND d.status IN ('reserved','retry_wait','pending','acknowledged')
RETURNING `+deliveryColumns, deliveryID, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Recovered Delivery/Run is not terminal or does not match", nil)
	}
	if err != nil {
		return domain.DeliverySnapshot{}, queueError("acknowledge recovered Delivery", err)
	}
	q.audit.RecordQueueEvent(ctx, AuditEvent{Type: "oneshot.delivery.acknowledged", DeliveryID: out.ID, TaskID: out.TaskID, Attempt: out.Attempt, OccurredAt: out.UpdatedAt})
	return out, nil
}
