-- SPDX-License-Identifier: AGPL-3.0-only
--
-- An incident open when alerts were switched on came about in shadow mode
-- (R10): nobody is alerted for it, and the digest lists it instead.
ALTER TABLE incidents ADD COLUMN shadow INTEGER NOT NULL DEFAULT 0 CHECK (shadow IN (0, 1));
