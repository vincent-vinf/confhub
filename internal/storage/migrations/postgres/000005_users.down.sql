CREATE TABLE admin (id INTEGER PRIMARY KEY, password_hash VARCHAR(128) NOT NULL);
INSERT INTO admin SELECT 1,password_hash FROM users WHERE username='admin';
DROP TABLE user_grants;
DROP TABLE users;
