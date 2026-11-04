-- SPDX-License-Identifier: AGPL-3.0-only

-- name: CreateAPIKey :exec
INSERT INTO api_keys (id, key_hash, workspace_id, project_id, name, scope, made_by_kind, made_by_id, made_by_name, made_by_outsider, made_via, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: APIKeyByHash :one
SELECT k.id, k.workspace_id, k.project_id, k.name, k.scope, k.used_at, p.name AS project_name
FROM api_keys k
JOIN projects p ON p.workspace_id = k.workspace_id AND p.id = k.project_id
WHERE k.key_hash = ? AND p.deleted_at IS NULL;

-- name: TouchAPIKey :exec
UPDATE api_keys SET used_at = ? WHERE id = ?;

-- name: APIKeysOfProject :many
SELECT id, project_id, name, scope, made_by_kind, made_by_id, made_by_name, made_by_outsider, made_via, created_at, used_at
FROM api_keys
WHERE workspace_id = ? AND project_id = ?
ORDER BY name, id;

-- name: APIKeyByID :one
SELECT id, project_id, name, scope FROM api_keys WHERE workspace_id = ? AND id = ?;

-- name: DeleteAPIKey :exec
DELETE FROM api_keys WHERE workspace_id = ? AND id = ?;
