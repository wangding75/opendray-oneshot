package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const stagedAttachmentColumns = `
    id,principal_kind,principal_id,project_id,source_kind,source_ref,name,
    declared_mime,detected_mime,size_bytes,sha256,storage_key,status,
    created_at,expires_at,deleted_at`

func scanStagedAttachment(row scanner) (domain.StagedAttachmentSnapshot, error) {
	var out domain.StagedAttachmentSnapshot
	if err := row.Scan(&out.ID, &out.PrincipalKind, &out.PrincipalID, &out.ProjectID,
		&out.SourceKind, &out.SourceRef, &out.Name, &out.DeclaredMIME, &out.DetectedMIME,
		&out.SizeBytes, &out.SHA256, &out.StorageKey, &out.Status, &out.CreatedAt,
		&out.ExpiresAt, &out.DeletedAt); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	if err := out.Validate(); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	return out, nil
}

func (s *Store) CreateStagedAttachment(ctx context.Context, snapshot domain.StagedAttachmentSnapshot) (domain.StagedAttachmentSnapshot, error) {
	if s == nil || s.pool == nil {
		return domain.StagedAttachmentSnapshot{}, domain.NewDomainError(domain.ErrorArtifactUnavailable, "One-shot attachment store unavailable", nil)
	}
	if err := snapshot.Validate(); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	out, err := scanStagedAttachment(s.pool.QueryRow(ctx, `
INSERT INTO oneshot_staged_attachments (`+stagedAttachmentColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT DO NOTHING
RETURNING `+stagedAttachmentColumns,
		snapshot.ID, snapshot.PrincipalKind, snapshot.PrincipalID, snapshot.ProjectID,
		snapshot.SourceKind, snapshot.SourceRef, snapshot.Name, snapshot.DeclaredMIME,
		snapshot.DetectedMIME, snapshot.SizeBytes, snapshot.SHA256, snapshot.StorageKey,
		snapshot.Status, snapshot.CreatedAt.UTC(), snapshot.ExpiresAt.UTC(), snapshot.DeletedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		out, err = scanStagedAttachment(s.pool.QueryRow(ctx, `SELECT `+stagedAttachmentColumns+`
FROM oneshot_staged_attachments
WHERE id=$1 OR (principal_kind=$2 AND principal_id=$3 AND project_id=$4 AND source_kind=$5 AND source_ref=$6 AND source_ref<>'' AND status='ready')
ORDER BY CASE WHEN id=$1 THEN 0 ELSE 1 END LIMIT 1`, snapshot.ID, snapshot.PrincipalKind, snapshot.PrincipalID, snapshot.ProjectID, snapshot.SourceKind, snapshot.SourceRef))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StagedAttachmentSnapshot{}, domain.NewDomainError(domain.ErrorArtifactUnavailable, "attachment metadata conflict", nil)
		}
		if err != nil {
			return domain.StagedAttachmentSnapshot{}, wrap("resolve staged attachment conflict", err)
		}
		return out, nil
	}
	if err != nil {
		return domain.StagedAttachmentSnapshot{}, mapWriteError("insert staged attachment", err)
	}
	return out, nil
}

func (s *Store) GetStagedAttachment(ctx context.Context, owner domain.Owner, id string) (domain.StagedAttachmentSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	if err := domain.ValidateStagedAttachmentID(id); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	out, err := scanStagedAttachment(s.pool.QueryRow(ctx, `SELECT `+stagedAttachmentColumns+`
FROM oneshot_staged_attachments
WHERE id=$1 AND principal_kind=$2 AND principal_id=$3`, id, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StagedAttachmentSnapshot{}, notFound(domain.ErrorArtifactNotFound, "staged attachment")
	}
	if err != nil {
		return domain.StagedAttachmentSnapshot{}, wrap("get staged attachment", err)
	}
	if out.Status != domain.StagedAttachmentReady || !out.ExpiresAt.After(time.Now().UTC()) {
		return domain.StagedAttachmentSnapshot{}, notFound(domain.ErrorArtifactNotFound, "staged attachment")
	}
	return out, nil
}

func (s *Store) DeleteStagedAttachment(ctx context.Context, owner domain.Owner, id string, now time.Time) (domain.StagedAttachmentSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	out, err := scanStagedAttachment(s.pool.QueryRow(ctx, `
UPDATE oneshot_staged_attachments SET status='deleted',deleted_at=$4
WHERE id=$1 AND principal_kind=$2 AND principal_id=$3 AND status='ready'
  AND NOT EXISTS (SELECT 1 FROM oneshot_delivery_attachments WHERE attachment_id=$1)
RETURNING `+stagedAttachmentColumns, id, owner.Kind, owner.ID, now.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		// A prior request may have committed the deleted metadata state while
		// storage deletion failed. Return the same owned, unbound tombstone so
		// the service can retry byte deletion without resurrecting the file.
		out, retryErr := scanStagedAttachment(s.pool.QueryRow(ctx, `SELECT `+stagedAttachmentColumns+`
FROM oneshot_staged_attachments
WHERE id=$1 AND principal_kind=$2 AND principal_id=$3 AND status='deleted'
  AND NOT EXISTS (SELECT 1 FROM oneshot_delivery_attachments WHERE attachment_id=$1)`, id, owner.Kind, owner.ID))
		if retryErr == nil {
			return out, nil
		}
		if !errors.Is(retryErr, pgx.ErrNoRows) {
			return domain.StagedAttachmentSnapshot{}, wrap("resolve deleted staged attachment", retryErr)
		}
		return domain.StagedAttachmentSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "attachment is unavailable or already bound to a Delivery", nil)
	}
	if err != nil {
		return domain.StagedAttachmentSnapshot{}, mapWriteError("delete staged attachment", err)
	}
	return out, nil
}

// BindDeliveryAttachments validates owner, project, readiness and expiry, then
// records immutable references in the caller's existing transaction.
func BindDeliveryAttachments(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, owner domain.Owner, projectID, taskID, deliveryID string, refs []string) error {
	for ordinal, raw := range refs {
		id := strings.TrimSpace(raw)
		if err := domain.ValidateStagedAttachmentID(id); err != nil {
			return err
		}
		result, err := q.Exec(ctx, `
INSERT INTO oneshot_delivery_attachments (task_id,delivery_id,attachment_id,ordinal)
SELECT $1,$2,a.id,$3
FROM oneshot_staged_attachments a
WHERE a.id=$4 AND a.principal_kind=$5 AND a.principal_id=$6 AND a.project_id=$7
  AND a.status='ready' AND a.expires_at>clock_timestamp()
ON CONFLICT (delivery_id,attachment_id) DO NOTHING`,
			taskID, deliveryID, ordinal, id, owner.Kind, owner.ID, projectID)
		if err != nil {
			return mapWriteError("bind staged attachment", err)
		}
		if result.RowsAffected() != 1 {
			return domain.NewDomainError(domain.ErrorArtifactNotFound, "attachment reference is unavailable for this owner and project", nil)
		}
	}
	return nil
}

// ListDeliveryAttachments resolves immutable inputs for executor/provider
// adapters without exposing other owners' staged bytes.
func (s *Store) ListDeliveryAttachments(ctx context.Context, owner domain.Owner, deliveryID string) ([]domain.StagedAttachmentSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT
    a.id,a.principal_kind,a.principal_id,a.project_id,a.source_kind,a.source_ref,a.name,
    a.declared_mime,a.detected_mime,a.size_bytes,a.sha256,a.storage_key,a.status,
    a.created_at,a.expires_at,a.deleted_at
FROM oneshot_delivery_attachments da
JOIN oneshot_staged_attachments a ON a.id=da.attachment_id
JOIN oneshot_tasks t ON t.id=da.task_id
WHERE da.delivery_id=$1 AND t.principal_kind=$2 AND t.principal_id=$3
ORDER BY da.ordinal`, deliveryID, owner.Kind, owner.ID)
	if err != nil {
		return nil, wrap("list delivery attachments", err)
	}
	defer rows.Close()
	var out []domain.StagedAttachmentSnapshot
	for rows.Next() {
		item, scanErr := scanStagedAttachment(rows)
		if scanErr != nil {
			return nil, wrap("scan delivery attachment", scanErr)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("list delivery attachment rows", err)
	}
	return out, nil
}

func (s *Store) ListExpiredStagedAttachments(ctx context.Context, now time.Time, limit int) ([]domain.StagedAttachmentSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, `SELECT `+stagedAttachmentColumns+`
FROM oneshot_staged_attachments
WHERE status='ready' AND expires_at <= $1
ORDER BY expires_at,id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, wrap("list expired staged attachments", err)
	}
	defer rows.Close()
	out := make([]domain.StagedAttachmentSnapshot, 0, limit)
	for rows.Next() {
		item, scanErr := scanStagedAttachment(rows)
		if scanErr != nil {
			return nil, wrap("scan expired staged attachment", scanErr)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("list expired staged attachment rows", err)
	}
	return out, nil
}

func (s *Store) ExpireStagedAttachment(ctx context.Context, owner domain.Owner, id string, now time.Time) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	result, err := s.pool.Exec(ctx, `UPDATE oneshot_staged_attachments SET status='expired'
WHERE id=$1 AND principal_kind=$2 AND principal_id=$3 AND status='ready' AND expires_at <= $4`, id, owner.Kind, owner.ID, now.UTC())
	if err != nil {
		return mapWriteError("expire staged attachment", err)
	}
	if result.RowsAffected() == 0 {
		return nil
	}
	return nil
}
