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

// ChannelBinding is the One-shot-only reply binding. It never points to an
// interactive Session.
type ChannelBinding struct {
	ID              int64
	Owner           domain.Owner
	ChannelID       string
	ConversationID  string
	ThreadID        string
	SourceMessageID *string
	TaskID          string
	Kind            string
	ExpiresAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (s *Store) UpsertChannelBinding(ctx context.Context, binding ChannelBinding) (ChannelBinding, error) {
	if err := binding.Owner.Validate(); err != nil {
		return ChannelBinding{}, err
	}
	if strings.TrimSpace(binding.ChannelID) == "" || strings.TrimSpace(binding.ConversationID) == "" || strings.TrimSpace(binding.TaskID) == "" {
		return ChannelBinding{}, domain.InvalidRequestf("channel_id, conversation_id and task_id are required")
	}
	switch binding.Kind {
	case "task", "continue", "notification":
	default:
		return ChannelBinding{}, domain.InvalidRequestf("invalid channel binding kind %q", binding.Kind)
	}
	row := s.pool.QueryRow(ctx, `
INSERT INTO oneshot_channel_bindings (
    principal_kind,principal_id,channel_id,conversation_id,thread_id,
    source_message_id,task_id,binding_kind,expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT DO UPDATE SET
    principal_kind=EXCLUDED.principal_kind,
    principal_id=EXCLUDED.principal_id,
    source_message_id=EXCLUDED.source_message_id,
    task_id=EXCLUDED.task_id,
    binding_kind=EXCLUDED.binding_kind,
    expires_at=EXCLUDED.expires_at,
    updated_at=NOW()
RETURNING id,principal_kind,principal_id,channel_id,conversation_id,thread_id,
          source_message_id,task_id,binding_kind,expires_at,created_at,updated_at`,
		binding.Owner.Kind, binding.Owner.ID, binding.ChannelID, binding.ConversationID,
		binding.ThreadID, binding.SourceMessageID, binding.TaskID, binding.Kind, binding.ExpiresAt)
	out, err := scanChannelBinding(row)
	if err != nil {
		return ChannelBinding{}, mapWriteError("upsert channel binding", err)
	}
	return out, nil
}

func (s *Store) ResolveChannelBinding(ctx context.Context, owner domain.Owner, channelID, conversationID, threadID, sourceMessageID string, now time.Time) (ChannelBinding, error) {
	if err := owner.Validate(); err != nil {
		return ChannelBinding{}, err
	}
	out, err := scanChannelBinding(s.pool.QueryRow(ctx, `
SELECT id,principal_kind,principal_id,channel_id,conversation_id,thread_id,
       source_message_id,task_id,binding_kind,expires_at,created_at,updated_at
FROM oneshot_channel_bindings
WHERE principal_kind=$1 AND principal_id=$2 AND channel_id=$3
  AND conversation_id=$4 AND thread_id=$5
  AND COALESCE(source_message_id,'')=$6
  AND (expires_at IS NULL OR expires_at>$7)`,
		owner.Kind, owner.ID, channelID, conversationID, threadID, strings.TrimSpace(sourceMessageID), now.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelBinding{}, notFound(domain.ErrorTaskNotFound, "One-shot channel binding")
	}
	if err != nil {
		return ChannelBinding{}, wrap("resolve channel binding", err)
	}
	return out, nil
}

func scanChannelBinding(row scanner) (ChannelBinding, error) {
	var out ChannelBinding
	if err := row.Scan(&out.ID, &out.Owner.Kind, &out.Owner.ID, &out.ChannelID,
		&out.ConversationID, &out.ThreadID, &out.SourceMessageID, &out.TaskID,
		&out.Kind, &out.ExpiresAt, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return ChannelBinding{}, err
	}
	return out, nil
}

// IdempotencyRecord stores the canonical request identity and replay response.
type IdempotencyRecord struct {
	Owner          domain.Owner
	Method         string
	CanonicalPath  string
	Key            string
	PayloadSHA256  string
	ResourceKind   *string
	ResourceID     *string
	ResponseStatus *int
	ResponseBody   map[string]any
	CreatedAt      time.Time
	ExpiresAt      *time.Time
}

// CreateIdempotencyRecord inserts a request identity. A caller that receives
// ErrorIdempotencyConflict must load the existing row and compare PayloadSHA256.
func (s *Store) CreateIdempotencyRecord(ctx context.Context, record IdempotencyRecord) (IdempotencyRecord, error) {
	if err := record.Owner.Validate(); err != nil {
		return IdempotencyRecord{}, err
	}
	if strings.TrimSpace(record.Method) == "" || strings.TrimSpace(record.CanonicalPath) == "" || strings.TrimSpace(record.Key) == "" || len(record.PayloadSHA256) != 64 {
		return IdempotencyRecord{}, domain.InvalidRequestf("method, canonical_path, key and payload_sha256 are required")
	}
	var response any
	if record.ResponseBody != nil {
		raw, err := marshalJSON(record.ResponseBody, "idempotency.response_body")
		if err != nil {
			return IdempotencyRecord{}, err
		}
		response = raw
	}
	out, err := scanIdempotency(s.pool.QueryRow(ctx, `
INSERT INTO oneshot_idempotency_keys (
    principal_kind,principal_id,method,canonical_path,idempotency_key,payload_sha256,
    resource_kind,resource_id,response_status,response_body,expires_at
) VALUES ($1,$2,upper($3),$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING principal_kind,principal_id,method,canonical_path,idempotency_key,payload_sha256,
          resource_kind,resource_id,response_status,response_body,created_at,expires_at`,
		record.Owner.Kind, record.Owner.ID, record.Method, record.CanonicalPath, record.Key,
		record.PayloadSHA256, record.ResourceKind, record.ResourceID, record.ResponseStatus,
		response, record.ExpiresAt))
	if err != nil {
		return IdempotencyRecord{}, mapWriteError("insert idempotency record", err)
	}
	return out, nil
}

func (s *Store) GetIdempotencyRecord(ctx context.Context, owner domain.Owner, method, canonicalPath, key string) (IdempotencyRecord, error) {
	if err := owner.Validate(); err != nil {
		return IdempotencyRecord{}, err
	}
	out, err := scanIdempotency(s.pool.QueryRow(ctx, `
SELECT principal_kind,principal_id,method,canonical_path,idempotency_key,payload_sha256,
       resource_kind,resource_id,response_status,response_body,created_at,expires_at
FROM oneshot_idempotency_keys
WHERE principal_kind=$1 AND principal_id=$2 AND method=upper($3)
  AND canonical_path=$4 AND idempotency_key=$5
  AND (expires_at IS NULL OR expires_at>NOW())`, owner.Kind, owner.ID, method, canonicalPath, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotencyRecord{}, domain.NewDomainError(domain.ErrorIdempotencyRequired, "idempotency record not found", nil)
	}
	if err != nil {
		return IdempotencyRecord{}, wrap("get idempotency record", err)
	}
	return out, nil
}

func scanIdempotency(row scanner) (IdempotencyRecord, error) {
	var out IdempotencyRecord
	var response []byte
	if err := row.Scan(&out.Owner.Kind, &out.Owner.ID, &out.Method, &out.CanonicalPath,
		&out.Key, &out.PayloadSHA256, &out.ResourceKind, &out.ResourceID,
		&out.ResponseStatus, &response, &out.CreatedAt, &out.ExpiresAt); err != nil {
		return IdempotencyRecord{}, err
	}
	if len(response) > 0 {
		if err := json.Unmarshal(response, &out.ResponseBody); err != nil {
			return IdempotencyRecord{}, fmt.Errorf("decode idempotency response: %w", err)
		}
	}
	return out, nil
}

// NotificationStatus belongs only to One-shot result notifications; transport
// retry remains in internal/channel/delivery.
type NotificationStatus string

const (
	NotificationPending   NotificationStatus = "pending"
	NotificationSending   NotificationStatus = "sending"
	NotificationRetry     NotificationStatus = "retry"
	NotificationDelivered NotificationStatus = "delivered"
	NotificationDead      NotificationStatus = "dead"
)

type NotificationOutboxRecord struct {
	Owner          domain.Owner
	ID             string
	IdempotencyKey string
	TaskID         string
	RunID          *string
	EventType      string
	Destination    map[string]any
	Payload        map[string]any
	Status         NotificationStatus
	AttemptCount   int
	NextAttemptAt  time.Time
	LeaseOwner     *string
	LeaseUntil     *time.Time
	LastError      *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeliveredAt    *time.Time
}

func (s *Store) CreateNotification(ctx context.Context, owner domain.Owner, record NotificationOutboxRecord) (NotificationOutboxRecord, error) {
	if err := owner.Validate(); err != nil {
		return NotificationOutboxRecord{}, err
	}
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.IdempotencyKey) == "" || strings.TrimSpace(record.TaskID) == "" || !strings.HasPrefix(record.EventType, "oneshot.") {
		return NotificationOutboxRecord{}, domain.InvalidRequestf("notification id, idempotency key, task and oneshot event type are required")
	}
	destination, err := marshalJSON(record.Destination, "notification.destination")
	if err != nil {
		return NotificationOutboxRecord{}, err
	}
	payload, err := marshalJSON(record.Payload, "notification.payload")
	if err != nil {
		return NotificationOutboxRecord{}, err
	}
	if record.Status == "" {
		record.Status = NotificationPending
	}
	if record.NextAttemptAt.IsZero() {
		record.NextAttemptAt = time.Now().UTC()
	}
	out, err := scanNotification(s.pool.QueryRow(ctx, `
INSERT INTO oneshot_notification_outbox (
    id,idempotency_key,task_id,run_id,event_type,destination,payload,status,
    attempt_count,next_attempt_at,lease_owner,lease_until,last_error
)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13
FROM oneshot_tasks t
WHERE t.id=$3 AND t.principal_kind=$14 AND t.principal_id=$15
RETURNING id,idempotency_key,task_id,run_id,event_type,destination,payload,status,
          attempt_count,next_attempt_at,lease_owner,lease_until,last_error,created_at,updated_at,delivered_at`,
		record.ID, record.IdempotencyKey, record.TaskID, record.RunID, record.EventType,
		destination, payload, record.Status, record.AttemptCount, record.NextAttemptAt.UTC(),
		record.LeaseOwner, record.LeaseUntil, record.LastError, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationOutboxRecord{}, notFound(domain.ErrorTaskNotFound, "Task")
	}
	if err != nil {
		return NotificationOutboxRecord{}, mapWriteError("insert notification", err)
	}
	out.Owner = owner
	return out, nil
}

func scanNotification(row scanner) (NotificationOutboxRecord, error) {
	var out NotificationOutboxRecord
	var destination, payload []byte
	if err := row.Scan(&out.ID, &out.IdempotencyKey, &out.TaskID, &out.RunID,
		&out.EventType, &destination, &payload, &out.Status, &out.AttemptCount,
		&out.NextAttemptAt, &out.LeaseOwner, &out.LeaseUntil, &out.LastError,
		&out.CreatedAt, &out.UpdatedAt, &out.DeliveredAt); err != nil {
		return NotificationOutboxRecord{}, err
	}
	if err := json.Unmarshal(destination, &out.Destination); err != nil {
		return NotificationOutboxRecord{}, fmt.Errorf("decode notification destination: %w", err)
	}
	if err := json.Unmarshal(payload, &out.Payload); err != nil {
		return NotificationOutboxRecord{}, fmt.Errorf("decode notification payload: %w", err)
	}
	return out, nil
}

func scanClaimedNotification(row scanner) (NotificationOutboxRecord, error) {
	var out NotificationOutboxRecord
	var destination, payload []byte
	if err := row.Scan(&out.Owner.Kind, &out.Owner.ID, &out.ID, &out.IdempotencyKey, &out.TaskID, &out.RunID,
		&out.EventType, &destination, &payload, &out.Status, &out.AttemptCount,
		&out.NextAttemptAt, &out.LeaseOwner, &out.LeaseUntil, &out.LastError,
		&out.CreatedAt, &out.UpdatedAt, &out.DeliveredAt); err != nil {
		return NotificationOutboxRecord{}, err
	}
	if err := out.Owner.Validate(); err != nil {
		return NotificationOutboxRecord{}, err
	}
	if err := json.Unmarshal(destination, &out.Destination); err != nil {
		return NotificationOutboxRecord{}, fmt.Errorf("decode notification destination: %w", err)
	}
	if err := json.Unmarshal(payload, &out.Payload); err != nil {
		return NotificationOutboxRecord{}, fmt.Errorf("decode notification payload: %w", err)
	}
	return out, nil
}

// ClaimNotifications leases due One-shot notification rows with SKIP LOCKED.
func (s *Store) ClaimNotifications(ctx context.Context, workerID string, limit int, lease time.Duration, now time.Time) ([]NotificationOutboxRecord, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil, domain.InvalidRequestf("notification worker_id is required")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		return nil, domain.InvalidRequestf("notification claim limit must not exceed 200")
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, wrap("begin notification claim", err)
	}
	defer rollback(ctx, tx)
	rows, err := tx.Query(ctx, `
WITH due AS (
    SELECT n.id,t.principal_kind,t.principal_id
    FROM oneshot_notification_outbox n
    JOIN oneshot_tasks t ON t.id=n.task_id
    WHERE (n.status IN ('pending','retry') OR (n.status='sending' AND n.lease_until<= $1))
      AND n.next_attempt_at <= $1
    ORDER BY n.next_attempt_at,n.created_at,n.id
    FOR UPDATE OF n SKIP LOCKED LIMIT $2
)
UPDATE oneshot_notification_outbox n SET
    status='sending',attempt_count=n.attempt_count+1,
    lease_owner=$3,lease_until=$1+$4::interval,updated_at=$1
FROM due WHERE n.id=due.id
RETURNING due.principal_kind,due.principal_id,
          n.id,n.idempotency_key,n.task_id,n.run_id,n.event_type,n.destination,n.payload,n.status,
          n.attempt_count,n.next_attempt_at,n.lease_owner,n.lease_until,n.last_error,n.created_at,n.updated_at,n.delivered_at`,
		now.UTC(), limit, workerID, intervalLiteral(lease))
	if err != nil {
		return nil, mapWriteError("claim notifications", err)
	}
	defer rows.Close()
	out := make([]NotificationOutboxRecord, 0, limit)
	for rows.Next() {
		item, scanErr := scanClaimedNotification(rows)
		if scanErr != nil {
			return nil, wrap("scan claimed notification", scanErr)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("claim notification rows", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapWriteError("commit notification claim", err)
	}
	return out, nil
}

func intervalLiteral(value time.Duration) string {
	seconds := value.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%f seconds", seconds)
}

// MarkNotificationDelivered clears the lease and records the terminal receipt.
func (s *Store) MarkNotificationDelivered(ctx context.Context, id, workerID string, now time.Time) (NotificationOutboxRecord, error) {
	out, err := scanNotification(s.pool.QueryRow(ctx, `
UPDATE oneshot_notification_outbox SET
    status='delivered',lease_owner=NULL,lease_until=NULL,last_error=NULL,
    delivered_at=$3,updated_at=$3
WHERE id=$1 AND status='sending' AND lease_owner=$2
RETURNING id,idempotency_key,task_id,run_id,event_type,destination,payload,status,
          attempt_count,next_attempt_at,lease_owner,lease_until,last_error,created_at,updated_at,delivered_at`, id, workerID, now.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationOutboxRecord{}, domain.NewDomainError(domain.ErrorRunConflict, "notification lease is not owned by worker", nil)
	}
	if err != nil {
		return NotificationOutboxRecord{}, mapWriteError("mark notification delivered", err)
	}
	return out, nil
}

// RetryNotification schedules exponential retry or dead-letters an exhausted row.
func (s *Store) RetryNotification(ctx context.Context, id, workerID, message string, maxAttempts int, next time.Time, now time.Time) (NotificationOutboxRecord, error) {
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	status := NotificationRetry
	if next.Before(now) {
		next = now
	}
	var out NotificationOutboxRecord
	row := s.pool.QueryRow(ctx, `
UPDATE oneshot_notification_outbox SET
    status=CASE WHEN attempt_count >= $4 THEN 'dead' ELSE $5 END,
    next_attempt_at=$6,lease_owner=NULL,lease_until=NULL,last_error=$3,updated_at=$7
WHERE id=$1 AND status='sending' AND lease_owner=$2
RETURNING id,idempotency_key,task_id,run_id,event_type,destination,payload,status,
          attempt_count,next_attempt_at,lease_owner,lease_until,last_error,created_at,updated_at,delivered_at`,
		id, workerID, strings.TrimSpace(message), maxAttempts, status, next.UTC(), now.UTC())
	out, err := scanNotification(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationOutboxRecord{}, domain.NewDomainError(domain.ErrorRunConflict, "notification lease is not owned by worker", nil)
	}
	if err != nil {
		return NotificationOutboxRecord{}, mapWriteError("retry notification", err)
	}
	return out, nil
}

// NotificationPreference is an explicit owner/project-scoped cross-device
// destination. It is not used to route inbound messages or choose an
// execution domain.
type NotificationPreference struct {
	Owner          domain.Owner
	ProjectID      string
	ChannelID      string
	ConversationID string
	ThreadID       string
	MessageID      string
	Metadata       map[string]string
	Enabled        bool
	UpdatedAt      time.Time
}

func (s *Store) UpsertNotificationPreference(ctx context.Context, pref NotificationPreference) (NotificationPreference, error) {
	if err := pref.Owner.Validate(); err != nil {
		return NotificationPreference{}, err
	}
	if strings.TrimSpace(pref.ProjectID) == "" || strings.TrimSpace(pref.ChannelID) == "" || strings.TrimSpace(pref.ConversationID) == "" {
		return NotificationPreference{}, domain.InvalidRequestf("notification preference project_id, channel_id and conversation_id are required")
	}
	metadata, err := json.Marshal(pref.Metadata)
	if err != nil {
		return NotificationPreference{}, domain.InvalidRequestf("notification preference metadata is not JSON-compatible")
	}
	if pref.UpdatedAt.IsZero() {
		pref.UpdatedAt = time.Now().UTC()
	}
	var raw []byte
	var out NotificationPreference
	err = s.pool.QueryRow(ctx, `
INSERT INTO oneshot_notification_preferences (
    principal_kind,principal_id,project_id,channel_id,conversation_id,thread_id,
    message_id,metadata,enabled,updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (principal_kind,principal_id,project_id) DO UPDATE SET
    channel_id=EXCLUDED.channel_id,conversation_id=EXCLUDED.conversation_id,
    thread_id=EXCLUDED.thread_id,message_id=EXCLUDED.message_id,
    metadata=EXCLUDED.metadata,enabled=EXCLUDED.enabled,updated_at=EXCLUDED.updated_at
RETURNING principal_kind,principal_id,project_id,channel_id,conversation_id,thread_id,
          message_id,metadata,enabled,updated_at`,
		pref.Owner.Kind, pref.Owner.ID, strings.TrimSpace(pref.ProjectID), strings.TrimSpace(pref.ChannelID),
		strings.TrimSpace(pref.ConversationID), strings.TrimSpace(pref.ThreadID), strings.TrimSpace(pref.MessageID),
		metadata, pref.Enabled, pref.UpdatedAt.UTC()).Scan(&out.Owner.Kind, &out.Owner.ID, &out.ProjectID,
		&out.ChannelID, &out.ConversationID, &out.ThreadID, &out.MessageID, &raw, &out.Enabled, &out.UpdatedAt)
	if err != nil {
		return NotificationPreference{}, mapWriteError("upsert notification preference", err)
	}
	if err := json.Unmarshal(raw, &out.Metadata); err != nil {
		return NotificationPreference{}, fmt.Errorf("decode notification preference metadata: %w", err)
	}
	return out, nil
}

func (s *Store) GetNotificationPreference(ctx context.Context, owner domain.Owner, projectID string) (NotificationPreference, error) {
	if err := owner.Validate(); err != nil {
		return NotificationPreference{}, err
	}
	var raw []byte
	var out NotificationPreference
	err := s.pool.QueryRow(ctx, `SELECT principal_kind,principal_id,project_id,channel_id,
       conversation_id,thread_id,message_id,metadata,enabled,updated_at
FROM oneshot_notification_preferences
WHERE principal_kind=$1 AND principal_id=$2 AND project_id=$3 AND enabled=TRUE`,
		owner.Kind, owner.ID, strings.TrimSpace(projectID)).Scan(&out.Owner.Kind, &out.Owner.ID, &out.ProjectID,
		&out.ChannelID, &out.ConversationID, &out.ThreadID, &out.MessageID, &raw, &out.Enabled, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationPreference{}, notFound(domain.ErrorArtifactNotFound, "notification preference")
	}
	if err != nil {
		return NotificationPreference{}, wrap("get notification preference", err)
	}
	if err := json.Unmarshal(raw, &out.Metadata); err != nil {
		return NotificationPreference{}, fmt.Errorf("decode notification preference metadata: %w", err)
	}
	return out, nil
}
