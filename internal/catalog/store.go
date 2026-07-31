package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// providerRow is the package-private DB shape for one row of the
// providers table.
type providerRow struct {
	ID           string
	ManifestHash string
	Config       map[string]any
	Enabled      bool
}

type catalogStore struct{ pool *pgxpool.Pool }

func newStore(pool *pgxpool.Pool) *catalogStore { return &catalogStore{pool: pool} }

// Upsert inserts a manifest hash row, preserving existing config and
// enabled value on conflict.
func (s *catalogStore) Upsert(ctx context.Context, id, hash string) error {
	_, err := s.pool.Exec(ctx, `
        INSERT INTO providers (id, manifest_hash, config, enabled)
        VALUES ($1, $2, '{}'::jsonb, TRUE)
        ON CONFLICT (id) DO UPDATE
          SET manifest_hash = EXCLUDED.manifest_hash,
              updated_at    = NOW()`,
		id, hash)
	if err != nil {
		return fmt.Errorf("catalog: upsert provider %s: %w", id, err)
	}
	return nil
}

func (s *catalogStore) Get(ctx context.Context, id string) (providerRow, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, manifest_hash, config, enabled FROM providers WHERE id=$1`, id)
	var (
		r   providerRow
		raw []byte
	)
	err := row.Scan(&r.ID, &r.ManifestHash, &raw, &r.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return providerRow{}, ErrNotFound
	}
	if err != nil {
		return providerRow{}, fmt.Errorf("catalog: scan provider: %w", err)
	}
	r.Config = make(map[string]any)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &r.Config)
	}
	return r, nil
}

func (s *catalogStore) UpdateConfig(ctx context.Context, id string, cfg map[string]any) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("catalog: marshal config: %w", err)
	}
	res, err := s.pool.Exec(ctx,
		`UPDATE providers SET config=$1::jsonb, updated_at=NOW() WHERE id=$2`, raw, id)
	if err != nil {
		return fmt.Errorf("catalog: update config: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *catalogStore) UpdateEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := s.pool.Exec(ctx,
		`UPDATE providers SET enabled=$1, updated_at=NOW() WHERE id=$2`, enabled, id)
	if err != nil {
		return fmt.Errorf("catalog: update enabled: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
