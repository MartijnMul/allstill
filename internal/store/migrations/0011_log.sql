-- SPDX-License-Identifier: AGPL-3.0-only
--
-- The log: what happened, when and by what (intent/activity-log/spec.md). A row
-- is written in the transaction that makes its thing true, so nothing here may
-- refuse one: no foreign keys, no checks, and every column has a value Go can
-- always give. It keeps its own copies of names, so it outlives what it mentions.
-- It holds facts, not sentences: the page writes the sentence, per reader.
CREATE TABLE log_entries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id    TEXT NOT NULL DEFAULT '',
    -- When it happened, and when Allstill learned of it: a probe's run can be
    -- hours old. Entries are listed in the order they were recorded.
    happened_at     INTEGER NOT NULL,
    recorded_at     INTEGER NOT NULL,
    -- Empty for what concerns the whole installation.
    project_id      TEXT NOT NULL DEFAULT '',
    project_name    TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL,
    about_kind      TEXT NOT NULL DEFAULT '',
    about_id        TEXT NOT NULL DEFAULT '',
    about_name      TEXT NOT NULL DEFAULT '',
    -- person, key, probe, job, command or allstill. `by_outsider` says it was an
    -- administrator with no membership of the project then, which `memberships`,
    -- keeping no history, cannot say later.
    by_kind         TEXT NOT NULL,
    by_id           TEXT NOT NULL DEFAULT '',
    by_name         TEXT NOT NULL DEFAULT '',
    by_outsider     INTEGER NOT NULL DEFAULT 0,
    -- The person it concerns, such as who was reached or whose role changed.
    person_id       TEXT NOT NULL DEFAULT '',
    person_name     TEXT NOT NULL DEFAULT '',
    person_outsider INTEGER NOT NULL DEFAULT 0,
    incident_id     TEXT NOT NULL DEFAULT '',
    -- Left out of the default list: going into `unknown`, say.
    hidden          INTEGER NOT NULL DEFAULT 0,
    -- Ties a line that stands for several to what it opens.
    group_id        TEXT NOT NULL DEFAULT '',
    -- JSON, written only from named fields, never by copying settings.
    details         TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX log_entries_project ON log_entries (project_id, id);
CREATE INDEX log_entries_about ON log_entries (about_id, id);
CREATE INDEX log_entries_name ON log_entries (about_name, id) WHERE about_name <> '';
CREATE INDEX log_entries_kind ON log_entries (kind, id);
CREATE INDEX log_entries_by ON log_entries (by_id, id);
CREATE INDEX log_entries_person ON log_entries (person_id, id);
CREATE INDEX log_entries_incident ON log_entries (incident_id, id) WHERE incident_id <> '';
CREATE INDEX log_entries_group ON log_entries (group_id, id) WHERE group_id <> '';
CREATE INDEX log_entries_recorded ON log_entries (recorded_at);
-- Whether a check is changing too often, looked up on each change of its state.
CREATE INDEX log_entries_flapping ON log_entries (about_id, id) WHERE kind IN ('check.flapping', 'check.steady');

-- Keeps a mistake in the code from rewriting history. Only the age limit removes
-- entries, which is why this is not on DELETE.
CREATE TRIGGER log_entries_stay BEFORE UPDATE ON log_entries
BEGIN
    SELECT RAISE(ABORT, 'a log entry is not changed');
END;

-- Where the log starts, as the oldest row cannot say once old rows are removed.
INSERT INTO installation (key, value) VALUES ('log_started_at', CAST(CAST(unixepoch('subsec') * 1000 AS INTEGER) AS TEXT));
