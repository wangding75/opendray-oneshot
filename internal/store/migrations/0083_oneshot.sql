-- One-shot Agent persistence. This subsystem is deliberately independent from
-- interactive PTY sessions: no table below depends on interactive PTY data.

CREATE TABLE oneshot_tasks (
    id                  TEXT PRIMARY KEY CHECK (id ~ '^otk_[a-z0-9]+$'),
    principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('admin','integration')),
    principal_id        TEXT NOT NULL CHECK (btrim(principal_id) <> ''),
    project_id          TEXT NOT NULL CHECK (btrim(project_id) <> ''),
    provider_id         TEXT NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
    source              JSONB NOT NULL CHECK (jsonb_typeof(source) = 'object'),
    source_kind         TEXT NOT NULL CHECK (source_kind IN ('api','telegram','mobile','web')),
    source_channel_id   TEXT,
    source_message_id   TEXT,
    prompt              TEXT NOT NULL CHECK (btrim(prompt) <> ''),
    status              TEXT NOT NULL CHECK (status IN (
                            'pending','queued','running','waiting_input',
                            'completed','failed','cancelled','timed_out'
                        )),
    current_run_id      TEXT,
    runtime_context_id  TEXT,
    version             BIGINT NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (updated_at >= created_at),
    CHECK (status <> 'running' OR current_run_id IS NOT NULL),
    CHECK ((source_channel_id IS NULL) = (source_message_id IS NULL)),
    CHECK (source_kind <> 'telegram' OR source_channel_id IS NOT NULL),
    UNIQUE (id, provider_id),
    UNIQUE (id, principal_kind, principal_id)
);

CREATE INDEX oneshot_tasks_owner_created_idx
    ON oneshot_tasks (principal_kind, principal_id, created_at DESC, id DESC);
CREATE INDEX oneshot_tasks_project_status_idx
    ON oneshot_tasks (project_id, status, updated_at DESC);
CREATE INDEX oneshot_tasks_provider_status_idx
    ON oneshot_tasks (provider_id, status, updated_at DESC);
CREATE UNIQUE INDEX oneshot_tasks_channel_source_uidx
    ON oneshot_tasks (source_channel_id, source_message_id)
    WHERE source_channel_id IS NOT NULL AND source_message_id IS NOT NULL;

CREATE TABLE oneshot_runtime_contexts (
    id                  TEXT PRIMARY KEY CHECK (id ~ '^orc_[a-z0-9]+$'),
    principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('admin','integration')),
    principal_id        TEXT NOT NULL CHECK (btrim(principal_id) <> ''),
    project_id          TEXT NOT NULL CHECK (btrim(project_id) <> ''),
    provider_id         TEXT NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
    provider_context_id TEXT NOT NULL CHECK (btrim(provider_context_id) <> ''),
    workspace_path      TEXT NOT NULL CHECK (btrim(workspace_path) <> ''),
    status              TEXT NOT NULL CHECK (status IN ('active','busy','invalid','revoked')),
    version             BIGINT NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (updated_at >= created_at),
    UNIQUE (provider_id, provider_context_id)
);

CREATE INDEX oneshot_runtime_contexts_owner_idx
    ON oneshot_runtime_contexts (principal_kind, principal_id, project_id, provider_id, status, updated_at DESC);

CREATE TABLE oneshot_deliveries (
    id                  TEXT PRIMARY KEY CHECK (id ~ '^odl_[a-z0-9]+$'),
    task_id             TEXT NOT NULL REFERENCES oneshot_tasks(id) ON DELETE CASCADE,
    operation           TEXT NOT NULL CHECK (operation IN ('new','continue','retry')),
    requested_by_kind   TEXT NOT NULL CHECK (requested_by_kind IN ('admin','integration')),
    requested_by_id     TEXT NOT NULL CHECK (btrim(requested_by_id) <> ''),
    input               JSONB NOT NULL CHECK (jsonb_typeof(input) = 'object'),
    idempotency_key     TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    payload_sha256      TEXT NOT NULL CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
    status              TEXT NOT NULL CHECK (status IN (
                            'pending','reserved','retry_wait','acknowledged','dead_letter','cancelled'
                        )),
    attempt             INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts        INTEGER NOT NULL CHECK (max_attempts > 0 AND attempt <= max_attempts),
    available_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_owner         TEXT,
    lease_until         TIMESTAMPTZ,
    run_id              TEXT,
    last_error_code     TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (updated_at >= created_at),
    CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
    CHECK (status <> 'reserved' OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL)),
    CHECK (status = 'reserved' OR (lease_owner IS NULL AND lease_until IS NULL)),
    CHECK (last_error_code IS NULL OR last_error_code IN (
        'oneshot.disabled','oneshot.unauthorized','oneshot.forbidden','oneshot.invalid_request',
        'oneshot.idempotency_required','oneshot.idempotency_conflict','oneshot.task_not_found',
        'oneshot.run_not_found','oneshot.artifact_not_found','oneshot.context_not_found',
        'oneshot.context_owner_mismatch','oneshot.unsupported_provider','oneshot.provider_unavailable',
        'oneshot.resume_unsupported','oneshot.resume_failed','oneshot.invalid_transition',
        'oneshot.run_conflict','oneshot.queue_unavailable','oneshot.delivery_exhausted',
        'oneshot.execution_failed','oneshot.output_persist_failed','oneshot.artifact_unavailable',
        'oneshot.cancel_failed','oneshot.timeout','oneshot.rate_limited','oneshot.internal'
    )),
    UNIQUE (requested_by_kind, requested_by_id, idempotency_key),
    FOREIGN KEY (task_id, requested_by_kind, requested_by_id)
        REFERENCES oneshot_tasks(id, principal_kind, principal_id) ON DELETE CASCADE
);

CREATE INDEX oneshot_deliveries_task_created_idx
    ON oneshot_deliveries (task_id, created_at DESC, id DESC);
CREATE INDEX oneshot_deliveries_due_idx
    ON oneshot_deliveries (available_at, created_at, id)
    WHERE status IN ('pending','retry_wait','reserved');

CREATE TABLE oneshot_runs (
    id                  TEXT PRIMARY KEY CHECK (id ~ '^orn_[a-z0-9]+$'),
    task_id             TEXT NOT NULL REFERENCES oneshot_tasks(id) ON DELETE CASCADE,
    delivery_id         TEXT NOT NULL UNIQUE REFERENCES oneshot_deliveries(id) ON DELETE RESTRICT,
    provider_id         TEXT NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
    runtime_context_id  TEXT REFERENCES oneshot_runtime_contexts(id) ON DELETE SET NULL,
    status              TEXT NOT NULL CHECK (status IN (
                            'created','starting','running','collecting_output','waiting_input',
                            'completed','failed','cancelled','timed_out'
                        )),
    pid                 INTEGER CHECK (pid > 0),
    exit_code           INTEGER,
    error_code          TEXT,
    error_message       TEXT,
    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (task_id, provider_id) REFERENCES oneshot_tasks(id, provider_id) ON DELETE CASCADE,
    CHECK (started_at IS NULL OR started_at >= created_at),
    CHECK (finished_at IS NULL OR finished_at >= created_at),
    CHECK (finished_at IS NULL OR started_at IS NULL OR finished_at >= started_at),
    CHECK (status NOT IN ('running','collecting_output') OR started_at IS NOT NULL),
    CHECK (status NOT IN ('waiting_input','completed','failed','cancelled','timed_out') OR finished_at IS NOT NULL),
    CHECK (status IN ('waiting_input','completed','failed','cancelled','timed_out') OR finished_at IS NULL),
    CHECK (status <> 'failed' OR error_code IS NOT NULL),
    UNIQUE (id, task_id),
    UNIQUE (id, delivery_id),
    CHECK (error_code IS NULL OR error_code IN (
        'oneshot.disabled','oneshot.unauthorized','oneshot.forbidden','oneshot.invalid_request',
        'oneshot.idempotency_required','oneshot.idempotency_conflict','oneshot.task_not_found',
        'oneshot.run_not_found','oneshot.artifact_not_found','oneshot.context_not_found',
        'oneshot.context_owner_mismatch','oneshot.unsupported_provider','oneshot.provider_unavailable',
        'oneshot.resume_unsupported','oneshot.resume_failed','oneshot.invalid_transition',
        'oneshot.run_conflict','oneshot.queue_unavailable','oneshot.delivery_exhausted',
        'oneshot.execution_failed','oneshot.output_persist_failed','oneshot.artifact_unavailable',
        'oneshot.cancel_failed','oneshot.timeout','oneshot.rate_limited','oneshot.internal'
    ))
);

CREATE INDEX oneshot_runs_task_created_idx
    ON oneshot_runs (task_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX oneshot_runs_one_active_per_task_uidx
    ON oneshot_runs (task_id)
    WHERE status IN ('created','starting','running','collecting_output');

ALTER TABLE oneshot_tasks
    ADD CONSTRAINT oneshot_tasks_current_run_fk
        FOREIGN KEY (current_run_id, id) REFERENCES oneshot_runs(id, task_id) DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT oneshot_tasks_runtime_context_fk
        FOREIGN KEY (runtime_context_id) REFERENCES oneshot_runtime_contexts(id) ON DELETE SET NULL;

ALTER TABLE oneshot_deliveries
    ADD CONSTRAINT oneshot_deliveries_run_fk
        FOREIGN KEY (run_id, id) REFERENCES oneshot_runs(id, delivery_id) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE oneshot_artifacts (
    id              TEXT PRIMARY KEY CHECK (id ~ '^oar_[a-z0-9]+$'),
    task_id         TEXT NOT NULL REFERENCES oneshot_tasks(id) ON DELETE CASCADE,
    run_id          TEXT REFERENCES oneshot_runs(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL CHECK (kind IN (
                        'raw_stdout','raw_stderr','structured_output','final_result',
                        'file','log','attachment'
                    )),
    name            TEXT NOT NULL CHECK (btrim(name) <> ''),
    content_type    TEXT NOT NULL CHECK (btrim(content_type) <> ''),
    size_bytes      BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256          TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_key     TEXT NOT NULL CHECK (btrim(storage_key) <> ''),
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (task_id, storage_key),
    UNIQUE (id, run_id),
    FOREIGN KEY (run_id, task_id) REFERENCES oneshot_runs(id, task_id) ON DELETE CASCADE
);

CREATE INDEX oneshot_artifacts_task_created_idx
    ON oneshot_artifacts (task_id, created_at DESC, id DESC);
CREATE INDEX oneshot_artifacts_run_created_idx
    ON oneshot_artifacts (run_id, created_at, id) WHERE run_id IS NOT NULL;

CREATE TABLE oneshot_stream_records (
    id              TEXT PRIMARY KEY CHECK (id ~ '^osr_[a-z0-9]+$'),
    run_id          TEXT NOT NULL REFERENCES oneshot_runs(id) ON DELETE CASCADE,
    sequence        BIGINT NOT NULL CHECK (sequence > 0),
    stream          TEXT NOT NULL CHECK (stream IN ('stdout','stderr')),
    byte_offset     BIGINT NOT NULL CHECK (byte_offset >= 0),
    byte_length     BIGINT NOT NULL CHECK (byte_length > 0),
    raw_artifact_id TEXT NOT NULL,
    text            TEXT,
    decode_status   TEXT NOT NULL CHECK (decode_status IN ('valid_utf8','lossy_utf8','binary')),
    sha256          TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    received_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (run_id, sequence),
    UNIQUE (run_id, stream, byte_offset),
    UNIQUE (id, run_id),
    FOREIGN KEY (raw_artifact_id, run_id) REFERENCES oneshot_artifacts(id, run_id) ON DELETE RESTRICT
);

CREATE INDEX oneshot_stream_records_run_sequence_idx
    ON oneshot_stream_records (run_id, sequence);

CREATE TABLE oneshot_standard_events (
    id                      TEXT PRIMARY KEY CHECK (id ~ '^ose_[a-z0-9]+$'),
    run_id                  TEXT NOT NULL REFERENCES oneshot_runs(id) ON DELETE CASCADE,
    sequence                BIGINT NOT NULL CHECK (sequence > 0),
    type                    TEXT NOT NULL CHECK (btrim(type) <> ''),
    source_stream_record_id TEXT,
    adapter_id              TEXT NOT NULL CHECK (btrim(adapter_id) <> ''),
    adapter_version         TEXT NOT NULL CHECK (btrim(adapter_version) <> ''),
    content                 JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(content) = 'object'),
    occurred_at             TIMESTAMPTZ NOT NULL,
    UNIQUE (run_id, sequence),
    FOREIGN KEY (source_stream_record_id, run_id) REFERENCES oneshot_stream_records(id, run_id) ON DELETE RESTRICT
);

CREATE INDEX oneshot_standard_events_run_sequence_idx
    ON oneshot_standard_events (run_id, sequence);
CREATE INDEX oneshot_standard_events_type_time_idx
    ON oneshot_standard_events (type, occurred_at DESC);

CREATE TABLE oneshot_channel_bindings (
    id                  BIGSERIAL PRIMARY KEY,
    principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('admin','integration')),
    principal_id        TEXT NOT NULL CHECK (btrim(principal_id) <> ''),
    channel_id          TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    conversation_id     TEXT NOT NULL CHECK (btrim(conversation_id) <> ''),
    thread_id           TEXT NOT NULL DEFAULT '',
    source_message_id   TEXT,
    task_id             TEXT NOT NULL REFERENCES oneshot_tasks(id) ON DELETE CASCADE,
    binding_kind        TEXT NOT NULL CHECK (binding_kind IN ('task','continue','notification')),
    expires_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (updated_at >= created_at),
    UNIQUE (channel_id, conversation_id, thread_id),
    FOREIGN KEY (task_id, principal_kind, principal_id)
        REFERENCES oneshot_tasks(id, principal_kind, principal_id) ON DELETE CASCADE
);

CREATE INDEX oneshot_channel_bindings_task_idx
    ON oneshot_channel_bindings (task_id, updated_at DESC);
CREATE INDEX oneshot_channel_bindings_expiry_idx
    ON oneshot_channel_bindings (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE oneshot_idempotency_keys (
    id                  BIGSERIAL PRIMARY KEY,
    principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('admin','integration')),
    principal_id        TEXT NOT NULL CHECK (btrim(principal_id) <> ''),
    method              TEXT NOT NULL CHECK (btrim(method) <> ''),
    canonical_path      TEXT NOT NULL CHECK (btrim(canonical_path) <> ''),
    idempotency_key     TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    payload_sha256      TEXT NOT NULL CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
    resource_kind       TEXT,
    resource_id         TEXT,
    response_status     INTEGER CHECK (response_status BETWEEN 100 AND 599),
    response_body       JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ,
    CHECK ((resource_kind IS NULL) = (resource_id IS NULL)),
    UNIQUE (principal_kind, principal_id, method, canonical_path, idempotency_key)
);

CREATE INDEX oneshot_idempotency_expiry_idx
    ON oneshot_idempotency_keys (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE oneshot_notification_outbox (
    id                  TEXT PRIMARY KEY,
    idempotency_key     TEXT NOT NULL UNIQUE CHECK (btrim(idempotency_key) <> ''),
    task_id             TEXT NOT NULL REFERENCES oneshot_tasks(id) ON DELETE CASCADE,
    run_id              TEXT REFERENCES oneshot_runs(id) ON DELETE CASCADE,
    event_type          TEXT NOT NULL CHECK (event_type LIKE 'oneshot.%'),
    destination         JSONB NOT NULL CHECK (jsonb_typeof(destination) = 'object'),
    payload             JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','sending','retry','delivered','dead')),
    attempt_count       INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_owner         TEXT,
    lease_until         TIMESTAMPTZ,
    last_error          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at        TIMESTAMPTZ,
    CHECK (updated_at >= created_at),
    CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
    FOREIGN KEY (run_id, task_id) REFERENCES oneshot_runs(id, task_id) ON DELETE CASCADE
);

CREATE INDEX oneshot_notification_outbox_due_idx
    ON oneshot_notification_outbox (next_attempt_at, created_at, id)
    WHERE status IN ('pending','retry','sending');
