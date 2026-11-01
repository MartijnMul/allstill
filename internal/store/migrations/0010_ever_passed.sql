-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A check Allstill runs opens no incident until it has passed once
-- (intent/manage-checks/spec.md, R35). That has to survive retention and a
-- resume, so it is kept on the check. Nothing that runs today may go quiet: a
-- check counts as having passed unless it is new and no run of it ever did.
ALTER TABLE checks ADD COLUMN ever_passed INTEGER NOT NULL DEFAULT 0;

UPDATE checks SET ever_passed = 1
WHERE state <> 'new'
   OR EXISTS (SELECT 1 FROM runs r WHERE r.check_id = checks.id AND r.outcome IN ('success', 'warning'));
