-- CHECK is enforced on supported MySQL 8.0.16+; refuse to discard protobuf data.
ALTER TABLE replication_configs ADD CONSTRAINT mirror_json_only CHECK (state_proto IS NULL);
ALTER TABLE replication_configs DROP CHECK mirror_json_only;
ALTER TABLE replication_configs MODIFY COLUMN state_json LONGTEXT NOT NULL;
ALTER TABLE replication_configs DROP COLUMN state_proto;
