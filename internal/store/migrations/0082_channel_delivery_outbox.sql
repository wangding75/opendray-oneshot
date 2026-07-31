-- Shared durable outbound channel delivery outbox.
-- Transport retries live here and are intentionally independent from
-- One-shot task/run retry state.

CREATE TABLE channel_delivery_outbox (
    id               TEXT PRIMARY KEY,
    idempotency_key  TEXT NOT NULL UNIQUE,
    channel_id       TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    payload          JSONB NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','sending','retry','delivered','dead')),
    progress         INTEGER NOT NULL DEFAULT 0 CHECK (progress >= 0),
    attempt_count    INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_owner      TEXT,
    lease_until      TIMESTAMPTZ,
    last_error       TEXT,
    receipt          JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at     TIMESTAMPTZ
);

CREATE INDEX channel_delivery_outbox_due_idx
    ON channel_delivery_outbox (next_attempt_at, created_at)
    WHERE status IN ('pending','retry','sending');

CREATE TABLE channel_delivery_attempts (
    id           BIGSERIAL PRIMARY KEY,
    delivery_id  TEXT NOT NULL REFERENCES channel_delivery_outbox(id) ON DELETE CASCADE,
    attempt_no   INTEGER NOT NULL CHECK (attempt_no > 0),
    operation    TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('delivered','retry','dead','failed')),
    started_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ NOT NULL,
    error        TEXT,
    retry_at     TIMESTAMPTZ,
    UNIQUE (delivery_id, attempt_no)
);

CREATE INDEX channel_delivery_attempts_delivery_idx
    ON channel_delivery_attempts (delivery_id, attempt_no);
