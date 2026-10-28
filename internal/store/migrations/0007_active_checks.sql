-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Checks Allstill runs itself: HTTP and TCP checks (intent/http-checks/spec.md).
--
-- `checks` is rebuilt, as SQLite changes a constraint only that way: its
-- `kind` is no longer limited here but checked by the code that writes it,
-- as the settings in JSON are, so a new kind needs no rebuild again. It gains
-- `fail_streak`, an active check's failed attempts in a row. Foreign keys are
-- off while this runs, and are checked before it commits.

CREATE TABLE checks_new (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL,
    project_id       TEXT NOT NULL,
    ping_uuid        TEXT NOT NULL UNIQUE CHECK (
                         length(ping_uuid) = 36
                         AND ping_uuid NOT GLOB '*[^0-9a-f-]*'
                         AND length(replace(ping_uuid, '-', '')) = 32
                         AND substr(ping_uuid, 9, 1) = '-' AND substr(ping_uuid, 14, 1) = '-'
                         AND substr(ping_uuid, 19, 1) = '-' AND substr(ping_uuid, 24, 1) = '-'
                     ),
    name             TEXT NOT NULL,
    slug             TEXT CHECK (
                         slug IS NULL OR (
                             length(slug) BETWEEN 1 AND 100
                             AND slug GLOB '[a-z0-9]*'
                             AND slug NOT GLOB '*[^a-z0-9_-]*'
                             AND slug NOT IN ('start', 'fail', 'log')
                         )
                     ),
    description      TEXT NOT NULL DEFAULT '',
    tags             TEXT NOT NULL DEFAULT '[]',
    kind             TEXT NOT NULL,
    schedule_kind    TEXT NOT NULL CHECK (schedule_kind IN ('interval', 'cron')),
    interval_seconds INTEGER,
    cron_expression  TEXT,
    time_zone        TEXT,
    grace_seconds    INTEGER NOT NULL CHECK (grace_seconds >= 60),
    settings         TEXT NOT NULL DEFAULT '{}',
    state            TEXT NOT NULL DEFAULT 'new'
                     CHECK (state IN ('new', 'up', 'warning', 'late', 'down', 'paused', 'unknown')),
    state_since      INTEGER NOT NULL,
    state_reason     TEXT NOT NULL DEFAULT '',
    last_ping_at     INTEGER,
    next_expected_at INTEGER,
    deadline         INTEGER,
    version          INTEGER NOT NULL DEFAULT 1,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    deleted_at       INTEGER,
    urgency          TEXT NOT NULL DEFAULT 'now' CHECK (urgency IN ('now', 'digest', 'none')),
    critical         INTEGER NOT NULL DEFAULT 0 CHECK (critical IN (0, 1)),
    fail_streak      INTEGER NOT NULL DEFAULT 0 CHECK (fail_streak >= 0),
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, project_id) REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    CHECK (
        (schedule_kind = 'interval' AND interval_seconds IS NOT NULL AND interval_seconds >= 60
            AND cron_expression IS NULL)
        OR (schedule_kind = 'cron' AND interval_seconds IS NULL
            AND cron_expression IS NOT NULL AND cron_expression <> ''
            AND time_zone IS NOT NULL AND time_zone <> '')
    )
) STRICT;

INSERT INTO checks_new (
    id, workspace_id, project_id, ping_uuid, name, slug, description, tags, kind,
    schedule_kind, interval_seconds, cron_expression, time_zone, grace_seconds, settings,
    state, state_since, state_reason, last_ping_at, next_expected_at, deadline, version,
    created_at, updated_at, deleted_at, urgency, critical
)
SELECT
    id, workspace_id, project_id, ping_uuid, name, slug, description, tags, kind,
    schedule_kind, interval_seconds, cron_expression, time_zone, grace_seconds, settings,
    state, state_since, state_reason, last_ping_at, next_expected_at, deadline, version,
    created_at, updated_at, deleted_at, urgency, critical
FROM checks;

DROP TABLE checks;
ALTER TABLE checks_new RENAME TO checks;

CREATE UNIQUE INDEX checks_slug ON checks (project_id, slug) WHERE slug IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX checks_project ON checks (project_id);
CREATE INDEX checks_deadline ON checks (deadline) WHERE deadline IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX checks_deleted ON checks (deleted_at) WHERE deleted_at IS NOT NULL;

-- Why an attempt of an active check failed, in plain words, without its URL.
ALTER TABLE runs ADD COLUMN reason TEXT NOT NULL DEFAULT '';
