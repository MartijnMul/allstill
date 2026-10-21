-- SPDX-License-Identifier: AGPL-3.0-only

-- name: PersonByEmail :one
SELECT * FROM people
WHERE workspace_id = ? AND email = sqlc.arg(email) COLLATE NOCASE AND disabled_at IS NULL;

-- name: PeopleCount :one
SELECT count(*) FROM people WHERE workspace_id = ?;

-- name: DeleteUnusedLinks :exec
DELETE FROM passkey_links WHERE person_id = ? AND used_at IS NULL;

-- name: CreatePasskeyLink :exec
INSERT INTO passkey_links (token_hash, workspace_id, person_id, created_at, expires_at)
VALUES (?, ?, ?, ?, ?);

-- name: PasskeyLink :one
SELECT l.workspace_id, l.person_id, l.expires_at, l.used_at
FROM passkey_links l
JOIN people p ON p.workspace_id = l.workspace_id AND p.id = l.person_id
WHERE l.token_hash = ? AND p.disabled_at IS NULL;

-- A link is used once: of two finishes at the same time, one changes the row.
-- name: UseLink :execrows
UPDATE passkey_links SET used_at = sqlc.arg(at)
WHERE token_hash = sqlc.arg(token_hash) AND used_at IS NULL AND expires_at > sqlc.arg(at);

-- name: PasskeysOfPerson :many
SELECT * FROM passkeys WHERE workspace_id = ? AND person_id = ? ORDER BY created_at, id;

-- name: CreatePasskey :exec
INSERT INTO passkeys (id, workspace_id, person_id, credential_id, rp_id, name, record, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: UsePasskey :execrows
UPDATE passkeys SET record = ?, last_used_at = ?
WHERE workspace_id = ? AND person_id = ? AND id = ?;

-- name: PasskeysOfOtherHosts :one
SELECT count(*) FROM passkeys WHERE rp_id <> ?;

-- name: CreateSession :exec
INSERT INTO sessions (id, token_hash, workspace_id, person_id, passkey_id, user_agent, created_at, seen_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SessionByToken :one
SELECT s.id, s.workspace_id, s.person_id, s.created_at, s.seen_at, s.expires_at
FROM sessions s
JOIN people p ON p.workspace_id = s.workspace_id AND p.id = s.person_id
WHERE s.token_hash = ? AND p.disabled_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET seen_at = ?, expires_at = ? WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= ?;

-- name: DeleteExpiredLinks :exec
DELETE FROM passkey_links WHERE expires_at <= ?;
