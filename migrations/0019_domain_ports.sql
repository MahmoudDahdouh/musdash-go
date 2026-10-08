-- The port of the app's container a domain leads to; 0 is the app's own
-- (apps.port), so a domain follows an app whose port is changed.
ALTER TABLE domains ADD COLUMN port INTEGER NOT NULL DEFAULT 0;

-- The loopback port of an app's serving container for each port a domain
-- of the app names. A container publishes what it was started with, so
-- this is written with the container (apps.container, apps.host_port) and
-- never from the domains alone: a domain whose port has no row here is not
-- served until the app is deployed again.
CREATE TABLE app_ports (
    app_id    TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    port      INTEGER NOT NULL,
    host_port INTEGER NOT NULL,
    PRIMARY KEY (app_id, port)
) STRICT;
