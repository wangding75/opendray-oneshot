package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/saga"
)

func validateRun(snapshot domain.RunSnapshot) error {
	_, err := domain.RestoreRun(snapshot)
	return err
}

func insertRun(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, snapshot domain.RunSnapshot) (domain.RunSnapshot, error) {
	out, err := scanRun(q.QueryRow(ctx, `
INSERT INTO oneshot_runs (
    id,task_id,delivery_id,provider_id,runtime_context_id,status,pid,exit_code,
    error_code,error_message,started_at,finished_at,created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
RETURNING id,task_id,delivery_id,provider_id,runtime_context_id,status,pid,exit_code,
          error_code,error_message,started_at,finished_at,created_at`,
		snapshot.ID, snapshot.TaskID, snapshot.DeliveryID, snapshot.ProviderID,
		snapshot.RuntimeContextID, snapshot.Status, snapshot.PID, snapshot.ExitCode,
		snapshot.ErrorCode, snapshot.ErrorMessage, snapshot.StartedAt, snapshot.FinishedAt,
		snapshot.CreatedAt.UTC()))
	if err != nil {
		return domain.RunSnapshot{}, mapWriteError("insert run", err)
	}
	return out, nil
}

// CreateRunWithState atomically creates the sole Run for a reserved Delivery
// and persists the domain-approved Delivery and Task transitions. New execution
// code uses CreateRunWithSaga so crash recovery exists in the same commit.
func (s *Store) CreateRunWithState(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, delivery domain.DeliverySnapshot, run domain.RunSnapshot) (domain.TaskSnapshot, domain.DeliverySnapshot, domain.RunSnapshot, error) {
	return s.createRunTransaction(ctx, owner, task, expectedTaskVersion, delivery, run, nil)
}

// CreateRunWithSaga atomically persists Task, Delivery, Run and the initial
// run_created checkpoint. There is no crash window containing a starting Run
// that is invisible to ListRecoverableRuns.
func (s *Store) CreateRunWithSaga(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, delivery domain.DeliverySnapshot, run domain.RunSnapshot, initial saga.State) (domain.TaskSnapshot, domain.DeliverySnapshot, domain.RunSnapshot, error) {
	if err := initial.Validate(); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if initial.Stage != saga.StageRunCreated || initial.RunID != run.ID || initial.TaskID != task.ID || initial.DeliveryID != delivery.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("initial Saga checkpoint must match the new Run at run_created")
	}
	return s.createRunTransaction(ctx, owner, task, expectedTaskVersion, delivery, run, &initial)
}

func (s *Store) createRunTransaction(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, delivery domain.DeliverySnapshot, run domain.RunSnapshot, initial *saga.State) (domain.TaskSnapshot, domain.DeliverySnapshot, domain.RunSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if err := validateTask(task); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if err := validateDelivery(delivery); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if err := validateRun(run); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID ||
		delivery.RequestedByKind != owner.Kind || delivery.RequestedByID != owner.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.NewDomainError(domain.ErrorForbidden, "Run owner mismatch", nil)
	}
	if task.ID != delivery.TaskID || task.ID != run.TaskID || delivery.ID != run.DeliveryID || task.ProviderID != run.ProviderID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("Task, Delivery and Run identity mismatch")
	}
	if delivery.RunID == nil || *delivery.RunID != run.ID || task.CurrentRunID == nil || *task.CurrentRunID != run.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("Task and Delivery must reference the new Run")
	}
	if task.Version != expectedTaskVersion+1 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("Task snapshot version must equal expected version plus one")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, wrap("begin run transaction", err)
	}
	defer rollback(ctx, tx)

	// Serialize task/run creation before the partial unique index is evaluated.
	var existingVersion int64
	if err := tx.QueryRow(ctx, `SELECT version FROM oneshot_tasks
WHERE id=$1 AND principal_kind=$2 AND principal_id=$3 FOR UPDATE`, task.ID, owner.Kind, owner.ID).Scan(&existingVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, notFound(domain.ErrorTaskNotFound, "Task")
		}
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, wrap("lock task for run", err)
	}
	if existingVersion != expectedTaskVersion {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Task version conflict", nil)
	}
	var existingRunID *string
	if err := tx.QueryRow(ctx, `SELECT run_id FROM oneshot_deliveries
WHERE id=$1 AND task_id=$2 FOR UPDATE`, delivery.ID, task.ID).Scan(&existingRunID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, notFound(domain.ErrorTaskNotFound, "Delivery")
		}
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, wrap("lock delivery for run", err)
	}
	if existingRunID != nil && *existingRunID != run.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Delivery already owns a Run", nil)
	}

	persistedRun, err := insertRun(ctx, tx, run)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	persistedDelivery, err := updateDeliveryRow(ctx, tx, owner, delivery)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	persistedTask, err := updateTaskRow(ctx, tx, owner, task, expectedTaskVersion)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if initial != nil {
		if err := insertInitialSagaState(ctx, tx, *initial); err != nil {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
		}
	}
	if err := insertRunCreatedLifecycle(ctx, tx, owner, persistedRun); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if err := insertTaskLifecycle(ctx, tx, persistedTask, persistedTask.Status, persistedTask.Version, persistedTask.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, mapWriteError("commit run transaction", err)
	}
	return persistedTask, persistedDelivery, persistedRun, nil
}

func insertInitialSagaState(ctx context.Context, tx pgx.Tx, state saga.State) error {
	_, err := tx.Exec(ctx, `INSERT INTO oneshot_run_sagas (
    run_id,task_id,delivery_id,stage,credential_lease_id,pid,exit_code,
    result_error_code,result_error_message,result_cancelled,result_timed_out,
    failure_stage,primary_error_code,primary_error_message,compensation_error,updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		state.RunID, state.TaskID, state.DeliveryID, state.Stage,
		state.CredentialLeaseID, state.PID, state.ExitCode, state.ResultErrorCode,
		state.ResultErrorMessage, state.ResultCancelled, state.ResultTimedOut,
		state.FailureStage, state.PrimaryErrorCode, state.PrimaryErrorMessage,
		state.CompensationError, state.UpdatedAt.UTC())
	if err != nil {
		return mapWriteError("insert initial run saga", err)
	}
	return nil
}

func updateDeliveryRow(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, owner domain.Owner, snapshot domain.DeliverySnapshot) (domain.DeliverySnapshot, error) {
	out, err := scanDelivery(q.QueryRow(ctx, `
UPDATE oneshot_deliveries d SET
    status=$1,attempt=$2,available_at=$3,lease_owner=$4,lease_until=$5,
    run_id=$6,last_error_code=$7,updated_at=$8
FROM oneshot_tasks t
WHERE d.id=$9 AND t.id=d.task_id AND t.principal_kind=$10 AND t.principal_id=$11
RETURNING d.id,d.task_id,d.operation,d.requested_by_kind,d.requested_by_id,d.input,
          d.idempotency_key,d.payload_sha256,d.status,d.attempt,d.max_attempts,
          d.available_at,d.lease_owner,d.lease_until,d.run_id,d.last_error_code,d.created_at,d.updated_at`,
		snapshot.Status, snapshot.Attempt, snapshot.AvailableAt.UTC(), snapshot.LeaseOwner,
		snapshot.LeaseUntil, snapshot.RunID, snapshot.LastErrorCode, snapshot.UpdatedAt.UTC(),
		snapshot.ID, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeliverySnapshot{}, notFound(domain.ErrorTaskNotFound, "Delivery")
	}
	if err != nil {
		return domain.DeliverySnapshot{}, mapWriteError("update delivery", err)
	}
	return out, nil
}

func (s *Store) GetRun(ctx context.Context, owner domain.Owner, id string) (domain.RunSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.RunSnapshot{}, err
	}
	out, err := scanRun(s.pool.QueryRow(ctx, `
SELECT r.id,r.task_id,r.delivery_id,r.provider_id,r.runtime_context_id,r.status,r.pid,r.exit_code,
       r.error_code,r.error_message,r.started_at,r.finished_at,r.created_at
FROM oneshot_runs r JOIN oneshot_tasks t ON t.id=r.task_id
WHERE r.id=$1 AND t.principal_kind=$2 AND t.principal_id=$3`, id, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RunSnapshot{}, notFound(domain.ErrorRunNotFound, "Run")
	}
	if err != nil {
		return domain.RunSnapshot{}, wrap("get run", err)
	}
	return out, nil
}

func (s *Store) UpdateRun(ctx context.Context, owner domain.Owner, snapshot domain.RunSnapshot) (domain.RunSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.RunSnapshot{}, err
	}
	if err := validateRun(snapshot); err != nil {
		return domain.RunSnapshot{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.RunSnapshot{}, wrap("begin Run lifecycle transaction", err)
	}
	defer rollback(ctx, tx)
	persisted, err := updateRunRow(ctx, tx, owner, snapshot)
	if err != nil {
		return domain.RunSnapshot{}, err
	}
	if persisted.Status == domain.RunRunning {
		if err := insertRunStatusLifecycle(ctx, tx, owner, persisted); err != nil {
			return domain.RunSnapshot{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunSnapshot{}, mapWriteError("commit Run lifecycle transaction", err)
	}
	return persisted, nil
}

func updateRunRow(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, owner domain.Owner, snapshot domain.RunSnapshot) (domain.RunSnapshot, error) {
	out, err := scanRun(q.QueryRow(ctx, `
UPDATE oneshot_runs r SET
    status=$1,pid=$2,exit_code=$3,error_code=$4,error_message=$5,started_at=$6,finished_at=$7
FROM oneshot_tasks t
WHERE r.id=$8 AND t.id=r.task_id AND t.principal_kind=$9 AND t.principal_id=$10
RETURNING r.id,r.task_id,r.delivery_id,r.provider_id,r.runtime_context_id,r.status,r.pid,r.exit_code,
          r.error_code,r.error_message,r.started_at,r.finished_at,r.created_at`,
		snapshot.Status, snapshot.PID, snapshot.ExitCode, snapshot.ErrorCode, snapshot.ErrorMessage,
		snapshot.StartedAt, snapshot.FinishedAt, snapshot.ID, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RunSnapshot{}, notFound(domain.ErrorRunNotFound, "Run")
	}
	if err != nil {
		return domain.RunSnapshot{}, mapWriteError("update run", err)
	}
	return out, nil
}

// FinalizeRunWithTask atomically persists a terminal Run and the matching Task
// outcome. This prevents a crash between separate Run and Task updates from
// leaving a terminal Run attached to a permanently running Task.
func (s *Store) FinalizeRunWithTask(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, run domain.RunSnapshot) (domain.TaskSnapshot, domain.RunSnapshot, error) {
	if s == nil || s.pool == nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.NewDomainError(domain.ErrorQueueUnavailable, "One-shot store unavailable", nil)
	}
	if err := validateOwner(owner); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	if err := validateTask(task); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	if err := validateRun(run); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	if !run.Status.Terminal() {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("Run must be terminal before finalization")
	}
	if task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.NewDomainError(domain.ErrorForbidden, "Task owner mismatch", nil)
	}
	if task.ID != run.TaskID || task.ProviderID != run.ProviderID || task.CurrentRunID == nil || *task.CurrentRunID != run.ID {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("Task and terminal Run identity mismatch")
	}
	if task.Version != expectedTaskVersion+1 {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("Task snapshot version must equal expected version plus one")
	}
	if string(task.Status) != string(run.Status) {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.InvalidRequestf("Task outcome must match terminal Run outcome")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, wrap("begin Run finalization transaction", err)
	}
	defer rollback(ctx, tx)

	persistedRun, err := updateRunRow(ctx, tx, owner, run)
	if err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	persistedTask, err := updateTaskRow(ctx, tx, owner, task, expectedTaskVersion)
	if err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	if err := insertRunStatusLifecycle(ctx, tx, owner, persistedRun); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	if err := insertTaskLifecycle(ctx, tx, persistedTask, persistedTask.Status, persistedTask.Version, persistedTask.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	if err := insertTerminalNotification(ctx, tx, persistedTask, persistedRun); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, mapWriteError("commit Run finalization transaction", err)
	}
	return persistedTask, persistedRun, nil
}

func (s *Store) ListRuns(ctx context.Context, owner domain.Owner, taskID string, req PageRequest) (Page[domain.RunSnapshot], error) {
	if err := validateOwner(owner); err != nil {
		return Page[domain.RunSnapshot]{}, err
	}
	limit, cursor, err := normalizePage(req)
	if err != nil {
		return Page[domain.RunSnapshot]{}, err
	}
	query := `
SELECT r.id,r.task_id,r.delivery_id,r.provider_id,r.runtime_context_id,r.status,r.pid,r.exit_code,
       r.error_code,r.error_message,r.started_at,r.finished_at,r.created_at
FROM oneshot_runs r JOIN oneshot_tasks t ON t.id=r.task_id
WHERE t.principal_kind=$1 AND t.principal_id=$2 AND ($3='' OR r.task_id=$3)`
	args := []any{owner.Kind, owner.ID, taskID}
	if cursor != nil {
		query += ` AND (r.created_at,r.id) < ($4,$5)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += ` ORDER BY r.created_at DESC,r.id DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[domain.RunSnapshot]{}, wrap("list runs", err)
	}
	defer rows.Close()
	items := make([]domain.RunSnapshot, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanRun(rows)
		if scanErr != nil {
			return Page[domain.RunSnapshot]{}, wrap("scan run page", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.RunSnapshot]{}, wrap("list runs rows", err)
	}
	page := Page[domain.RunSnapshot]{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func scanRun(row scanner) (domain.RunSnapshot, error) {
	var snapshot domain.RunSnapshot
	if err := row.Scan(&snapshot.ID, &snapshot.TaskID, &snapshot.DeliveryID,
		&snapshot.ProviderID, &snapshot.RuntimeContextID, &snapshot.Status,
		&snapshot.PID, &snapshot.ExitCode, &snapshot.ErrorCode, &snapshot.ErrorMessage,
		&snapshot.StartedAt, &snapshot.FinishedAt, &snapshot.CreatedAt); err != nil {
		return domain.RunSnapshot{}, err
	}
	restored, err := domain.RestoreRun(snapshot)
	if err != nil {
		return domain.RunSnapshot{}, fmt.Errorf("restore run: %w", err)
	}
	return restored.Snapshot(), nil
}

// CreateContinueRunWithSaga atomically acquires the RuntimeContext and creates
// the sole Run for a reserved continue Delivery. The busy transition is in the
// same serializable transaction as Task/Delivery/Run/Saga persistence.
func (s *Store) CreateContinueRunWithSaga(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, delivery domain.DeliverySnapshot, run domain.RunSnapshot, runtimeContext domain.RuntimeContextSnapshot, expectedContextVersion int64, initial saga.State) (domain.TaskSnapshot, domain.DeliverySnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, error) {
	if err := initial.Validate(); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := validateTask(task); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := validateDelivery(delivery); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := validateRun(run); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := validateRuntimeContext(runtimeContext); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if delivery.Operation != domain.DeliveryContinue || run.RuntimeContextID == nil || *run.RuntimeContextID != runtimeContext.ID || runtimeContext.Status != domain.ContextBusy {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("continue Run requires the acquired RuntimeContext")
	}
	if initial.Stage != saga.StageRunCreated || initial.RunID != run.ID || initial.TaskID != task.ID || initial.DeliveryID != delivery.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("initial Saga checkpoint must match Task, Delivery, and Run")
	}
	if task.Version != expectedTaskVersion+1 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("Task snapshot version must equal expected version plus one")
	}
	if task.CurrentRunID == nil || *task.CurrentRunID != run.ID || delivery.RunID == nil || *delivery.RunID != run.ID || run.TaskID != task.ID || run.DeliveryID != delivery.ID || run.ProviderID != task.ProviderID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("continue Task, Delivery, and Run identity mismatch")
	}
	if runtimeContext.Version != expectedContextVersion+1 {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("RuntimeContext snapshot version must equal expected version plus one")
	}
	if task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID || runtimeContext.PrincipalKind != owner.Kind || runtimeContext.PrincipalID != owner.ID ||
		task.ProjectID != runtimeContext.ProjectID || task.ProviderID != runtimeContext.ProviderID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "continue RuntimeContext identity mismatch", nil)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, wrap("begin continue Run transaction", err)
	}
	defer rollback(ctx, tx)

	persistedContext, err := updateRuntimeContextRow(ctx, tx, owner, runtimeContext, expectedContextVersion)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	var existingVersion int64
	if err := tx.QueryRow(ctx, `SELECT version FROM oneshot_tasks WHERE id=$1 AND principal_kind=$2 AND principal_id=$3 FOR UPDATE`, task.ID, owner.Kind, owner.ID).Scan(&existingVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, notFound(domain.ErrorTaskNotFound, "Task")
		}
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, wrap("lock Task for continue Run", err)
	}
	if existingVersion != expectedTaskVersion {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Task version conflict", nil)
	}
	var existingRunID *string
	if err := tx.QueryRow(ctx, `SELECT run_id FROM oneshot_deliveries WHERE id=$1 AND task_id=$2 FOR UPDATE`, delivery.ID, task.ID).Scan(&existingRunID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, notFound(domain.ErrorTaskNotFound, "Delivery")
		}
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, wrap("lock continue Delivery", err)
	}
	if existingRunID != nil && *existingRunID != run.ID {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "Delivery already owns a Run", nil)
	}
	persistedRun, err := insertRun(ctx, tx, run)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	persistedDelivery, err := updateDeliveryRow(ctx, tx, owner, delivery)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	persistedTask, err := updateTaskRow(ctx, tx, owner, task, expectedTaskVersion)
	if err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := insertInitialSagaState(ctx, tx, initial); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := insertRunCreatedLifecycle(ctx, tx, owner, persistedRun); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := insertTaskLifecycle(ctx, tx, persistedTask, persistedTask.Status, persistedTask.Version, persistedTask.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskSnapshot{}, domain.DeliverySnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, mapWriteError("commit continue Run transaction", err)
	}
	return persistedTask, persistedDelivery, persistedRun, persistedContext, nil
}

// FinalizeRunWithTaskAndContext atomically persists terminal Run/Task state and
// either creates the first active provider RuntimeContext or releases the
// acquired continue RuntimeContext back to active.
func (s *Store) FinalizeRunWithTaskAndContext(ctx context.Context, owner domain.Owner, task domain.TaskSnapshot, expectedTaskVersion int64, run domain.RunSnapshot, runtimeContext domain.RuntimeContextSnapshot, expectedContextVersion int64, createContext bool) (domain.TaskSnapshot, domain.RunSnapshot, domain.RuntimeContextSnapshot, error) {
	if err := validateTask(task); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := validateRun(run); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := validateRuntimeContext(runtimeContext); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if !run.Status.Terminal() || runtimeContext.Status != domain.ContextActive {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("terminal Run and active RuntimeContext are required")
	}
	if task.RuntimeContextID == nil || *task.RuntimeContextID != runtimeContext.ID || task.ID != run.TaskID || task.ProviderID != runtimeContext.ProviderID || task.ProjectID != runtimeContext.ProjectID {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("Task, Run, and RuntimeContext identity mismatch")
	}
	if task.PrincipalKind != owner.Kind || task.PrincipalID != owner.ID || runtimeContext.PrincipalKind != owner.Kind || runtimeContext.PrincipalID != owner.ID {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "terminal RuntimeContext ownership mismatch", nil)
	}
	if task.Version != expectedTaskVersion+1 || task.CurrentRunID == nil || *task.CurrentRunID != run.ID || string(task.Status) != string(run.Status) {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("terminal Task and Run versions or states do not match")
	}
	if !createContext && runtimeContext.Version != expectedContextVersion+1 {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("released RuntimeContext version must equal expected version plus one")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, wrap("begin context finalization transaction", err)
	}
	defer rollback(ctx, tx)
	var persistedContext domain.RuntimeContextSnapshot
	if createContext {
		if runtimeContext.Version != 1 {
			return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("new RuntimeContext must start at version 1")
		}
		persistedContext, err = insertRuntimeContextRow(ctx, tx, runtimeContext)
	} else {
		persistedContext, err = updateRuntimeContextRow(ctx, tx, owner, runtimeContext, expectedContextVersion)
	}
	if err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	persistedRun, err := updateRunRow(ctx, tx, owner, run)
	if err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	persistedTask, err := updateTaskRow(ctx, tx, owner, task, expectedTaskVersion)
	if err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := insertRunStatusLifecycle(ctx, tx, owner, persistedRun); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := insertTaskLifecycle(ctx, tx, persistedTask, persistedTask.Status, persistedTask.Version, persistedTask.UpdatedAt); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := insertTerminalNotification(ctx, tx, persistedTask, persistedRun); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskSnapshot{}, domain.RunSnapshot{}, domain.RuntimeContextSnapshot{}, mapWriteError("commit context finalization transaction", err)
	}
	return persistedTask, persistedRun, persistedContext, nil
}
