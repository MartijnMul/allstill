-- SPDX-License-Identifier: AGPL-3.0-only

-- name: CheckByPingUUID :one
SELECT * FROM checks WHERE ping_uuid = ? AND deleted_at IS NULL;

-- name: ProjectByPingKeyHash :one
SELECT * FROM projects WHERE ping_key_hash = ? AND deleted_at IS NULL;

-- name: CheckBySlug :one
SELECT * FROM checks WHERE project_id = ? AND slug = ? AND deleted_at IS NULL;

-- name: CheckByID :one
SELECT * FROM checks WHERE workspace_id = ? AND id = ? AND deleted_at IS NULL;

-- name: ChecksOfProject :many
SELECT * FROM checks
WHERE workspace_id = ? AND project_id = ? AND deleted_at IS NULL
ORDER BY name, id;

-- name: CreateCheck :exec
INSERT INTO checks (
    id, workspace_id, project_id, ping_uuid, name, slug, description, tags, kind,
    schedule_kind, interval_seconds, cron_expression, time_zone, grace_seconds, settings,
    state, state_since, created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?
);

-- name: UpdateCheckStatus :exec
UPDATE checks
SET state = ?, state_since = ?, state_reason = ?, last_ping_at = ?, next_expected_at = ?, deadline = ?
WHERE id = ?;

-- name: OpenRuns :many
SELECT id, rid, started_at, timed_out_at FROM runs
WHERE check_id = ? AND ended_at IS NULL AND started_at IS NOT NULL
ORDER BY id;

-- name: CreateRun :exec
INSERT INTO runs (id, workspace_id, check_id, rid, outcome, started_at, ended_at, exit_status)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: RestartRun :exec
UPDATE runs SET started_at = ?, timed_out_at = NULL, outcome = 'running' WHERE id = ?;

-- name: FinishRun :exec
UPDATE runs SET outcome = ?, ended_at = ?, exit_status = ? WHERE id = ?;

-- name: TimeOutRun :exec
UPDATE runs SET outcome = 'timeout', timed_out_at = ? WHERE id = ? AND ended_at IS NULL;

-- name: CreatePing :exec
INSERT INTO pings (
    workspace_id, check_id, run_id, kind, exit_status, received_at, method, remote_addr, user_agent,
    body_truncated, body
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CreateEvent :exec
INSERT INTO events (workspace_id, project_id, type, subject_id, created_at) VALUES (?, ?, ?, ?, ?);

-- name: DueChecks :many
SELECT * FROM checks
WHERE deadline IS NOT NULL AND deadline <= ? AND deleted_at IS NULL
ORDER BY deadline
LIMIT ?;

-- name: EarliestDeadline :one
SELECT CAST(coalesce(min(deadline), 0) AS INTEGER) FROM checks WHERE deadline IS NOT NULL AND deleted_at IS NULL;

-- name: RunsOfCheck :many
SELECT * FROM runs WHERE check_id = ? AND id < ? ORDER BY id DESC LIMIT ?;

-- name: PingsOfRun :many
SELECT id, kind, exit_status, received_at, body_truncated, body FROM pings
WHERE run_id = ?
ORDER BY id;

-- name: InstallationValue :one
SELECT value FROM installation WHERE key = ?;

-- name: SetInstallationValue :exec
INSERT INTO installation (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- Values a ping carries join those of its run; a later value of the same name wins.
-- name: MergeMeasured :exec
UPDATE runs SET measured = json_patch(measured, ?) WHERE id = ?;
