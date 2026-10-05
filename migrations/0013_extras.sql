-- Whether a server is sampled: once a minute, what it and its containers
-- use is stored. Off unless somebody switches it on.
ALTER TABLE servers ADD COLUMN metrics INTEGER NOT NULL DEFAULT 0;

-- The samples, a day's worth: older rows are deleted as new ones arrive.
-- resource_id is '' for the server itself, otherwise the app, database or
-- service whose containers were measured (their sum).
--
-- cpu is in hundredths of a percent: of the whole machine for a server, of
-- one core for a resource. load is the one-minute load average times a
-- hundred. The rest are bytes. The last three are the server's only.
CREATE TABLE metric_samples (
    server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL DEFAULT '',
    at          INTEGER NOT NULL,
    cpu         INTEGER NOT NULL DEFAULT 0,
    mem         INTEGER NOT NULL DEFAULT 0,
    mem_total   INTEGER NOT NULL DEFAULT 0,
    load        INTEGER NOT NULL DEFAULT 0,
    disk_used   INTEGER NOT NULL DEFAULT 0,
    disk_total  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, resource_id, at)
) STRICT, WITHOUT ROWID;
CREATE INDEX metric_samples_at ON metric_samples(at);
