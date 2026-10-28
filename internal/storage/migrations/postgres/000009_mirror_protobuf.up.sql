ALTER TABLE replication_configs ADD COLUMN state_proto BYTEA;
ALTER TABLE replication_configs ALTER COLUMN state_json DROP NOT NULL;
