-- Fresh databases only. The application refuses upgrades from legacy schema 1.
DROP TABLE gray_rules;
CREATE TABLE gray_rules (config_id VARCHAR(36) NOT NULL, id VARCHAR(36) NOT NULL, position INTEGER NOT NULL, rule_json TEXT NOT NULL, PRIMARY KEY(config_id,id), FOREIGN KEY(config_id) REFERENCES configs(id) ON DELETE CASCADE);
