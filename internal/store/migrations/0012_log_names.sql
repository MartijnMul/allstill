-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Reading the log (intent/activity-log/spec.md). A deleted check is found by its name,
-- however it is typed (R28).
DROP INDEX log_entries_name;
CREATE INDEX log_entries_name ON log_entries (about_name COLLATE NOCASE, id) WHERE about_name <> '';

-- What concerns a person outside any project, read by everyone on every page of the
-- log, without going through what concerns them elsewhere.
CREATE INDEX log_entries_own ON log_entries (person_id, id) WHERE project_id = '';
