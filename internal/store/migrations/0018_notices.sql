-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Teams, the second step (intent/teams/plan-2.md, part B): a command on the
-- server that reaches into a team tells the team's owners.

-- What an owner was told, and whether they have read it: until then it is at
-- the top of their overview. `details` holds the facts, as a log line does.
CREATE TABLE notices (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    team_id      TEXT NOT NULL,
    kind         TEXT NOT NULL,
    details      TEXT NOT NULL CHECK (json_valid(details)),
    created_at   INTEGER NOT NULL,
    read_at      INTEGER,
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, team_id) REFERENCES teams (workspace_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX notices_person ON notices (person_id, created_at);

-- A notice goes out as a message of its own kind. The kinds a delivery may
-- have were a constraint on the table, which cannot be changed, so the table
-- is made again and its rows copied as they are. The new one names no kinds:
-- the code that writes a delivery knows them, and the next needs no rebuild.
CREATE TABLE deliveries_new (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    channel_id   TEXT REFERENCES channels (id) ON DELETE SET NULL,
    kind         TEXT NOT NULL,
    items        TEXT NOT NULL CHECK (json_valid(items)),
    state        TEXT NOT NULL CHECK (state IN ('shadow', 'pending', 'sent', 'failed')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   INTEGER NOT NULL,
    sent_at      INTEGER,
    next_at      INTEGER,
    message_id   TEXT,
    channel_kind TEXT,
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT;

INSERT INTO deliveries_new (id, workspace_id, person_id, channel_id, kind, items, state, attempts, last_error,
    created_at, sent_at, next_at, message_id, channel_kind)
SELECT id, workspace_id, person_id, channel_id, kind, items, state, attempts, last_error,
    created_at, sent_at, next_at, message_id, channel_kind
FROM deliveries;

DROP TABLE deliveries;
ALTER TABLE deliveries_new RENAME TO deliveries;

CREATE INDEX deliveries_person ON deliveries (person_id, created_at);
CREATE INDEX deliveries_pending ON deliveries (next_at) WHERE state = 'pending';
CREATE INDEX deliveries_message ON deliveries (message_id);
CREATE INDEX deliveries_channel ON deliveries (channel_id);
