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
| Public port | Published on `0.0.0.0:<port>` with a port musdash assigns from 30000–30999, or one the person enters | Fixed so connection strings stay valid across restarts |
| Health | Each engine's own client run with `docker exec`; a new database waits up to 120 s | First start initialises the data directory, which takes longer than an app's start |
| Delete | Removes the container; the volume is deleted only when the person also ticks "delete the data" | Data loss should need its own explicit act |
| Image variants | `-alpine` where the engine publishes one (PostgreSQL, Redis, KeyDB, ClickHouse) | Smaller pull and footprint |

## Data model (migration `0006_databases.sql`)

| Table | Columns |
|---|---|
| `databases` | `id`, `environment_id`, `server_id`, `name`, `engine`, `image`, `username`, `password` (sealed), `db_name`, `public_port`, `memory_mb`, `cpus`, `status`, `container`, `created_at`, `updated_at`; unique `(environment_id, name)` |

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
func (d *Deployer) StartDatabase(ctx, id string) error    // also used for restart
func (d *Deployer) StopDatabase(ctx, id string) error
func (d *Deployer) DestroyDatabase(ctx, id string, deleteData bool) error
```

## Tasks

### Task 1 — Catalogue and data model
- [ ] The eight templates. Table test: every template renders with test credentials, has a health command and a volume path, and its URL parses with `net/url`.
- [ ] Migration and team-scoped queries; name uniqueness across apps and databases in an environment.

### Task 2 — Lifecycle
- [ ] `StartDatabase`: pull → network → env file (`0600`) → remove any old container → run with volume, limits, alias and optional public port → wait for the health command.
- [ ] `StopDatabase`, `DestroyDatabase` under the per-resource lock, with the same ordering rules as apps.
- [ ] Monitor and reconcile handle `kind=database`.
- [ ] Tests with the scripted Runner: command line never contains the password; env file content and mode; public port flag only when enabled; a failed health check reports the container's output; destroy keeps the volume unless asked.

### Task 3 — UI
- [ ] Project page lists databases with apps; "New database" with the engine choice.
- [ ] Database page: status and actions, internal and public connection strings with copy buttons, logs, settings (public port, limits), delete.
- [ ] Tests: validation, team scoping, the password is shown only on the database's own page.

### Task 4 — End to end
- [ ] With `MUSDASH_DOCKER_TEST=1`: create PostgreSQL and Redis, wait until healthy, connect from a second container on the environment network using the internal connection string, restart and check the data survived, switch the public port on and connect through it, then delete with data and check the volume is gone.

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
