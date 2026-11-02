-- CHECK enforcement requires MySQL 8.0.16+, as in the protobuf downgrade guard.
ALTER TABLE replication_mirror ADD CONSTRAINT mirror_rebuild_clear CHECK (NOT rebuild_required);
ALTER TABLE replication_mirror DROP CHECK mirror_rebuild_clear;
ALTER TABLE replication_mirror DROP COLUMN rebuild_required;
