package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresOutboxStore is the production durable delivery outbox.
type PostgresOutboxStore struct {
	pool *pgxpool.Pool
}

func NewPostgresOutboxStore(pool *pgxpool.Pool) *PostgresOutboxStore {
	return &PostgresOutboxStore{pool: pool}
}

func (s *PostgresOutboxStore) Create(ctx context.Context, record OutboxRecord) (OutboxRecord, bool, error) {
	if s == nil || s.pool == nil {
		return OutboxRecord{}, false, errors.New("channel delivery postgres store is unavailable")
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	if record.NextAttemptAt.IsZero() {
		record.NextAttemptAt = now
	}
	if record.Status == "" {
		record.Status = StatusPending
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO channel_delivery_outbox
			(id, idempotency_key, channel_id, payload, status, progress,
			 attempt_count, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, idempotency_key, channel_id, payload, status, progress,
		          attempt_count, next_attempt_at, lease_owner, lease_until,
		          last_error, receipt, created_at, updated_at, delivered_at`,
		record.ID, record.IdempotencyKey, record.ChannelID, record.Payload,
		string(record.Status), record.Progress, record.AttemptCount,
		record.NextAttemptAt, record.CreatedAt, record.UpdatedAt,
	)
	stored, err := scanOutbox(row)
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return OutboxRecord{}, false, fmt.Errorf("insert channel delivery outbox: %w", err)
	}

	row = s.pool.QueryRow(ctx, `
		SELECT id, idempotency_key, channel_id, payload, status, progress,
		       attempt_count, next_attempt_at, lease_owner, lease_until,
		       last_error, receipt, created_at, updated_at, delivered_at
		FROM channel_delivery_outbox WHERE idempotency_key=$1`, record.IdempotencyKey)
	stored, err = scanOutbox(row)
	if err != nil {
		return OutboxRecord{}, false, fmt.Errorf("load existing channel delivery outbox: %w", err)
	}
	return stored, false, nil
}

func (s *PostgresOutboxStore) Get(ctx context.Context, id string) (OutboxRecord, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, idempotency_key, channel_id, payload, status, progress,
		       attempt_count, next_attempt_at, lease_owner, lease_until,
		       last_error, receipt, created_at, updated_at, delivered_at
		FROM channel_delivery_outbox WHERE id=$1`, id)
	record, err := scanOutbox(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutboxRecord{}, ErrOutboxNotFound
	}
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("get channel delivery outbox: %w", err)
	}
	return record, nil
}

func (s *PostgresOutboxStore) Claim(ctx context.Context, id, owner string, lease time.Duration) (OutboxRecord, bool, error) {
	now := time.Now().UTC()
	row := s.pool.QueryRow(ctx, `
		UPDATE channel_delivery_outbox
		SET status='sending', lease_owner=$2, lease_until=$3,
		    attempt_count=attempt_count+1, updated_at=$4
		WHERE id=$1
		  AND status NOT IN ('delivered','dead')
		  AND next_attempt_at <= $4
		  AND (status <> 'sending' OR lease_until IS NULL OR lease_until <= $4)
		RETURNING id, idempotency_key, channel_id, payload, status, progress,
		          attempt_count, next_attempt_at, lease_owner, lease_until,
		          last_error, receipt, created_at, updated_at, delivered_at`,
		id, owner, now.Add(lease), now)
	record, err := scanOutbox(row)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := s.Get(ctx, id)
		if getErr != nil {
			return OutboxRecord{}, false, getErr
		}
		return existing, false, nil
	}
	if err != nil {
		return OutboxRecord{}, false, fmt.Errorf("claim channel delivery outbox: %w", err)
	}
	return record, true, nil
}

func (s *PostgresOutboxStore) ClaimDue(ctx context.Context, owner string, limit int, lease time.Duration) ([]OutboxRecord, error) {
	if limit <= 0 {
		limit = 32
	}
	now := time.Now().UTC()
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id
			FROM channel_delivery_outbox
			WHERE status NOT IN ('delivered','dead')
			  AND next_attempt_at <= $1
			  AND (status <> 'sending' OR lease_until IS NULL OR lease_until <= $1)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE channel_delivery_outbox AS outbox
		SET status='sending', lease_owner=$3, lease_until=$4,
		    attempt_count=attempt_count+1, updated_at=$1
		FROM due
		WHERE outbox.id=due.id
		RETURNING outbox.id, outbox.idempotency_key, outbox.channel_id,
		          outbox.payload, outbox.status, outbox.progress,
		          outbox.attempt_count, outbox.next_attempt_at,
		          outbox.lease_owner, outbox.lease_until, outbox.last_error,
		          outbox.receipt, outbox.created_at, outbox.updated_at,
		          outbox.delivered_at`, now, limit, owner, now.Add(lease))
	if err != nil {
		return nil, fmt.Errorf("claim due channel deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]OutboxRecord, 0, limit)
	for rows.Next() {
		record, scanErr := scanOutbox(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *PostgresOutboxStore) AppendAttempt(ctx context.Context, attempt ChannelDeliveryAttempt) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO channel_delivery_attempts
			(delivery_id, attempt_no, operation, status, started_at,
			 finished_at, error, retry_at)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8)`,
		attempt.DeliveryID, attempt.Attempt, attempt.Operation, attempt.Status,
		attempt.StartedAt, attempt.FinishedAt, attempt.Error, nullableTime(attempt.RetryAt))
	if err != nil {
		return fmt.Errorf("append channel delivery attempt: %w", err)
	}
	return nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func (s *PostgresOutboxStore) MarkProgress(ctx context.Context, id, owner string, progress int, receipt DeliveryReceipt) error {
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("marshal delivery receipt: %w", err)
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE channel_delivery_outbox
		SET progress=$3, receipt=$4::jsonb, updated_at=NOW()
		WHERE id=$1 AND lease_owner=$2`, id, owner, progress, receiptJSON)
	return checkUpdated(result.RowsAffected(), err, "mark channel delivery progress")
}

func (s *PostgresOutboxStore) MarkDelivered(ctx context.Context, id, owner string, receipt DeliveryReceipt) error {
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("marshal delivery receipt: %w", err)
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE channel_delivery_outbox
		SET status='delivered', progress=$3, receipt=$4::jsonb,
		    delivered_at=$5, updated_at=$5, lease_owner=NULL,
		    lease_until=NULL, last_error=NULL
		WHERE id=$1 AND lease_owner=$2`, id, owner, receipt.CompletedParts, receiptJSON, receipt.DeliveredAt)
	return checkUpdated(result.RowsAffected(), err, "mark channel delivery delivered")
}

func (s *PostgresOutboxStore) MarkRetry(ctx context.Context, id, owner, lastError string, nextAttempt time.Time) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE channel_delivery_outbox
		SET status='retry', last_error=$3, next_attempt_at=$4,
		    updated_at=NOW(), lease_owner=NULL, lease_until=NULL
		WHERE id=$1 AND lease_owner=$2`, id, owner, lastError, nextAttempt)
	return checkUpdated(result.RowsAffected(), err, "mark channel delivery retry")
}

func (s *PostgresOutboxStore) MarkDead(ctx context.Context, id, owner, lastError string) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE channel_delivery_outbox
		SET status='dead', last_error=$3, updated_at=NOW(),
		    lease_owner=NULL, lease_until=NULL
		WHERE id=$1 AND lease_owner=$2`, id, owner, lastError)
	return checkUpdated(result.RowsAffected(), err, "mark channel delivery dead")
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOutbox(row rowScanner) (OutboxRecord, error) {
	var record OutboxRecord
	var status string
	var leaseOwner, lastError *string
	var leaseUntil, deliveredAt *time.Time
	var receiptJSON []byte
	if err := row.Scan(
		&record.ID, &record.IdempotencyKey, &record.ChannelID, &record.Payload,
		&status, &record.Progress, &record.AttemptCount, &record.NextAttemptAt,
		&leaseOwner, &leaseUntil, &lastError, &receiptJSON, &record.CreatedAt,
		&record.UpdatedAt, &deliveredAt,
	); err != nil {
		return OutboxRecord{}, err
	}
	record.Status = OutboxStatus(status)
	if leaseOwner != nil {
		record.LeaseOwner = *leaseOwner
	}
	if leaseUntil != nil {
		record.LeaseUntil = *leaseUntil
	}
	if lastError != nil {
		record.LastError = *lastError
	}
	if deliveredAt != nil {
		record.DeliveredAt = *deliveredAt
	}
	if len(receiptJSON) > 0 && string(receiptJSON) != "null" {
		if err := json.Unmarshal(receiptJSON, &record.Receipt); err != nil {
			return OutboxRecord{}, fmt.Errorf("decode channel delivery receipt: %w", err)
		}
	}
	return record, nil
}

func checkUpdated(rows int64, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if rows == 0 {
		return fmt.Errorf("%s: lease lost", operation)
	}
	return nil
}
