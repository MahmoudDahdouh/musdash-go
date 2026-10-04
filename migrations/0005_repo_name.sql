-- "owner/name" in lower case, for matching push webhooks to apps.
ALTER TABLE apps ADD COLUMN repo_name TEXT NOT NULL DEFAULT '';
CREATE INDEX apps_repo ON apps(repo_name) WHERE repo_name <> '';
