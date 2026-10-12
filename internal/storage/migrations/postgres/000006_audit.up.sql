CREATE TABLE audit_logs (
 id BIGSERIAL PRIMARY KEY,
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
 details_json TEXT NOT NULL
);
CREATE INDEX audit_created_at_idx ON audit_logs(created_at);
CREATE INDEX audit_username_idx ON audit_logs(username,id);
CREATE INDEX audit_attempted_username_idx ON audit_logs(attempted_username,id);
CREATE INDEX audit_ip_idx ON audit_logs(ip,id);
CREATE INDEX audit_action_idx ON audit_logs(action,id);
CREATE INDEX audit_result_idx ON audit_logs(result,id);
