CREATE TABLE admin (id INTEGER PRIMARY KEY, password_hash VARCHAR(128) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
INSERT INTO admin SELECT 1,password_hash FROM users WHERE username='admin';
DROP TABLE user_grants;
DROP TABLE users;
