-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Signing in: a person's passkeys, their browser sessions, and the one-time
-- links that let them add a passkey. People have no password. Secrets that are
-- handed out (a session's cookie, a link's token) are stored as their SHA-256.

-- A passkey. `credential_id` is the id its authenticator chose; `rp_id` is the
-- host name it was made for, so a change of host name can be told apart from
-- a wrong key. `record` is the WebAuthn credential record as the library keeps
-- it (public key, sign count, backup flags, transports); it is rewritten after
-- every sign-in, as the spec asks. The person's WebAuthn user handle is their
-- person id.
CREATE TABLE passkeys (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    person_id     TEXT NOT NULL,
    credential_id BLOB NOT NULL UNIQUE CHECK (length(credential_id) BETWEEN 1 AND 1023),
    rp_id         TEXT NOT NULL,
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    record        TEXT NOT NULL CHECK (json_valid(record) AND json_type(record) = 'object' AND length(record) <= 16384),
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER,
    UNIQUE (workspace_id, person_id, id),
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT;

-- A signed-in browser. The cookie holds a random token; only its hash is kept.
-- A session ends at `expires_at`, which moves forward while it is used, when
-- the person signs out, or when the passkey it was opened with is removed.
CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    token_hash   BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    workspace_id TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    passkey_id   TEXT NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT '' CHECK (length(user_agent) <= 512),
    created_at   INTEGER NOT NULL,
    seen_at      INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL CHECK (expires_at > created_at),
    FOREIGN KEY (workspace_id, person_id, passkey_id)
        REFERENCES passkeys (workspace_id, person_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX sessions_passkey ON sessions (workspace_id, person_id, passkey_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- A one-time link to add a passkey: printed by `allstill admin create-user`
-- for a new person, or made again for someone who lost their devices. A used
-- link is kept until it expires, so it can say it was used.
CREATE TABLE passkey_links (
    token_hash   BLOB PRIMARY KEY CHECK (length(token_hash) = 32),
    workspace_id TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL CHECK (expires_at > created_at),
    used_at      INTEGER CHECK (used_at IS NULL OR used_at >= created_at),
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE INDEX passkey_links_person ON passkey_links (person_id);
CREATE INDEX passkey_links_expires ON passkey_links (expires_at);
