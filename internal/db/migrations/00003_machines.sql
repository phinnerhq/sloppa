-- Tokens belong to machines: one per computer the CLI is set up on, which a setup link registers and
-- running setup again re-pairs. Every agent token becomes a machine of its own, named as it was.

-- +goose Up
CREATE TABLE machines (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  -- Shown in the dashboard, and renamable there: the hostname, until someone renames it.
  name TEXT NOT NULL,
  -- What the CLI said at setup; NULL for machines made by hand or from agent tokens.
  hostname TEXT,
  os TEXT,
  arch TEXT,
  cli_version TEXT,
  created_ms INTEGER NOT NULL
);

CREATE INDEX machines_user ON machines (user_id, created_ms DESC);

-- A machine's token, with the machine: deleting the machine revokes it, and the previews it started
-- go with it.
ALTER TABLE credentials ADD COLUMN machine_id TEXT REFERENCES machines (id) ON DELETE CASCADE;

-- Tokens of users long gone signed nobody in, and would have no user to belong to.
DELETE FROM credentials WHERE kind = 'agent' AND user_id NOT IN (SELECT id FROM users);

-- The machine takes its token's id, the one the dashboard knew the agent by.
INSERT INTO machines (id, user_id, name, created_ms)
  SELECT id, user_id, COALESCE(NULLIF(TRIM(name), ''), 'machine'), created_ms FROM credentials WHERE kind = 'agent';
UPDATE credentials SET kind = 'machine', machine_id = id, name = NULL WHERE kind = 'agent';

-- +goose Down
UPDATE credentials SET kind = 'agent', name = (SELECT name FROM machines WHERE machines.id = credentials.machine_id),
  machine_id = NULL WHERE kind = 'machine';
-- machine_id stays, always NULL: SQLite can't drop a column with a foreign key.
DROP TABLE machines;
