CREATE TABLE databases (
    id             TEXT PRIMARY KEY,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
    server_id      TEXT NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    -- DNS-safe; it is how apps in the environment reach the database.
    name           TEXT NOT NULL,
    engine         TEXT NOT NULL,
    image          TEXT NOT NULL,
    username       TEXT NOT NULL DEFAULT '',
    password       TEXT NOT NULL DEFAULT '', -- sealed
    db_name        TEXT NOT NULL DEFAULT '',
    -- 0 means reachable only inside its environment.
    public_port    INTEGER NOT NULL DEFAULT 0,
    memory_mb      INTEGER NOT NULL DEFAULT 0,
    cpus           REAL NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'created',
    container      TEXT NOT NULL DEFAULT '',
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    UNIQUE (environment_id, name)
) STRICT;
CREATE INDEX databases_server ON databases(server_id);
