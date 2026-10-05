-- Phase 8: members of the team and their roles, a second step at sign-in,
-- shared variables, tags, API tokens.

-- The second step: a TOTP key, sealed. totp_pending is a key that was shown
-- but not yet confirmed with a code. totp_step is the last thirty-second
-- step a code was accepted for; only a later one is accepted, so a code
-- works once.
ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_pending TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_step INTEGER NOT NULL DEFAULT 0;

-- Codes for a lost phone, as SHA-256. A used code is deleted.
CREATE TABLE recovery_codes (
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL,
    PRIMARY KEY (user_id, code_hash)
) STRICT;

-- An invitation is a link that creates an account in the team. The token
-- is stored as its hash, like a session's.
CREATE TABLE invitations (
    id         TEXT PRIMARY KEY,
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    email      TEXT NOT NULL COLLATE NOCASE,
    role       TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    token_hash TEXT NOT NULL UNIQUE,
    -- Who made it, by name: the account may be gone when it is read.
    invited_by TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    UNIQUE (team_id, email)
) STRICT;

-- A person's token for the API. It acts as that person in that team and
-- goes when either does.
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id      TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    ability      TEXT NOT NULL CHECK (ability IN ('read', 'deploy')),
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    -- 0 for a token that does not expire.
    expires_at   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX api_tokens_user ON api_tokens(user_id);

-- Values that resources take by name: {{team.NAME}}, {{project.NAME}},
-- {{environment.NAME}}, {{server.NAME}}. scope_id is the id of the team,
-- project, environment or server.
CREATE TABLE shared_vars (
    id       TEXT PRIMARY KEY,
    team_id  TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    scope    TEXT NOT NULL CHECK (scope IN ('team', 'project', 'environment', 'server')),
    scope_id TEXT NOT NULL,
    key      TEXT NOT NULL,
    value    TEXT NOT NULL, -- sealed
    UNIQUE (scope, scope_id, key)
) STRICT;

-- scope_id cannot be a foreign key: it points at one of four tables. The
-- rows go with what they belong to by trigger instead, which also covers
-- an environment that is deleted by the cascade from its project.
CREATE TRIGGER shared_vars_project AFTER DELETE ON projects BEGIN
    DELETE FROM shared_vars WHERE scope = 'project' AND scope_id = OLD.id;
END;
CREATE TRIGGER shared_vars_environment AFTER DELETE ON environments BEGIN
    DELETE FROM shared_vars WHERE scope = 'environment' AND scope_id = OLD.id;
END;
CREATE TRIGGER shared_vars_server AFTER DELETE ON servers BEGIN
    DELETE FROM shared_vars WHERE scope = 'server' AND scope_id = OLD.id;
END;

-- A tag is a name on an app or a service. There is no table of tags: a tag
-- exists while something has it.
CREATE TABLE resource_tags (
    team_id       TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    tag           TEXT NOT NULL,
    resource_kind TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    PRIMARY KEY (team_id, tag, resource_kind, resource_id)
) STRICT;
CREATE INDEX resource_tags_resource ON resource_tags(resource_kind, resource_id);
