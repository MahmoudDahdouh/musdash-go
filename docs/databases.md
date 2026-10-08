# Databases

[← All documentation](../README.md#documentation)

Creating a database, reaching it from your apps, and backing it up.

## Creating and running a database

**Add resource** on a project page offers eight database engines. musdash generates a user and a 32-character password, creates a Docker volume for the data and starts the engine on the environment's network.

- Apps in the same environment connect by the database's name, for example `postgres://postgres:…@maindb:5432/postgres`. The Overview page has the connection string ready to copy into an app's variables.
- Nothing outside the environment can reach a database until you switch on its **public port** in Settings. It is then open on the server to anyone with the password. Docker publishes the port past ufw and firewalld, so only a firewall in front of the server, such as your provider's, can narrow who reaches it.
- Restart replaces the container and keeps the volume, so expect a few seconds of downtime. Saving a change in Settings (image tag, public port, limits) restarts a running database to apply it.
- An image already on the server is not downloaded again. To move to another version of the same major release, change the tag. A new major version usually cannot open the old data: create a new database and move the data.
- MongoDB does not start on servers whose Linux kernel is 6.19 or newer; that is MongoDB's own limitation, and its message is shown on the database's page.
- Deleting a database keeps its volume unless you tick "Also delete the data".

## Backups

A database's Backups tab sets a schedule, how many backups to keep, and optionally a bucket to copy each one to. A backup is a dump made by the engine's own tool inside the database's container (`pg_dump`, `mysqldump`, `mariadb-dump`, `mongodump`, `redis-cli --rdb`), compressed and written to `<data>/backups/<database id>/`. It is streamed, so its memory use does not depend on the size of the database. Older backups beyond the number to keep are removed from the server and from the bucket.

- Restore puts a backup back into the same database after you type its name; the database keeps running. Redis backups can be downloaded but not restored from the page: an RDB file is loaded by replacing the file and restarting.
- KeyDB, Dragonfly and ClickHouse are not backed up yet. Their pages say so.
- Buckets are added under Settings, Backup storage: any S3-compatible service. Copies are made with `rclone` in a container that runs only for the upload; the keys reach it through a file that is removed afterwards. A bucket hosted on the same server is reached through its public domain. Deleting a database with its data removes its backups on the server and leaves the copies in the bucket.

How a schedule is written is in [Schedules](operations.md#schedules).
