-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A job that pings a moment after its time goes late and back on every run, and those
-- lines stay out of every list until asked for (intent/activity-log/spec.md, R17a).
-- They get indexes of their own, so a check with hundreds of thousands of them reads as
-- quickly as any other, and looking up how often its state changed does not go through
-- them.
DROP INDEX log_entries_about;
CREATE INDEX log_entries_about ON log_entries (about_id, id) WHERE kind <> 'check.late';
CREATE INDEX log_entries_about_late ON log_entries (about_id, id) WHERE kind = 'check.late';

DROP INDEX log_entries_project;
CREATE INDEX log_entries_project ON log_entries (project_id, id) WHERE kind <> 'check.late';
CREATE INDEX log_entries_project_late ON log_entries (project_id, id) WHERE kind = 'check.late';

DROP INDEX log_entries_name;
CREATE INDEX log_entries_name ON log_entries (about_name COLLATE NOCASE, id) WHERE about_name <> '' AND kind <> 'check.late';
CREATE INDEX log_entries_name_late ON log_entries (about_name COLLATE NOCASE, id) WHERE about_name <> '' AND kind = 'check.late';
