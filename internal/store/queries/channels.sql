-- SPDX-License-Identifier: AGPL-3.0-only

-- name: CreateVAPIDKey :exec
INSERT OR IGNORE INTO vapid_keys (workspace_id, private_key, public_key, created_at) VALUES (?, ?, ?, ?);

-- name: VAPIDKey :one
SELECT private_key FROM vapid_keys WHERE workspace_id = ?;

-- name: ChannelOfEndpoint :one
SELECT * FROM channels WHERE workspace_id = ? AND kind = 'webpush' AND json_extract(config, '$.endpoint') = sqlc.arg(endpoint);

-- name: CreateChannel :exec
INSERT INTO channels (id, workspace_id, person_id, kind, name, config, created_at) VALUES (?, ?, ?, ?, ?, ?, ?);

-- A device that subscribed again with other keys, or for another person,
-- must be confirmed again.
-- name: ReplaceChannel :exec
UPDATE channels SET person_id = ?, name = ?, config = ?, verified_at = NULL, failed_at = NULL WHERE id = ?;

-- name: ChannelsOfPerson :many
SELECT * FROM channels WHERE person_id = ? ORDER BY created_at, id;

-- name: ChannelOfID :one
SELECT * FROM channels WHERE id = ?;

-- name: ChannelOfPerson :one
SELECT * FROM channels WHERE id = ? AND person_id = ?;

-- name: DeleteChannel :execrows
DELETE FROM channels WHERE id = ? AND person_id = ?;

-- name: DeleteChannelByID :exec
DELETE FROM channels WHERE id = ?;

-- Only while the channel still holds the subscription the test went to.
-- name: SetChannelConfirm :exec
UPDATE channels SET config = json_set(config, '$.confirm', sqlc.arg(confirm))
WHERE id = sqlc.arg(id) AND person_id = sqlc.arg(person_id) AND json_extract(config, '$.auth') = sqlc.arg(auth);

-- name: ConfirmChannel :exec
UPDATE channels SET verified_at = ?, failed_at = NULL WHERE id = ?;

-- name: SetChannelFailed :exec
UPDATE channels SET failed_at = ? WHERE id = ?;

-- name: CreatePendingDelivery :exec
INSERT INTO deliveries (id, workspace_id, person_id, channel_id, kind, items, state, created_at, next_at)
VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?);

-- Deliveries whose time to be sent has come, with their channel: whose it
-- is now, and whether it is confirmed.
-- name: PendingDeliveries :many
SELECT d.id, d.person_id, d.kind, d.items, d.attempts, d.created_at, c.id AS channel_id, c.config,
       CAST(COALESCE(c.person_id, '') AS TEXT) AS channel_person, c.verified_at
FROM deliveries d JOIN channels c ON c.id = d.channel_id
WHERE d.state = 'pending' AND d.next_at <= ?
ORDER BY d.next_at LIMIT ?;

-- A pending delivery whose channel was removed cannot be sent.
-- name: FailOrphanDeliveries :exec
UPDATE deliveries SET state = 'failed', next_at = NULL, last_error = 'the channel was removed'
WHERE state = 'pending' AND channel_id IS NULL;

-- The incidents of a delivery that are still open and that the person still
-- gets alerts for, as DueRecipients works it out.
-- name: StillFollowed :many
SELECT i.id FROM incidents i
JOIN people p ON p.id = sqlc.arg(person_id)
WHERE i.id IN (sqlc.slice(ids)) AND i.state <> 'resolved' AND p.disabled_at IS NULL
  AND EXISTS (
      SELECT 1 FROM incident_checks ic
      LEFT JOIN checks c ON c.id = ic.check_id
      LEFT JOIN memberships m ON m.project_id = COALESCE(c.project_id, ic.project_id) AND m.person_id = p.id
      WHERE ic.incident_id = i.id
        AND (m.alerts = 1 OR (m.person_id IS NULL AND p.workspace_admin = 1)));

-- A down that went out to none of the person's devices was not heard: a
-- recovery must not follow it.
-- name: Unheard :exec
UPDATE recipients SET told = 'nothing', told_at = NULL, told_how = NULL
WHERE recipients.person_id = sqlc.arg(person_id) AND recipients.incident_id IN (sqlc.slice(ids)) AND recipients.told = 'down'
  AND NOT EXISTS (
      SELECT 1 FROM deliveries d, json_each(d.items) j
      WHERE d.person_id = recipients.person_id AND d.state = 'sent' AND d.kind IN ('alert', 'digest')
        AND json_extract(j.value, '$.incident_id') = recipients.incident_id AND json_extract(j.value, '$.what') = 'down');

-- What a delivery that fell in shadow mode told was heard in shadow mode
-- only, unless one of the person's devices got it for real.
-- name: HeardInShadow :exec
UPDATE recipients SET told_shadow = 1
WHERE recipients.person_id = sqlc.arg(person_id) AND recipients.incident_id IN (sqlc.slice(ids))
  AND NOT EXISTS (
      SELECT 1 FROM deliveries d, json_each(d.items) j
      WHERE d.person_id = recipients.person_id AND d.state = 'sent' AND d.kind IN ('alert', 'digest')
        AND json_extract(j.value, '$.incident_id') = recipients.incident_id
        AND json_extract(j.value, '$.what') = recipients.told);

-- name: FinishDelivery :exec
UPDATE deliveries SET state = ?, attempts = attempts + 1, last_error = ?, sent_at = ?, next_at = NULL WHERE id = ?;

-- name: RetryDelivery :exec
UPDATE deliveries SET attempts = attempts + 1, last_error = ?, next_at = ? WHERE id = ?;

-- The earliest moment the sender should already have acted on: a recipient
-- to look at, or a delivery to send. 0 when nothing waits.
-- name: OldestDue :one
SELECT CAST(COALESCE(MIN(t), 0) AS INTEGER) FROM (
    SELECT MIN(due_at) AS t FROM recipients WHERE due_at IS NOT NULL
    UNION ALL
    SELECT MIN(next_at) FROM deliveries WHERE state = 'pending'
);

-- A message left out because nothing in it was still true did not fail.
-- name: FailedSince :one
SELECT COUNT(*) FROM deliveries
WHERE person_id = ? AND state = 'failed' AND created_at >= ? AND COALESCE(last_error, '') <> sqlc.arg(not_failed);
