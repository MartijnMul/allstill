-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Teams, the second step (intent/teams/plan-2.md): what the wall between
-- teams needs of the tables.

-- A line about a team and no project, such as a team that was made or a
-- person let into one, says which team. Lines written before this have none.
ALTER TABLE log_entries ADD COLUMN team_id TEXT NOT NULL DEFAULT '';

CREATE INDEX log_entries_team ON log_entries (team_id, id) WHERE team_id <> '';

-- A row of one's own in a project that gives no more than one's role in the
-- project's team gives nothing, and would turn into a real role the day one
-- leaves the team. Such rows exist for administrators, who own the team since
-- the first step, and only said whether they were told, which alert_choices
-- says since then (spec, R15).
DELETE FROM memberships
WHERE EXISTS (
    SELECT 1 FROM projects p
    JOIN team_members tm ON tm.team_id = p.team_id AND tm.person_id = memberships.person_id
    WHERE p.id = memberships.project_id
      AND CASE tm.role WHEN 'owner' THEN 3 WHEN 'editor' THEN 2 ELSE 1 END
       >= CASE memberships.role WHEN 'owner' THEN 3 WHEN 'editor' THEN 2 ELSE 1 END
);

-- Who made a key is read as the log's lines are, and gains what those gain: 2
-- for someone in the project through its team who is no administrator. The
-- column allowed 0 and 1 only, and a table's constraint cannot be changed, so
-- the table is made again and its rows copied as they are.
CREATE TABLE api_keys_new (
    id             TEXT PRIMARY KEY,
    key_hash       BLOB NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    workspace_id   TEXT NOT NULL,
    project_id     TEXT NOT NULL,
    name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    scope          TEXT NOT NULL CHECK (scope IN ('read', 'manage')),
    made_by_kind   TEXT NOT NULL,
    made_by_id     TEXT NOT NULL,
    made_by_name   TEXT NOT NULL,
    made_by_outsider INTEGER NOT NULL CHECK (made_by_outsider IN (0, 1, 2)),
    made_via       TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    used_at        INTEGER,
    UNIQUE (project_id, name),
    FOREIGN KEY (workspace_id, project_id) REFERENCES projects (workspace_id, id)
) STRICT;

INSERT INTO api_keys_new (id, key_hash, workspace_id, project_id, name, scope, made_by_kind, made_by_id,
    made_by_name, made_by_outsider, made_via, created_at, used_at)
SELECT id, key_hash, workspace_id, project_id, name, scope, made_by_kind, made_by_id,
    made_by_name, made_by_outsider, made_via, created_at, used_at
FROM api_keys;

DROP TABLE api_keys;
ALTER TABLE api_keys_new RENAME TO api_keys;
