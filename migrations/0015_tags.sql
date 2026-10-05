-- A tag now exists on its own: it can be made before anything has it, and
-- it stays when the last thing that had it is deleted. resource_tags keeps
-- saying which resources have which tag.
--
-- No foreign key from resource_tags to this table: adding one to a table
-- that exists means rebuilding it. The queries that write either table keep
-- the two in step inside one transaction each.
CREATE TABLE tags (
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (team_id, tag)
) STRICT;

INSERT INTO tags (team_id, tag, created_at)
    SELECT DISTINCT team_id, tag, unixepoch() FROM resource_tags;
