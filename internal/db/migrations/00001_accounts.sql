-- The accounts the TypeScript server created, as it left them. IF NOT EXISTS adopts its database.

-- +goose Up
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  created_ms INTEGER NOT NULL,
  -- The BOOTSTRAP_PASSWORD last applied, hashed: one left set mustn't undo a password changed since.
  seed_hash TEXT
);

-- A signed-in browser ('session'), an agent's token ('agent'), or a setup link waiting for agents to
-- redeem it ('setup'). Only a SHA-256 of each is kept: the database alone lets nobody in.
CREATE TABLE IF NOT EXISTS credentials (
  id TEXT PRIMARY KEY,
  hash TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL,
  user_id INTEGER NOT NULL,
  name TEXT,
  created_ms INTEGER NOT NULL,
  expires_ms INTEGER,
  last_used_ms INTEGER
);

CREATE INDEX IF NOT EXISTS credentials_user ON credentials (user_id, kind, created_ms DESC);

-- +goose Down
DROP TABLE credentials;
DROP TABLE users;
