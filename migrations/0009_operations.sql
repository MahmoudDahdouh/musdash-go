-- S3-compatible buckets that backups are copied to.
CREATE TABLE s3_storages (
    id         TEXT PRIMARY KEY,
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    endpoint   TEXT NOT NULL DEFAULT '',
    region     TEXT NOT NULL DEFAULT '',
    bucket     TEXT NOT NULL,
    prefix     TEXT NOT NULL DEFAULT '',
    access_key TEXT NOT NULL, -- sealed
    secret_key TEXT NOT NULL, -- sealed
    created_at INTEGER NOT NULL,
    UNIQUE (team_id, name)
) STRICT;

-- How a database is backed up. One row per database that has backups set
-- up.
CREATE TABLE backup_configs (
    database_id TEXT PRIMARY KEY REFERENCES databases(id) ON DELETE CASCADE,
    -- A cron expression, in UTC.
    schedule    TEXT NOT NULL,
    -- How many backups to keep, on the server and in the storage.
    keep        INTEGER NOT NULL DEFAULT 7,
    -- Empty for backups kept on the server only.
    storage_id  TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    -- When the schedule next fires, Unix seconds; 0 when it never does.
    next_run    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX backup_configs_due ON backup_configs(enabled, next_run);

CREATE TABLE backups (
    id          TEXT PRIMARY KEY,
    database_id TEXT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'running', -- running, success, failed
    trigger     TEXT NOT NULL DEFAULT 'manual',  -- manual, schedule
    -- The file's name inside the database's backup directory.
    file        TEXT NOT NULL DEFAULT '',
    size        INTEGER NOT NULL DEFAULT 0,
    -- The storage the file was also copied to; empty when it is only on
    -- the server. Kept per backup so that a later change of storage still
    -- removes old copies from where they are.
    storage_id  TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0,
    -- The last time this backup was restored: '', running, success, failed.
    restore_status TEXT NOT NULL DEFAULT '',
    restore_error  TEXT NOT NULL DEFAULT '',
    restored_at    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX backups_database ON backups(database_id, started_at);

-- A command run inside an app's container on a schedule.
CREATE TABLE scheduled_tasks (
    id         TEXT PRIMARY KEY,
    app_id     TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    schedule   TEXT NOT NULL,
    command    TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    next_run   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX scheduled_tasks_due ON scheduled_tasks(enabled, next_run);
CREATE INDEX scheduled_tasks_app ON scheduled_tasks(app_id);

CREATE TABLE task_runs (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'running', -- running, success, failed
    trigger     TEXT NOT NULL DEFAULT 'schedule',
    exit_code   INTEGER NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX task_runs_task ON task_runs(task_id, started_at);

-- Where a team is told about events.
CREATE TABLE notification_channels (
    id         TEXT PRIMARY KEY,
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    config     TEXT NOT NULL,            -- sealed JSON object
    events     TEXT NOT NULL DEFAULT '', -- comma separated event kinds
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX notification_channels_team ON notification_channels(team_id);
