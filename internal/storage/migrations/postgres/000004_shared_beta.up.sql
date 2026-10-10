-- Fresh databases only; legacy schema 1/2/3 upgrades are rejected by the app.
CREATE TABLE config_beta (config_id VARCHAR(36) PRIMARY KEY REFERENCES configs(id) ON DELETE CASCADE, beta_json TEXT NOT NULL);
