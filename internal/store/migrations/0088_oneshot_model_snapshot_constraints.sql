-- 0088_oneshot_model_snapshot_constraints — enforce model snapshot non-null/not-blank
-- Rationale: 0087 added model columns but left them nullable. This migration
-- normalises blank values, backfills runs from their task snapshot where
-- possible, and then locks the columns with NOT NULL + CHECK constraints.
-- Historical data that cannot be resolved fails closed (RAISE EXCEPTION).

-- 5.1 Normalise blank values: convert purely-whitespace strings to NULL.
--    Normal model strings with leading/trailing whitespace are left untouched.
UPDATE oneshot_tasks SET model = NULL WHERE model IS NOT NULL AND btrim(model) = '';
UPDATE oneshot_runs   SET model = NULL WHERE model IS NOT NULL AND btrim(model) = '';

-- 5.2 Backfill runs from their task's model snapshot.
--    Only fills when the run is NULL and the task has a non-NULL model.
--    Never infers from the current provider config.
UPDATE oneshot_runs r
SET model = t.model
FROM oneshot_tasks t
WHERE r.task_id = t.id
  AND r.model IS NULL
  AND t.model IS NOT NULL;

-- 5.3 Fail closed if any unresolved (NULL) model remains.
--    The migration runs in a single transaction, so a failure here rolls
--    back everything above — no partial schema changes are committed.
DO $$
DECLARE
    unresolved_tasks INT;
    unresolved_runs  INT;
BEGIN
    SELECT COUNT(*) INTO unresolved_tasks FROM oneshot_tasks WHERE model IS NULL;
    SELECT COUNT(*) INTO unresolved_runs  FROM oneshot_runs   WHERE model IS NULL;

    IF unresolved_tasks > 0 OR unresolved_runs > 0 THEN
        RAISE EXCEPTION 'unresolved tasks: %, unresolved runs: %', unresolved_tasks, unresolved_runs;
    END IF;
END $$;

-- 5.4 Enforce NOT NULL + non-blank constraints.
--    At this point the DO block has guaranteed no NULL values exist.
ALTER TABLE oneshot_tasks ALTER COLUMN model SET NOT NULL;
ALTER TABLE oneshot_runs   ALTER COLUMN model SET NOT NULL;

ALTER TABLE oneshot_tasks ADD CONSTRAINT oneshot_tasks_model_not_blank CHECK (btrim(model) <> '');
ALTER TABLE oneshot_runs   ADD CONSTRAINT oneshot_runs_model_not_blank   CHECK (btrim(model) <> '');