-- A service whose Compose file lives in a Git repository. The columns mirror
-- the ones an app deployed from Git has.
ALTER TABLE services ADD COLUMN repo_url TEXT NOT NULL DEFAULT '';
-- "owner/name", lower case: what a push webhook is matched against.
ALTER TABLE services ADD COLUMN repo_name TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN branch TEXT NOT NULL DEFAULT '';
-- The Compose file's path inside the repository.
ALTER TABLE services ADD COLUMN compose_path TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN git_source_id TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN ssh_key_id TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN auto_deploy INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN webhook_secret TEXT NOT NULL DEFAULT ''; -- sealed
ALTER TABLE services ADD COLUMN deploy_token_hash TEXT NOT NULL DEFAULT '';
-- The commit the running stack was deployed from.
ALTER TABLE services ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';
-- Which of the service's two checkout directories the running stack uses:
-- 'a', 'b', or '' before the first deployment. A deployment clones into the
-- other one, so the files the running containers have mounted stay where
-- they are until the new stack is up.
ALTER TABLE services ADD COLUMN checkout TEXT NOT NULL DEFAULT '';
CREATE INDEX services_push ON services(repo_name, branch) WHERE repo_name <> '';
