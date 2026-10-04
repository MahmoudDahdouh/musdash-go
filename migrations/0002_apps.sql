-- Servers that run containers. Exactly one 'local' server exists per install;
-- remote servers arrive with phase 6.
CREATE TABLE servers (
    id         TEXT PRIMARY KEY,
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('local', 'ssh')),
    host       TEXT NOT NULL DEFAULT '',
    port       INTEGER NOT NULL DEFAULT 22,
    ssh_user   TEXT NOT NULL DEFAULT '',
    -- Public address, used to build generated <id>.<ip>.sslip.io domains.
    ip         TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX servers_team ON servers(team_id);

-- Instance-wide settings such as the dashboard's own domain.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

CREATE TABLE apps (
    id             TEXT PRIMARY KEY,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
    server_id      TEXT NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    -- DNS-safe; it is the container's alias on the environment network.
    name           TEXT NOT NULL,
    source         TEXT NOT NULL DEFAULT 'image' CHECK (source IN ('image', 'git')),
    image          TEXT NOT NULL DEFAULT '',
    port           INTEGER NOT NULL DEFAULT 80,
    memory_mb      INTEGER NOT NULL DEFAULT 0,
    cpus           REAL NOT NULL DEFAULT 0,
    health_path    TEXT NOT NULL DEFAULT '',
    health_cmd     TEXT NOT NULL DEFAULT '',
    health_timeout INTEGER NOT NULL DEFAULT 60,
    status         TEXT NOT NULL DEFAULT 'created',
    -- The container serving traffic now, and the loopback port it publishes.
    container      TEXT NOT NULL DEFAULT '',
    host_port      INTEGER NOT NULL DEFAULT 0,
    deployed_image TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    UNIQUE (environment_id, name)
) STRICT;
CREATE INDEX apps_server ON apps(server_id);

-- Host names routed to a resource. A host is unique across the install.
CREATE TABLE domains (
    id            TEXT PRIMARY KEY,
    resource_kind TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    host          TEXT NOT NULL UNIQUE,
    tls           INTEGER NOT NULL DEFAULT 1,
    -- Also route the www. variant (or the bare name) as a redirect here.
    redirect_www  INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX domains_resource ON domains(resource_kind, resource_id);

CREATE TABLE env_vars (
    id            TEXT PRIMARY KEY,
    resource_kind TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    key           TEXT NOT NULL,
    value         TEXT NOT NULL, -- sealed
    build_time    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (resource_kind, resource_id, key)
) STRICT;

CREATE TABLE storages (
    id            TEXT PRIMARY KEY,
    resource_kind TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('volume', 'bind', 'file')),
    -- volume: Docker volume name; bind: host path; file: unused.
    source        TEXT NOT NULL DEFAULT '',
    target        TEXT NOT NULL,
    content       TEXT NOT NULL DEFAULT '', -- sealed; file mounts only
    created_at    INTEGER NOT NULL,
    UNIQUE (resource_kind, resource_id, target)
) STRICT;

CREATE TABLE deployments (
    id          TEXT PRIMARY KEY,
    app_id      TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'success', 'failed')),
    trigger     TEXT NOT NULL DEFAULT 'manual',
    image       TEXT NOT NULL DEFAULT '',
    commit_sha  TEXT NOT NULL DEFAULT '',
    log_path    TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    started_at  INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX deployments_app ON deployments(app_id, created_at);
