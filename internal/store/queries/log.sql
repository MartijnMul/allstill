-- SPDX-License-Identifier: AGPL-3.0-only
-- The log (intent/activity-log/spec.md).

-- name: CreateLogEntry :exec
INSERT INTO log_entries (
    workspace_id, happened_at, recorded_at, project_id, project_name, kind,
    about_kind, about_id, about_name, by_kind, by_id, by_name, by_outsider,
    person_id, person_name, person_outsider, incident_id, hidden, group_id, details, team_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: NameOfProject :one
SELECT name FROM projects WHERE id = ?;

-- name: PersonForLog :one
SELECT name, workspace_admin FROM people WHERE workspace_id = ? AND id = ?;

-- name: RoleInProject :one
SELECT role FROM memberships WHERE project_id = ? AND person_id = ?;

-- The time of a check's state change this many back, going through its lines but not
-- the late ones.
-- name: StateEntryAgo :one
SELECT recorded_at FROM log_entries
WHERE about_id = ? AND kind <> 'check.late' AND +kind = 'check.state' ORDER BY id DESC LIMIT 1 OFFSET ?;

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

-- When a check's last incident closed, or zero.
-- name: LastIncidentClosedOfCheck :one
SELECT CAST(COALESCE(MAX(i.resolved_at), 0) AS INTEGER)
FROM incident_checks ic JOIN incidents i ON i.id = ic.incident_id
WHERE ic.check_id = ?;

-- A deleted check counts too: it is steady by being gone.
-- name: StateSinceOfCheck :one
SELECT state, state_since, deleted_at, project_id, name, kind FROM checks WHERE id = ?;

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

-- Each way of reading the log: the newest entries before a number, from one index.
-- Who may see what is judged in one place, on what these return (logread.go), so a
-- filter cannot reach past it. Late lines have queries and indexes of their own, used
-- only when asked for, so a job that goes late on every run cannot use up what a page
-- may read and hide an older outage.

-- name: LogEntriesOfProject :many
SELECT * FROM log_entries WHERE project_id = sqlc.arg(of) AND kind <> 'check.late' AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(batch);

-- name: LogEntriesAbout :many
SELECT * FROM log_entries WHERE about_id = sqlc.arg(of) AND kind <> 'check.late' AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(batch);

-- name: LogEntriesLateOfProject :many
SELECT * FROM log_entries WHERE project_id = sqlc.arg(of) AND kind = 'check.late' AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(batch);

-- name: LogEntriesLateAbout :many
SELECT * FROM log_entries WHERE about_id = sqlc.arg(of) AND kind = 'check.late' AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(batch);

-- name: LogEntriesOwn :many
SELECT * FROM log_entries WHERE person_id = sqlc.arg(of) AND project_id = '' AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(batch);

-- A team's own lines, which are about no project.
-- name: LogEntriesOfTeam :many
SELECT * FROM log_entries WHERE team_id = sqlc.arg(of) AND team_id <> '' AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(batch);

-- Whether a later line says the same person was not reached about the same
-- incident at the same moment: one was written per check of the incident.
-- name: LaterUnreached :one
SELECT COUNT(*) FROM log_entries
WHERE incident_id = sqlc.arg(incident_id) AND incident_id <> '' AND person_id = sqlc.arg(person_id)
  AND kind = 'message.unreached' AND happened_at = sqlc.arg(happened_at) AND id > sqlc.arg(id);

-- name: LogEntriesOfKind :many
SELECT * FROM log_entries WHERE kind = sqlc.arg(of) AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(batch);

-- name: LogEntriesOfGroup :many
SELECT * FROM log_entries WHERE group_id = sqlc.arg(of) AND group_id <> '' AND id < sqlc.arg(before) ORDER BY id DESC LIMIT sqlc.arg(batch);
