DROP TABLE gray_rules;
CREATE TABLE gray_rules (config_id VARCHAR(36) NOT NULL, id VARCHAR(36) NOT NULL, position INTEGER NOT NULL, target_version BIGINT NOT NULL, rule_json TEXT NOT NULL, PRIMARY KEY(config_id,id), FOREIGN KEY(config_id,target_version) REFERENCES config_versions(config_id,number));
