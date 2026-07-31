-- 0002_seed_default_providers — minimal provider rows so the sessions
-- foreign key is satisfiable before the M2 catalog lands.
--
-- Each row stores the executable in config.executable. M2 replaces this
-- with the full v1-style manifest payload and richer config schema.
-- ON CONFLICT keeps the migration idempotent for environments that may
-- already have these rows from a prior dev seed.

INSERT INTO providers (id, manifest_hash, config, enabled) VALUES
    ('shell',  'seed-m1', '{"executable":"/bin/bash"}'::jsonb,  TRUE),
    ('claude', 'seed-m1', '{"executable":"claude"}'::jsonb,     TRUE),
    ('codex',  'seed-m1', '{"executable":"codex"}'::jsonb,      TRUE),
    ('gemini', 'seed-m1', '{"executable":"gemini"}'::jsonb,     TRUE)
ON CONFLICT (id) DO NOTHING;
