-- Complete initial schema; historical migrations have been consolidated.
CREATE TABLE namespaces (name VARCHAR(128) PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE config_groups (namespace VARCHAR(128) NOT NULL, name VARCHAR(128) NOT NULL, PRIMARY KEY(namespace,name), FOREIGN KEY(namespace) REFERENCES namespaces(name)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE configs (id VARCHAR(36) PRIMARY KEY, namespace VARCHAR(128) NOT NULL, group_name VARCHAR(128) NOT NULL, name VARCHAR(128) NOT NULL, revision BIGINT NOT NULL, last_version BIGINT NOT NULL, global_version BIGINT NOT NULL, UNIQUE(namespace,group_name,name), FOREIGN KEY(namespace,group_name) REFERENCES config_groups(namespace,name)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE config_versions (config_id VARCHAR(36) NOT NULL, number BIGINT NOT NULL, content MEDIUMTEXT NOT NULL, format VARCHAR(16) NOT NULL, description TEXT NOT NULL, action VARCHAR(32) NOT NULL, source_version BIGINT NOT NULL, created_at BIGINT NOT NULL, PRIMARY KEY(config_id,number), FOREIGN KEY(config_id) REFERENCES configs(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE change_stream (id INTEGER PRIMARY KEY, sequence BIGINT NOT NULL, purged_through BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
INSERT INTO change_stream VALUES (1,0,0);
CREATE TABLE change_events (sequence BIGINT PRIMARY KEY, config_id VARCHAR(36) NOT NULL, namespace VARCHAR(128) NOT NULL, group_name VARCHAR(128) NOT NULL, name VARCHAR(128) NOT NULL, deleted BOOLEAN NOT NULL, created_at BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE maintenance_lease (id INTEGER PRIMARY KEY, owner VARCHAR(36) NOT NULL, expires_at BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
INSERT INTO maintenance_lease VALUES (1,'',0);
INSERT INTO namespaces VALUES ('public');
INSERT INTO config_groups VALUES ('public','DEFAULT_GROUP');

CREATE TABLE gray_rules (config_id VARCHAR(36) NOT NULL, id VARCHAR(36) NOT NULL, position INTEGER NOT NULL, rule_json MEDIUMTEXT NOT NULL, PRIMARY KEY(config_id,id), FOREIGN KEY(config_id) REFERENCES configs(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE config_beta (config_id VARCHAR(36) PRIMARY KEY, beta_json LONGTEXT NOT NULL, FOREIGN KEY(config_id) REFERENCES configs(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE client_instances (id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, refreshed_at BIGINT NOT NULL, expires_at BIGINT NOT NULL) ENGINE=InnoDB;
CREATE INDEX client_instances_expiry ON client_instances(expires_at);
CREATE TABLE connected_clients (id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, instance_id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, fingerprint VARCHAR(64) NOT NULL, snapshot_json LONGTEXT NOT NULL, FOREIGN KEY(instance_id) REFERENCES client_instances(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX connected_clients_instance ON connected_clients(instance_id);
CREATE TABLE client_tags (client_id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, name VARBINARY(128) NOT NULL, value VARBINARY(512) NOT NULL, PRIMARY KEY(client_id,name), FOREIGN KEY(client_id) REFERENCES connected_clients(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX client_tags_suggestions ON client_tags(name,value);

CREATE TABLE users (id VARCHAR(36) PRIMARY KEY, username VARCHAR(64) NOT NULL UNIQUE, password_hash VARCHAR(128) NOT NULL, enabled BOOLEAN NOT NULL, system_admin BOOLEAN NOT NULL, clients_read BOOLEAN NOT NULL, auth_version BIGINT NOT NULL, revision BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE user_grants (user_id VARCHAR(36) NOT NULL, namespace VARCHAR(128) NOT NULL, group_name VARCHAR(128) NOT NULL, access VARCHAR(8) NOT NULL, PRIMARY KEY(user_id, namespace, group_name), FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE audit_logs (
 id BIGINT AUTO_INCREMENT PRIMARY KEY,
 request_id VARCHAR(36) NOT NULL UNIQUE,
 created_at BIGINT NOT NULL,
 user_id VARCHAR(36) NOT NULL DEFAULT '',
 username VARCHAR(64) NOT NULL DEFAULT '',
 attempted_username VARCHAR(64) NOT NULL DEFAULT '',
 ip VARCHAR(45) NOT NULL,
 action VARCHAR(64) NOT NULL,
 subject VARCHAR(512) NOT NULL,
 method VARCHAR(8) NOT NULL,
 result VARCHAR(8) NOT NULL,
 status INTEGER NOT NULL DEFAULT 0,
 duration_ms BIGINT NOT NULL DEFAULT 0,
 details_json MEDIUMTEXT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX audit_created_at_idx ON audit_logs(created_at);
CREATE INDEX audit_username_idx ON audit_logs(username,id);
CREATE INDEX audit_attempted_username_idx ON audit_logs(attempted_username,id);
CREATE INDEX audit_ip_idx ON audit_logs(ip,id);
CREATE INDEX audit_action_idx ON audit_logs(action,id);
CREATE INDEX audit_result_idx ON audit_logs(result,id);

CREATE TABLE replication_source (id INTEGER PRIMARY KEY, source_id VARCHAR(36) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
INSERT INTO replication_source VALUES (1,'');
CREATE TABLE replication_mirror (id INTEGER PRIMARY KEY, source_id VARCHAR(36) NOT NULL, source_sequence BIGINT NOT NULL, ready BOOLEAN NOT NULL, last_success BIGINT NOT NULL, owner VARCHAR(36) NOT NULL, epoch BIGINT NOT NULL, expires_at BIGINT NOT NULL, active_generation VARCHAR(36) NOT NULL DEFAULT '', rebuild_required BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
INSERT INTO replication_mirror VALUES (1,'',0,FALSE,0,'',0,0,'',FALSE);

CREATE TABLE replication_generations (id VARCHAR(36) PRIMARY KEY, owner VARCHAR(36) NOT NULL, epoch BIGINT NOT NULL, staging BOOLEAN NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE replication_configs (generation VARCHAR(36) NOT NULL, namespace VARCHAR(128) NOT NULL, group_name VARCHAR(128) NOT NULL, name VARCHAR(128) NOT NULL, config_id VARCHAR(36) NOT NULL, state_json LONGTEXT, state_proto LONGBLOB, PRIMARY KEY(generation,namespace,group_name,name), UNIQUE(generation,config_id), FOREIGN KEY(generation) REFERENCES replication_generations(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
