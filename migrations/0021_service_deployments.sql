-- A service's deployments, one row each, as an app's are in deployments.
-- Until now a service kept only how its last deployment ended (status,
-- last_error) and one log file that the next deployment wrote over.
--
-- A table of its own: deployments.app_id refers to apps, and what an app's
-- deployment has (an image, a kept image, a rollback) a stack's has not.
--
-- step is the part of the deployment that is under way, or the one it
-- failed in: 'clone', 'read', 'images', 'start', and '' before the first.
-- A page shows a deployment's process from it without reading the log.
--
-- Nothing is filled in for deployments made before this: when they were
-- and how long they took is not known.
CREATE TABLE service_deployments (
    id          TEXT PRIMARY KEY,
    service_id  TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'success', 'failed')),
    trigger     TEXT NOT NULL DEFAULT '',
    step        TEXT NOT NULL DEFAULT '',
    commit_sha  TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    started_at  INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX service_deployments_service ON service_deployments(service_id, created_at);
