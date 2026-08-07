package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

type scanner interface {
	Scan(dest ...any) error
}

const taskColumns = `
    id, principal_kind, principal_id, project_id, provider_id, model, source, prompt,
    status, current_run_id, runtime_context_id, version, created_at, updated_at`

func validateTask(snapshot domain.TaskSnapshot) error {
	_, err := domain.RestoreTask(snapshot)
	return err
}

func validateDelivery(snapshot domain.DeliverySnapshot) error {
	_, err := domain.RestoreDelivery(snapshot)
	return err
}

func (s *Store) insertTask(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, snapshot domain.TaskSnapshot) (domain.TaskSnapshot, error) {
	source, err := marshalJSON(snapshot.Source, "task.source")
	if err != nil {
		return domain.TaskSnapshot{}, err
	}
	var sourceChannelID, sourceMessageID string
	if snapshot.Source.Kind == domain.SourceTelegram {
		sourceChannelID = snapshot.Source.ChannelID
		sourceMessageID = snapshot.Source.SourceMessageID
	}
	row := q.QueryRow(ctx, `
INSERT INTO oneshot_tasks (
    id, principal_kind, principal_id, project_id, provider_id, model,
    source, source_kind, source_channel_id, source_message_id, prompt,
    status, current_run_id, runtime_context_id, version, created_at, updated_at
) VALUES (
    $1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),NULLIF($10,''),$11,$12,$13,$14,$15,$16,$17
)
RETURNING `+taskColumns,
		snapshot.ID, snapshot.PrincipalKind, snapshot.PrincipalID, snapshot.ProjectID, snapshot.ProviderID, snapshot.Model,
		source, snapshot.Source.Kind, sourceChannelID, sourceMessageID, snapshot.Prompt,
		snapshot.Status, snapshot.CurrentRunID, snapshot.RuntimeContextID, snapshot.Version,
		snapshot.CreatedAt.UTC(), snapshot.UpdatedAt.UTC())
	out, err := scanTask(row)
	if err != nil {
		return domain.TaskSnapshot{}, mapWriteError("insert task", err)
	}
	return out, nil
}

func (s *Store) insertDelivery(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, snapshot domain.DeliverySnapshot) (domain.DeliverySnapshot, error) {
	input, err := marshalJSON(snapshot.Input, "delivery.input")
	if err != nil {
		return domain.DeliverySnapshot{}, err
	}
	row := q.QueryRow(ctx, `
INSERT INTO oneshot_deliveries (
    id, task_id, operation, requested_by_kind, requested_by_id, input,
    idempotency_key, payload_sha256, status, attempt, max_attempts,
    available_at, lease_owner, lease_until, run_id, last_error_code,
    created_at, updated_at
) VALUES (
    $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
)
RETURNING
    id, task_id, operation, requested_by_kind, requested_by_id, input,
    idempotency_key, payload_sha256, status, attempt, max_attempts,
    available_at, lease_owner, lease_until, run_id, last_error_code,
    created_at, updated_at`,
		snapshot.ID, snapshot.TaskID, snapshot.Operation, snapshot.RequestedByKind, snapshot.RequestedByID,
		input, snapshot.IdempotencyKey, snapshot.PayloadSHA256, snapshot.Status, snapshot.Attempt,
		snapshot.MaxAttempts, snapshot.AvailableAt.UTC(), snapshot.LeaseOwner, snapshot.LeaseUntil,
		snapshot.RunID, snapshot.LastErrorCode, snapshot.CreatedAt.UTC(), snapshot.UpdatedAt.UTC())
	out, err := scanDelivery(row)
	if err != nil {
		return domain.DeliverySnapshot{}, mapWriteError("insert delivery", err)
	}
	return out, nil
}

// CreateTaskWithDelivery atomically creates the Task and its initial Delivery.
func (s *Store) CreateTaskWithDelivery(ctx context.Context, task domain.TaskSnapshot, delivery domain.DeliverySnapshot) (domain.TaskSnapshot, domain.DeliverySnapshot, error) {
	if s == nil || s.pool == nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot store unavailable", nil)
	}
	if err := validateTask(task); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := validateDelivery(delivery); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if delivery.TaskID != task.ID || delivery.RequestedByKind != task.PrincipalKind || delivery.RequestedByID != task.PrincipalID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.InvalidRequestf("initial Delivery must belong to and be requested by Task owner")
	}
	if delivery.Operation != domain.DeliveryNew || delivery.Status != domain.DeliveryPending {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.InvalidRequestf("initial Delivery must be pending new operation")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, wrap("begin task delivery transaction", err)
	}
	defer rollback(ctx, tx)

	persistedTask, err := s.insertTask(ctx, tx, task)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	persistedDelivery, err := s.insertDelivery(ctx, tx, delivery)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := BindDeliveryAttachments(ctx, tx, domain.Owner{Kind: persistedTask.PrincipalKind, ID: persistedTask.PrincipalID}, persistedTask.ProjectID, persistedTask.ID, persistedDelivery.ID, persistedDelivery.Input.AttachmentRefs); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := insertInitialTaskLifecycle(ctx, tx, persistedTask); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, mapWriteError("commit task delivery transaction", err)
	}
	return persistedTask, persistedDelivery, nil
}

// CreateDeliveryWithTaskUpdate atomically saves a domain-approved Task state
// transition and creates its continue/retry Delivery.
func (s *Store) CreateDeliveryWithTaskUpdate(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedVersion int64, delivery domain.DeliverySnapshot) (domain.TaskSnapshot, domain.DeliverySnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := validateTask(task); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := validateDelivery(delivery); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID || delivery.TaskID != task.ID || delivery.RequestedByKind != owner.Kind || delivery.RequestedByID != owner.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorForbidden, "Task or Delivery owner mismatch", nil)
	}
	if task.Version != expectedVersion+1 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.InvalidRequestf("Task snapshot version must equal expected version plus one")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, wrap("begin task update delivery transaction", err)
	}
	defer rollback(ctx, tx)

	persistedTask, err := updateTaskRow(ctx, tx, owner, task, expectedVersion)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	persistedDelivery, err := s.insertDelivery(ctx, tx, delivery)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := BindDeliveryAttachments(ctx, tx, owner, persistedTask.ProjectID, persistedTask.ID, persistedDelivery.ID, persistedDelivery.Input.AttachmentRefs); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := insertTaskLifecycle(ctx, tx, persistedTask, persistedTask.Status, persistedTask.Version, persistedTask.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, mapWriteError("commit task update delivery transaction", err)
	}
	return persistedTask, persistedDelivery, nil
}

// FindContinueReplay returns the original accepted Task/Delivery for an
// unexpired continue request. A reused key with different immutable input is a
// stable idempotency conflict even if the Task has already advanced.
func (s *Store) FindContinueReplay(ctx context.Context, owner domain.Owner, canonicalPath, idempotencyKey, payloadSHA string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	return s.findDeliveryReplay(ctx, owner, canonicalPath, idempotencyKey, payloadSHA, "continue")
}

func (s *Store) FindRetryReplay(ctx context.Context, owner domain.Owner, canonicalPath, idempotencyKey, payloadSHA string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	return s.findDeliveryReplay(ctx, owner, canonicalPath, idempotencyKey, payloadSHA, "retry")
}

func (s *Store) findDeliveryReplay(ctx context.Context, owner domain.Owner, canonicalPath, idempotencyKey, payloadSHA, operation string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	if s == nil || s.pool == nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot store unavailable", nil)
	}
	if err := validateOwner(owner); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	canonicalPath = strings.TrimSpace(canonicalPath)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if canonicalPath == "" || idempotencyKey == "" || len(payloadSHA) != 64 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.InvalidRequestf("%s canonical path, idempotency key, and payload hash are required", operation)
	}
	return loadDeliveryReplay(ctx, s.pool, owner, canonicalPath, idempotencyKey, payloadSHA, operation, false)
}

func (s *Store) CreateContinueDelivery(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedVersion int64, delivery domain.DeliverySnapshot, canonicalPath string, expiresAt *time.Time) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	return s.createIdempotentDelivery(ctx, owner, task, expectedVersion, delivery, canonicalPath, expiresAt, domain.DeliveryContinue, "continue")
}

func (s *Store) CreateRetryDelivery(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedVersion int64, delivery domain.DeliverySnapshot, canonicalPath string, expiresAt *time.Time) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	return s.createIdempotentDelivery(ctx, owner, task, expectedVersion, delivery, canonicalPath, expiresAt, domain.DeliveryRetry, "retry")
}

func (s *Store) createIdempotentDelivery(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedVersion int64, delivery domain.DeliverySnapshot, canonicalPath string, expiresAt *time.Time, expectedOperation domain.DeliveryOperation, operation string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	if s == nil || s.pool == nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot store unavailable", nil)
	}
	if err := validateOwner(owner); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	if err := validateTask(task); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	if err := validateDelivery(delivery); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	canonicalPath = strings.TrimSpace(canonicalPath)
	key := strings.TrimSpace(delivery.IdempotencyKey)
	if canonicalPath == "" || key == "" {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorIdempotencyRequired, operation+" canonical path and Idempotency-Key are required", nil)
	}
	if delivery.Operation != expectedOperation || delivery.Status != domain.DeliveryPending || delivery.TaskID != task.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.InvalidRequestf("%s persistence requires queued Task and pending %s Delivery", operation, operation)
	}
	if task.Status != domain.TaskQueued || task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID || delivery.RequestedByKind != owner.Kind || delivery.RequestedByID != owner.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorForbidden, operation+" Task or Delivery owner mismatch", nil)
	}
	if task.Version != expectedVersion+1 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.InvalidRequestf("Task snapshot version must equal expected version plus one")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, wrap("begin idempotent "+operation+" transaction", err)
	}
	defer rollback(ctx, tx)

	if _, err := tx.Exec(ctx, `DELETE FROM oneshot_idempotency_keys
WHERE principal_kind=$1 AND principal_id=$2 AND method='POST' AND canonical_path=$3
  AND idempotency_key=$4 AND expires_at IS NOT NULL AND expires_at<=clock_timestamp()`,
		owner.Kind, owner.ID, canonicalPath, key); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, mapWriteError("remove expired "+operation+" idempotency key", err)
	}

	var inserted int
	err = tx.QueryRow(ctx, `INSERT INTO oneshot_idempotency_keys (
    principal_kind,principal_id,method,canonical_path,idempotency_key,payload_sha256,expires_at
) VALUES ($1,$2,'POST',$3,$4,$5,$6)
ON CONFLICT (principal_kind,principal_id,method,canonical_path,idempotency_key) DO NOTHING
RETURNING 1`, owner.Kind, owner.ID, canonicalPath, key, delivery.PayloadSHA256, expiresAt).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		replayTask, replayDelivery, found, replayErr := loadDeliveryReplay(ctx, tx, owner, canonicalPath, key, delivery.PayloadSHA256, operation, true)
		if replayErr != nil {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, replayErr
		}
		if !found {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorQueueUnavailable, operation+" idempotency record disappeared", nil)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, mapWriteError("commit "+operation+" idempotent replay", err)
		}
		return replayTask, replayDelivery, false, nil
	}
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, mapWriteError("insert "+operation+" idempotency key", err)
	}

	persistedTask, err := updateTaskRow(ctx, tx, owner, task, expectedVersion)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	persistedDelivery, err := s.insertDelivery(ctx, tx, delivery)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	if err := BindDeliveryAttachments(ctx, tx, owner, persistedTask.ProjectID, persistedTask.ID, persistedDelivery.ID, persistedDelivery.Input.AttachmentRefs); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	if err := insertTaskLifecycle(ctx, tx, persistedTask, persistedTask.Status, persistedTask.Version, persistedTask.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, err
	}
	response, err := json.Marshal(struct {
		Task     domain.TaskSnapshot     `json:"task"`
		Delivery domain.DeliverySnapshot `json:"delivery"`
	}{Task: persistedTask, Delivery: persistedDelivery})
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.InvalidRequestf("%s replay response is not JSON-compatible", operation)
	}
	result, err := tx.Exec(ctx, `UPDATE oneshot_idempotency_keys SET
    resource_kind='delivery',resource_id=$6,response_status=202,response_body=$7::jsonb
WHERE principal_kind=$1 AND principal_id=$2 AND method='POST' AND canonical_path=$3
  AND idempotency_key=$4 AND payload_sha256=$5`,
		owner.Kind, owner.ID, canonicalPath, key, delivery.PayloadSHA256, persistedDelivery.ID, response)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, mapWriteError("complete "+operation+" idempotency key", err)
	}
	if result.RowsAffected() != 1 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorQueueUnavailable, operation+" idempotency record disappeared", nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, mapWriteError("commit idempotent "+operation+" transaction", err)
	}
	return persistedTask, persistedDelivery, true, nil
}

type continueReplayQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadDeliveryReplay(ctx context.Context, q continueReplayQuery, owner domain.Owner, canonicalPath, key, payloadSHA, operation string, lock bool) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	query := `SELECT payload_sha256,resource_kind,resource_id,response_body
FROM oneshot_idempotency_keys
WHERE principal_kind=$1 AND principal_id=$2 AND method='POST' AND canonical_path=$3
  AND idempotency_key=$4 AND (expires_at IS NULL OR expires_at>clock_timestamp())`
	if lock {
		query += ` FOR UPDATE`
	}
	var storedHash string
	var resourceKind, resourceID *string
	var responseRaw []byte
	err := q.QueryRow(ctx, query, owner.Kind, owner.ID, canonicalPath, key).Scan(&storedHash, &resourceKind, &resourceID, &responseRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, nil
	}
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, wrap("load "+operation+" idempotency replay", err)
	}
	if storedHash != payloadSHA {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorIdempotencyConflict, "Idempotency-Key was reused with a different "+operation+" payload", nil)
	}
	if resourceKind == nil || resourceID == nil || *resourceKind != "delivery" {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorQueueUnavailable, operation+" idempotency request has no committed Delivery", nil)
	}
	if len(responseRaw) > 0 && string(responseRaw) != "null" {
		var original struct {
			Task     domain.TaskSnapshot     `json:"task"`
			Delivery domain.DeliverySnapshot `json:"delivery"`
		}
		if json.Unmarshal(responseRaw, &original) == nil && original.Delivery.ID == *resourceID &&
			original.Task.PrincipalKind == owner.Kind && original.Task.PrincipalID == owner.ID &&
			original.Delivery.RequestedByKind == owner.Kind && original.Delivery.RequestedByID == owner.ID {
			if _, taskErr := domain.RestoreTask(original.Task); taskErr == nil {
				if _, deliveryErr := domain.RestoreDelivery(original.Delivery); deliveryErr == nil {
					return original.Task, original.Delivery, true, nil
				}
			}
		}
	}
	persistedDelivery, err := scanDelivery(q.QueryRow(ctx, `
SELECT d.id,d.task_id,d.operation,d.requested_by_kind,d.requested_by_id,d.input,
       d.idempotency_key,d.payload_sha256,d.status,d.attempt,d.max_attempts,
       d.available_at,d.lease_owner,d.lease_until,d.run_id,d.last_error_code,d.created_at,d.updated_at
FROM oneshot_deliveries d JOIN oneshot_tasks t ON t.id=d.task_id
WHERE d.id=$1 AND t.principal_kind=$2 AND t.principal_id=$3`, *resourceID, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorQueueUnavailable, operation+" replay Delivery is unavailable", nil)
	}
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, wrap("load "+operation+" replay Delivery", err)
	}
	persistedTask, err := scanTask(q.QueryRow(ctx, `SELECT `+taskColumns+`
FROM oneshot_tasks WHERE id=$1 AND principal_kind=$2 AND principal_id=$3`, persistedDelivery.TaskID, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, domain.NewDomainError(domain.ErrorQueueUnavailable, operation+" replay Task is unavailable", nil)
	}
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, false, wrap("load "+operation+" replay Task", err)
	}
	return persistedTask, persistedDelivery, true, nil
}

func (s *Store) GetTask(ctx context.Context, owner domain.Owner, id string) (domain.TaskSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.TaskSnapshot{}, err
	}
	out, err := scanTask(s.pool.QueryRow(ctx, `SELECT `+taskColumns+`
FROM oneshot_tasks WHERE id=$1 AND principal_kind=$2 AND principal_id=$3`, id, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TaskSnapshot{}, notFound(domain.ErrorTaskNotFound, "Task")
	}
	if err != nil {
		return domain.TaskSnapshot{}, wrap("get task", err)
	}
	return out, nil
}

func (s *Store) ListTasks(ctx context.Context, owner domain.Owner, req PageRequest) (Page[domain.TaskSnapshot], error) {
	if err := validateOwner(owner); err != nil {
		return Page[domain.TaskSnapshot]{}, err
	}
	limit, cursor, err := normalizePage(req)
	if err != nil {
		return Page[domain.TaskSnapshot]{}, err
	}
	var rows pgx.Rows
	if cursor == nil {
		rows, err = s.pool.Query(ctx, `SELECT `+taskColumns+`
FROM oneshot_tasks
WHERE principal_kind=$1 AND principal_id=$2
ORDER BY created_at DESC, id DESC LIMIT $3`, owner.Kind, owner.ID, limit+1)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT `+taskColumns+`
FROM oneshot_tasks
WHERE principal_kind=$1 AND principal_id=$2 AND (created_at,id) < ($3,$4)
ORDER BY created_at DESC, id DESC LIMIT $5`, owner.Kind, owner.ID, cursor.CreatedAt, cursor.ID, limit+1)
	}
	if err != nil {
		return Page[domain.TaskSnapshot]{}, wrap("list tasks", err)
	}
	defer rows.Close()
	items := make([]domain.TaskSnapshot, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanTask(rows)
		if scanErr != nil {
			return Page[domain.TaskSnapshot]{}, wrap("scan task page", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.TaskSnapshot]{}, wrap("list tasks rows", err)
	}
	page := Page[domain.TaskSnapshot]{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *Store) UpdateTask(ctx context.Context, owner domain.Owner, snapshot domain.TaskSnapshot, expectedVersion int64) (domain.TaskSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.TaskSnapshot{}, err
	}
	if err := validateTask(snapshot); err != nil {
		return domain.TaskSnapshot{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.TaskSnapshot{}, wrap("begin Task lifecycle transaction", err)
	}
	defer rollback(ctx, tx)
	persisted, err := updateTaskRow(ctx, tx, owner, snapshot, expectedVersion)
	if err != nil {
		return domain.TaskSnapshot{}, err
	}
	if err := insertTaskLifecycle(ctx, tx, persisted, persisted.Status, persisted.Version, persisted.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, err
	}
	if err := insertTaskTerminalNotification(ctx, tx, persisted); err != nil {
		return domain.TaskSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskSnapshot{}, mapWriteError("commit Task lifecycle transaction", err)
	}
	return persisted, nil
}

func updateTaskRow(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, owner domain.Owner, snapshot domain.TaskSnapshot, expectedVersion int64) (domain.TaskSnapshot, error) {
	if snapshot.PrincipalKind != owner.Kind || snapshot.PrincipalID != owner.ID {
		return domain.TaskSnapshot{}, domain.NewDomainError(domain.ErrorForbidden, "Task owner mismatch", nil)
	}
	if snapshot.Version != expectedVersion+1 {
		return domain.TaskSnapshot{}, domain.InvalidRequestf("Task snapshot version must equal expected version plus one")
	}
	out, err := scanTask(q.QueryRow(ctx, `
UPDATE oneshot_tasks SET
    status=$1, current_run_id=$2, runtime_context_id=$3,
    version=$4, updated_at=$5
WHERE id=$6 AND principal_kind=$7 AND principal_id=$8 AND version=$9
RETURNING `+taskColumns,
		snapshot.Status, snapshot.CurrentRunID, snapshot.RuntimeContextID,
		snapshot.Version, snapshot.UpdatedAt.UTC(), snapshot.ID, owner.Kind, owner.ID, expectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TaskSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Task version conflict or Task not found", nil)
	}
	if err != nil {
		return domain.TaskSnapshot{}, mapWriteError("update task", err)
	}
	return out, nil
}

func (s *Store) GetDelivery(ctx context.Context, owner domain.Owner, id string) (domain.DeliverySnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	out, err := scanDelivery(s.pool.QueryRow(ctx, `
SELECT d.id,d.task_id,d.operation,d.requested_by_kind,d.requested_by_id,d.input,
       d.idempotency_key,d.payload_sha256,d.status,d.attempt,d.max_attempts,
       d.available_at,d.lease_owner,d.lease_until,d.run_id,d.last_error_code,d.created_at,d.updated_at
FROM oneshot_deliveries d JOIN oneshot_tasks t ON t.id=d.task_id
WHERE d.id=$1 AND t.principal_kind=$2 AND t.principal_id=$3`, id, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeliverySnapshot{}, notFound(domain.ErrorTaskNotFound, "Delivery")
	}
	if err != nil {
		return domain.DeliverySnapshot{}, wrap("get delivery", err)
	}
	return out, nil
}

func (s *Store) UpdateDelivery(ctx context.Context, owner domain.Owner, snapshot domain.DeliverySnapshot) (domain.DeliverySnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := validateDelivery(snapshot); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if snapshot.RequestedByKind != owner.Kind || snapshot.RequestedByID != owner.ID {
		return domain.DeliverySnapshot{}, domain.NewDomainError(domain.ErrorForbidden, "Delivery owner mismatch", nil)
	}
	return updateDeliveryRow(ctx, s.pool, owner, snapshot)
}

func scanTask(row scanner) (domain.TaskSnapshot, error) {
	var snapshot domain.TaskSnapshot
	var sourceRaw []byte
	if err := row.Scan(&snapshot.ID, &snapshot.PrincipalKind, &snapshot.PrincipalID,
		&snapshot.ProjectID, &snapshot.ProviderID, &snapshot.Model, &sourceRaw, &snapshot.Prompt,
		&snapshot.Status, &snapshot.CurrentRunID, &snapshot.RuntimeContextID,
		&snapshot.Version, &snapshot.CreatedAt, &snapshot.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, err
	}
	if err := json.Unmarshal(sourceRaw, &snapshot.Source); err != nil {
		return domain.TaskSnapshot{}, fmt.Errorf("decode source: %w", err)
	}
	restored, err := domain.RestoreTask(snapshot)
	if err != nil {
		return domain.TaskSnapshot{}, fmt.Errorf("restore task: %w", err)
	}
	return restored.Snapshot(), nil
}

func scanDelivery(row scanner) (domain.DeliverySnapshot, error) {
	var snapshot domain.DeliverySnapshot
	var inputRaw []byte
	if err := row.Scan(&snapshot.ID, &snapshot.TaskID, &snapshot.Operation,
		&snapshot.RequestedByKind, &snapshot.RequestedByID, &inputRaw,
		&snapshot.IdempotencyKey, &snapshot.PayloadSHA256, &snapshot.Status,
		&snapshot.Attempt, &snapshot.MaxAttempts, &snapshot.AvailableAt,
		&snapshot.LeaseOwner, &snapshot.LeaseUntil, &snapshot.RunID,
		&snapshot.LastErrorCode, &snapshot.CreatedAt, &snapshot.UpdatedAt); err != nil {
		return domain.DeliverySnapshot{}, err
	}
	if err := json.Unmarshal(inputRaw, &snapshot.Input); err != nil {
		return domain.DeliverySnapshot{}, fmt.Errorf("decode delivery input: %w", err)
	}
	restored, err := domain.RestoreDelivery(snapshot)
	if err != nil {
		return domain.DeliverySnapshot{}, fmt.Errorf("restore delivery: %w", err)
	}
	return restored.Snapshot(), nil
}

// ListDeliveries returns Task-owned deliveries using stable cursor pagination.
func (s *Store) ListDeliveries(ctx context.Context, owner domain.Owner, taskID string, req PageRequest) (Page[domain.DeliverySnapshot], error) {
	if err := validateOwner(owner); err != nil {
		return Page[domain.DeliverySnapshot]{}, err
	}
	limit, cursor, err := normalizePage(req)
	if err != nil {
		return Page[domain.DeliverySnapshot]{}, err
	}
	query := `
SELECT d.id,d.task_id,d.operation,d.requested_by_kind,d.requested_by_id,d.input,
       d.idempotency_key,d.payload_sha256,d.status,d.attempt,d.max_attempts,
       d.available_at,d.lease_owner,d.lease_until,d.run_id,d.last_error_code,d.created_at,d.updated_at
FROM oneshot_deliveries d JOIN oneshot_tasks t ON t.id=d.task_id
WHERE t.principal_kind=$1 AND t.principal_id=$2 AND ($3='' OR d.task_id=$3)`
	args := []any{owner.Kind, owner.ID, taskID}
	if cursor != nil {
		query += ` AND (d.created_at,d.id) < ($4,$5)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += ` ORDER BY d.created_at DESC,d.id DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[domain.DeliverySnapshot]{}, wrap("list deliveries", err)
	}
	defer rows.Close()
	items := make([]domain.DeliverySnapshot, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanDelivery(rows)
		if scanErr != nil {
			return Page[domain.DeliverySnapshot]{}, wrap("scan delivery page", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.DeliverySnapshot]{}, wrap("list deliveries rows", err)
	}
	page := Page[domain.DeliverySnapshot]{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// DatabaseNow returns the authoritative timestamp for lease/state decisions.
func (s *Store) DatabaseNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	if err := s.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, wrap("read database clock", err)
	}
	return now.UTC(), nil
}

// TaskListFilter is the authorization-safe task list filter exposed to REST and channels.
type TaskListFilter struct {
	ProjectID string
	Status    domain.TaskStatus
	Page      PageRequest
}

// ListTasksFiltered applies owner, project and status predicates before pagination.
func (s *Store) ListTasksFiltered(ctx context.Context, owner domain.Owner, filter TaskListFilter) (Page[domain.TaskSnapshot], error) {
	if err := validateOwner(owner); err != nil {
		return Page[domain.TaskSnapshot]{}, err
	}
	if filter.Status != "" && !filter.Status.Valid() {
		return Page[domain.TaskSnapshot]{}, domain.InvalidRequestf("invalid task status %q", filter.Status)
	}
	limit, cursor, err := normalizePage(filter.Page)
	if err != nil {
		return Page[domain.TaskSnapshot]{}, err
	}
	projectID := strings.TrimSpace(filter.ProjectID)
	status := string(filter.Status)
	args := []any{owner.Kind, owner.ID, projectID, status}
	query := `SELECT ` + taskColumns + `
FROM oneshot_tasks
WHERE principal_kind=$1 AND principal_id=$2
  AND ($3='' OR project_id=$3)
  AND ($4='' OR status=$4)`
	if cursor != nil {
		query += ` AND (created_at,id) < ($5,$6)
ORDER BY created_at DESC,id DESC LIMIT $7`
		args = append(args, cursor.CreatedAt, cursor.ID, limit+1)
	} else {
		query += ` ORDER BY created_at DESC,id DESC LIMIT $5`
		args = append(args, limit+1)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[domain.TaskSnapshot]{}, wrap("list filtered tasks", err)
	}
	defer rows.Close()
	items := make([]domain.TaskSnapshot, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanTask(rows)
		if scanErr != nil {
			return Page[domain.TaskSnapshot]{}, wrap("scan filtered task page", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.TaskSnapshot]{}, wrap("list filtered task rows", err)
	}
	page := Page[domain.TaskSnapshot]{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}
