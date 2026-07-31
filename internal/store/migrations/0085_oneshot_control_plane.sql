-- OD-OS-18/19/20 control-plane hardening.
-- The frozen One-shot contract uses provider id "claude-code" while the
-- interactive catalog keeps the legacy id "claude". Persist a narrow alias so
-- One-shot Tasks retain their public provider identity without changing PTY.
INSERT INTO providers (id, manifest_hash, config, enabled)
VALUES ('claude-code', 'oneshot-alias:claude-code', '{}'::jsonb, TRUE)
ON CONFLICT (id) DO NOTHING;

UPDATE providers AS alias SET
    manifest_hash=source.manifest_hash,
    config=source.config,
    enabled=source.enabled,
    updated_at=NOW()
FROM providers AS source
WHERE alias.id='claude-code' AND source.id='claude';


-- A Telegram group/thread may contain multiple authorized users and multiple
-- One-shot result messages. Bindings are isolated by principal and exact
-- outbound source message so replies cannot cross users, Tasks, or PTY.
ALTER TABLE oneshot_channel_bindings
    DROP CONSTRAINT IF EXISTS oneshot_channel_bindings_channel_id_conversation_id_thread_id_key;

ALTER TABLE oneshot_channel_bindings
    DROP CONSTRAINT IF EXISTS oneshot_channel_bindings_owner_channel_conversation_thread_key;

DROP INDEX IF EXISTS oneshot_channel_bindings_owner_reply_key;
CREATE UNIQUE INDEX oneshot_channel_bindings_owner_reply_key
    ON oneshot_channel_bindings (
        principal_kind, principal_id, channel_id, conversation_id, thread_id,
        COALESCE(source_message_id, '')
    );

CREATE TABLE IF NOT EXISTS oneshot_lifecycle_events (
    id                  BIGSERIAL PRIMARY KEY,
    principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('admin','integration')),
    principal_id        TEXT NOT NULL CHECK (btrim(principal_id) <> ''),
    project_id          TEXT NOT NULL CHECK (btrim(project_id) <> ''),
    task_id             TEXT NOT NULL REFERENCES oneshot_tasks(id) ON DELETE CASCADE,
    run_id              TEXT REFERENCES oneshot_runs(id) ON DELETE CASCADE,
    aggregate_kind      TEXT NOT NULL CHECK (aggregate_kind IN ('task','run')),
    aggregate_id        TEXT NOT NULL CHECK (btrim(aggregate_id) <> ''),
    sequence            BIGINT NOT NULL CHECK (sequence > 0),
    topic               TEXT NOT NULL CHECK (topic LIKE 'oneshot.task.%' OR topic LIKE 'oneshot.run.%'),
    data                JSONB NOT NULL CHECK (jsonb_typeof(data)='object'),
    occurred_at         TIMESTAMPTZ NOT NULL,
    CHECK ((aggregate_kind='task' AND aggregate_id=task_id AND run_id IS NULL)
        OR (aggregate_kind='run' AND aggregate_id=run_id AND run_id IS NOT NULL)),
    UNIQUE (aggregate_kind,aggregate_id,sequence),
    FOREIGN KEY (run_id,task_id) REFERENCES oneshot_runs(id,task_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS oneshot_lifecycle_events_owner_task_replay_idx
    ON oneshot_lifecycle_events (principal_kind,principal_id,aggregate_kind,occurred_at,id);
CREATE INDEX IF NOT EXISTS oneshot_lifecycle_events_run_replay_idx
    ON oneshot_lifecycle_events (run_id,occurred_at,id) WHERE run_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS oneshot_lifecycle_events_run_topic_key
    ON oneshot_lifecycle_events (aggregate_id,topic) WHERE aggregate_kind='run';

CREATE INDEX IF NOT EXISTS oneshot_standard_events_task_replay_idx
    ON oneshot_standard_events (occurred_at, id);

CREATE INDEX IF NOT EXISTS oneshot_notification_outbox_lease_idx
    ON oneshot_notification_outbox (lease_until)
    WHERE status='sending';
