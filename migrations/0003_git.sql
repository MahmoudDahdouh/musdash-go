-- Where code comes from: GitHub Apps and SSH deploy keys.
CREATE TABLE git_sources (
    id             TEXT PRIMARY KEY,
    team_id        TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL DEFAULT 'github_app' CHECK (kind IN ('github_app')),
    -- Filled when GitHub returns the new App's credentials. Until then the
    -- row is a pending manifest flow identified by state.
    app_id         INTEGER NOT NULL DEFAULT 0,
    slug           TEXT NOT NULL DEFAULT '',
    html_url       TEXT NOT NULL DEFAULT '',
    client_id      TEXT NOT NULL DEFAULT '',
    client_secret  TEXT NOT NULL DEFAULT '', -- sealed
    private_key    TEXT NOT NULL DEFAULT '', -- sealed
    webhook_secret TEXT NOT NULL DEFAULT '', -- sealed
    state          TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL
) STRICT;
CREATE INDEX git_sources_team ON git_sources(team_id);

CREATE TABLE ssh_keys (
    id          TEXT PRIMARY KEY,
    team_id     TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    public_key  TEXT NOT NULL,
    private_key TEXT NOT NULL, -- sealed
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX ssh_keys_team ON ssh_keys(team_id);

ALTER TABLE apps ADD COLUMN repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN branch TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN build_pack TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN dockerfile_path TEXT NOT NULL DEFAULT '';
-- Directory inside the repository that is the build context.
ALTER TABLE apps ADD COLUMN base_dir TEXT NOT NULL DEFAULT '';
-- Static build pack: the directory to serve, and whether unknown paths fall
-- back to index.html for single-page apps.
ALTER TABLE apps ADD COLUMN publish_dir TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN spa_fallback INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN start_command TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN docker_options TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN git_source_id TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN ssh_key_id TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN auto_deploy INTEGER NOT NULL DEFAULT 1;
ALTER TABLE apps ADD COLUMN webhook_secret TEXT NOT NULL DEFAULT ''; -- sealed
ALTER TABLE apps ADD COLUMN deploy_token_hash TEXT NOT NULL DEFAULT '';
CREATE INDEX apps_git_source ON apps(git_source_id) WHERE git_source_id <> '';
CREATE INDEX apps_deploy_token ON apps(deploy_token_hash) WHERE deploy_token_hash <> '';

-- Webhook deliveries already acted on, so a redelivered push does not deploy
-- twice. Rows older than a day are removed by housekeeping.
CREATE TABLE webhook_deliveries (
    id          TEXT PRIMARY KEY,
    received_at INTEGER NOT NULL
) STRICT;
