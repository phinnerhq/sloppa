-- Previews become rows: the tunnel reconnects to one with its agent token, and revoking the token,
-- stopping the preview or letting it expire is all it takes to end it.

-- +goose Up
CREATE TABLE previews (
  -- 8 random base36 characters.
  id TEXT PRIMARY KEY,
  -- The subdomain: the name if it has one, the id otherwise.
  label TEXT NOT NULL UNIQUE,
  name TEXT,
  user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  -- The session or agent token that started it, which the tunnel must connect with.
  credential_id TEXT NOT NULL REFERENCES credentials (id) ON DELETE CASCADE,
  -- argon2id, PHC format; NULL when unprotected.
  password_hash TEXT,
  port INTEGER NOT NULL,
  command TEXT,
  cwd TEXT,
  hostname TEXT,
  created_ms INTEGER NOT NULL,
  expires_ms INTEGER NOT NULL
);

CREATE INDEX previews_user ON previews (user_id, created_ms DESC);

-- Upload tickets were signed with this key; nothing is signed anymore.
DROP TABLE IF EXISTS secrets;
-- The TypeScript server's migration log, superseded by goose's.
DROP TABLE IF EXISTS effect_sql_migrations;

-- +goose Down
DROP TABLE previews;
