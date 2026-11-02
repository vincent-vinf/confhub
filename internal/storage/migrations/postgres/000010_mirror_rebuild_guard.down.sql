-- Refuse to erase a persisted source-divergence fence on downgrade.
ALTER TABLE replication_mirror ADD CONSTRAINT mirror_rebuild_clear CHECK (NOT rebuild_required);
ALTER TABLE replication_mirror DROP CONSTRAINT mirror_rebuild_clear;
ALTER TABLE replication_mirror DROP COLUMN rebuild_required;
