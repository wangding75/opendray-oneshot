-- 0003_drop_legacy_seed_config — clear stale "executable" keys written
-- by the 0002 seed migration. The catalog subsystem (M2) now serves
-- executable from embedded manifests; providers.config holds only
-- user-edited values. Resetting to '{}' for the four builtin ids is
-- safe because v2 has no production users yet.

UPDATE providers
SET config     = '{}'::jsonb,
    updated_at = NOW()
WHERE id IN ('claude', 'codex', 'gemini', 'shell');
