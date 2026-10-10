CREATE TABLE client_instances (id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, refreshed_at BIGINT NOT NULL, expires_at BIGINT NOT NULL) ENGINE=InnoDB;
CREATE INDEX client_instances_expiry ON client_instances(expires_at);
CREATE TABLE connected_clients (id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, instance_id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, fingerprint VARCHAR(64) NOT NULL, snapshot_json LONGTEXT NOT NULL, FOREIGN KEY(instance_id) REFERENCES client_instances(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX connected_clients_instance ON connected_clients(instance_id);
CREATE TABLE client_tags (client_id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, name VARBINARY(128) NOT NULL, value VARBINARY(512) NOT NULL, PRIMARY KEY(client_id,name), FOREIGN KEY(client_id) REFERENCES connected_clients(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX client_tags_suggestions ON client_tags(name,value);
