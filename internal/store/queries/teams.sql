-- SPDX-License-Identifier: AGPL-3.0-only

-- name: CreateTeam :exec
INSERT INTO teams (id, workspace_id, name, created_at) VALUES (?, ?, ?, ?);

-- name: Teams :many
SELECT id, name FROM teams WHERE workspace_id = ? ORDER BY created_at, id;

-- name: AddTeamMember :exec
INSERT INTO team_members (workspace_id, team_id, person_id, role, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (team_id, person_id) DO UPDATE SET role = excluded.role;

-- A person's role in a project by any way in, from the view project_roles.
-- name: HighestRoleInProject :one
SELECT role FROM project_roles WHERE project_id = ? AND person_id = ?;

-- name: IsAlerted :one
SELECT COUNT(*) FROM project_alerted WHERE project_id = ? AND person_id = ?;

-- name: SetAlertChoice :exec
INSERT INTO alert_choices (workspace_id, project_id, person_id, alerts)
VALUES (?, ?, ?, ?)
ON CONFLICT (project_id, person_id) DO UPDATE SET alerts = excluded.alerts;

-- name: ClearAlertChoice :exec
DELETE FROM alert_choices WHERE project_id = ? AND person_id = ?;
