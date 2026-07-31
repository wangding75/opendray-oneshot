package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func validateRuntimeContext(snapshot domain.RuntimeContextSnapshot) error {
	_, err := domain.RestoreRuntimeContext(snapshot)
	return err
}

func (s *Store) CreateRuntimeContext(ctx context.Context, snapshot domain.RuntimeContextSnapshot) (domain.RuntimeContextSnapshot, error) {
	if err := validateRuntimeContext(snapshot); err != nil {
		return domain.RuntimeContextSnapshot{}, err
	}
	return insertRuntimeContextRow(ctx, s.pool, snapshot)
}

func insertRuntimeContextRow(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, snapshot domain.RuntimeContextSnapshot) (domain.RuntimeContextSnapshot, error) {
	out, err := scanRuntimeContext(q.QueryRow(ctx, `
INSERT INTO oneshot_runtime_contexts (
    id,principal_kind,principal_id,project_id,provider_id,provider_context_id,
    workspace_path,status,version,created_at,updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING id,principal_kind,principal_id,project_id,provider_id,provider_context_id,
          workspace_path,status,version,created_at,updated_at`,
		snapshot.ID, snapshot.PrincipalKind, snapshot.PrincipalID, snapshot.ProjectID,
		snapshot.ProviderID, snapshot.ProviderContextID, snapshot.WorkspacePath,
		snapshot.Status, snapshot.Version, snapshot.CreatedAt.UTC(), snapshot.UpdatedAt.UTC()))
	if err != nil {
		return domain.RuntimeContextSnapshot{}, mapWriteError("insert runtime context", err)
	}
	return out, nil
}

func (s *Store) GetRuntimeContext(ctx context.Context, owner domain.Owner, id string) (domain.RuntimeContextSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.RuntimeContextSnapshot{}, err
	}
	out, err := scanRuntimeContext(s.pool.QueryRow(ctx, `
SELECT id,principal_kind,principal_id,project_id,provider_id,provider_context_id,
       workspace_path,status,version,created_at,updated_at
FROM oneshot_runtime_contexts
WHERE id=$1 AND principal_kind=$2 AND principal_id=$3`, id, owner.Kind, owner.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeContextSnapshot{}, notFound(domain.ErrorContextNotFound, "RuntimeContext")
	}
	if err != nil {
		return domain.RuntimeContextSnapshot{}, wrap("get runtime context", err)
	}
	return out, nil
}

func (s *Store) UpdateRuntimeContext(ctx context.Context, owner domain.Owner, snapshot domain.RuntimeContextSnapshot, expectedVersion int64) (domain.RuntimeContextSnapshot, error) {
	if err := validateOwner(owner); err != nil {
		return domain.RuntimeContextSnapshot{}, err
	}
	if err := validateRuntimeContext(snapshot); err != nil {
		return domain.RuntimeContextSnapshot{}, err
	}
	if snapshot.PrincipalKind != owner.Kind || snapshot.PrincipalID != owner.ID {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorContextOwnerMismatch, "RuntimeContext owner mismatch", nil)
	}
	if snapshot.Version != expectedVersion+1 {
		return domain.RuntimeContextSnapshot{}, domain.InvalidRequestf("RuntimeContext snapshot version must equal expected version plus one")
	}
	return updateRuntimeContextRow(ctx, s.pool, owner, snapshot, expectedVersion)
}

func updateRuntimeContextRow(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, owner domain.Owner, snapshot domain.RuntimeContextSnapshot, expectedVersion int64) (domain.RuntimeContextSnapshot, error) {
	out, err := scanRuntimeContext(q.QueryRow(ctx, `
UPDATE oneshot_runtime_contexts SET status=$1,version=$2,updated_at=$3
WHERE id=$4 AND principal_kind=$5 AND principal_id=$6 AND version=$7
RETURNING id,principal_kind,principal_id,project_id,provider_id,provider_context_id,
          workspace_path,status,version,created_at,updated_at`,
		snapshot.Status, snapshot.Version, snapshot.UpdatedAt.UTC(), snapshot.ID,
		owner.Kind, owner.ID, expectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeContextSnapshot{}, domain.NewDomainError(domain.ErrorRunConflict, "RuntimeContext version conflict or not found", nil)
	}
	if err != nil {
		return domain.RuntimeContextSnapshot{}, mapWriteError("update runtime context", err)
	}
	return out, nil
}

func (s *Store) ListRuntimeContexts(ctx context.Context, owner domain.Owner, projectID, providerID string, req PageRequest) (Page[domain.RuntimeContextSnapshot], error) {
	if err := validateOwner(owner); err != nil {
		return Page[domain.RuntimeContextSnapshot]{}, err
	}
	limit, cursor, err := normalizePage(req)
	if err != nil {
		return Page[domain.RuntimeContextSnapshot]{}, err
	}
	query := `
SELECT id,principal_kind,principal_id,project_id,provider_id,provider_context_id,
       workspace_path,status,version,created_at,updated_at
FROM oneshot_runtime_contexts
WHERE principal_kind=$1 AND principal_id=$2
  AND ($3='' OR project_id=$3) AND ($4='' OR provider_id=$4)`
	args := []any{owner.Kind, owner.ID, projectID, providerID}
	if cursor != nil {
		query += ` AND (created_at,id) < ($5,$6)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit+1)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[domain.RuntimeContextSnapshot]{}, wrap("list runtime contexts", err)
	}
	defer rows.Close()
	items := make([]domain.RuntimeContextSnapshot, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanRuntimeContext(rows)
		if scanErr != nil {
			return Page[domain.RuntimeContextSnapshot]{}, wrap("scan runtime context page", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.RuntimeContextSnapshot]{}, wrap("list runtime contexts rows", err)
	}
	page := Page[domain.RuntimeContextSnapshot]{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func scanRuntimeContext(row scanner) (domain.RuntimeContextSnapshot, error) {
	var snapshot domain.RuntimeContextSnapshot
	if err := row.Scan(&snapshot.ID, &snapshot.PrincipalKind, &snapshot.PrincipalID,
		&snapshot.ProjectID, &snapshot.ProviderID, &snapshot.ProviderContextID,
		&snapshot.WorkspacePath, &snapshot.Status, &snapshot.Version,
		&snapshot.CreatedAt, &snapshot.UpdatedAt); err != nil {
		return domain.RuntimeContextSnapshot{}, err
	}
	restored, err := domain.RestoreRuntimeContext(snapshot)
	if err != nil {
		return domain.RuntimeContextSnapshot{}, fmt.Errorf("restore runtime context: %w", err)
	}
	return restored.Snapshot(), nil
}
