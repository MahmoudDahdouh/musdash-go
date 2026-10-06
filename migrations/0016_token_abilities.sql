-- What an API token may do becomes a list of permissions: read, write,
-- deploy, read:sensitive, or root alone. The names are checked where a
-- token is made (db.NormalAbilities), not here: a CHECK would have to be
-- rewritten, table and all, for each permission a later version adds.
ALTER TABLE api_tokens ADD COLUMN abilities TEXT NOT NULL DEFAULT '';

-- A token keeps exactly what it could do: one that deployed also read,
-- started and stopped.
UPDATE api_tokens SET abilities = CASE ability WHEN 'deploy' THEN 'read,write,deploy' ELSE 'read' END;

ALTER TABLE api_tokens DROP COLUMN ability;
