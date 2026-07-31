package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/saga"
)

// RecordSagaState upserts the owner-scoped durable execution checkpoint.
func (s *Store) RecordSagaState(ctx context.Context, owner domain.Owner, state saga.State) error {
	if s == nil || s.pool == nil {
		return domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot store unavailable", nil)
	}
	if err := validateOwner(owner); err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return err
	}
	commandTag, err := s.pool.Exec(ctx, `
INSERT INTO oneshot_run_sagas (
    run_id,task_id,delivery_id,stage,credential_lease_id,pid,exit_code,
    result_error_code,result_error_message,result_cancelled,result_timed_out,
    failure_stage,primary_error_code,primary_error_message,compensation_error,updated_at
)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16
FROM oneshot_runs r JOIN oneshot_tasks t ON t.id=r.task_id
WHERE r.id=$1 AND r.task_id=$2 AND r.delivery_id=$3
  AND t.principal_kind=$17 AND t.principal_id=$18
ON CONFLICT (run_id) DO UPDATE SET
    stage=EXCLUDED.stage,
    credential_lease_id=EXCLUDED.credential_lease_id,
    pid=EXCLUDED.pid,
    exit_code=EXCLUDED.exit_code,
    result_error_code=EXCLUDED.result_error_code,
    result_error_message=EXCLUDED.result_error_message,
    result_cancelled=EXCLUDED.result_cancelled,
    result_timed_out=EXCLUDED.result_timed_out,
    failure_stage=EXCLUDED.failure_stage,
    primary_error_code=EXCLUDED.primary_error_code,
    primary_error_message=EXCLUDED.primary_error_message,
    compensation_error=EXCLUDED.compensation_error,
    updated_at=EXCLUDED.updated_at`,
		state.RunID, state.TaskID, state.DeliveryID, state.Stage,
		state.CredentialLeaseID, state.PID, state.ExitCode, state.ResultErrorCode,
		state.ResultErrorMessage, state.ResultCancelled, state.ResultTimedOut,
		state.FailureStage, state.PrimaryErrorCode, state.PrimaryErrorMessage,
		state.CompensationError, state.UpdatedAt.UTC(), owner.Kind, owner.ID)
	if err != nil {
		return mapWriteError("record run saga state", err)
	}
	if commandTag.RowsAffected() == 0 {
		return notFound(domain.ErrorRunNotFound, "Run")
	}
	return nil
}

// GetSagaState loads one owner-scoped execution checkpoint.
func (s *Store) GetSagaState(ctx context.Context, owner domain.Owner, runID string) (saga.State, error) {
	if err := validateOwner(owner); err != nil {
		return saga.State{}, err
	}
	state, err := scanSagaState(s.pool.QueryRow(ctx, `
SELECT g.run_id,g.task_id,g.delivery_id,g.stage,g.credential_lease_id,g.pid,g.exit_code,
       g.result_error_code,g.result_error_message,g.result_cancelled,g.result_timed_out,
       g.failure_stage,g.primary_error_code,g.primary_error_message,g.compensation_error,g.updated_at
FROM oneshot_run_sagas g
JOIN oneshot_runs r ON r.id=g.run_id
JOIN oneshot_tasks t ON t.id=r.task_id
WHERE g.run_id=$1 AND t.principal_kind=$2 AND t.principal_id=$3`, runID, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return saga.State{}, notFound(domain.ErrorRunNotFound, "Run saga")
	}
	if err != nil {
		return saga.State{}, wrap("get run saga state", err)
	}
	return state, nil
}

func scanSagaState(row scanner) (saga.State, error) {
	var state saga.State
	if err := row.Scan(&state.RunID, &state.TaskID, &state.DeliveryID, &state.Stage,
		&state.CredentialLeaseID, &state.PID, &state.ExitCode, &state.ResultErrorCode,
		&state.ResultErrorMessage, &state.ResultCancelled, &state.ResultTimedOut,
		&state.FailureStage, &state.PrimaryErrorCode, &state.PrimaryErrorMessage,
		&state.CompensationError, &state.UpdatedAt); err != nil {
		return saga.State{}, err
	}
	state.UpdatedAt = state.UpdatedAt.UTC()
	if err := state.Validate(); err != nil {
		return saga.State{}, fmt.Errorf("restore saga state: %w", err)
	}
	return state, nil
}

// ListRecoverableRuns returns non-terminal Runs only after their queue lease
// expires, plus terminal Sagas that are unacknowledged or still own a credential
// lease. Active workers must never be mistaken for crash recovery. This includes
// the narrow case where queue ACK committed but the AckObserver checkpoint
// failed; startup/periodic recovery can idempotently close that Saga.
func (s *Store) ListRecoverableRuns(ctx context.Context, limit int) ([]saga.RecoveryItem, error) {
	if s == nil || s.pool == nil {
		return nil, domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot store unavailable", nil)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
SELECT
    t.id,t.principal_kind,t.principal_id,t.project_id,t.provider_id,t.source,t.prompt,
    t.status,t.current_run_id,t.runtime_context_id,t.version,t.created_at,t.updated_at,
    d.id,d.task_id,d.operation,d.requested_by_kind,d.requested_by_id,d.input,
    d.idempotency_key,d.payload_sha256,d.status,d.attempt,d.max_attempts,
    d.available_at,d.lease_owner,d.lease_until,d.run_id,d.last_error_code,d.created_at,d.updated_at,
    r.id,r.task_id,r.delivery_id,r.provider_id,r.runtime_context_id,r.status,r.pid,r.exit_code,
    r.error_code,r.error_message,r.started_at,r.finished_at,r.created_at,
    g.run_id,g.task_id,g.delivery_id,g.stage,g.credential_lease_id,g.pid,g.exit_code,
    g.result_error_code,g.result_error_message,g.result_cancelled,g.result_timed_out,
    g.failure_stage,g.primary_error_code,g.primary_error_message,g.compensation_error,g.updated_at
FROM oneshot_runs r
JOIN oneshot_tasks t ON t.id=r.task_id
JOIN oneshot_deliveries d ON d.id=r.delivery_id
JOIN oneshot_run_sagas g ON g.run_id=r.id
WHERE (
        r.status IN ('created','starting','running','collecting_output')
        AND (d.lease_until IS NULL OR d.lease_until<=clock_timestamp())
      )
   OR (
        r.status IN ('waiting_input','completed','failed','cancelled','timed_out')
        AND (d.status<>'acknowledged' OR g.stage<>'acknowledged' OR g.credential_lease_id IS NOT NULL)
      )
ORDER BY g.updated_at,r.created_at,r.id
LIMIT $1`, limit)
	if err != nil {
		return nil, wrap("list recoverable runs", err)
	}
	defer rows.Close()
	items := make([]saga.RecoveryItem, 0, limit)
	for rows.Next() {
		item, scanErr := scanRecoveryItem(rows)
		if scanErr != nil {
			return nil, wrap("scan recoverable run", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("iterate recoverable runs", err)
	}
	return items, nil
}

func scanRecoveryItem(row scanner) (saga.RecoveryItem, error) {
	var task domain.TaskSnapshot
	var delivery domain.DeliverySnapshot
	var run domain.RunSnapshot
	var state saga.State
	var sourceRaw, inputRaw []byte
	if err := row.Scan(
		&task.ID, &task.PrincipalKind, &task.PrincipalID, &task.ProjectID, &task.ProviderID,
		&sourceRaw, &task.Prompt, &task.Status, &task.CurrentRunID, &task.RuntimeContextID,
		&task.Version, &task.CreatedAt, &task.UpdatedAt,
		&delivery.ID, &delivery.TaskID, &delivery.Operation, &delivery.RequestedByKind,
		&delivery.RequestedByID, &inputRaw, &delivery.IdempotencyKey, &delivery.PayloadSHA256,
		&delivery.Status, &delivery.Attempt, &delivery.MaxAttempts, &delivery.AvailableAt,
		&delivery.LeaseOwner, &delivery.LeaseUntil, &delivery.RunID, &delivery.LastErrorCode,
		&delivery.CreatedAt, &delivery.UpdatedAt,
		&run.ID, &run.TaskID, &run.DeliveryID, &run.ProviderID, &run.RuntimeContextID,
		&run.Status, &run.PID, &run.ExitCode, &run.ErrorCode, &run.ErrorMessage,
		&run.StartedAt, &run.FinishedAt, &run.CreatedAt,
		&state.RunID, &state.TaskID, &state.DeliveryID, &state.Stage,
		&state.CredentialLeaseID, &state.PID, &state.ExitCode, &state.ResultErrorCode,
		&state.ResultErrorMessage, &state.ResultCancelled, &state.ResultTimedOut,
		&state.FailureStage, &state.PrimaryErrorCode, &state.PrimaryErrorMessage,
		&state.CompensationError, &state.UpdatedAt,
	); err != nil {
		return saga.RecoveryItem{}, err
	}
	if err := json.Unmarshal(sourceRaw, &task.Source); err != nil {
		return saga.RecoveryItem{}, fmt.Errorf("decode recovery task source: %w", err)
	}
	if err := json.Unmarshal(inputRaw, &delivery.Input); err != nil {
		return saga.RecoveryItem{}, fmt.Errorf("decode recovery delivery input: %w", err)
	}
	restoredTask, err := domain.RestoreTask(task)
	if err != nil {
		return saga.RecoveryItem{}, err
	}
	restoredDelivery, err := domain.RestoreDelivery(delivery)
	if err != nil {
		return saga.RecoveryItem{}, err
	}
	restoredRun, err := domain.RestoreRun(run)
	if err != nil {
		return saga.RecoveryItem{}, err
	}
	state.UpdatedAt = state.UpdatedAt.UTC()
	if err := state.Validate(); err != nil {
		return saga.RecoveryItem{}, err
	}
	owner := domain.Owner{Kind: task.PrincipalKind, ID: task.PrincipalID}
	return saga.RecoveryItem{
		Owner: owner, Task: restoredTask.Snapshot(), Delivery: restoredDelivery.Snapshot(),
		Run: restoredRun.Snapshot(), Saga: state,
	}, nil
}

// DatabaseRecoveryNow is a narrow test seam for recovery timestamp ordering.
func (s *Store) DatabaseRecoveryNow(ctx context.Context) (time.Time, error) {
	return s.DatabaseNow(ctx)
}
