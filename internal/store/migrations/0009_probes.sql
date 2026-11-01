-- SPDX-License-Identifier: AGPL-3.0-only
--
-- A probe runs checks inside a network the server cannot reach
-- (intent/http-checks/spec.md). It belongs to the workspace, serves the projects
-- an administrator gives it, and has a check of its own, which every post pings.
-- A check is run by the server, or by one probe.
CREATE TABLE probes (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id),
    name         TEXT NOT NULL,
    key_hash     BLOB NOT NULL UNIQUE,
    check_id     TEXT NOT NULL REFERENCES checks (id),
    created_at   INTEGER NOT NULL,
    UNIQUE (workspace_id, name)
);

CREATE TABLE probe_projects (
    workspace_id TEXT NOT NULL REFERENCES workspaces (id),
    probe_id   TEXT NOT NULL REFERENCES probes (id),
    project_id TEXT NOT NULL REFERENCES projects (id),
    PRIMARY KEY (probe_id, project_id)
) WITHOUT ROWID;

-- The runs a probe has reported, so a batch sent twice counts once. Kept for
-- two days: a run older than a day is not applied.
CREATE TABLE probe_runs_seen (
    workspace_id TEXT NOT NULL REFERENCES workspaces (id),
    probe_id TEXT NOT NULL REFERENCES probes (id),
    run_id   TEXT NOT NULL,
    seen_at  INTEGER NOT NULL,
    PRIMARY KEY (probe_id, run_id)
) WITHOUT ROWID;
CREATE INDEX probe_runs_seen_at ON probe_runs_seen (seen_at);

ALTER TABLE checks ADD COLUMN probe_id TEXT REFERENCES probes (id);
CREATE INDEX checks_probe ON checks (probe_id) WHERE probe_id IS NOT NULL;
