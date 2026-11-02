-- SPDX-License-Identifier: AGPL-3.0-only

-- name: CreatePersonToken :exec
INSERT INTO person_tokens (id, token_hash, workspace_id, person_id, passkey_id, name, scope, created_at, used_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: PersonTokenByHash :one
SELECT t.id, t.workspace_id, t.person_id, t.name, t.scope, t.created_at, t.used_at, t.expires_at
FROM person_tokens t
JOIN people p ON p.workspace_id = t.workspace_id AND p.id = t.person_id
WHERE t.token_hash = ? AND p.disabled_at IS NULL;

-- name: TouchPersonToken :exec
UPDATE person_tokens SET used_at = ?, expires_at = ? WHERE id = ?;

-- name: PersonTokensOfPerson :many
SELECT id, name, scope, created_at, used_at, expires_at
FROM person_tokens
WHERE workspace_id = sqlc.arg(workspace_id) AND person_id = sqlc.arg(person_id) AND expires_at > sqlc.arg(at)
ORDER BY created_at DESC, id DESC;

-- name: PersonTokensOfPasskey :many
SELECT id, name, scope FROM person_tokens
WHERE workspace_id = sqlc.arg(workspace_id) AND person_id = sqlc.arg(person_id) AND passkey_id = sqlc.arg(passkey_id) AND expires_at > sqlc.arg(at);

-- name: PersonTokenOfPerson :one
SELECT id, name, scope FROM person_tokens WHERE workspace_id = ? AND person_id = ? AND id = ?;

-- name: DeletePersonToken :execrows
DELETE FROM person_tokens WHERE id = ?;

-- name: DeleteExpiredPersonTokens :exec
DELETE FROM person_tokens WHERE expires_at <= ?;

-- name: SessionsOfPerson :many
SELECT id, user_agent, created_at, seen_at, expires_at
FROM sessions
WHERE workspace_id = sqlc.arg(workspace_id) AND person_id = sqlc.arg(person_id) AND expires_at > sqlc.arg(at)
ORDER BY seen_at DESC, id DESC;

-- name: SessionIDsOfPasskey :many
SELECT id FROM sessions
WHERE workspace_id = sqlc.arg(workspace_id) AND person_id = sqlc.arg(person_id) AND passkey_id = sqlc.arg(passkey_id) AND expires_at > sqlc.arg(at);

-- name: DeleteSessionOfPerson :execrows
DELETE FROM sessions WHERE workspace_id = ? AND person_id = ? AND id = ?;

-- name: PasskeyExists :one
SELECT count(*) FROM passkeys WHERE workspace_id = ? AND person_id = ? AND id = ?;

-- name: DeletePasskey :execrows
DELETE FROM passkeys WHERE workspace_id = ? AND person_id = ? AND id = ?;
