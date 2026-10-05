-- Servers reached over SSH.

-- The key musdash signs in with (an ssh_keys row of the same team).
ALTER TABLE servers ADD COLUMN ssh_key_id TEXT NOT NULL DEFAULT '';
-- The key the server presented when it was first checked, as an
-- authorized_keys line. Every later connection must see the same one.
ALTER TABLE servers ADD COLUMN host_key TEXT NOT NULL DEFAULT '';
-- Where musdash keeps its files on the server. Empty for the local server,
-- whose files are in the control plane's own data directory.
ALTER TABLE servers ADD COLUMN data_dir TEXT NOT NULL DEFAULT '';
-- What the last check found.
ALTER TABLE servers ADD COLUMN arch TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN docker_version TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN status TEXT NOT NULL DEFAULT 'unknown'; -- unknown, ok, unreachable
ALTER TABLE servers ADD COLUMN status_detail TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN checked_at INTEGER NOT NULL DEFAULT 0;
-- Whether musdash installed its proxy there: none, installed.
ALTER TABLE servers ADD COLUMN proxy TEXT NOT NULL DEFAULT 'none';

-- The server an app's image is built on when that is not the one it runs
-- on. Empty builds where it runs.
ALTER TABLE apps ADD COLUMN build_server_id TEXT NOT NULL DEFAULT '';
