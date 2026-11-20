-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Teams, the third step (intent/teams/plan-3.md, part A): a team's owners let
-- people in themselves, by an invitation they hand over.

-- What is offered: a role in a team, or a role of one's own in one of its
-- projects. The kind names no values, so that a share (step 4) is a new kind
-- and not a new table. The secret is kept as its SHA-256; the maker sees it
-- once. made_person is the account made through it, as it makes one at most.
CREATE TABLE invites (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    team_id      TEXT NOT NULL,
    project_id   TEXT,
    kind         TEXT NOT NULL,
    role         TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'owner')),
    label        TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 100),
    secret_hash  BLOB NOT NULL UNIQUE CHECK (length(secret_hash) = 32),
    made_by      TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL CHECK (expires_at > created_at),
    withdrawn_at INTEGER,
    accepted_at  INTEGER,
    accepted_by  TEXT,
    made_person  TEXT,
    FOREIGN KEY (workspace_id, team_id) REFERENCES teams (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, project_id) REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, made_by) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX invites_team ON invites (team_id, created_at);
CREATE INDEX invites_project ON invites (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX invites_made_by ON invites (made_by);
CREATE INDEX invites_expires ON invites (expires_at);
