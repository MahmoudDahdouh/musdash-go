# Phase 3 — Databases: implementation plan

**Goal:** create a database from the UI — PostgreSQL, MySQL, MariaDB, MongoDB, Redis, KeyDB, Dragonfly or ClickHouse — with generated credentials, a persistent volume, limits, a health check, connection strings, and an optional public port.

**Spec:** `docs/spec.md` sections 4.3 and 6.2. Builds on phases 0–2.

**Done when:** an app connects to a PostgreSQL created in the UI, by the database's name on the environment network.

## Global constraints

- Earlier constraints hold. No new modules.
- A password never appears on a command line: it reaches the container through a `0600` env file.
- A database is not reachable from outside its environment unless the public port is switched on.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| Template shape | The spec's `DBTemplate`, plus `Command` (some engines take the password as a server argument) and `ExtraPorts` | Redis-family servers have no password environment variable; the command reads it from the container's environment instead |
| Passwords | 32 characters of `[A-Za-z0-9]` | Safe in URLs, shells and every engine's own quoting, with about 190 bits of entropy |
| Names | A database's name shares one namespace with app names inside an environment | Both are DNS names on the environment's network; two resources with one name would answer for each other |
| Restart | Stop the old container, then start the new one | A database's files can be opened by one server process only, so there is no rolling update; expect a few seconds of downtime |
| Settings | Saving a change restarts a database that has a container | The page then never describes a database other than the one running; above all, a public port that was switched off is closed at once |
| Where the data is mounted | The path the image itself declares as its volume, when the engine's template knows it; recorded at the first start and never moved | PostgreSQL 18 keeps its data one directory above where 17 did. An image that expects another path than the recorded one is refused before the running database is touched, instead of starting on an empty directory |
| Readiness | Each health command asks over the network, not a socket or the loopback address | Most images first run a private server to create users and databases; a check that passes against it reports a database as running that is about to restart |
| A start that times out | The container is left running and stays on record; the database is marked failed with the container's last output | A first start on a slow disk may just need longer, and stopping it mid-initialisation can leave half-created files. Stop, Logs and the monitor still reach it |
| Public port | Published on `0.0.0.0:<port>` with a port musdash assigns from 30000–30999, or one the person enters | Fixed so connection strings stay valid across restarts |
| Health | Each engine's own client run with `docker exec`; a new database waits up to 5 minutes | First start initialises the data directory, which takes longer than an app's start |
| Delete | Removes the container; the volume is deleted only when the person also ticks "delete the data" | Data loss should need its own explicit act |
| Pulling | A database's image is pulled only when it is not on the server yet | A stopped database can then be started while the registry is unreachable, and a restart never moves it to a newer build of the same tag unasked; changing the tag is how a version is changed |
| Password on screen | The Overview page draws bullets in place of the password and hands the real value to the copy button | Keeps it out of a screen share; the page is `no-store` and is the only one that opens the password |
| Image variants | `-alpine` where the engine publishes one (PostgreSQL, Redis, KeyDB, ClickHouse) | Smaller pull and footprint |

## Data model (migration `0006_databases.sql`)

| Table | Columns |
|---|---|
| `databases` | `id`, `environment_id`, `server_id`, `name`, `engine`, `image`, `username`, `password` (sealed), `db_name`, `public_port`, `memory_mb`, `cpus`, `status`, `container`, `last_error`, `created_at`, `updated_at`; unique `(environment_id, name)` |

Migration `0007_database_details.sql` adds `volume_path` and a unique index on `(server_id, public_port)` for public ports.

## File map

```
migrations/0006_databases.sql
internal/catalog/databases.go    the eight engine templates
internal/db/databases.go         queries
internal/deploy/database.go      start, stop, restart, destroy
internal/deploy/monitor.go       events and reconcile also cover databases
internal/web/handlers_databases.go
internal/web/pages/databases.templ
```

## Interfaces

```go
// internal/catalog
type DBTemplate struct {
    Engine, Label, Image string
    Port                 int
    Env                  map[string]string // values are templates: {{.User}} {{.Pass}} {{.DB}}
    Command              []string          // optional; replaces the image's command
    VolumePath           string
    HealthCmd            []string          // run inside the container
    URLFormat            string
    DefaultUser, DefaultDB string
}
func Databases() []DBTemplate
func Database(engine string) (DBTemplate, bool)
func (t DBTemplate) Render(c Creds) (env map[string]string, err error)
func (t DBTemplate) URL(c Creds, host string, port int) string

// internal/deploy
func (d *Deployer) EnqueueDatabase(ctx, m db.Database) error // start or restart, as a job
func (d *Deployer) StopDatabase(ctx, id string) error
func (d *Deployer) DestroyDatabase(ctx, id string, deleteData bool) error
```

## Tasks

### Task 1 — Catalogue and data model
- [x] The eight templates. Table test: every template renders with test credentials, has a health command and a volume path, and its URL parses with `net/url`.
- [x] Migration and team-scoped queries; name uniqueness across apps and databases in an environment.

### Task 2 — Lifecycle
- [x] `EnqueueDatabase` and its job: pull if missing → network → env file (`0600`) → remove any old container → run with volume, limits, alias and optional public port → wait for the health command.
- [x] `StopDatabase`, `DestroyDatabase` under the per-resource lock, with the same ordering rules as apps.
- [x] Monitor and reconcile handle `kind=database`.
- [x] Tests with the scripted Runner: command line never contains the password; env file content and mode; public port flag only when enabled; a failed health check reports the container's output; destroy keeps the volume unless asked.

### Task 3 — UI
- [x] Project page lists databases with apps; "New database" with the engine choice.
- [x] Database page: status and actions, internal and public connection strings with copy buttons, logs, settings (public port, limits), delete.
- [x] Tests: validation, team scoping, the password is shown only on the database's own page.

### Task 4 — End to end
- [x] With `MUSDASH_DOCKER_TEST=1`: create PostgreSQL and Redis, wait until healthy, connect from a second container on the environment network using the internal connection string, restart and check the data survived, switch the public port on and connect through it, then delete with data and check the volume is gone.

## Review focus

1. **The password on a command line or in a log** — asserted in the lifecycle tests.
2. **A database reachable from the internet by default** — the run arguments carry no publish flag unless the toggle is on.
3. **Two resources with one network name** — creation test across apps and databases.
4. **Restart losing data** — end-to-end check.
5. **Deleting data without intent** — destroy test for both choices.

## Self-review of this plan

- Spec coverage for phase 3 rows: the eight engines (task 1), generated credentials and connection strings (1, 3), persistent volume, limits, health check (2), public port toggle (2, 3). SSL for database connections is marked Skip in the spec. Backups are phase 5.
- Only PostgreSQL and Redis are exercised against real Docker here; the other six are covered by template tests and need a run on the VPS.
- No placeholders.

**Approved for implementation.**

## Outcome

- `TestDatabasesWithDocker` against real Docker: PostgreSQL 17 and Redis are created, reached from a second container by name with the generated password (and refused with a wrong one or none), not published until asked, restarted with their data intact, reached through a public port from outside Docker's network, and deleted with and without their volume. PostgreSQL 18 gets its volume at the path that image uses, keeps its data across a restart, and a change to an image with another data path is refused with the database left running. The Redis server does not run as root and no process's arguments contain the password.
- `TestEveryEngineStartsWithDocker` starts, restarts and deletes each engine: PostgreSQL, MySQL, MariaDB, Redis, KeyDB, Dragonfly and ClickHouse pass.
- **MongoDB could not be verified here.** The local Docker runs Linux 7.0, and both `mongo:8` and `mongo:9` refuse to start on kernels from 6.19 on ("known incompatibility", SERVER-121912). musdash reports that message on the database's page as intended. On a server with an older kernel it should start; that, and its health check, need a run on the VPS. A server with a 6.19+ kernel cannot run MongoDB until MongoDB ships a fix.
- Idle memory on Linux after this phase: server 23.5 MB, proxy 17.2 MB (phase 2: 24.2 and 18.2). Static assets are now read on first use and start-up garbage is returned to the system after two seconds, which is where the megabyte came from. Roughly half of the proxy's figure is pages of the shared binary; if later phases push it over 20 MB, the remaining lever is a separate, smaller proxy binary, which would be a change to the "one binary" decision.
- Not verifiable here, to check on the VPS: MongoDB as above; a public port reached from another machine; the first start of MySQL and ClickHouse on a small server within the 5-minute limit.

### Found while testing

- `docker ps` output was trimmed as a whole, so a database's container, whose line ends with an empty field, was dropped when it came last. At start-up such a database would have been marked "not running". Fixed in `docker.Client.List`.
- A database left "starting" by a crash stayed that way for good; `ResetStuckDatabases` now repairs it at start-up as apps are.

### Fixes from the independent code review

1. **PostgreSQL 18 data path**: see "Where the data is mounted" above.
2. **Readiness during first boot**: PostgreSQL is asked over TCP, MongoDB and ClickHouse over the container's own address, Redis and KeyDB likewise.
3. **A failed start hid a live container**: the container stays on record, the error carries its last output, and the page says so.
4. **Public port closed late**: saving settings restarts a running database. The page no longer claims a firewall can close the port: Docker publishes past ufw and firewalld.
5. **Public port uniqueness**: checked and picked inside the insert's transaction, and backed by a unique index.
6. **A start that could not be queued** left the database "starting" with no buttons: it is marked failed. Two clicks on Start queue one start.
7. **Passwords in argument lists inside the container**: Redis and KeyDB read theirs from a private file their own shell writes, and are started through the image's entry point so they drop from root; the clients use `REDISCLI_AUTH`; MySQL's health check needs no password; the dump commands for phase 5 use `MYSQL_PWD`. MongoDB's dump command still passes the password as an argument and is to be changed in phase 5 before it is used.
8. **MySQL and MariaDB**: the administrator account shares the generated password but can only log in from inside the container (`*_ROOT_HOST=localhost`).

Left as is, on purpose:

- KeyDB, Dragonfly and ClickHouse default to floating tags, because their version tags are per-architecture or change too often to pin here. An image already on the server is not pulled again, so a database does not change version on its own.
- A second team cannot use the one local server (`CreateDatabase` and `CreateApp` look the server up by team). Phase 8 introduces teams properly and decides who may use which server.
