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

func validateArtifact(snapshot domain.ArtifactSnapshot) error {
	_, err := domain.RestoreArtifact(snapshot)
	return err
}

func validateStreamRecord(snapshot domain.StreamRecordSnapshot) error {
	_, err := domain.RestoreStreamRecord(snapshot)
	return err
}

func validateStandardEvent(snapshot domain.StandardEventSnapshot) error {
	_, err := domain.RestoreStandardEvent(snapshot)
	return err
}

func insertArtifact(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, snapshot domain.ArtifactSnapshot) (domain.ArtifactSnapshot, error) {
	metadata, err := marshalJSON(snapshot.Metadata, "artifact.metadata")
	if err != nil {
		return domain.ArtifactSnapshot{}, err
	}
	out, err := scanArtifact(q.QueryRow(ctx, `
INSERT INTO oneshot_artifacts (
    id,task_id,run_id,kind,name,content_type,size_bytes,sha256,storage_key,metadata,created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING id,task_id,run_id,kind,name,content_type,size_bytes,sha256,storage_key,metadata,created_at`,
		snapshot.ID, snapshot.TaskID, snapshot.RunID, snapshot.Kind, snapshot.Name,
		snapshot.ContentType, snapshot.SizeBytes, snapshot.SHA256, snapshot.StorageKey,
		metadata, snapshot.CreatedAt.UTC()))
	if err != nil {
		return domain.ArtifactSnapshot{}, mapWriteError("insert artifact", err)
	}
	return out, nil
}

func insertStreamRecord(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, snapshot domain.StreamRecordSnapshot) (domain.StreamRecordSnapshot, error) {
	out, err := scanStreamRecord(q.QueryRow(ctx, `
INSERT INTO oneshot_stream_records (
    id,run_id,sequence,stream,byte_offset,byte_length,raw_artifact_id,text,decode_status,sha256,received_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING id,run_id,sequence,stream,byte_offset,byte_length,raw_artifact_id,text,decode_status,sha256,received_at`,
		snapshot.ID, snapshot.RunID, snapshot.Sequence, snapshot.Stream, snapshot.ByteOffset,
		snapshot.ByteLength, snapshot.RawArtifactID, snapshot.Text, snapshot.DecodeStatus,
		snapshot.SHA256, snapshot.ReceivedAt.UTC()))
	if err != nil {
		return domain.StreamRecordSnapshot{}, mapWriteError("insert stream record", err)
	}
	return out, nil
}

func insertStandardEvent(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, snapshot domain.StandardEventSnapshot) (domain.StandardEventSnapshot, error) {
	content, err := marshalJSON(snapshot.Content, "standard_event.content")
	if err != nil {
		return domain.StandardEventSnapshot{}, err
	}
	out, err := scanStandardEvent(q.QueryRow(ctx, `
INSERT INTO oneshot_standard_events (
    id,run_id,sequence,type,source_stream_record_id,adapter_id,adapter_version,content,occurred_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
RETURNING id,run_id,sequence,type,source_stream_record_id,adapter_id,adapter_version,content,occurred_at`,
		snapshot.ID, snapshot.RunID, snapshot.Sequence, snapshot.Type,
		snapshot.SourceStreamRecordID, snapshot.AdapterID, snapshot.AdapterVersion,
		content, snapshot.OccurredAt.UTC()))
	if err != nil {
		return domain.StandardEventSnapshot{}, mapWriteError("insert standard event", err)
	}
	return out, nil
}

// OutputBatch is persisted atomically so a Run never references stream/event
// metadata whose artifact row was not committed.
type OutputBatch struct {
	Artifacts      []domain.ArtifactSnapshot
	StreamRecords  []domain.StreamRecordSnapshot
	StandardEvents []domain.StandardEventSnapshot
}

// PersistOutputBatch atomically appends artifacts, raw stream records and
// normalized events for one owner-authorized Run.
func (s *Store) PersistOutputBatch(ctx context.Context, owner domain.Owner, runID string, batch OutputBatch) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	for _, artifact := range batch.Artifacts {
		if err := validateArtifact(artifact); err != nil {
			return err
		}
		if artifact.RunID != nil && *artifact.RunID != runID {
			return domain.InvalidRequestf("Artifact belongs to another Run")
		}
	}
	for _, record := range batch.StreamRecords {
		if err := validateStreamRecord(record); err != nil {
			return err
		}
		if record.RunID != runID {
			return domain.InvalidRequestf("StreamRecord belongs to another Run")
		}
	}
	for _, event := range batch.StandardEvents {
		if err := validateStandardEvent(event); err != nil {
			return err
		}
		if event.RunID != runID {
			return domain.InvalidRequestf("StandardEvent belongs to another Run")
		}
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return wrap("begin output batch transaction", err)
	}
	defer rollback(ctx, tx)

	var taskID string
	if err := tx.QueryRow(ctx, `
SELECT r.task_id FROM oneshot_runs r JOIN oneshot_tasks t ON t.id=r.task_id
WHERE r.id=$1 AND t.principal_kind=$2 AND t.principal_id=$3 FOR UPDATE OF r`,
		runID, owner.Kind, owner.ID).Scan(&taskID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.ErrorRunNotFound, "Run")
		}
		return wrap("authorize output batch", err)
	}
	for _, artifact := range batch.Artifacts {
		if artifact.TaskID != taskID {
			return domain.NewDomainError(domain.ErrorForbidden, "Artifact Task owner mismatch", nil)
		}
		if _, err := insertArtifact(ctx, tx, artifact); err != nil {
			return err
		}
	}

	var maxStream int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM oneshot_stream_records WHERE run_id=$1`, runID).Scan(&maxStream); err != nil {
		return wrap("read stream sequence", err)
	}
	for _, record := range batch.StreamRecords {
		if record.Sequence <= maxStream {
			return domain.InvalidRequestf("StreamRecord sequence must be strictly increasing")
		}
		if _, err := insertStreamRecord(ctx, tx, record); err != nil {
			return err
		}
		maxStream = record.Sequence
	}

	var maxEvent int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM oneshot_standard_events WHERE run_id=$1`, runID).Scan(&maxEvent); err != nil {
		return wrap("read event sequence", err)
	}
	for _, event := range batch.StandardEvents {
		if event.Sequence <= maxEvent {
			return domain.InvalidRequestf("StandardEvent sequence must be strictly increasing")
		}
		if _, err := insertStandardEvent(ctx, tx, event); err != nil {
			return err
		}
		maxEvent = event.Sequence
	}
	if err := tx.Commit(ctx); err != nil {
		return mapWriteError("commit output batch transaction", err)
	}
	return nil
}

func (s *Store) CreateArtifact(ctx context.Context, owner domain.Owner, snapshot domain.ArtifactSnapshot) (domain.ArtifactSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.ArtifactSnapshot{}, err
	}
	if err := validateArtifact(snapshot); err != nil {
		return domain.ArtifactSnapshot{}, err
	}
	var allowed bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(
        SELECT 1 FROM oneshot_tasks WHERE id=$1 AND principal_kind=$2 AND principal_id=$3
    )`, snapshot.TaskID, owner.Kind, owner.ID).Scan(&allowed); err != nil {
		return domain.ArtifactSnapshot{}, wrap("authorize artifact", err)
	}
	if !allowed {
		return domain.ArtifactSnapshot{}, notFound(domain.ErrorTaskNotFound, "Task")
	}
	return insertArtifact(ctx, s.pool, snapshot)
}

func (s *Store) GetArtifact(ctx context.Context, owner domain.Owner, id string) (domain.ArtifactSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.ArtifactSnapshot{}, err
	}
	out, err := scanArtifact(s.pool.QueryRow(ctx, `
SELECT a.id,a.task_id,a.run_id,a.kind,a.name,a.content_type,a.size_bytes,a.sha256,a.storage_key,a.metadata,a.created_at
FROM oneshot_artifacts a JOIN oneshot_tasks t ON t.id=a.task_id
WHERE a.id=$1 AND t.principal_kind=$2 AND t.principal_id=$3`, id, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ArtifactSnapshot{}, notFound(domain.ErrorArtifactNotFound, "Artifact")
	}
	if err != nil {
		return domain.ArtifactSnapshot{}, wrap("get artifact", err)
	}
	return out, nil
}

func (s *Store) ListStreamRecords(ctx context.Context, owner domain.Owner, runID string, afterSequence int64, limit int) ([]domain.StreamRecordSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = defaultPageLimit
	}
	if limit < 1 || limit > maxPageLimit {
		return nil, domain.InvalidRequestf("limit must be between 1 and %d", maxPageLimit)
	}
	rows, err := s.pool.Query(ctx, `
SELECT sr.id,sr.run_id,sr.sequence,sr.stream,sr.byte_offset,sr.byte_length,
       sr.raw_artifact_id,sr.text,sr.decode_status,sr.sha256,sr.received_at
FROM oneshot_stream_records sr
JOIN oneshot_runs r ON r.id=sr.run_id JOIN oneshot_tasks t ON t.id=r.task_id
WHERE sr.run_id=$1 AND sr.sequence>$2 AND t.principal_kind=$3 AND t.principal_id=$4
ORDER BY sr.sequence LIMIT $5`, runID, afterSequence, owner.Kind, owner.ID, limit)
	if err != nil {
		return nil, wrap("list stream records", err)
	}
	defer rows.Close()
	var out []domain.StreamRecordSnapshot
	for rows.Next() {
		item, scanErr := scanStreamRecord(rows)
		if scanErr != nil {
			return nil, wrap("scan stream record", scanErr)
		}
		out = append(out, item)
	}
	return out, wrap("list stream record rows", rows.Err())
}

func (s *Store) ListStandardEvents(ctx context.Context, owner domain.Owner, runID string, afterSequence int64, limit int) ([]domain.StandardEventSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = defaultPageLimit
	}
	if limit < 1 || limit > maxPageLimit {
		return nil, domain.InvalidRequestf("limit must be between 1 and %d", maxPageLimit)
	}
	rows, err := s.pool.Query(ctx, `
SELECT e.id,e.run_id,e.sequence,e.type,e.source_stream_record_id,e.adapter_id,e.adapter_version,e.content,e.occurred_at
FROM oneshot_standard_events e
JOIN oneshot_runs r ON r.id=e.run_id JOIN oneshot_tasks t ON t.id=r.task_id
WHERE e.run_id=$1 AND e.sequence>$2 AND t.principal_kind=$3 AND t.principal_id=$4
ORDER BY e.sequence LIMIT $5`, runID, afterSequence, owner.Kind, owner.ID, limit)
	if err != nil {
		return nil, wrap("list standard events", err)
	}
	defer rows.Close()
	var out []domain.StandardEventSnapshot
	for rows.Next() {
		item, scanErr := scanStandardEvent(rows)
		if scanErr != nil {
			return nil, wrap("scan standard event", scanErr)
		}
		out = append(out, item)
	}
	return out, wrap("list standard event rows", rows.Err())
}

func scanArtifact(row scanner) (domain.ArtifactSnapshot, error) {
	var snapshot domain.ArtifactSnapshot
	var metadata []byte
	if err := row.Scan(&snapshot.ID, &snapshot.TaskID, &snapshot.RunID, &snapshot.Kind,
		&snapshot.Name, &snapshot.ContentType, &snapshot.SizeBytes, &snapshot.SHA256,
		&snapshot.StorageKey, &metadata, &snapshot.CreatedAt); err != nil {
		return domain.ArtifactSnapshot{}, err
	}
	if err := json.Unmarshal(metadata, &snapshot.Metadata); err != nil {
		return domain.ArtifactSnapshot{}, fmt.Errorf("decode artifact metadata: %w", err)
	}
	restored, err := domain.RestoreArtifact(snapshot)
	if err != nil {
		return domain.ArtifactSnapshot{}, fmt.Errorf("restore artifact: %w", err)
	}
	return restored.Snapshot(), nil
}

func scanStreamRecord(row scanner) (domain.StreamRecordSnapshot, error) {
	var snapshot domain.StreamRecordSnapshot
	if err := row.Scan(&snapshot.ID, &snapshot.RunID, &snapshot.Sequence, &snapshot.Stream,
		&snapshot.ByteOffset, &snapshot.ByteLength, &snapshot.RawArtifactID, &snapshot.Text,
		&snapshot.DecodeStatus, &snapshot.SHA256, &snapshot.ReceivedAt); err != nil {
		return domain.StreamRecordSnapshot{}, err
	}
	restored, err := domain.RestoreStreamRecord(snapshot)
	if err != nil {
		return domain.StreamRecordSnapshot{}, fmt.Errorf("restore stream record: %w", err)
	}
	return restored.Snapshot(), nil
}

func scanStandardEvent(row scanner) (domain.StandardEventSnapshot, error) {
	var snapshot domain.StandardEventSnapshot
	var content []byte
	if err := row.Scan(&snapshot.ID, &snapshot.RunID, &snapshot.Sequence, &snapshot.Type,
		&snapshot.SourceStreamRecordID, &snapshot.AdapterID, &snapshot.AdapterVersion,
		&content, &snapshot.OccurredAt); err != nil {
		return domain.StandardEventSnapshot{}, err
	}
	if err := json.Unmarshal(content, &snapshot.Content); err != nil {
		return domain.StandardEventSnapshot{}, fmt.Errorf("decode standard event content: %w", err)
	}
	restored, err := domain.RestoreStandardEvent(snapshot)
	if err != nil {
		return domain.StandardEventSnapshot{}, fmt.Errorf("restore standard event: %w", err)
	}
	return restored.Snapshot(), nil
}

// AppendOutput is the executor-facing persistence boundary. It preserves the
// existing atomic OutputBatch transaction while avoiding a concrete store
// dependency in the executor package.
func (s *Store) AppendOutput(ctx context.Context, owner domain.Owner, runID string, artifacts []domain.ArtifactSnapshot, records []domain.StreamRecordSnapshot, events []domain.StandardEventSnapshot) error {
	return s.PersistOutputBatch(ctx, owner, runID, OutputBatch{
		Artifacts: artifacts, StreamRecords: records, StandardEvents: events,
	})
}

// LoadOutputCursor returns durable append positions for stream/event sequence
// and per-stream byte offsets. Owner authorization is derived through Task.
func (s *Store) LoadOutputCursor(ctx context.Context, owner domain.Owner, runID string) (int64, int64, int64, int64, error) {
	if err := validateOwner(owner); err != nil {
		return 0, 0, 0, 0, err
	}
	var streamSequence, eventSequence, stdoutOffset, stderrOffset int64
	err := s.pool.QueryRow(ctx, `
SELECT
    COALESCE((SELECT MAX(sr.sequence) FROM oneshot_stream_records sr WHERE sr.run_id=r.id),0),
    COALESCE((SELECT MAX(e.sequence) FROM oneshot_standard_events e WHERE e.run_id=r.id),0),
    COALESCE((SELECT MAX(sr.byte_offset + sr.byte_length) FROM oneshot_stream_records sr WHERE sr.run_id=r.id AND sr.stream='stdout'),0),
    COALESCE((SELECT MAX(sr.byte_offset + sr.byte_length) FROM oneshot_stream_records sr WHERE sr.run_id=r.id AND sr.stream='stderr'),0)
FROM oneshot_runs r
JOIN oneshot_tasks t ON t.id=r.task_id
WHERE r.id=$1 AND t.principal_kind=$2 AND t.principal_id=$3`, runID, owner.Kind, owner.ID).
		Scan(&streamSequence, &eventSequence, &stdoutOffset, &stderrOffset)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, 0, 0, notFound(domain.ErrorRunNotFound, "Run")
	}
	if err != nil {
		return 0, 0, 0, 0, wrap("load output cursor", err)
	}
	return streamSequence, eventSequence, stdoutOffset, stderrOffset, nil
}

// ListArtifacts returns authorization-filtered artifact metadata for a Task or Run.
func (s *Store) ListArtifacts(ctx context.Context, owner domain.Owner, taskID, runID string, req PageRequest) (Page[domain.ArtifactSnapshot], error) {
	if err := validateOwner(owner); err != nil {
		return Page[domain.ArtifactSnapshot]{}, err
	}
	if strings.TrimSpace(taskID) == "" {
		return Page[domain.ArtifactSnapshot]{}, domain.InvalidRequestf("task_id is required")
	}
	limit, cursor, err := normalizePage(req)
	if err != nil {
		return Page[domain.ArtifactSnapshot]{}, err
	}
	args := []any{taskID, owner.Kind, owner.ID, strings.TrimSpace(runID)}
	query := `SELECT a.id,a.task_id,a.run_id,a.kind,a.name,a.content_type,a.size_bytes,
       a.sha256,a.storage_key,a.metadata,a.created_at
FROM oneshot_artifacts a JOIN oneshot_tasks t ON t.id=a.task_id
WHERE a.task_id=$1 AND t.principal_kind=$2 AND t.principal_id=$3
  AND ($4='' OR a.run_id=$4)`
	if cursor != nil {
		query += ` AND (a.created_at,a.id) < ($5,$6)
ORDER BY a.created_at DESC,a.id DESC LIMIT $7`
		args = append(args, cursor.CreatedAt, cursor.ID, limit+1)
	} else {
		query += ` ORDER BY a.created_at DESC,a.id DESC LIMIT $5`
		args = append(args, limit+1)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[domain.ArtifactSnapshot]{}, wrap("list artifacts", err)
	}
	defer rows.Close()
	items := make([]domain.ArtifactSnapshot, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanArtifact(rows)
		if scanErr != nil {
			return Page[domain.ArtifactSnapshot]{}, wrap("scan artifact page", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.ArtifactSnapshot]{}, wrap("list artifact rows", err)
	}
	page := Page[domain.ArtifactSnapshot]{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// ListTaskStandardEvents replays persisted events across all Runs of one Task.
func (s *Store) ListTaskStandardEvents(ctx context.Context, owner domain.Owner, taskID string, afterTime time.Time, afterID string, limit int) ([]domain.StandardEventSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		return nil, domain.InvalidRequestf("event limit must not exceed 1000")
	}
	rows, err := s.pool.Query(ctx, `
SELECT e.id,e.run_id,e.sequence,e.type,e.source_stream_record_id,
       e.adapter_id,e.adapter_version,e.content,e.occurred_at
FROM oneshot_standard_events e
JOIN oneshot_runs r ON r.id=e.run_id
JOIN oneshot_tasks t ON t.id=r.task_id
WHERE ($1='' OR t.id=$1) AND t.principal_kind=$2 AND t.principal_id=$3
  AND ($4::timestamptz IS NULL OR (e.occurred_at,e.id)>($4,$5))
ORDER BY e.occurred_at,e.id LIMIT $6`, strings.TrimSpace(taskID), owner.Kind, owner.ID, nullableTime(afterTime), afterID, limit)
	if err != nil {
		return nil, wrap("list task standard events", err)
	}
	defer rows.Close()
	out := make([]domain.StandardEventSnapshot, 0, limit)
	for rows.Next() {
		item, scanErr := scanStandardEvent(rows)
		if scanErr != nil {
			return nil, wrap("scan task standard event", scanErr)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("list task standard event rows", err)
	}
	return out, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
