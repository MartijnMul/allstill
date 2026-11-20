-- SPDX-License-Identifier: AGPL-3.0-only

-- name: CreateInvite :exec
INSERT INTO invites (id, workspace_id, team_id, project_id, kind, role, label, secret_hash, made_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- The open invitations of one kind in a team, or to one project.
-- name: CountOpenInvitesOfTeam :one
SELECT COUNT(*) FROM invites
WHERE team_id = ? AND kind = ? AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at > sqlc.arg(at);

-- name: CountOpenInvitesOfProject :one
SELECT COUNT(*) FROM invites
WHERE project_id = ? AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at > sqlc.arg(at);

-- The invitations of a team, open and accepted, with who made and who took
-- each; a project's name for those to one project.
-- name: InvitesOfTeam :many
SELECT i.id, i.team_id, CAST(COALESCE(i.project_id, '') AS TEXT) AS project_id, CAST(COALESCE(pr.name, '') AS TEXT) AS project_name,
       i.kind, i.role, i.label, i.made_by, m.name AS made_by_name, i.created_at, i.expires_at, i.withdrawn_at, i.accepted_at,
       CAST(COALESCE(i.accepted_by, '') AS TEXT) AS accepted_by
FROM invites i
JOIN people m ON m.id = i.made_by
LEFT JOIN projects pr ON pr.id = i.project_id
WHERE i.team_id = ?
ORDER BY i.created_at DESC, i.id DESC;

-- name: InviteBySecret :one
SELECT i.*, t.name AS team_name, CAST(COALESCE(pr.name, '') AS TEXT) AS project_name, m.name AS made_by_name
FROM invites i
JOIN teams t ON t.id = i.team_id
JOIN people m ON m.id = i.made_by
LEFT JOIN projects pr ON pr.id = i.project_id
WHERE i.secret_hash = ? AND (i.project_id IS NULL OR pr.deleted_at IS NULL);

-- name: InviteByID :one
SELECT * FROM invites WHERE workspace_id = ? AND id = ?;

-- An invitation is used once: of two at the same time, one changes the row.
-- name: AcceptInvite :execrows
UPDATE invites SET accepted_at = sqlc.arg(at), accepted_by = sqlc.arg(person_id)
WHERE id = sqlc.arg(id) AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at > sqlc.arg(at);

-- It makes one account at most.
-- name: MakePersonOfInvite :execrows
UPDATE invites SET made_person = sqlc.arg(person_id)
WHERE id = sqlc.arg(id) AND made_person IS NULL AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at > sqlc.arg(at);

-- name: WithdrawInvite :execrows
UPDATE invites SET withdrawn_at = sqlc.arg(at)
WHERE workspace_id = sqlc.arg(workspace_id) AND id = sqlc.arg(id) AND accepted_at IS NULL AND withdrawn_at IS NULL
  AND expires_at > sqlc.arg(at);

-- The open invitations someone made in a team, to check whether they may still
-- make each.
-- name: OpenInvitesMadeIn :many
SELECT id, kind, CAST(COALESCE(project_id, '') AS TEXT) AS project_id FROM invites
WHERE team_id = ? AND made_by = ? AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at > sqlc.arg(at);

-- name: WithdrawInvitesOfProject :exec
UPDATE invites SET withdrawn_at = sqlc.arg(at)
WHERE project_id = sqlc.arg(project_id) AND accepted_at IS NULL AND withdrawn_at IS NULL;

-- Invitations that ended or were withdrawn a while ago say nothing anyone needs;
-- one that was accepted says how someone came in, and stays.
-- name: DeleteStaleInvites :execrows
DELETE FROM invites
WHERE accepted_at IS NULL AND (withdrawn_at < sqlc.arg(before) OR expires_at < sqlc.arg(before));

-- How each member of a team came in, by the invitation they accepted last.
-- name: HowMembersCameIn :many
SELECT accepted_by AS person_id, label, made_by, kind, CAST(COALESCE(project_id, '') AS TEXT) AS project_id, accepted_at
FROM invites
WHERE team_id = ? AND accepted_by IS NOT NULL
ORDER BY accepted_at;
