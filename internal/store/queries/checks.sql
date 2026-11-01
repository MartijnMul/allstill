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
SET state = ?, state_since = ?, state_reason = ?, last_ping_at = ?, next_expected_at = ?, deadline = ?, fail_streak = ?
WHERE id = ?;

-- name: OpenRuns :many
SELECT id, rid, started_at, timed_out_at FROM runs
WHERE check_id = ? AND ended_at IS NULL AND started_at IS NOT NULL
ORDER BY id;

-- name: EndedRunByRid :one
SELECT id FROM runs
WHERE check_id = ? AND rid = ? AND started_at IS NULL AND ended_at >= ?
ORDER BY id DESC LIMIT 1;

-- name: EndedRunWithoutRid :one
SELECT r.id FROM runs r
WHERE r.id = (SELECT l.id FROM runs l WHERE l.check_id = ? ORDER BY l.id DESC LIMIT 1)
AND r.rid IS NULL AND r.started_at IS NULL AND r.ended_at >= ?
AND NOT EXISTS (SELECT 1 FROM pings p WHERE p.run_id = r.id AND p.kind = 'start');

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

-- name: RunOfCheck :one
SELECT id FROM runs WHERE check_id = ? AND id = ?;

-- name: CountChecksOfProject :one
SELECT count(*) FROM checks WHERE project_id = ? AND deleted_at IS NULL;

-- Keeping history in bounds. Deletes go in batches, so pings never wait long
-- for the write lock.

-- name: ChecksAfter :many
SELECT id FROM checks WHERE id > ? ORDER BY id LIMIT ?;

-- The newest run of a check that is past what it keeps.
-- name: RunPastRetention :one
SELECT id FROM runs WHERE check_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?;

-- A run still waiting for its end is kept: its check counts on it.
-- name: DeleteRunsUpTo :execrows
DELETE FROM runs WHERE id IN (
    SELECT r.id FROM runs r
    WHERE r.check_id = sqlc.arg(of_check) AND r.id <= sqlc.arg(up_to) AND r.outcome <> 'running'
    ORDER BY r.id LIMIT sqlc.arg(batch)
);

-- name: DropOutputUpTo :execrows
UPDATE pings SET body = NULL
WHERE id IN (
    SELECT p.id FROM pings p JOIN runs r ON r.id = p.run_id
    WHERE r.check_id = sqlc.arg(of_check) AND r.id <= sqlc.arg(up_to) AND p.body IS NOT NULL
    LIMIT sqlc.arg(batch)
);

-- Pings outside any run, such as a log line while nothing runs.
-- name: LoosePingPastRetention :one
SELECT id FROM pings WHERE check_id = ? AND run_id IS NULL ORDER BY id DESC LIMIT 1 OFFSET ?;

-- name: DeleteLoosePingsUpTo :execrows
DELETE FROM pings WHERE id IN (
    SELECT p.id FROM pings p
    WHERE p.check_id = sqlc.arg(of_check) AND p.run_id IS NULL AND p.id <= sqlc.arg(up_to)
    ORDER BY p.id LIMIT sqlc.arg(batch)
);

-- name: DeleteEventsBefore :execrows
DELETE FROM events WHERE id IN (
    SELECT e.id FROM events e WHERE e.created_at < sqlc.arg(before) ORDER BY e.id LIMIT sqlc.arg(batch)
);

-- name: DropLooseOutputUpTo :execrows
UPDATE pings SET body = NULL
WHERE id IN (
    SELECT p.id FROM pings p
    WHERE p.check_id = sqlc.arg(of_check) AND p.run_id IS NULL AND p.id <= sqlc.arg(up_to) AND p.body IS NOT NULL
    LIMIT sqlc.arg(batch)
);

-- name: ProjectOfCheck :one
SELECT project_id FROM checks WHERE workspace_id = ? AND id = ? AND deleted_at IS NULL;

-- name: MoveCheck :exec
UPDATE checks SET project_id = sqlc.arg(project_id), version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE workspace_id = sqlc.arg(workspace_id) AND id = sqlc.arg(id);

-- name: SetAlerting :execrows
UPDATE checks SET urgency = ?, critical = ?, version = version + 1, updated_at = ?
WHERE workspace_id = ? AND id = ? AND deleted_at IS NULL;

-- An attempt of an active check, as a run that ended when it did.
-- name: CreateAttemptRun :exec
INSERT INTO runs (id, workspace_id, check_id, outcome, started_at, ended_at, measured, reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- The checks Allstill runs itself, for its loop; a paused one does not run.
-- name: ActiveChecks :many
SELECT id, workspace_id, kind, interval_seconds, settings FROM checks
WHERE kind IN ('http', 'tcp') AND deleted_at IS NULL AND state <> 'paused'
ORDER BY id;

-- name: UpdateActiveCheck :execrows
UPDATE checks
SET interval_seconds = ?, grace_seconds = ?, settings = ?, deadline = ?, version = version + 1, updated_at = ?
WHERE workspace_id = ? AND id = ? AND deleted_at IS NULL;
