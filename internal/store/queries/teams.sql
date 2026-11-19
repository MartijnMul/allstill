-- SPDX-License-Identifier: AGPL-3.0-only

-- name: CreateTeam :exec
INSERT INTO teams (id, workspace_id, name, created_at) VALUES (?, ?, ?, ?);

-- name: Teams :many
SELECT id, name FROM teams WHERE workspace_id = ? ORDER BY created_at, id;

-- name: Team :one
SELECT id, name FROM teams WHERE workspace_id = ? AND id = ?;

-- The teams a person is in, with their role in each.
-- name: TeamsOfPerson :many
SELECT t.id, t.name, tm.role
FROM team_members tm JOIN teams t ON t.id = tm.team_id
WHERE tm.workspace_id = ? AND tm.person_id = ?
ORDER BY t.created_at, t.id;

-- name: RoleInTeam :one
SELECT role FROM team_members WHERE team_id = ? AND person_id = ?;

-- A person's role in the team that owns a project, when they are in it.
-- name: RoleInTeamOfProject :one
SELECT tm.role FROM projects p
JOIN team_members tm ON tm.team_id = p.team_id AND tm.person_id = sqlc.arg(person_id)
WHERE p.id = sqlc.arg(project_id);

-- name: RemoveTeamMember :execrows
DELETE FROM team_members WHERE workspace_id = ? AND team_id = ? AND person_id = ?;

-- name: OwnersOfTeam :many
SELECT p.id, p.name FROM team_members tm
JOIN people p ON p.id = tm.person_id
WHERE tm.team_id = ? AND tm.role = 'owner' AND p.disabled_at IS NULL
ORDER BY p.name, p.id;

-- The rows of their own a person has in a team's projects.
-- name: OwnRolesInTeam :many
SELECT m.project_id, m.role FROM memberships m
JOIN projects p ON p.id = m.project_id
WHERE p.team_id = ? AND m.person_id = ?;

-- name: ProjectsOfTeam :many
SELECT id, name FROM projects WHERE team_id = ? AND deleted_at IS NULL ORDER BY name, id;

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

-- What a person chose about a team's projects in which they have no row of their own.
-- name: ClearAlertChoicesOutside :exec
DELETE FROM alert_choices
WHERE alert_choices.person_id = sqlc.arg(person_id)
  AND project_id IN (SELECT p.id FROM projects p WHERE p.team_id = sqlc.arg(team_id))
  AND NOT EXISTS (SELECT 1 FROM memberships m WHERE m.project_id = alert_choices.project_id AND m.person_id = alert_choices.person_id);

-- name: ClearAlertChoice :execrows
DELETE FROM alert_choices WHERE project_id = ? AND person_id = ?;

-- The teams a person is in, or has a role of their own in a project of.
-- name: TeamsConcerning :many
SELECT tm.team_id FROM team_members tm WHERE tm.person_id = sqlc.arg(person_id)
UNION
SELECT p.team_id FROM memberships m JOIN projects p ON p.id = m.project_id WHERE m.person_id = sqlc.arg(person_id);

-- name: CreateNotice :exec
INSERT INTO notices (id, workspace_id, person_id, team_id, kind, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: NoticesOfPerson :many
SELECT id, kind, details, created_at, read_at FROM notices WHERE person_id = ? ORDER BY created_at DESC, id DESC LIMIT ?;

-- name: MarkNoticesRead :exec
UPDATE notices SET read_at = sqlc.arg(read_at) WHERE person_id = sqlc.arg(person_id) AND read_at IS NULL AND created_at <= sqlc.arg(up_to);

-- The channels a notice goes to: every one its person has confirmed.
-- name: ConfirmedChannelsOfPerson :many
SELECT id, workspace_id, kind FROM channels WHERE person_id = ? AND verified_at IS NOT NULL ORDER BY created_at, id;
