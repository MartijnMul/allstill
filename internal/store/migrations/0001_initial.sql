-- SPDX-License-Identifier: AGPL-3.0-only
--
-- The first data model: the workspace and its people, projects with their
-- members, checks, runs, pings, and the event log that the live stream replays.
-- Login, keys, calendars, dependencies, rules, incidents and maintenance
-- windows come in their own migrations, when their features are built.
--
-- Conventions: ids are opaque text with a prefix (`chk_...`), time ordered;
-- times are Unix milliseconds in UTC; booleans are 0 or 1; lists and nested
-- settings are JSON text, validated by the code that writes them.
--
-- Every table carries `workspace_id`, although an installation has one
-- workspace (ADR 0001). Composite foreign keys make a row's workspace that of
-- its parent, so it cannot drift.
--
-- Checks and projects are deleted in two steps: `deleted_at` hides them at
-- once, and their runs and pings are removed afterwards in small batches, so a
-- large delete never holds the write lock that ping ingest needs.

CREATE TABLE workspaces (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- A person: who signs in, follows projects and is alerted. An email address is
-- where email alerts and the digest go; it is not a login.
CREATE TABLE people (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id),
    name            TEXT NOT NULL,
    email           TEXT,
    workspace_admin INTEGER NOT NULL DEFAULT 0 CHECK (workspace_admin IN (0, 1)),
    time_zone       TEXT NOT NULL DEFAULT 'UTC',
    created_at      INTEGER NOT NULL,
    disabled_at     INTEGER,
    UNIQUE (workspace_id, id)
) STRICT;

CREATE UNIQUE INDEX people_email ON people (workspace_id, email COLLATE NOCASE) WHERE email IS NOT NULL;

-- The unit of access. The ping key, for pings by slug, is stored as its SHA-256.
-- The default interval and grace are what `create=1` gives a new check.
CREATE TABLE projects (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces (id),
    name              TEXT NOT NULL,
    ping_key_hash     BLOB UNIQUE CHECK (ping_key_hash IS NULL OR length(ping_key_hash) = 32),
    recovery_messages INTEGER NOT NULL DEFAULT 1 CHECK (recovery_messages IN (0, 1)),
    default_interval  INTEGER NOT NULL DEFAULT 86400 CHECK (default_interval >= 60),
    default_grace     INTEGER NOT NULL DEFAULT 3600 CHECK (default_grace >= 60),
    created_at        INTEGER NOT NULL,
    deleted_at        INTEGER,
    UNIQUE (workspace_id, id)
) STRICT;

CREATE TABLE memberships (
    workspace_id TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    person_id    TEXT NOT NULL,
    role         TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'owner')),
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (project_id, person_id),
    FOREIGN KEY (workspace_id, project_id) REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, person_id) REFERENCES people (workspace_id, id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE INDEX memberships_person ON memberships (person_id);

-- What is watched. The schedule is an interval or a cron expression in a time
-- zone; `settings` holds what later features add (values, rules, the HTTP or
-- certificate target, settings carried over from Healthchecks) as JSON in the
-- API's shape.
--
-- `state` is what the last ping or deadline decided. `unknown` means Allstill
-- itself was down when a deadline passed. Whether a check is outside its
-- calendar or in a maintenance window is worked out when it is read.
-- `deadline` is when the scheduler must look at the check next: when it becomes
-- late, when it goes down, when a started run times out. One timer waits for
-- the earliest. `version` changes with every change of settings, for ETags.
CREATE TABLE checks (
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
    kind             TEXT NOT NULL CHECK (kind IN ('ping', 'http', 'certificate')),
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

CREATE UNIQUE INDEX checks_slug ON checks (project_id, slug) WHERE slug IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX checks_project ON checks (project_id);
CREATE INDEX checks_deadline ON checks (deadline) WHERE deadline IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX checks_deleted ON checks (deleted_at) WHERE deleted_at IS NOT NULL;

-- One run of a job, from its start ping (if any) to its end. `rid` is the run
-- id the job chose, to pair a start with its end when runs overlap.
--
-- A run that does not end in time gets `timed_out_at` and the outcome
-- `timeout`, but keeps waiting for its end: a backup that finishes late still
-- finds its start, and its real run time is recorded.
CREATE TABLE runs (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    check_id     TEXT NOT NULL,
    rid          TEXT,
    outcome      TEXT NOT NULL CHECK (outcome IN ('running', 'success', 'warning', 'fail', 'timeout')),
    started_at   INTEGER,
    ended_at     INTEGER,
    timed_out_at INTEGER,
    exit_status  INTEGER CHECK (exit_status BETWEEN 0 AND 255),
    measured     TEXT NOT NULL DEFAULT '{}',
    FOREIGN KEY (workspace_id, check_id) REFERENCES checks (workspace_id, id) ON DELETE CASCADE,
    CHECK (started_at IS NOT NULL OR ended_at IS NOT NULL),
    CHECK (outcome <> 'running' OR (ended_at IS NULL AND timed_out_at IS NULL)),
    CHECK (outcome IN ('running', 'timeout') OR ended_at IS NOT NULL),
    CHECK (outcome <> 'timeout' OR timed_out_at IS NOT NULL),
    CHECK (started_at IS NULL OR ended_at IS NULL OR ended_at >= started_at),
    CHECK (started_at IS NULL OR timed_out_at IS NULL OR timed_out_at >= started_at)
) STRICT;

CREATE INDEX runs_check ON runs (check_id, id);
CREATE INDEX runs_rid ON runs (check_id, rid) WHERE rid IS NOT NULL;
CREATE INDEX runs_open ON runs (check_id) WHERE ended_at IS NULL;

-- Every ping as received; there are many, so the row is kept small and its
-- key is an integer. A run's log is the bodies of its pings, in order. The
-- body comes last, so reading the other columns never touches its pages.
-- `ignored` is a ping that was received but did not count, such as a GET to a
-- check that only accepts POST.
CREATE TABLE pings (
    id             INTEGER PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    check_id       TEXT NOT NULL,
    run_id         TEXT REFERENCES runs (id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('start', 'success', 'fail', 'log', 'ignored')),
    exit_status    INTEGER CHECK (exit_status BETWEEN 0 AND 255),
    received_at    INTEGER NOT NULL,
    method         TEXT NOT NULL,
    remote_addr    TEXT NOT NULL DEFAULT '',
    user_agent     TEXT NOT NULL DEFAULT '',
    body_truncated INTEGER NOT NULL DEFAULT 0 CHECK (body_truncated IN (0, 1)),
    body           BLOB,
    FOREIGN KEY (workspace_id, check_id) REFERENCES checks (workspace_id, id) ON DELETE CASCADE,
    CHECK (exit_status IS NULL OR kind IN ('success', 'fail'))
) STRICT;

CREATE INDEX pings_check ON pings (check_id, received_at);
CREATE INDEX pings_run ON pings (run_id) WHERE run_id IS NOT NULL;

-- The live stream's log. An event names what changed, not its content: on
-- replay the current state is rendered for each caller, as they may see it.
-- The id is the stream position, `Last-Event-ID`; AUTOINCREMENT keeps it from
-- being reused after old events are pruned.
CREATE TABLE events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id),
    project_id   TEXT,
    type         TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX events_created ON events (created_at);

-- Facts about the installation itself, such as when the server last ran, so a
-- restart after downtime marks checks unknown instead of down.
CREATE TABLE installation (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;
