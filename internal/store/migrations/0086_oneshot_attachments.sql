-- OD-OS-24: owner-scoped immutable input attachment staging.
-- This schema is isolated from PTY Session tables and binds bytes to a
-- specific One-shot Delivery only after owner/project/expiry checks pass.
CREATE TABLE IF NOT EXISTS oneshot_staged_attachments (
    id              TEXT PRIMARY KEY CHECK (id ~ '^oat_[a-z0-9]+$'),
    principal_kind  TEXT NOT NULL CHECK (principal_kind IN ('admin','integration')),
    principal_id    TEXT NOT NULL CHECK (btrim(principal_id) <> ''),
    project_id      TEXT NOT NULL CHECK (btrim(project_id) <> ''),
    source_kind     TEXT NOT NULL CHECK (source_kind IN ('api','telegram','mobile','web')),
    source_ref      TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL CHECK (btrim(name) <> '' AND name !~ '[/\\]'),
    declared_mime   TEXT NOT NULL DEFAULT '',
    detected_mime   TEXT NOT NULL CHECK (btrim(detected_mime) <> ''),
    size_bytes      BIGINT NOT NULL CHECK (size_bytes > 0),
    sha256          TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_key     TEXT NOT NULL UNIQUE CHECK (btrim(storage_key) <> ''),
    status          TEXT NOT NULL CHECK (status IN ('ready','deleted','expired')),
    created_at      TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at),
    deleted_at      TIMESTAMPTZ,
    CHECK ((status='deleted') = (deleted_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS oneshot_staged_attachments_owner_expiry_idx
    ON oneshot_staged_attachments (principal_kind,principal_id,project_id,status,expires_at,id);
CREATE UNIQUE INDEX IF NOT EXISTS oneshot_staged_attachments_source_dedup_idx
    ON oneshot_staged_attachments (principal_kind,principal_id,project_id,source_kind,source_ref)
    WHERE source_ref <> '' AND status <> 'deleted';

CREATE UNIQUE INDEX IF NOT EXISTS oneshot_deliveries_id_task_uidx
    ON oneshot_deliveries (id,task_id);

CREATE TABLE IF NOT EXISTS oneshot_delivery_attachments (
    task_id         TEXT NOT NULL REFERENCES oneshot_tasks(id) ON DELETE CASCADE,
    delivery_id     TEXT NOT NULL REFERENCES oneshot_deliveries(id) ON DELETE CASCADE,
    attachment_id   TEXT NOT NULL REFERENCES oneshot_staged_attachments(id) ON DELETE RESTRICT,
    ordinal         INTEGER NOT NULL CHECK (ordinal >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (delivery_id,attachment_id),
    UNIQUE (delivery_id,ordinal),
    FOREIGN KEY (delivery_id,task_id) REFERENCES oneshot_deliveries(id,task_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS oneshot_delivery_attachments_task_idx
    ON oneshot_delivery_attachments (task_id,delivery_id,ordinal);

-- Explicit cross-device notification preference. Telegram activity refreshes
-- this owner/project-scoped address; mobile creation may opt into it. It is not
-- a Session/Task routing heuristic and never selects an execution domain.
CREATE TABLE IF NOT EXISTS oneshot_notification_preferences (
    principal_kind  TEXT NOT NULL CHECK (principal_kind IN ('admin','integration')),
    principal_id    TEXT NOT NULL CHECK (btrim(principal_id) <> ''),
    project_id      TEXT NOT NULL CHECK (btrim(project_id) <> ''),
    channel_id      TEXT NOT NULL CHECK (btrim(channel_id) <> ''),
    conversation_id TEXT NOT NULL CHECK (btrim(conversation_id) <> ''),
    thread_id       TEXT NOT NULL DEFAULT '',
    message_id      TEXT NOT NULL DEFAULT '',
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (principal_kind,principal_id,project_id)
);
