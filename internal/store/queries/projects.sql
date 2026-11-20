-- SPDX-License-Identifier: AGPL-3.0-only

-- A team's projects run by its owners (intent/teams/plan-3.md, part B).

-- Who can see a project, through what, and whether they are told (R7). The
-- highest role and whether they are told are what the views say; the team's
-- role and their own row say only through what.
-- name: PeopleOfProject :many
SELECT p.id, p.name, r.role,
       CAST(COALESCE(tm.role, '') AS TEXT) AS team_role,
       CAST(COALESCE(m.role, '') AS TEXT) AS own_role,
       CAST(EXISTS (SELECT 1 FROM project_alerted a WHERE a.project_id = r.project_id AND a.person_id = r.person_id) AS INTEGER) AS told
FROM project_roles r
JOIN projects pr ON pr.id = r.project_id
JOIN people p ON p.id = r.person_id
LEFT JOIN team_members tm ON tm.team_id = pr.team_id AND tm.person_id = p.id
LEFT JOIN memberships m ON m.project_id = pr.id AND m.person_id = p.id
WHERE r.project_id = sqlc.arg(project_id) AND pr.deleted_at IS NULL AND p.disabled_at IS NULL
ORDER BY p.name, p.id;

-- The owners of a project by any way in, but one person, among those not disabled.
-- name: OtherOwnersOfProject :one
SELECT COUNT(*) FROM project_roles r JOIN people p ON p.id = r.person_id
WHERE r.project_id = sqlc.arg(project_id) AND r.role = 'owner' AND r.person_id <> sqlc.arg(person_id) AND p.disabled_at IS NULL;

-- A deleted project keeps its row, so its log lines and its deleted checks
-- still point somewhere; its ping key no longer finds it.
-- name: MarkProjectDeleted :execrows
UPDATE projects SET deleted_at = sqlc.arg(at), ping_key_hash = NULL
WHERE workspace_id = sqlc.arg(workspace_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeleteAPIKeysOfProject :execrows
DELETE FROM api_keys WHERE workspace_id = ? AND project_id = ?;

-- name: DeleteInvitesOfProject :exec
DELETE FROM invites WHERE workspace_id = ? AND project_id = ?;

-- name: DeleteMembershipsOfProject :exec
DELETE FROM memberships WHERE workspace_id = ? AND project_id = ?;

-- name: DeleteAlertChoicesOfProject :exec
DELETE FROM alert_choices WHERE workspace_id = ? AND project_id = ?;

-- name: DeleteProbeProjectsOfProject :exec
DELETE FROM probe_projects WHERE workspace_id = ? AND project_id = ?;

-- The checks still left in a team's deleted projects, which tidying removes
-- in time, and removing the team at once.
-- name: ChecksOfDeletedProjects :many
SELECT c.id FROM checks c JOIN projects p ON p.id = c.project_id
WHERE p.team_id = ? AND p.deleted_at IS NOT NULL AND c.deleted_at IS NOT NULL ORDER BY c.id;

-- A team's deleted projects that hold no check any more.
-- name: RemoveDeletedProjects :exec
DELETE FROM projects
WHERE team_id = ? AND deleted_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM checks c WHERE c.project_id = projects.id);
