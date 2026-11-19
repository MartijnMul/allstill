-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Teams, the second step (intent/teams/plan-2.md, part C): what one team may
-- take of the installation, so that a second team can be made.

-- How many checks a team may have: those Allstill runs itself, and those jobs
-- ping. Empty for no limit, which is what a team that exists keeps, so an
-- upgrade refuses nothing (spec, R34).
ALTER TABLE teams ADD COLUMN max_active_checks INTEGER CHECK (max_active_checks IS NULL OR max_active_checks >= 0);
ALTER TABLE teams ADD COLUMN max_ping_checks INTEGER CHECK (max_ping_checks IS NULL OR max_ping_checks >= 0);

-- The private ranges the server opened that this team's checks may reach:
-- empty for none, '*' for all of them. A new team gets none until an
-- administrator says so, and a team that exists keeps what it had (R13). Text,
-- so that naming ranges per team later needs no new table.
ALTER TABLE teams ADD COLUMN private_ranges TEXT NOT NULL DEFAULT '';
UPDATE teams SET private_ranges = '*';
