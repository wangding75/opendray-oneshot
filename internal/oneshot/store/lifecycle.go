package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// ReplayCursor is an opaque stable sort position shared by lifecycle and
// normalized output events. Clients encode/decode it through the API layer.
type ReplayCursor struct {
	OccurredAt time.Time `json:"occurred_at"`
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
}

// ReplayEvent is the durable event representation used by One-shot WebSocket
// replay. Topic always belongs to the frozen oneshot.* namespace.
type ReplayEvent struct {
	Topic      string
	Data       map[string]any
	OccurredAt time.Time
	Cursor     ReplayCursor
}

type lifecycleQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func taskLifecycleTopic(status domain.TaskStatus) (string, bool) {
	if !status.Valid() {
		return "", false
	}
	if status == domain.TaskPending {
		return "oneshot.task.created", true
	}
	return "oneshot.task." + string(status), true
}

func runLifecycleTopic(status domain.RunStatus) (string, bool) {
	switch status {
	case domain.RunRunning:
		return "oneshot.run.started", true
	case domain.RunWaitingInput, domain.RunCompleted, domain.RunFailed, domain.RunCancelled, domain.RunTimedOut:
		return "oneshot.run." + string(status), true
	default:
		return "", false
	}
}

func taskLifecyclePayload(snapshot domain.TaskSnapshot, status domain.TaskStatus, version int64) map[string]any {
	payload := map[string]any{
		"task_id": snapshot.ID, "status": status, "principal_kind": snapshot.PrincipalKind,
		"principal_id": snapshot.PrincipalID, "project_id": snapshot.ProjectID,
		"provider_id": snapshot.ProviderID, "source": snapshot.Source, "version": version,
	}
	if snapshot.CurrentRunID != nil {
		payload["current_run_id"] = *snapshot.CurrentRunID
	}
	if snapshot.RuntimeContextID != nil {
		payload["runtime_context_id"] = *snapshot.RuntimeContextID
	}
	return payload
}

func runLifecyclePayload(snapshot domain.RunSnapshot) map[string]any {
	payload := map[string]any{
		"run_id": snapshot.ID, "task_id": snapshot.TaskID, "delivery_id": snapshot.DeliveryID,
		"provider_id": snapshot.ProviderID, "status": snapshot.Status,
	}
	if snapshot.RuntimeContextID != nil {
		payload["runtime_context_id"] = *snapshot.RuntimeContextID
	}
	if snapshot.PID != nil {
		payload["pid"] = *snapshot.PID
	}
	if snapshot.ExitCode != nil {
		payload["exit_code"] = *snapshot.ExitCode
	}
	if snapshot.ErrorCode != nil {
		payload["error_code"] = *snapshot.ErrorCode
	}
	if snapshot.ErrorMessage != nil {
		payload["error_message"] = *snapshot.ErrorMessage
	}
	return payload
}

func insertTaskLifecycle(ctx context.Context, q lifecycleQueryer, snapshot domain.TaskSnapshot, status domain.TaskStatus, version int64, occurredAt time.Time) error {
	topic, ok := taskLifecycleTopic(status)
	if !ok {
		return domain.InvalidRequestf("unsupported Task lifecycle status %q", status)
	}
	if version < 1 {
		return domain.InvalidRequestf("Task lifecycle version must be positive")
	}
	payload, err := json.Marshal(taskLifecyclePayload(snapshot, status, version))
	if err != nil {
		return domain.InvalidRequestf("Task lifecycle payload is not JSON-compatible")
	}
	var id int64
	err = q.QueryRow(ctx, `
INSERT INTO oneshot_lifecycle_events (
    principal_kind,principal_id,project_id,task_id,run_id,aggregate_kind,
    aggregate_id,sequence,topic,data,occurred_at
) VALUES ($1,$2,$3,$4,NULL,'task',$4,$5,$6,$7,$8)
ON CONFLICT (aggregate_kind,aggregate_id,sequence) DO NOTHING
RETURNING id`, snapshot.PrincipalKind, snapshot.PrincipalID, snapshot.ProjectID, snapshot.ID,
		version, topic, payload, occurredAt.UTC()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return mapWriteError("insert Task lifecycle event", err)
	}
	return nil
}

func insertInitialTaskLifecycle(ctx context.Context, q lifecycleQueryer, snapshot domain.TaskSnapshot) error {
	created := snapshot
	created.Status = domain.TaskPending
	created.CurrentRunID = nil
	created.RuntimeContextID = nil
	created.Version = 1
	created.UpdatedAt = created.CreatedAt
	if err := insertTaskLifecycle(ctx, q, created, domain.TaskPending, 1, created.CreatedAt); err != nil {
		return err
	}
	return insertTaskLifecycle(ctx, q, snapshot, snapshot.Status, snapshot.Version, snapshot.UpdatedAt)
}

func insertRunLifecycle(ctx context.Context, q lifecycleQueryer, owner domain.Owner, snapshot domain.RunSnapshot, topic string, occurredAt time.Time) error {
	if !strings.HasPrefix(topic, "oneshot.run.") {
		return domain.InvalidRequestf("invalid Run lifecycle topic %q", topic)
	}
	payload, err := json.Marshal(runLifecyclePayload(snapshot))
	if err != nil {
		return domain.InvalidRequestf("Run lifecycle payload is not JSON-compatible")
	}
	var id int64
	err = q.QueryRow(ctx, `
WITH locked_run AS (
    SELECT id
    FROM oneshot_runs
    WHERE id=$4 AND task_id=$1
    FOR UPDATE
), owned_task AS (
    SELECT principal_kind,principal_id,project_id,id
    FROM oneshot_tasks
    WHERE id=$1 AND principal_kind=$2 AND principal_id=$3
), next_sequence AS (
    SELECT COALESCE(MAX(le.sequence),0)+1 AS value
    FROM locked_run lr
    LEFT JOIN oneshot_lifecycle_events le
      ON le.aggregate_kind='run' AND le.aggregate_id=lr.id
)
INSERT INTO oneshot_lifecycle_events (
    principal_kind,principal_id,project_id,task_id,run_id,aggregate_kind,
    aggregate_id,sequence,topic,data,occurred_at
)
SELECT t.principal_kind,t.principal_id,t.project_id,t.id,lr.id,'run',lr.id,n.value,$5,$6,$7
FROM owned_task t CROSS JOIN locked_run lr CROSS JOIN next_sequence n
ON CONFLICT (aggregate_id,topic) WHERE aggregate_kind='run' DO UPDATE
SET data=oneshot_lifecycle_events.data
RETURNING id`, snapshot.TaskID, owner.Kind, owner.ID, snapshot.ID, topic, payload, occurredAt.UTC()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NewDomainError(domain.ErrorRunNotFound, "Run not found", nil)
	}
	if err != nil {
		return mapWriteError("insert Run lifecycle event", err)
	}
	return nil
}

func insertRunCreatedLifecycle(ctx context.Context, q lifecycleQueryer, owner domain.Owner, snapshot domain.RunSnapshot) error {
	return insertRunLifecycle(ctx, q, owner, snapshot, "oneshot.run.created", snapshot.CreatedAt)
}

func insertRunStatusLifecycle(ctx context.Context, q lifecycleQueryer, owner domain.Owner, snapshot domain.RunSnapshot) error {
	topic, ok := runLifecycleTopic(snapshot.Status)
	if !ok {
		return nil
	}
	occurredAt := snapshot.CreatedAt
	if snapshot.Status == domain.RunRunning && snapshot.StartedAt != nil {
		occurredAt = *snapshot.StartedAt
	}
	if snapshot.Status.Terminal() && snapshot.FinishedAt != nil {
		occurredAt = *snapshot.FinishedAt
	}
	return insertRunLifecycle(ctx, q, owner, snapshot, topic, occurredAt)
}

func resolveNotificationAddress(ctx context.Context, q lifecycleQueryer, task domain.TaskSnapshot) (*domain.ReplyAddress, error) {
	var address domain.ReplyAddress
	var metadataRaw []byte
	err := q.QueryRow(ctx, `
SELECT channel_id,conversation_id,thread_id,message_id,metadata
FROM oneshot_notification_preferences
WHERE principal_kind=$1 AND principal_id=$2 AND project_id=$3 AND enabled=TRUE`,
		task.PrincipalKind, task.PrincipalID, task.ProjectID).Scan(
		&address.ChannelID, &address.ConversationID, &address.ThreadID, &address.MessageID, &metadataRaw)
	if err == nil {
		if len(metadataRaw) > 0 {
			if unmarshalErr := json.Unmarshal(metadataRaw, &address.Metadata); unmarshalErr != nil {
				return nil, fmt.Errorf("decode notification preference metadata: %w", unmarshalErr)
			}
		}
		return &address, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, mapWriteError("resolve notification preference", err)
	}
	if task.Source.ReplyAddress == nil {
		return nil, nil
	}
	fallback := *task.Source.ReplyAddress
	return &fallback, nil
}

func insertTerminalNotification(ctx context.Context, q lifecycleQueryer, task domain.TaskSnapshot, run domain.RunSnapshot) error {
	if !run.Status.Terminal() {
		return nil
	}
	address, err := resolveNotificationAddress(ctx, q, task)
	if err != nil {
		return err
	}
	if address == nil {
		return nil
	}
	destination, err := json.Marshal(map[string]any{
		"channel_id": address.ChannelID, "conversation_id": address.ConversationID,
		"thread_id": address.ThreadID, "message_id": address.MessageID,
	})
	if err != nil {
		return domain.InvalidRequestf("notification destination is not JSON-compatible")
	}
	payloadMap := map[string]any{
		"task_id": task.ID, "run_id": run.ID, "project_id": task.ProjectID,
		"provider_id": task.ProviderID, "task_status": task.Status, "run_status": run.Status,
		"continue_available": task.RuntimeContextID != nil,
		"artifact_path":      "/api/v1/oneshot/runs/" + run.ID + "/artifacts",
	}
	if run.ErrorCode != nil {
		payloadMap["error_code"] = *run.ErrorCode
	}
	if run.ErrorMessage != nil {
		payloadMap["error_message"] = *run.ErrorMessage
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return domain.InvalidRequestf("notification payload is not JSON-compatible")
	}
	digest := sha256.Sum256([]byte("terminal:" + run.ID))
	id := "onf_" + hex.EncodeToString(digest[:12])
	when := task.UpdatedAt
	if run.FinishedAt != nil {
		when = *run.FinishedAt
	}
	var inserted string
	err = q.QueryRow(ctx, `
INSERT INTO oneshot_notification_outbox (
    id,idempotency_key,task_id,run_id,event_type,destination,payload,status,
    attempt_count,next_attempt_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',0,$8)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING id`, id, "terminal:"+run.ID, task.ID, run.ID,
		"oneshot.task."+string(task.Status), destination, payload, when.UTC()).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return mapWriteError("insert terminal notification outbox", err)
	}
	return nil
}

func insertTaskTerminalNotification(ctx context.Context, q lifecycleQueryer, task domain.TaskSnapshot) error {
	if task.Status != domain.TaskCancelled {
		return nil
	}
	address, err := resolveNotificationAddress(ctx, q, task)
	if err != nil {
		return err
	}
	if address == nil {
		return nil
	}
	destination, err := json.Marshal(map[string]any{"channel_id": address.ChannelID, "conversation_id": address.ConversationID, "thread_id": address.ThreadID, "message_id": address.MessageID})
	if err != nil {
		return domain.InvalidRequestf("notification destination is not JSON-compatible")
	}
	payload, err := json.Marshal(map[string]any{"task_id": task.ID, "project_id": task.ProjectID, "provider_id": task.ProviderID, "task_status": task.Status, "continue_available": task.RuntimeContextID != nil})
	if err != nil {
		return domain.InvalidRequestf("notification payload is not JSON-compatible")
	}
	key := fmt.Sprintf("task-terminal:%s:%d", task.ID, task.Version)
	digest := sha256.Sum256([]byte(key))
	id := "onf_" + hex.EncodeToString(digest[:12])
	var inserted string
	err = q.QueryRow(ctx, `
INSERT INTO oneshot_notification_outbox (
    id,idempotency_key,task_id,run_id,event_type,destination,payload,status,
    attempt_count,next_attempt_at
) VALUES ($1,$2,$3,NULL,$4,$5,$6,'pending',0,$7)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING id`, id, key, task.ID, "oneshot.task.cancelled", destination, payload, task.UpdatedAt.UTC()).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return mapWriteError("insert Task terminal notification outbox", err)
	}
	return nil
}

func scanReplayRows(rows pgx.Rows) ([]ReplayEvent, error) {
	out := make([]ReplayEvent, 0)
	for rows.Next() {
		var event ReplayEvent
		var raw []byte
		if err := rows.Scan(&event.Topic, &raw, &event.OccurredAt, &event.Cursor.Kind, &event.Cursor.ID); err != nil {
			return nil, wrap("scan replay event", err)
		}
		if err := json.Unmarshal(raw, &event.Data); err != nil {
			return nil, fmt.Errorf("decode replay event payload: %w", err)
		}
		event.OccurredAt = event.OccurredAt.UTC()
		event.Cursor.OccurredAt = event.OccurredAt
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("list replay event rows", err)
	}
	return out, nil
}

// ListTaskReplayEvents replays durable task lifecycle state ordered by Task
// aggregate version. Project and ownership filters are applied before cursor
// pagination, preventing cross-project cursor leakage.
func (s *Store) ListTaskReplayEvents(ctx context.Context, owner domain.Owner, taskID, projectID string, after ReplayCursor, limit int) ([]ReplayEvent, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 64
	}
	if limit > 256 {
		return nil, domain.InvalidRequestf("replay limit must not exceed 256")
	}
	rows, err := s.pool.Query(ctx, `
SELECT topic,data,occurred_at,'l' AS cursor_kind,lpad(id::text,20,'0') AS cursor_id
FROM oneshot_lifecycle_events
WHERE principal_kind=$1 AND principal_id=$2 AND aggregate_kind='task'
  AND ($3='' OR task_id=$3) AND ($4='' OR project_id=$4)
  AND ($5::timestamptz IS NULL OR (occurred_at,'l',lpad(id::text,20,'0'))>($5,$6,$7))
ORDER BY occurred_at,'l',lpad(id::text,20,'0') LIMIT $8`, owner.Kind, owner.ID,
		strings.TrimSpace(taskID), strings.TrimSpace(projectID), nullableTime(after.OccurredAt), after.Kind, after.ID, limit)
	if err != nil {
		return nil, wrap("list Task replay events", err)
	}
	defer rows.Close()
	return scanReplayRows(rows)
}

// ListRunReplayEvents merges durable Run lifecycle events with persisted
// normalized output. A single opaque cursor preserves exact ordering across
// process lifecycle and output rows after reconnect or service restart.
func (s *Store) ListRunReplayEvents(ctx context.Context, owner domain.Owner, runID, projectID string, after ReplayCursor, limit int) ([]ReplayEvent, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 64
	}
	if limit > 256 {
		return nil, domain.InvalidRequestf("replay limit must not exceed 256")
	}
	rows, err := s.pool.Query(ctx, `
SELECT topic,data,occurred_at,cursor_kind,cursor_id FROM (
    SELECT le.topic,le.data,le.occurred_at,'l'::text AS cursor_kind,lpad(le.id::text,20,'0') AS cursor_id
    FROM oneshot_lifecycle_events le
    WHERE le.principal_kind=$1 AND le.principal_id=$2 AND le.aggregate_kind='run'
      AND le.run_id=$3 AND ($4='' OR le.project_id=$4)
    UNION ALL
    SELECT 'oneshot.run.output'::text,
           jsonb_build_object(
               'event_id',e.id,'run_id',e.run_id,'sequence',e.sequence,
               'event_type',e.type,'content',e.content,'adapter_id',e.adapter_id,
               'adapter_version',e.adapter_version
           ),e.occurred_at,'o'::text AS cursor_kind,e.id AS cursor_id
    FROM oneshot_standard_events e
    JOIN oneshot_runs r ON r.id=e.run_id
    JOIN oneshot_tasks t ON t.id=r.task_id
    WHERE e.run_id=$3 AND t.principal_kind=$1 AND t.principal_id=$2
      AND ($4='' OR t.project_id=$4)
) replay
WHERE ($5::timestamptz IS NULL OR (occurred_at,cursor_kind,cursor_id)>($5,$6,$7))
ORDER BY occurred_at,cursor_kind,cursor_id LIMIT $8`, owner.Kind, owner.ID, strings.TrimSpace(runID),
		strings.TrimSpace(projectID), nullableTime(after.OccurredAt), after.Kind, after.ID, limit)
	if err != nil {
		return nil, wrap("list Run replay events", err)
	}
	defer rows.Close()
	return scanReplayRows(rows)
}
