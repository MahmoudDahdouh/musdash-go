-- A source can be a GitLab access token besides a GitHub App. The table is
-- rebuilt because the old one's CHECK allowed one kind only.
CREATE TABLE git_sources_new (
    id             TEXT PRIMARY KEY,
    team_id        TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL DEFAULT 'github_app' CHECK (kind IN ('github_app', 'gitlab')),
    -- GitHub App: filled when GitHub returns the new App's credentials.
    -- Until then the row is a pending manifest flow identified by state.
    app_id         INTEGER NOT NULL DEFAULT 0,
    -- The App's slug, or the GitLab user the token acts as.
    slug           TEXT NOT NULL DEFAULT '',
    html_url       TEXT NOT NULL DEFAULT '',
    client_id      TEXT NOT NULL DEFAULT '',
    client_secret  TEXT NOT NULL DEFAULT '', -- sealed
    private_key    TEXT NOT NULL DEFAULT '', -- sealed
    webhook_secret TEXT NOT NULL DEFAULT '', -- sealed
    state          TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    -- GitLab: the instance, as https://host[:port], and its access token.
    base_url       TEXT NOT NULL DEFAULT '',
    token          TEXT NOT NULL DEFAULT ''  -- sealed
) STRICT;
INSERT INTO git_sources_new (id, team_id, name, kind, app_id, slug, html_url, client_id, client_secret, private_key, webhook_secret, state, created_at)
    SELECT id, team_id, name, kind, app_id, slug, html_url, client_id, client_secret, private_key, webhook_secret, state, created_at FROM git_sources;
DROP TABLE git_sources;
ALTER TABLE git_sources_new RENAME TO git_sources;
CREATE INDEX git_sources_team ON git_sources(team_id);
