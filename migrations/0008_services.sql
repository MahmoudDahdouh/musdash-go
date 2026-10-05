-- A service is a stack of containers described by a Docker Compose file.
CREATE TABLE services (
    id             TEXT PRIMARY KEY,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
    server_id      TEXT NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    -- Shares the environment's namespace with apps and databases.
    name           TEXT NOT NULL,
    -- A catalogue key, or 'custom' for a file a person pasted.
    template       TEXT NOT NULL DEFAULT 'custom',
    compose        TEXT NOT NULL DEFAULT '',
    -- Sealed JSON object, name to value: the generated values and the ones
    -- a person entered.
    variables      TEXT NOT NULL DEFAULT '',
    -- Whether the stack's containers also join the environment's network.
    connect_env    INTEGER NOT NULL DEFAULT 0,
    -- The Compose service names of the last deployment, comma separated:
    -- the names its containers answer to on a network.
    members        TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'created',
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    UNIQUE (environment_id, name)
) STRICT;
CREATE INDEX services_server ON services(server_id);

-- A port of one container of a stack that is given a domain. Its domain is
-- the row of `domains` with resource_kind 'service' and this row's id.
CREATE TABLE service_endpoints (
    id              TEXT PRIMARY KEY,
    service_id      TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    -- The name in the magic variables: N8N in SERVICE_FQDN_N8N_5678.
    name            TEXT NOT NULL,
    -- Which Compose service listens, and on which port: known once the
    -- file has been loaded for a deployment.
    compose_service TEXT NOT NULL DEFAULT '',
    port            INTEGER NOT NULL DEFAULT 0,
    -- The loopback port of the server the proxy forwards to.
    host_port       INTEGER NOT NULL DEFAULT 0,
    UNIQUE (service_id, name)
) STRICT;
