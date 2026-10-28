CREATE TABLE replication_source (id INTEGER PRIMARY KEY, source_id VARCHAR(36) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
INSERT INTO replication_source VALUES (1,'');
CREATE TABLE replication_mirror (id INTEGER PRIMARY KEY, source_id VARCHAR(36) NOT NULL, source_sequence BIGINT NOT NULL, ready BOOLEAN NOT NULL, last_success BIGINT NOT NULL, owner VARCHAR(36) NOT NULL, epoch BIGINT NOT NULL, expires_at BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
INSERT INTO replication_mirror VALUES (1,'',0,FALSE,0,'',0,0);
