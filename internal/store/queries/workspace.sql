-- SPDX-License-Identifier: AGPL-3.0-only

-- name: Workspace :one
SELECT * FROM workspaces ORDER BY id LIMIT 1;

-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, name, created_at) VALUES (?, ?, ?);

-- name: CreatePerson :exec
INSERT INTO people (id, workspace_id, name, email, workspace_admin, time_zone, created_at, quiet_hours)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: Person :one
SELECT * FROM people WHERE workspace_id = ? AND id = ? AND disabled_at IS NULL;

-- name: CreateProject :exec
INSERT INTO projects (id, workspace_id, team_id, name, ping_key_hash, created_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: RemoveMember :execrows
DELETE FROM memberships WHERE workspace_id = ? AND project_id = ? AND person_id = ?;

-- Whether they are told about the project is not the membership's: see
-- alert_choices, which setMember keeps as the role changes.
-- name: AddMember :exec
INSERT INTO memberships (workspace_id, project_id, person_id, role, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (project_id, person_id) DO UPDATE SET role = excluded.role;

-- The projects a person may see, with their role: what the view project_roles
-- says and nothing else.
-- name: ProjectsOfPerson :many
SELECT p.id, p.name, r.role, p.team_id
FROM projects p
JOIN people per ON per.workspace_id = p.workspace_id AND per.id = sqlc.arg(person_id)
JOIN project_roles r ON r.project_id = p.id AND r.person_id = per.id
WHERE p.workspace_id = sqlc.arg(workspace_id)
  AND p.deleted_at IS NULL
  AND per.disabled_at IS NULL
ORDER BY p.name, p.id;

-- A name is told apart within a team, so several teams can hold it.
-- name: ProjectsByName :many
SELECT id, team_id FROM projects WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL ORDER BY id;

-- name: AllProjects :many
SELECT id, name, team_id FROM projects WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name, id;

-- name: TeamOfProject :one
SELECT team_id FROM projects WHERE workspace_id = ? AND id = ?;

-- name: RenameProject :execrows
UPDATE projects SET name = ? WHERE workspace_id = ? AND id = ? AND deleted_at IS NULL;

-- name: ProjectInWorkspace :one
SELECT id FROM projects WHERE workspace_id = ? AND id = ? AND deleted_at IS NULL;
