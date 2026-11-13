-- Agent setup that `sloppa agents sync` brings to every machine: instructions, shared and per machine,
-- and skills with their files.

-- +goose Up
CREATE TABLE agent_instructions (
  user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  updated_ms INTEGER NOT NULL
);

CREATE TABLE machine_instructions (
  machine_id TEXT PRIMARY KEY REFERENCES machines (id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  updated_ms INTEGER NOT NULL
);

CREATE TABLE skills (
  user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT,
  -- The GitHub repository it was imported from, as owner/repo, its folder there and the commit read;
  -- all NULL for a skill uploaded by hand.
  source TEXT,
  source_path TEXT,
  source_ref TEXT,
  -- SHA-256 over the files: what machines compare to know they have this version.
  hash TEXT NOT NULL,
  files INTEGER NOT NULL,
  size INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_ms INTEGER NOT NULL,
  PRIMARY KEY (user_id, name)
);

CREATE TABLE skill_files (
  user_id INTEGER NOT NULL,
  skill TEXT NOT NULL,
  path TEXT NOT NULL,
  executable INTEGER NOT NULL DEFAULT 0,
  content BLOB NOT NULL,
  PRIMARY KEY (user_id, skill, path),
  FOREIGN KEY (user_id, skill) REFERENCES skills (user_id, name) ON DELETE CASCADE
);

-- Machines an enabled skill stays off.
CREATE TABLE skill_exclusions (
  user_id INTEGER NOT NULL,
  skill TEXT NOT NULL,
  machine_id TEXT NOT NULL REFERENCES machines (id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, skill, machine_id),
  FOREIGN KEY (user_id, skill) REFERENCES skills (user_id, name) ON DELETE CASCADE
);

-- What each machine said the last time it synced.
CREATE TABLE agent_syncs (
  machine_id TEXT PRIMARY KEY REFERENCES machines (id) ON DELETE CASCADE,
  fingerprint TEXT NOT NULL,
  problems TEXT NOT NULL,
  cli_version TEXT,
  synced_ms INTEGER NOT NULL
);

-- +goose Down
DROP TABLE agent_syncs;
DROP TABLE skill_exclusions;
DROP TABLE skill_files;
DROP TABLE skills;
DROP TABLE machine_instructions;
DROP TABLE agent_instructions;
