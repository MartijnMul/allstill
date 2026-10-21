-- SPDX-License-Identifier: AGPL-3.0-only

-- name: Workspace :one
SELECT * FROM workspaces ORDER BY id LIMIT 1;

-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, name, created_at) VALUES (?, ?, ?);

-- name: CreatePerson :exec
INSERT INTO people (id, workspace_id, name, email, workspace_admin, time_zone, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: Person :one
SELECT * FROM people WHERE workspace_id = ? AND id = ? AND disabled_at IS NULL;

-- name: CreateProject :exec
INSERT INTO projects (id, workspace_id, name, ping_key_hash, created_at) VALUES (?, ?, ?, ?, ?);

-- name: AddMember :exec
INSERT INTO memberships (workspace_id, project_id, person_id, role, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (project_id, person_id) DO UPDATE SET role = excluded.role;

-- The projects a person may see, with their role. A workspace administrator
-- sees every project, as owner.
-- name: ProjectsOfPerson :many
SELECT p.id, p.name, CAST(CASE WHEN per.workspace_admin = 1 THEN 'owner' ELSE m.role END AS TEXT) AS role
FROM projects p
JOIN people per ON per.workspace_id = p.workspace_id AND per.id = sqlc.arg(person_id)
LEFT JOIN memberships m ON m.project_id = p.id AND m.person_id = per.id
WHERE p.workspace_id = sqlc.arg(workspace_id)
  AND p.deleted_at IS NULL
  AND per.disabled_at IS NULL
  AND (m.role IS NOT NULL OR per.workspace_admin = 1)
ORDER BY p.name, p.id;

-- name: ProjectsByName :many
SELECT id FROM projects WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL ORDER BY id;

-- name: AllProjects :many
SELECT id, name FROM projects WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name, id;
