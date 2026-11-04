-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A project's keys, for scripts (intent/cli-anywhere/spec.md, R11 to R13). Only a
-- key's hash is kept. It belongs to its project, not to whoever made it, so it keeps
-- working when they have gone: the row has its own copy of who made it, and through
-- which sign-in, read as the log's lines are. One that has been revoked is gone: what
-- happened to it is in the log.
CREATE TABLE api_keys (
    id             TEXT PRIMARY KEY,
    key_hash       BLOB NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    workspace_id   TEXT NOT NULL,
    project_id     TEXT NOT NULL,
    name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    scope          TEXT NOT NULL CHECK (scope IN ('read', 'manage')),
    made_by_kind   TEXT NOT NULL,
    made_by_id     TEXT NOT NULL,
    made_by_name   TEXT NOT NULL,
    made_by_outsider INTEGER NOT NULL CHECK (made_by_outsider IN (0, 1)),
    made_via       TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    used_at        INTEGER,
    UNIQUE (project_id, name),
    FOREIGN KEY (workspace_id, project_id) REFERENCES projects (workspace_id, id)
) STRICT;
