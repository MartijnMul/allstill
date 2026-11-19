-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Teams (intent/teams/spec.md, ADR 0005). A team owns projects and probes, and
-- its members have one role in all its projects. This is the first step: an
-- installation becomes one team that owns everything, its administrators own
-- the team, and nobody sees or is told anything they were not before.
CREATE TABLE teams (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id),
    name         TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (workspace_id, id)
) STRICT;

CREATE TABLE team_members (
    workspace_id TEXT NOT NULL,
    team_id      TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    role         TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'owner')),
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (team_id, person_id),
    FOREIGN KEY (workspace_id, team_id) REFERENCES teams (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE INDEX team_members_person ON team_members (person_id);

-- The installation as it is becomes one team, named as it is.
INSERT INTO teams (id, workspace_id, name, created_at)
SELECT 'team_' || lower(hex(randomblob(13))), id, name, created_at FROM workspaces;

-- Its administrators own it, which is how they keep every project.
INSERT INTO team_members (workspace_id, team_id, person_id, role, created_at)
SELECT p.workspace_id, t.id, p.id, 'owner', p.created_at
FROM people p JOIN teams t ON t.workspace_id = p.workspace_id
WHERE p.workspace_admin = 1;

-- Every project belongs to a team. A column added to a table cannot be made
-- never empty, so the table is made again and its rows copied as they are.
CREATE TABLE projects_new (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces (id),
    team_id           TEXT NOT NULL,
    name              TEXT NOT NULL,
    ping_key_hash     BLOB UNIQUE CHECK (ping_key_hash IS NULL OR length(ping_key_hash) = 32),
    recovery_messages INTEGER NOT NULL DEFAULT 1 CHECK (recovery_messages IN (0, 1)),
    default_interval  INTEGER NOT NULL DEFAULT 86400 CHECK (default_interval >= 60),
    default_grace     INTEGER NOT NULL DEFAULT 3600 CHECK (default_grace >= 60),
    created_at        INTEGER NOT NULL,
    deleted_at        INTEGER,
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, team_id) REFERENCES teams (workspace_id, id)
) STRICT;

INSERT INTO projects_new (id, workspace_id, team_id, name, ping_key_hash, recovery_messages,
    default_interval, default_grace, created_at, deleted_at)
SELECT p.id, p.workspace_id, (SELECT t.id FROM teams t WHERE t.workspace_id = p.workspace_id),
    p.name, p.ping_key_hash, p.recovery_messages, p.default_interval, p.default_grace, p.created_at, p.deleted_at
FROM projects p;

DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;

CREATE INDEX projects_team ON projects (team_id);

-- A probe belongs to a team as well, and its name is told apart within it.
CREATE TABLE probes_new (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id),
    team_id      TEXT NOT NULL,
    name         TEXT NOT NULL,
    key_hash     BLOB NOT NULL UNIQUE,
    check_id     TEXT NOT NULL REFERENCES checks (id),
    created_at   INTEGER NOT NULL,
    UNIQUE (team_id, name),
    FOREIGN KEY (workspace_id, team_id) REFERENCES teams (workspace_id, id)
);

INSERT INTO probes_new (id, workspace_id, team_id, name, key_hash, check_id, created_at)
SELECT p.id, p.workspace_id, (SELECT t.id FROM teams t WHERE t.workspace_id = p.workspace_id),
    p.name, p.key_hash, p.check_id, p.created_at
FROM probes p;

DROP TABLE probes;
ALTER TABLE probes_new RENAME TO probes;

-- Whether a person is told about a project is their own choice, apart from
-- their role. A row only where they chose otherwise than their highest role
-- gives: told as owner or editor, not as viewer.
CREATE TABLE alert_choices (
    workspace_id TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    alerts       INTEGER NOT NULL CHECK (alerts IN (0, 1)),
    PRIMARY KEY (project_id, person_id),
    FOREIGN KEY (workspace_id, project_id) REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE INDEX alert_choices_person ON alert_choices (person_id);

-- What a membership's flag said, kept where it differs from that. An
-- administrator owns the team, so their highest role is owner whatever their
-- own row says.
INSERT INTO alert_choices (workspace_id, project_id, person_id, alerts)
SELECT m.workspace_id, m.project_id, m.person_id, m.alerts
FROM memberships m JOIN people p ON p.id = m.person_id
WHERE m.alerts <> (p.workspace_admin = 1 OR m.role <> 'viewer');

-- Before the views: SQLite lets a column go that a view still reads.
ALTER TABLE memberships DROP COLUMN alerts;

-- The one place that says what a person's role in a project is: the highest of
-- their role in the team that owns it and their own role in it. It leaves out
-- nobody: a query that must not count a disabled person or a deleted project
-- says so itself.
CREATE VIEW project_roles AS
SELECT pr.workspace_id AS workspace_id, pr.id AS project_id, p.id AS person_id,
       CAST(CASE
           WHEN tm.role = 'owner' OR m.role = 'owner' THEN 'owner'
           WHEN tm.role = 'editor' OR m.role = 'editor' THEN 'editor'
           ELSE 'viewer'
       END AS TEXT) AS role
FROM projects pr
JOIN people p ON p.workspace_id = pr.workspace_id
LEFT JOIN team_members tm ON tm.team_id = pr.team_id AND tm.person_id = p.id
LEFT JOIN memberships m ON m.project_id = pr.id AND m.person_id = p.id
WHERE tm.person_id IS NOT NULL OR m.person_id IS NOT NULL;

-- The one place that says who is told about a project.
CREATE VIEW project_alerted AS
SELECT r.workspace_id AS workspace_id, r.project_id AS project_id, r.person_id AS person_id
FROM project_roles r
LEFT JOIN alert_choices a ON a.project_id = r.project_id AND a.person_id = r.person_id
WHERE COALESCE(a.alerts, r.role <> 'viewer') = 1;
