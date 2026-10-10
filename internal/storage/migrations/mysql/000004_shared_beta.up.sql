-- Fresh databases only; legacy schema 1/2/3 upgrades are rejected by the app.
CREATE TABLE config_beta (config_id VARCHAR(36) PRIMARY KEY, beta_json LONGTEXT NOT NULL, FOREIGN KEY(config_id) REFERENCES configs(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
