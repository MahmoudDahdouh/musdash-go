-- Phase 7: routing by path and passwords on domains, rollback, previews of
-- pull requests.

-- A host may now be routed more than once, by path: (host, path) is what is
-- unique. The table is rebuilt because the old one declared host UNIQUE.
CREATE TABLE domains_new (
    id            TEXT PRIMARY KEY,
    resource_kind TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    host          TEXT NOT NULL,
    -- '' for the whole host, or a prefix such as /api.
    path          TEXT NOT NULL DEFAULT '',
    -- Take the prefix off before the request is passed on.
    strip_prefix  INTEGER NOT NULL DEFAULT 0,
    tls           INTEGER NOT NULL DEFAULT 1,
    -- Also route the www. variant (or the bare name) as a redirect here.
    redirect_www  INTEGER NOT NULL DEFAULT 0,
    -- A user name and password the proxy asks for; the hash is bcrypt.
    auth_user     TEXT NOT NULL DEFAULT '',
    auth_hash     TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    UNIQUE (host, path)
) STRICT;
INSERT INTO domains_new (id, resource_kind, resource_id, host, tls, redirect_www, created_at)
    SELECT id, resource_kind, resource_id, host, tls, redirect_www, created_at FROM domains;
DROP TABLE domains;
ALTER TABLE domains_new RENAME TO domains;
CREATE INDEX domains_resource ON domains(resource_kind, resource_id);

-- A rollback is a deployment that runs an earlier one's image again.
ALTER TABLE deployments ADD COLUMN rollback_of TEXT NOT NULL DEFAULT '';
-- The name, in the app's own repository, under which the image that was
-- deployed is kept on the server. A tag such as nginx:latest moves; this
-- one does not.
ALTER TABLE deployments ADD COLUMN kept_image TEXT NOT NULL DEFAULT '';
-- Images built from Git have always had such a name.
UPDATE deployments SET kept_image = image WHERE status = 'success' AND image LIKE 'musdash/%';

-- A preview is an app of its own, the child of the app whose pull request
-- it shows.
ALTER TABLE apps ADD COLUMN preview_of TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN pr_number INTEGER NOT NULL DEFAULT 0;
-- On the parent: whether pull requests get previews, and under which
-- domain (pr-<n>-<app>.<preview_domain>).
ALTER TABLE apps ADD COLUMN previews INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN preview_domain TEXT NOT NULL DEFAULT '';
-- On the preview: the comment on the pull request that carries its address.
ALTER TABLE apps ADD COLUMN pr_comment_id INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX apps_preview ON apps(preview_of, pr_number) WHERE preview_of <> '';
