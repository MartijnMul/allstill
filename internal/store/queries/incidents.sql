-- SPDX-License-Identifier: AGPL-3.0-only

-- name: OpenIncidentOfCheck :one
SELECT i.* FROM incident_checks ic JOIN incidents i ON i.id = ic.incident_id
WHERE ic.check_id = ? AND ic.open = 1;

-- name: CreateIncident :exec
INSERT INTO incidents (id, workspace_id, title, state, urgency, critical, cause_check_id, opened_at)
VALUES (?, ?, ?, 'open', ?, ?, ?, ?);

-- name: AddIncidentCheck :exec
INSERT INTO incident_checks (workspace_id, incident_id, check_id, check_name, project_id, joined_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: SetRecovering :exec
UPDATE incidents SET recovering_since = ? WHERE id = ? AND state <> 'resolved';

-- name: ResolveIncident :exec
UPDATE incidents SET state = 'resolved', resolved_at = ?, resolve_reason = ?, recovering_since = NULL
WHERE id = ? AND state <> 'resolved';

-- name: CloseIncidentChecks :exec
UPDATE incident_checks SET open = 0, left_at = ? WHERE incident_id = ? AND open = 1;

-- name: MoveIncidentCheck :exec
UPDATE incident_checks SET project_id = ? WHERE check_id = ? AND open = 1;

-- Everyone who gets alerts for a project becomes a recipient of an incident,
-- with the sender to look at once: members whose alerts are on, and
-- workspace administrators who are no member, as they see every project.
-- name: AddFollowers :exec
INSERT OR IGNORE INTO recipients (workspace_id, incident_id, person_id, due_at)
SELECT p.workspace_id, sqlc.arg(incident_id), p.id, sqlc.arg(due_at)
FROM people p
LEFT JOIN memberships m ON m.project_id = sqlc.arg(project_id) AND m.person_id = p.id
WHERE p.workspace_id = sqlc.arg(workspace_id) AND p.disabled_at IS NULL
  AND (m.alerts = 1 OR (m.person_id IS NULL AND p.workspace_admin = 1));

-- name: LookAgainAtTold :exec
UPDATE recipients SET due_at = ? WHERE incident_id = ? AND told = 'down';

-- name: SettledIncidents :many
SELECT id, recovering_since FROM incidents WHERE recovering_since IS NOT NULL AND recovering_since <= ? ORDER BY recovering_since LIMIT ?;

-- What the sender needs to decide for each recipient that is due: the
-- incident, the person's settings and what they were told. Who follows is
-- worked out from each check's current project, so a moved check is followed
-- by its new project's people; the urgency and critical flag are its cause's
-- as they are now.
-- name: DueRecipients :many
SELECT r.workspace_id, r.incident_id, r.person_id, r.told, r.told_how,
       CAST(r.told_shadow = 1 OR i.shadow = 1 AS INTEGER) AS told_shadow, r.due_at,
       i.state, i.title, i.resolve_reason,
       CAST(COALESCE((SELECT c.urgency FROM checks c WHERE c.id = i.cause_check_id), i.urgency) AS TEXT) AS urgency,
       CAST(COALESCE((SELECT c.critical FROM checks c WHERE c.id = i.cause_check_id), i.critical) AS INTEGER) AS critical,
       CAST(COALESCE((
           SELECT c.state_since < i.opened_at FROM checks c
           WHERE c.id = i.cause_check_id AND c.state IN ('up', 'warning')
       ), 0) AS INTEGER) AS over_when_heard,
       p.time_zone, p.quiet_hours, CAST(p.disabled_at IS NOT NULL AS INTEGER) AS disabled,
       CAST(EXISTS (
           SELECT 1 FROM incident_checks ic
           LEFT JOIN checks c ON c.id = ic.check_id
           LEFT JOIN memberships m ON m.project_id = COALESCE(c.project_id, ic.project_id) AND m.person_id = r.person_id
           WHERE ic.incident_id = r.incident_id
             AND (m.alerts = 1 OR (m.person_id IS NULL AND p.workspace_admin = 1))
       ) AS INTEGER) AS follows,
       CAST(COALESCE((
           SELECT pr.recovery_messages FROM incident_checks ic
           LEFT JOIN checks c ON c.id = ic.check_id
           JOIN projects pr ON pr.id = COALESCE(c.project_id, ic.project_id)
           WHERE ic.incident_id = r.incident_id AND ic.check_id = i.cause_check_id
       ), 1) AS INTEGER) AS recovery_messages
FROM recipients r
JOIN incidents i ON i.id = r.incident_id
JOIN people p ON p.id = r.person_id
WHERE r.due_at IS NOT NULL AND r.due_at <= ?
ORDER BY r.person_id, r.due_at, r.incident_id
LIMIT ?;

-- name: SetTold :exec
UPDATE recipients SET told = ?, told_at = ?, told_how = ?, told_shadow = ?, due_at = ?
WHERE incident_id = ? AND person_id = ?;

-- name: SetDue :exec
UPDATE recipients SET due_at = ? WHERE incident_id = ? AND person_id = ?;

-- name: CreateDelivery :exec
INSERT INTO deliveries (id, workspace_id, person_id, channel_id, kind, items, state, attempts, last_error, created_at, sent_at, message_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: LastMessageAt :one
SELECT CAST(COALESCE(MAX(created_at), 0) AS INTEGER) FROM deliveries WHERE person_id = ? AND kind IN ('alert', 'digest');

-- name: DigestPeople :many
SELECT id, workspace_id, time_zone, digest_time, digest_at, created_at FROM people WHERE disabled_at IS NULL ORDER BY id;

-- name: SetDigestAt :exec
UPDATE people SET digest_at = ? WHERE id = ?;

-- A person's part of their digest: incidents still open, and those resolved
-- since the last digest, that they follow and that may alert.
-- name: DigestIncidents :many
SELECT r.incident_id, r.told, r.told_how,
       CAST(r.told_shadow = 1 OR i.shadow = 1 AS INTEGER) AS told_shadow,
       i.state, i.title, i.resolve_reason, i.opened_at, i.resolved_at,
       CAST(COALESCE((SELECT c.urgency FROM checks c WHERE c.id = i.cause_check_id), i.urgency) AS TEXT) AS urgency,
       CAST(EXISTS (
           SELECT 1 FROM incident_checks ic
           LEFT JOIN checks c ON c.id = ic.check_id
           LEFT JOIN memberships m ON m.project_id = COALESCE(c.project_id, ic.project_id) AND m.person_id = r.person_id
           WHERE ic.incident_id = r.incident_id
             AND (m.alerts = 1 OR (m.person_id IS NULL AND p.workspace_admin = 1))
       ) AS INTEGER) AS follows,
       CAST(COALESCE((
           SELECT pr.recovery_messages FROM incident_checks ic
           LEFT JOIN checks c ON c.id = ic.check_id
           JOIN projects pr ON pr.id = COALESCE(c.project_id, ic.project_id)
           WHERE ic.incident_id = r.incident_id AND ic.check_id = i.cause_check_id
       ), 1) AS INTEGER) AS recovery_messages
FROM recipients r
JOIN incidents i ON i.id = r.incident_id
JOIN people p ON p.id = r.person_id
WHERE r.person_id = sqlc.arg(person_id)
  AND COALESCE((SELECT c.urgency FROM checks c WHERE c.id = i.cause_check_id), i.urgency) <> 'none'
  AND (i.state <> 'resolved' OR i.resolved_at > sqlc.arg(since))
ORDER BY i.opened_at, i.id;

-- Checks a person follows that cannot alert: never pinged or never worked, paused,
-- or with a schedule Allstill cannot read. For a check Allstill runs itself,
-- unknown without a deadline is its normal state while it cannot be judged
-- (intent/http-checks/spec.md); its settings are checked when it is made.
-- name: SilentChecks :many
SELECT c.name, c.state, c.state_reason, pr.name AS project_name
FROM checks c
JOIN projects pr ON pr.id = c.project_id AND pr.deleted_at IS NULL
JOIN people p ON p.id = sqlc.arg(person_id)
LEFT JOIN memberships m ON m.project_id = c.project_id AND m.person_id = p.id
WHERE c.workspace_id = p.workspace_id AND c.deleted_at IS NULL
  AND (c.state = 'new' OR (c.state = 'paused' AND c.urgency <> 'none')
       OR (c.state = 'unknown' AND c.deadline IS NULL AND c.kind NOT IN ('http', 'tcp')))
  AND (m.alerts = 1 OR (m.person_id IS NULL AND p.workspace_admin = 1))
ORDER BY pr.name, c.name;

-- Checks in warning that the person follows, such as a certificate running
-- out, for the digest (intent/http-checks/spec.md, R9). A check that never
-- alerts is shown, not sent.
-- name: WarningChecks :many
SELECT c.name, c.state_reason, pr.name AS project_name
FROM checks c
JOIN projects pr ON pr.id = c.project_id AND pr.deleted_at IS NULL
JOIN people p ON p.id = sqlc.arg(person_id)
LEFT JOIN memberships m ON m.project_id = c.project_id AND m.person_id = p.id
WHERE c.workspace_id = p.workspace_id AND c.deleted_at IS NULL
  AND c.state = 'warning' AND c.urgency <> 'none'
  AND (m.alerts = 1 OR (m.person_id IS NULL AND p.workspace_admin = 1))
ORDER BY pr.name, c.name;

-- name: DeliveriesOfPerson :many
SELECT * FROM deliveries WHERE person_id = ? ORDER BY created_at DESC, id DESC LIMIT ?;

-- Projects where nobody gets alerts.
-- name: ProjectsNobodyFollows :many
SELECT pr.id, pr.name FROM projects pr
WHERE pr.workspace_id = ? AND pr.deleted_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM people p
      LEFT JOIN memberships m ON m.project_id = pr.id AND m.person_id = p.id
      WHERE p.workspace_id = pr.workspace_id AND p.disabled_at IS NULL
        AND (m.alerts = 1 OR (m.person_id IS NULL AND p.workspace_admin = 1)))
ORDER BY pr.name;

-- A check's alerting changed: who has not been told about its open incident
-- is looked at again at once, under the new setting.
-- Due when the setting changed, not since the incident opened, which would
-- read as a sender behind.
-- name: LookAgainAtUntold :exec
UPDATE recipients SET due_at = sqlc.arg(due_at)
WHERE told = 'nothing'
  AND incident_id IN (SELECT incident_id FROM incident_checks WHERE check_id = sqlc.arg(check_id) AND open = 1);

-- The incidents a check was in that were open at some time since then, for
-- comparing with another service.
-- name: IncidentsOfCheck :many
SELECT ic.joined_at, i.recovering_since, i.resolved_at, i.resolve_reason
FROM incident_checks ic JOIN incidents i ON i.id = ic.incident_id
WHERE ic.check_id = ? AND (i.resolved_at IS NULL OR i.resolved_at >= ?)
ORDER BY ic.joined_at;

-- Switching alerts on: every incident open now that nobody was really told
-- about came about in shadow mode.
-- name: MarkOpenIncidentsShadow :execrows
UPDATE incidents SET shadow = 1
WHERE state <> 'resolved' AND shadow = 0
  AND NOT EXISTS (SELECT 1 FROM recipients r WHERE r.incident_id = incidents.id AND r.told <> 'nothing' AND r.told_shadow = 0);

-- name: QuietIncidents :one
SELECT COUNT(*) FROM incidents WHERE state <> 'resolved' AND shadow = 1;

-- name: RecentFailures :one
SELECT COUNT(*) FROM deliveries WHERE person_id = ? AND state = 'failed' AND last_error = ? AND created_at >= ?;

-- People who get alerts for at least one project: members whose alerts are
-- on, and workspace administrators.
-- name: PeopleWhoGetAlerts :many
SELECT p.id, p.name FROM people p
WHERE p.disabled_at IS NULL
  AND (EXISTS (SELECT 1 FROM memberships m WHERE m.person_id = p.id AND m.alerts = 1)
       OR (p.workspace_admin = 1 AND EXISTS (
           SELECT 1 FROM projects pr WHERE pr.workspace_id = p.workspace_id AND pr.deleted_at IS NULL
             AND NOT EXISTS (SELECT 1 FROM memberships m WHERE m.project_id = pr.id AND m.person_id = p.id))))
ORDER BY p.name;

-- When a check went down: when it joined each of its incidents, newest
-- first, from a time on, and when that incident resolved.
-- name: DownsOfCheck :many
SELECT ic.joined_at, i.resolved_at FROM incident_checks ic JOIN incidents i ON i.id = ic.incident_id
WHERE ic.check_id = ? AND ic.joined_at >= ? ORDER BY ic.joined_at DESC LIMIT ?;

-- name: OpenIncidentsOfProject :many
SELECT DISTINCT incident_id FROM incident_checks WHERE workspace_id = ? AND project_id = ? AND open = 1;

-- name: LookAgainAtUntoldPerson :exec
UPDATE recipients SET due_at = ? WHERE incident_id = ? AND person_id = ? AND told = 'nothing';
