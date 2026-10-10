CREATE TABLE client_instances (id VARCHAR(36) PRIMARY KEY, refreshed_at BIGINT NOT NULL, expires_at BIGINT NOT NULL);
CREATE INDEX client_instances_expiry ON client_instances(expires_at);
CREATE TABLE connected_clients (id VARCHAR(36) PRIMARY KEY, instance_id VARCHAR(36) NOT NULL REFERENCES client_instances(id) ON DELETE CASCADE, fingerprint VARCHAR(64) NOT NULL, snapshot_json TEXT NOT NULL);
CREATE INDEX connected_clients_instance ON connected_clients(instance_id);
CREATE TABLE client_tags (client_id VARCHAR(36) NOT NULL REFERENCES connected_clients(id) ON DELETE CASCADE, name BYTEA NOT NULL CHECK (octet_length(name)<=128), value BYTEA NOT NULL CHECK (octet_length(value)<=512), PRIMARY KEY(client_id,name));
CREATE INDEX client_tags_suggestions ON client_tags(name,value);
