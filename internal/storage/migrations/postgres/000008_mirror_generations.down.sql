DROP TABLE replication_configs;
DROP TABLE replication_generations;
ALTER TABLE replication_mirror DROP COLUMN active_generation;
