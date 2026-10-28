-- SPDX-License-Identifier: AGPL-3.0-only
--
-- The overview marks where each check went down between its runs: when the
-- check joined an incident. It looks that up per check, on every load.
CREATE INDEX incident_checks_check ON incident_checks (check_id, joined_at);
