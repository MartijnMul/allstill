-- SPDX-License-Identifier: AGPL-3.0-only

-- name: CreateProbe :exec
INSERT INTO probes (id, workspace_id, team_id, name, key_hash, check_id, created_at)
SELECT sqlc.arg(id), pr.workspace_id, pr.team_id, sqlc.arg(name), sqlc.arg(key_hash), sqlc.arg(check_id), sqlc.arg(created_at)
FROM projects pr WHERE pr.workspace_id = sqlc.arg(workspace_id) AND pr.id = sqlc.arg(project_id);

-- name: AddProbeProject :exec
INSERT OR IGNORE INTO probe_projects (workspace_id, probe_id, project_id) VALUES (?, ?, ?);

-- name: ProbeByKeyHash :one
SELECT p.id, p.workspace_id, p.name, p.check_id, c.ping_uuid FROM probes p
JOIN checks c ON c.id = p.check_id
WHERE p.key_hash = ?;

-- A probe's name is told apart within its team.
-- name: ProbesByName :many
SELECT id, name, team_id FROM probes WHERE workspace_id = ? AND name = ? ORDER BY id;

-- name: ProbeInTeam :one
SELECT id, name FROM probes WHERE team_id = ? AND name = ?;

-- name: ProbeNameByID :one
SELECT name FROM probes WHERE workspace_id = ? AND id = ?;

-- name: UpdateProbeKey :execrows
UPDATE probes SET key_hash = ? WHERE workspace_id = ? AND id = ?;

-- name: ProbeServes :one
SELECT count(*) FROM probe_projects WHERE workspace_id = ? AND probe_id = ? AND project_id = ?;

-- name: ProbesOfProject :many
SELECT p.id, p.name FROM probes p
JOIN probe_projects pp ON pp.probe_id = p.id AND pp.workspace_id = p.workspace_id
WHERE pp.workspace_id = ? AND pp.project_id = ?
ORDER BY p.name;

-- name: ChecksOfProbe :many
SELECT id, kind, interval_seconds, settings FROM checks
WHERE probe_id = ? AND kind IN ('http', 'tcp') AND deleted_at IS NULL AND state <> 'paused'
ORDER BY id;

-- name: SetCheckProbe :exec
UPDATE checks SET probe_id = ?, deadline = ?, fail_streak = 0, updated_at = ? WHERE workspace_id = ? AND id = ?;

-- name: MarkProbeRunSeen :execrows
INSERT OR IGNORE INTO probe_runs_seen (workspace_id, probe_id, run_id, seen_at) VALUES (?, ?, ?, ?);

-- name: DeleteOldProbeRunsSeen :exec
DELETE FROM probe_runs_seen WHERE seen_at < ?;

-- name: ProbeOfOwnCheck :one
SELECT id FROM probes WHERE workspace_id = ? AND check_id = ?;
