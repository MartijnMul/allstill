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

-- name: DeleteChannelByID :execrows
DELETE FROM channels WHERE id = ?;

-- Only while the channel still holds the subscription or address the test
-- went to.
-- name: SetChannelConfirm :exec
UPDATE channels SET config = json_set(config, '$.confirm', sqlc.arg(confirm))
WHERE id = sqlc.arg(id) AND person_id = sqlc.arg(person_id)
  AND COALESCE(json_extract(config, '$.auth'), json_extract(config, '$.address')) = sqlc.arg(holds);

-- name: EmailChannelsOfPerson :many
SELECT * FROM channels WHERE person_id = ? AND kind = 'email' ORDER BY created_at, id;

-- name: LastTestOfChannel :one
SELECT CAST(COALESCE(MAX(created_at), 0) AS INTEGER) FROM deliveries WHERE channel_id = ? AND kind = 'test';

-- Tests are counted per person, so removing and adding a channel does not
-- make more.
-- name: TestsOfPersonSince :one
SELECT COUNT(*) FROM deliveries WHERE person_id = ? AND kind = 'test' AND created_at >= ?;

-- name: ConfirmChannel :exec
UPDATE channels SET verified_at = ?, failed_at = NULL WHERE id = ?;

-- name: SetChannelFailed :exec
UPDATE channels SET failed_at = ? WHERE id = ?;

-- A device that took a push works again. An address that was refused keeps
-- its mark until it is confirmed: alerts no longer go to it.
-- name: ClearPushFailed :exec
UPDATE channels SET failed_at = NULL WHERE id = ? AND kind = 'webpush' AND failed_at IS NOT NULL;

-- name: CreatePendingDelivery :exec
INSERT INTO deliveries (id, workspace_id, person_id, channel_id, channel_kind, kind, items, state, created_at, next_at, message_id)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?);

-- Deliveries whose time to be sent has come, with their channel: whose it
-- is now, and whether it is confirmed. A delivery whose channel was removed
-- comes with none, to fail as any other and fall back.
-- name: PendingDeliveries :many
SELECT d.id, d.workspace_id, d.person_id, d.kind, d.items, d.attempts, d.created_at,
       CAST(COALESCE(d.message_id, d.id) AS TEXT) AS message_id,
       CAST(COALESCE(d.channel_id, '') AS TEXT) AS channel_id,
       CAST(COALESCE(d.channel_kind, c.kind, '') AS TEXT) AS channel_kind,
       CAST(COALESCE(c.config, '{}') AS TEXT) AS config,
       CAST(COALESCE(c.person_id, '') AS TEXT) AS channel_person, c.verified_at, c.failed_at
FROM deliveries d LEFT JOIN channels c ON c.id = d.channel_id
WHERE d.state = 'pending' AND d.next_at <= ?
ORDER BY d.next_at LIMIT ?;

-- The incidents whose down is still on its way to the person: a recovery
-- waits for it (R6).
-- name: PendingDowns :many
SELECT DISTINCT CAST(json_extract(j.value, '$.incident_id') AS TEXT) AS incident_id
FROM deliveries d, json_each(d.items) j
WHERE d.person_id = ? AND d.state = 'pending' AND json_extract(j.value, '$.what') = 'down';

-- The incidents of a delivery that are still open and that the person still
-- gets alerts for, as DueRecipients works it out.
-- name: StillFollowed :many
SELECT i.id FROM incidents i
JOIN people p ON p.id = sqlc.arg(person_id)
WHERE i.id IN (sqlc.slice(ids)) AND i.state <> 'resolved' AND p.disabled_at IS NULL
  AND EXISTS (
      SELECT 1 FROM incident_checks ic
      LEFT JOIN checks c ON c.id = ic.check_id
      JOIN project_alerted a ON a.project_id = COALESCE(c.project_id, ic.project_id) AND a.person_id = p.id
      WHERE ic.incident_id = i.id);

-- A down that went out to none of the person's devices was not heard: a
-- recovery must not follow it.
-- name: Unheard :exec
UPDATE recipients SET told = 'nothing', told_at = NULL, told_how = NULL, due_at = sqlc.arg(due_at)
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

-- The deliveries of one message: whether one is still on its way or went
-- out, and which kinds of channel it was tried on.
-- name: MessageDeliveries :many
SELECT state, CAST(COALESCE(channel_kind, '') AS TEXT) AS channel_kind FROM deliveries WHERE message_id = ?;

-- An email that cannot go this pass, as the mail server is out of reach,
-- waits without using a try.
-- name: PostponeDelivery :exec
UPDATE deliveries SET next_at = ? WHERE id = ?;

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

-- A message left out because nothing in it was still true did not fail, nor
-- did one that another delivery of it brought. Once an alert or a digest
-- reached the person after it, a way to them works again, and what failed
-- before is history. A test does not count: it also goes to a device that is
-- not confirmed, which alerts never go to.
-- name: FailedSince :one
SELECT COUNT(*) FROM deliveries d
WHERE d.person_id = sqlc.arg(person_id) AND d.state = 'failed' AND d.created_at >= sqlc.arg(since)
  AND COALESCE(d.last_error, '') <> sqlc.arg(not_failed)
  AND NOT EXISTS (SELECT 1 FROM deliveries s WHERE s.message_id = d.message_id AND s.state = 'sent')
  AND d.created_at >= COALESCE((SELECT MAX(s.sent_at) FROM deliveries s
    WHERE s.person_id = sqlc.arg(person_id) AND s.created_at >= sqlc.arg(since) AND s.state = 'sent' AND s.kind <> 'test'), 0);
