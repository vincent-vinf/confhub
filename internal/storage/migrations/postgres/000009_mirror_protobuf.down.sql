-- A protobuf mirror cannot be converted back by a SQL migration. Refuse data loss.
ALTER TABLE replication_configs ADD CONSTRAINT mirror_json_only CHECK (state_proto IS NULL);
ALTER TABLE replication_configs DROP CONSTRAINT mirror_json_only;
ALTER TABLE replication_configs ALTER COLUMN state_json SET NOT NULL;
ALTER TABLE replication_configs DROP COLUMN state_proto;
