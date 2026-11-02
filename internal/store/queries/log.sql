-- SPDX-License-Identifier: AGPL-3.0-only
-- The log (intent/activity-log/spec.md).

-- name: CreateLogEntry :exec
INSERT INTO log_entries (
    workspace_id, happened_at, recorded_at, project_id, project_name, kind,
    about_kind, about_id, about_name, by_kind, by_id, by_name, by_outsider,
    person_id, person_name, person_outsider, incident_id, hidden, group_id, details
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: NameOfProject :one
SELECT name FROM projects WHERE id = ?;

-- name: PersonForLog :one
SELECT name, workspace_admin FROM people WHERE workspace_id = ? AND id = ?;

-- name: RoleInProject :one
SELECT role FROM memberships WHERE project_id = ? AND person_id = ?;

-- The time of a check's state change this many back. On the path of a ping that
-- changes a state, so `+kind` keeps it to the check's own rows, statistics or not.
-- name: StateEntryAgo :one
SELECT recorded_at FROM log_entries
WHERE about_id = ? AND +kind = 'check.state' ORDER BY id DESC LIMIT 1 OFFSET ?;

-- name: LastFlappingEntry :one
SELECT kind FROM log_entries
WHERE about_id = ? AND kind IN ('check.flapping', 'check.steady') ORDER BY id DESC LIMIT 1;

-- The checks that were changing too often and have not been called steady since.
-- name: FlappingChecks :many
SELECT workspace_id, project_id, project_name, about_id, about_name, recorded_at FROM log_entries f
WHERE f.kind IN ('check.flapping', 'check.steady') AND f.kind = 'check.flapping'
  AND NOT EXISTS (
      SELECT 1 FROM log_entries s
      WHERE s.about_id = f.about_id AND s.kind IN ('check.flapping', 'check.steady') AND s.id > f.id
  );

-- A deleted check counts too: it is steady by being gone.
-- name: StateSinceOfCheck :one
SELECT state, state_since, deleted_at, project_id, name FROM checks WHERE id = ?;

-- The checks of an incident as they are now, or as they were when one has gone.
-- name: ChecksOfIncident :many
SELECT ic.workspace_id, ic.check_id,
       CAST(COALESCE(c.name, ic.check_name) AS TEXT) AS name,
       CAST(COALESCE(c.project_id, ic.project_id) AS TEXT) AS project_id
FROM incident_checks ic LEFT JOIN checks c ON c.id = ic.check_id
WHERE ic.incident_id = ?
ORDER BY ic.joined_at, ic.check_id;

-- Whether a person was last reached about an incident, or last could not be.
-- name: LastReachOfIncident :one
SELECT kind FROM log_entries
WHERE incident_id = ? AND incident_id <> '' AND person_id = ? AND kind IN ('message.reached', 'message.unreached')
ORDER BY id DESC LIMIT 1;

-- name: DeleteLogEntriesBefore :execrows
DELETE FROM log_entries WHERE id IN (
    SELECT e.id FROM log_entries e WHERE e.recorded_at < sqlc.arg(before) ORDER BY e.recorded_at LIMIT sqlc.arg(batch)
);
