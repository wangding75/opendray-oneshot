-- 0017_backups_target_id_set_null — let target deletion succeed
-- when only soft-deleted (status='deleted') historical backup
-- rows reference it.
--
-- Original 0014 set backups.target_id ON DELETE RESTRICT, mirroring
-- backup_schedules. That made sense for live runs but caused a
-- real UX bug: once any backup ever ran against a target, the
-- target became un-deletable for the lifetime of those audit
-- rows — even if every blob had been pruned and the rows were
-- all status='deleted'. Found end-to-end testing the SMB
-- target on the home UNAS.
--
-- Fix: switch to ON DELETE SET NULL. backups.target_id becomes
-- nullable; deleting a target nulls the column on every
-- referencing row, preserving the audit history (id, started_at,
-- bytes, status='deleted', etc.) without the FK chain holding
-- the target hostage.
--
-- backup_schedules.target_id stays RESTRICT — an active schedule
-- pointing at a phantom target would silently fail every tick,
-- worse than an explicit "delete the schedule first" error.

ALTER TABLE backups
    DROP CONSTRAINT backups_target_id_fkey,
    ALTER COLUMN target_id DROP NOT NULL,
    ADD CONSTRAINT backups_target_id_fkey
        FOREIGN KEY (target_id) REFERENCES backup_targets(id)
        ON DELETE SET NULL;
