// Package store persists the One-shot execution domain in PostgreSQL.
//
// The package owns only One-shot tables. It deliberately has no dependency on
// interactive Session or PTY packages.
package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// Store is the PostgreSQL repository for the isolated One-shot domain.
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// PageRequest is a stable created_at/id cursor request.
type PageRequest struct {
	Cursor string
	Limit  int
}

// Page is an opaque-cursor result.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type pageCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func normalizePage(req PageRequest) (int, *pageCursor, error) {
	limit := req.Limit
	if limit == 0 {
		limit = defaultPageLimit
	}
	if limit < 1 || limit > maxPageLimit {
		return 0, nil, domain.InvalidRequestf("limit must be between 1 and %d", maxPageLimit)
	}
	if req.Cursor == "" {
		return limit, nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(req.Cursor)
	if err != nil {
		return 0, nil, domain.InvalidRequestf("invalid cursor")
	}
	var cursor pageCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.ID == "" || cursor.CreatedAt.IsZero() {
		return 0, nil, domain.InvalidRequestf("invalid cursor")
	}
	cursor.CreatedAt = cursor.CreatedAt.UTC()
	return limit, &cursor, nil
}

func encodeCursor(createdAt time.Time, id string) string {
	raw, _ := json.Marshal(pageCursor{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func validateOwner(owner domain.Owner) error { return owner.Validate() }

func marshalJSON(value any, field string) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, domain.InvalidRequestf("%s must be JSON-compatible: %v", field, err)
	}
	return raw, nil
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("oneshot/store: %s: %w", op, err)
}

func notFound(code domain.ErrorCode, resource string) error {
	return domain.NewDomainError(code, resource+" not found", nil)
}

func mapWriteError(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if strings.Contains(pgErr.ConstraintName, "one_active_per_task") ||
				strings.Contains(pgErr.ConstraintName, "delivery_id") ||
				strings.Contains(pgErr.ConstraintName, "run_id") {
				return domain.NewDomainError(domain.ErrorRunConflict, "active Run or Delivery conflict", err)
			}
			if strings.Contains(pgErr.ConstraintName, "idempotency") ||
				strings.Contains(pgErr.ConstraintName, "channel_source") {
				return domain.NewDomainError(domain.ErrorIdempotencyConflict, "idempotency key or source message already exists", err)
			}
			return domain.NewDomainError(domain.ErrorInvalidRequest, "unique constraint violation", err)
		case "23503", "23514", "23502", "22P02":
			return domain.NewDomainError(domain.ErrorInvalidRequest, "database constraint rejected One-shot data", err)
		case "40001", "40P01", "55P03":
			return domain.NewDomainError(domain.ErrorQueueUnavailable, "database concurrency conflict", err)
		}
	}
	return wrap(op, err)
}

func rollback(ctx context.Context, tx pgx.Tx) { _ = tx.Rollback(ctx) }
