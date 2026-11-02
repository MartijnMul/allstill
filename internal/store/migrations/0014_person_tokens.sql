-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A person's sign-ins from the command line (intent/cli-anywhere/spec.md). Like
-- a browser session, a sign-in ends at `expires_at`, which moves forward while
-- it is used, or when the passkey it was approved with is removed. One that has
-- ended is gone: what happened to it is in the log. `name` is what the command
-- line said it is called, as given. Only the token's hash is kept.
CREATE TABLE person_tokens (
    id           TEXT PRIMARY KEY,
    token_hash   BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    workspace_id TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    passkey_id   TEXT NOT NULL,
    name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    scope        TEXT NOT NULL CHECK (scope IN ('read', 'manage')),
    created_at   INTEGER NOT NULL,
    used_at      INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL CHECK (expires_at > created_at),
    FOREIGN KEY (workspace_id, person_id, passkey_id)
        REFERENCES passkeys (workspace_id, person_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX person_tokens_passkey ON person_tokens (workspace_id, person_id, passkey_id);
CREATE INDEX person_tokens_expires ON person_tokens (expires_at);
