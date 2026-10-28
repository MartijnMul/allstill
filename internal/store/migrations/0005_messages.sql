-- SPDX-License-Identifier: AGPL-3.0-only
--
-- One message to a person can take several deliveries: one to each of their
-- devices, and one by email when no device took it (intent/alerts/plan.md,
-- step 4). They share a message id, so Allstill can tell when none of them
-- arrived, and each keeps the kind of channel it went to, which outlives a
-- removed channel. Deliveries made before stand alone.
ALTER TABLE deliveries ADD COLUMN message_id TEXT;
ALTER TABLE deliveries ADD COLUMN channel_kind TEXT;
UPDATE deliveries SET message_id = id;
UPDATE deliveries SET channel_kind = (SELECT kind FROM channels WHERE channels.id = deliveries.channel_id);

CREATE INDEX deliveries_message ON deliveries (message_id);
CREATE INDEX deliveries_channel ON deliveries (channel_id);
