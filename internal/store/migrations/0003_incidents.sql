-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Incidents and what each person is told about them (intent/alerts/spec.md).
-- A check going down opens an incident; for each person who gets alerts for
-- its project, a recipient row keeps what that person has been told. A sender
-- compares that with what is true when it falls due, and records each message
-- as a delivery: sent, failed, or in shadow mode only recorded.

-- How fast a problem with a check reaches people, as the API's Alerting:
-- `now` alerts phones, `digest` waits for the digest, `none` never alerts.
-- A critical check breaks through quiet hours.
ALTER TABLE checks ADD COLUMN urgency TEXT NOT NULL DEFAULT 'now' CHECK (urgency IN ('now', 'digest', 'none'));
ALTER TABLE checks ADD COLUMN critical INTEGER NOT NULL DEFAULT 0 CHECK (critical IN (0, 1));

-- Whether a member gets alerts for the project: on for owners and editors.
ALTER TABLE memberships ADD COLUMN alerts INTEGER NOT NULL DEFAULT 1 CHECK (alerts IN (0, 1));
UPDATE memberships SET alerts = 0 WHERE role = 'viewer';

-- A person's quiet hours, in the API's QuietHours shape (JSON), and when
-- their digest goes out, as HH:MM in their time zone. New people get quiet
-- hours from 22:00 to 07:00; so do the people already here.
ALTER TABLE people ADD COLUMN quiet_hours TEXT CHECK (quiet_hours IS NULL OR json_valid(quiet_hours));
ALTER TABLE people ADD COLUMN digest_time TEXT NOT NULL DEFAULT '08:00'
    CHECK (digest_time GLOB '[0-2][0-9]:[0-5][0-9]' AND CAST(substr(digest_time, 1, 2) AS INTEGER) <= 23);
-- When the sender last looked whether a digest was due for the person. The
-- people already here start now, so their first digest is the next 08:00.
ALTER TABLE people ADD COLUMN digest_at INTEGER;
UPDATE people SET digest_at = CAST(unixepoch('subsec') * 1000 AS INTEGER);
UPDATE people SET quiet_hours = json_object('time_zone', time_zone, 'periods', json_array(
    json_object('days', json_array('mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'), 'start', '22:00', 'end', '07:00')));

-- One problem. It belongs to the workspace, so dependencies can later join
-- checks of several projects. While its checks are back up but not yet for
-- the settle period, `recovering_since` is set and it stays open.
CREATE TABLE incidents (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces (id),
    title            TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    state            TEXT NOT NULL CHECK (state IN ('open', 'acknowledged', 'resolved')),
    urgency          TEXT NOT NULL CHECK (urgency IN ('now', 'digest', 'none')),
    critical         INTEGER NOT NULL DEFAULT 0 CHECK (critical IN (0, 1)),
    cause_check_id   TEXT,
    opened_at        INTEGER NOT NULL,
    recovering_since INTEGER,
    acknowledged_at  INTEGER,
    acknowledged_by  TEXT,
    resolved_at      INTEGER,
    resolve_reason   TEXT CHECK (resolve_reason IN ('recovered', 'paused', 'deleted')),
    merged_into      TEXT REFERENCES incidents (id),
    UNIQUE (workspace_id, id),
    CHECK ((state = 'resolved') = (resolved_at IS NOT NULL AND resolve_reason IS NOT NULL)),
    CHECK (state <> 'resolved' OR recovering_since IS NULL),
    CHECK ((acknowledged_at IS NULL) = (acknowledged_by IS NULL))
) STRICT;

CREATE INDEX incidents_recovering ON incidents (recovering_since) WHERE recovering_since IS NOT NULL;
CREATE INDEX incidents_resolved ON incidents (workspace_id, resolved_at) WHERE resolved_at IS NOT NULL;

-- The checks of an incident. The name and project are copied and there is no
-- foreign key to checks, so the history outlives a deleted check. `open`
-- mirrors whether the incident is open, so a check is in one open incident
-- at most.
CREATE TABLE incident_checks (
    workspace_id TEXT NOT NULL,
    incident_id  TEXT NOT NULL,
    check_id     TEXT NOT NULL,
    check_name   TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    joined_at    INTEGER NOT NULL,
    left_at      INTEGER,
    open         INTEGER NOT NULL DEFAULT 1 CHECK (open IN (0, 1)),
    PRIMARY KEY (incident_id, check_id),
    FOREIGN KEY (workspace_id, incident_id) REFERENCES incidents (workspace_id, id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX incident_checks_open ON incident_checks (check_id) WHERE open = 1;
CREATE INDEX incident_checks_project ON incident_checks (project_id);

-- Where a person is reached: their phone's browser, their email address.
-- Shared channels, such as a team chat, come later with `person_id` empty.
-- A channel is usable once a test message reached it.
CREATE TABLE channels (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    person_id    TEXT,
    kind         TEXT NOT NULL CHECK (kind IN ('webpush', 'email')),
    name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    config       TEXT NOT NULL CHECK (json_valid(config) AND length(config) <= 4096),
    verified_at  INTEGER,
    failed_at    INTEGER,
    created_at   INTEGER NOT NULL,
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX channels_person ON channels (person_id);

-- What one recipient has been told about one incident: nothing, down or
-- recovered, when, how (at once or in the digest), and whether for real or
-- only in shadow mode. `due_at` is
-- when the sender must look again; empty when there is nothing to do.
CREATE TABLE recipients (
    workspace_id TEXT NOT NULL,
    incident_id  TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    told         TEXT NOT NULL DEFAULT 'nothing' CHECK (told IN ('nothing', 'down', 'recovered')),
    told_at      INTEGER,
    told_how     TEXT CHECK (told_how IN ('now', 'digest')),
    told_shadow  INTEGER NOT NULL DEFAULT 0 CHECK (told_shadow IN (0, 1)),
    due_at       INTEGER,
    PRIMARY KEY (incident_id, person_id),
    FOREIGN KEY (workspace_id, incident_id) REFERENCES incidents (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE INDEX recipients_due ON recipients (due_at) WHERE due_at IS NOT NULL;
CREATE INDEX recipients_person ON recipients (person_id);

-- One message to one person: what it reports (`items`, JSON, fixed so a
-- retry sends the same) and what became of it. A delivery in shadow mode is
-- recorded and never sent.
CREATE TABLE deliveries (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    channel_id   TEXT REFERENCES channels (id) ON DELETE SET NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('alert', 'digest', 'test')),
    items        TEXT NOT NULL CHECK (json_valid(items)),
    state        TEXT NOT NULL CHECK (state IN ('shadow', 'pending', 'sent', 'failed')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   INTEGER NOT NULL,
    sent_at      INTEGER,
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX deliveries_person ON deliveries (person_id, created_at);
CREATE INDEX deliveries_pending ON deliveries (created_at) WHERE state = 'pending';
