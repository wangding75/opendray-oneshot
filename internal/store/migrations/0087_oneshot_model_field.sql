-- 0087_oneshot_model_field — add model column to oneshot_tasks and oneshot_runs

ALTER TABLE oneshot_tasks ADD COLUMN model TEXT;
ALTER TABLE oneshot_runs ADD COLUMN model TEXT;

-- Migrate existing tasks with provider's default model from providers config
UPDATE oneshot_tasks t
SET model = p.config->>'model'
FROM providers p
WHERE p.id = t.provider_id AND p.config->>'model' IS NOT NULL AND p.config->>'model' <> '';

-- Migrate existing runs from their task's model
UPDATE oneshot_runs r
SET model = t.model
FROM oneshot_tasks t
WHERE t.id = r.task_id AND t.model IS NOT NULL;
