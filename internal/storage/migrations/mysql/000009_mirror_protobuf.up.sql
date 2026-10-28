ALTER TABLE replication_configs ADD COLUMN state_proto LONGBLOB;
ALTER TABLE replication_configs MODIFY COLUMN state_json LONGTEXT NULL;
