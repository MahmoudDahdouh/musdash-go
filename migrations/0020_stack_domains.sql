-- A domain a person gave to a service of a stack, on a port they named:
-- the endpoint is theirs, not one the Compose file asks for with a
-- SERVICE_FQDN variable, and saving the file leaves it alone. Its name is
-- '~' and its own id, which no variable can spell.
ALTER TABLE service_endpoints ADD COLUMN manual INTEGER NOT NULL DEFAULT 0;

-- What the stack's Compose file holds, as the last deployment read it: a
-- JSON list of its services with their images and container ports. Empty
-- until a deployment has read the file, and again once a different text is
-- saved: it describes the stored text or nothing.
ALTER TABLE services ADD COLUMN layout TEXT NOT NULL DEFAULT '';
