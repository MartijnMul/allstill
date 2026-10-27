-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Web Push (ADR 0003). The key that signs every push, and each device's
-- subscription as a `webpush` channel, are kept as they are: Allstill must
-- read them back, so they cannot be hashed. The database and its copies are
-- therefore secrets.

-- A workspace's VAPID key pair: `private_key` is the 32-octet P-256 scalar,
-- `public_key` the uncompressed point in base64url, the key browsers
-- subscribe for. Allstill makes it once and never replaces it on its own.
CREATE TABLE vapid_keys (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces (id),
    private_key  BLOB NOT NULL CHECK (length(private_key) = 32),
    public_key   TEXT NOT NULL CHECK (length(public_key) = 87),
    created_at   INTEGER NOT NULL
) STRICT;

-- One channel per push endpoint in a workspace: a browser that subscribes
-- again updates its channel, and a device that changes hands moves.
CREATE UNIQUE INDEX channels_endpoint ON channels (workspace_id, json_extract(config, '$.endpoint')) WHERE kind = 'webpush';

-- When a delivery that failed for now is tried again.
ALTER TABLE deliveries ADD COLUMN next_at INTEGER;
DROP INDEX deliveries_pending;
CREATE INDEX deliveries_pending ON deliveries (next_at) WHERE state = 'pending';
