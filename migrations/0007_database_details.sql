-- Where the data volume is mounted inside the container. Recorded at the
-- first start and never moved afterwards: an image that keeps its data
-- somewhere else would otherwise start on an empty directory.
ALTER TABLE databases ADD COLUMN volume_path TEXT NOT NULL DEFAULT '';

-- Two databases on one server cannot publish the same port.
CREATE UNIQUE INDEX databases_public_port ON databases(server_id, public_port) WHERE public_port > 0;
