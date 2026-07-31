-- Durable execution Saga checkpoints and failure audit. This table is internal
-- orchestration state and does not alter the frozen public Run resource.

CREATE TABLE oneshot_run_sagas (
    run_id                  TEXT PRIMARY KEY REFERENCES oneshot_runs(id) ON DELETE CASCADE,
    task_id                 TEXT NOT NULL,
    delivery_id             TEXT NOT NULL,
    stage                   TEXT NOT NULL CHECK (stage IN (
                                'run_created','credential_acquired','command_built',
                                'process_started','running_persisted','process_exited',
                                'output_committed','terminal_persisted',
                                'credential_released','acknowledged'
                            )),
    credential_lease_id     TEXT,
    pid                     INTEGER CHECK (pid > 0),
    exit_code               INTEGER,
    result_error_code       TEXT,
    result_error_message    TEXT,
    result_cancelled        BOOLEAN NOT NULL DEFAULT FALSE,
    result_timed_out        BOOLEAN NOT NULL DEFAULT FALSE,
    failure_stage           TEXT,
    primary_error_code      TEXT,
    primary_error_message   TEXT,
    compensation_error      TEXT,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (run_id, task_id) REFERENCES oneshot_runs(id, task_id) ON DELETE CASCADE,
    FOREIGN KEY (run_id, delivery_id) REFERENCES oneshot_runs(id, delivery_id) ON DELETE CASCADE,
    CHECK (result_error_code IS NULL OR result_error_code IN (
        'oneshot.disabled','oneshot.unauthorized','oneshot.forbidden','oneshot.invalid_request',
        'oneshot.idempotency_required','oneshot.idempotency_conflict','oneshot.task_not_found',
        'oneshot.run_not_found','oneshot.artifact_not_found','oneshot.context_not_found',
        'oneshot.context_owner_mismatch','oneshot.unsupported_provider','oneshot.provider_unavailable',
        'oneshot.resume_unsupported','oneshot.resume_failed','oneshot.invalid_transition',
        'oneshot.run_conflict','oneshot.queue_unavailable','oneshot.delivery_exhausted',
        'oneshot.execution_failed','oneshot.output_persist_failed','oneshot.artifact_unavailable',
        'oneshot.cancel_failed','oneshot.timeout','oneshot.rate_limited','oneshot.internal'
    )),
    CHECK (primary_error_code IS NULL OR primary_error_code IN (
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

CREATE INDEX oneshot_run_sagas_recovery_idx
    ON oneshot_run_sagas (stage, updated_at, run_id)
    WHERE stage NOT IN ('credential_released','acknowledged')
       OR credential_lease_id IS NOT NULL;
