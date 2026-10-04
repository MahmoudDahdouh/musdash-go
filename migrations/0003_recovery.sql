-- An install has exactly one local server. Without this, two first requests
-- arriving together could each create one, and the second would get its own
-- monitor and an empty routes file.
CREATE UNIQUE INDEX servers_one_local ON servers(kind) WHERE kind = 'local';
