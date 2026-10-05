# Phase 5 — Operations: implementation plan

**Goal:** scheduled database backups to the server's disk and to S3-compatible storage, with retention and restore; scheduled commands inside containers; notifications about what happened; and automatic clean-up of what Docker leaves behind.

**Spec:** `docs/spec.md` sections 4.3 (backups), 4.5 (Docker cleanup), 4.6 (scheduled tasks, notifications) and 6.4. Builds on phases 0–4.

**Done when:** a nightly PostgreSQL backup lands in S3 and a Discord message confirms it.

## Global constraints

- Earlier constraints hold. No new modules: the cron parser, SMTP and the webhook calls are standard library; S3 is reached with a container run on demand.
- A dump is streamed, never held: memory use does not depend on the size of a database.
- Storage keys, SMTP passwords and webhook URLs are sealed at rest and reach a process only through `0600` files or its environment, never its arguments.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| S3 client | `rclone/rclone` run on demand, configured through an env file | The spec's `minio/mc` image is no longer published with updates. rclone is maintained, speaks every S3 dialect, does multipart uploads for files over 5 GB, and can list and delete for retention |
| Compression | gzip in the stream between `docker exec` and the file, by the standard library | No dependence on a shell with `pipefail` inside each image: a dump that fails is seen as a failed command, not as a short file |
| Dump commands | From the engine templates (phase 3). MongoDB's is rewritten to read its password from a file | Phase 3's review: a password must not be in an argument list |
| Engines without a dump command | KeyDB, Dragonfly and ClickHouse offer no backup in this phase; the page says so | Their tools differ enough to deserve their own work; better absent than wrong |
| Restore | Into the same database, after typing its name. The database keeps running; the engine's restore command replaces its contents | The spec's design. A restore into a new database is: create one, then restore there (the backup can be downloaded and uploaded) — out of scope here |
| Scheduler | One goroutine wakes at the top of each minute, asks the database which schedules are due, and queues a job for each | No timer per schedule; a schedule's next run is stored, so a restart neither repeats nor skips more than the runs that fell in the downtime (those are skipped, not replayed) |
| Cron syntax | Five fields with `*`, lists, ranges and steps, plus `@hourly`, `@daily`, `@weekly`, `@monthly`; evaluated in UTC | The common subset. The time zone is stated on every form |
| Scheduled tasks | A command run with `docker exec` in an app's serving container; output kept in a capped log per run; the last 50 runs are kept | As the spec. Tasks for services and databases can follow once the shape is proven |
| Notification channels | Email (SMTP with STARTTLS or implicit TLS), Discord, Slack, Mattermost, Telegram, Pushover, generic webhook | The spec's list. Each is one small function over `net/http` or `net/smtp` |
| Where webhooks may go | Not to loopback, link-local or unspecified addresses, checked at connection time | A channel's URL is typed by a team member; it must not become a way to make the dashboard call itself or a cloud metadata service. Private network ranges are allowed: a self-hosted Mattermost lives there |
| Docker cleanup | Daily: `docker image prune` (dangling images only), `docker builder prune` of cache older than a week, and removal of stopped containers musdash no longer refers to | `image prune --all` would delete the images kept for rollback and the image of every stopped app and database |
| Disk usage | Checked with the cleanup; a notification when the Docker data directory's filesystem is over 85 % full | The event the spec lists; one threshold, no setting yet |

## Data model (migration `0009_operations.sql`)

| Table | Columns |
|---|---|
| `s3_storages` | `id`, `team_id`, `name`, `endpoint`, `region`, `bucket`, `prefix`, `access_key` (sealed), `secret_key` (sealed), `created_at` |
| `backup_configs` | `id`, `database_id` (unique), `schedule`, `keep` (how many to keep), `storage_id` (optional), `enabled`, `next_run`, `created_at` |
| `backups` | `id`, `database_id`, `status`, `trigger`, `file`, `size`, `storage_id` (where a copy went), `error`, `started_at`, `finished_at`, `restore_status`, `restore_error`, `restored_at` |
| `scheduled_tasks` | `id`, `app_id`, `name`, `schedule`, `command`, `enabled`, `next_run`, `created_at` |
| `task_runs` | `id`, `task_id`, `status`, `exit_code`, `error`, `started_at`, `finished_at` |
| `notification_channels` | `id`, `team_id`, `name`, `kind`, `config` (sealed JSON), `events` (comma separated), `enabled`, `created_at` |

## File map

```
migrations/0009_operations.sql
internal/cron/cron.go               parser and next-run calculation
internal/ops/ops.go                 the once-a-minute tick, recovery after a crash
internal/ops/backup.go, task.go, cleanup.go, notifier.go   the jobs
internal/backup/backup.go           dump, restore
internal/backup/s3.go               rclone on demand
internal/notify/notify.go           Send(event), channel kinds
internal/notify/dial.go             the address check
internal/db/operations.go
internal/web/handlers_operations.go
internal/web/pages/operations.templ
```

## Interfaces

```go
// internal/cron
type Schedule struct{ /* bit sets per field */ }
func Parse(expr string) (Schedule, error)
func (s Schedule) Next(after time.Time) time.Time // the first matching minute after t, in UTC

// internal/backup
func Dump(ctx, r runner.Runner, container, dumpCmd, dest string) (size int64, err error)
func Restore(ctx, r runner.Runner, container, restoreCmd, src string) error
func (s S3) Upload(ctx, r runner.Runner, file, name string) error
func (s S3) Prune(ctx, r runner.Runner, keep []string) error
func (s S3) Check(ctx, r runner.Runner) error

// internal/notify
type Event struct{ Kind, Title, Body, URL string; OK bool; TeamID string }
func (n *Notifier) Send(ctx, e Event)       // never blocks the caller for long, never fails it
```

## Tasks

### Task 1 — Cron and the scheduler
- [x] `cron.Parse` and `Next`: table tests including steps, ranges, lists, day-of-month with day-of-week (either matches, as in cron), month ends, leap days, the shortcuts, and every malformed form.
- [x] Scheduler tick: due schedules are claimed with one `UPDATE … RETURNING` that also stores the next run, so two ticks never queue the same run.

### Task 2 — Backups
- [x] `Dump`: `docker exec` → gzip → file, written under a temporary name and renamed; a failing dump leaves no file.
- [x] `Restore`: file → gunzip → `docker exec -i`.
- [x] Backup job: dump → record size → upload when a storage is set → retention locally and remotely → notify.
- [x] S3 through rclone: upload, list, delete, check; credentials in an env file removed afterwards.
- [x] Tests with the scripted Runner; with `MUSDASH_DOCKER_TEST=1`: PostgreSQL is backed up, changed, restored and has its old content; the upload lands in an S3 server run by `rclone serve s3`, and retention removes the oldest object.

### Task 3 — Scheduled tasks
- [x] Task job: `docker exec` in the app's serving container with a time limit; exit code, duration and capped output recorded; the last 50 runs kept.
- [x] Tests: a failing command is recorded as failed with its output; an app with no container records why nothing ran.

### Task 4 — Notifications
- [x] The seven channel kinds, each against `httptest` or a local SMTP listener.
- [x] The address check: loopback, link-local, unspecified and metadata addresses are refused, also when a public name resolves to one, also after a redirect.
- [x] Events wired in: deployment finished, backup finished, task failed, container stopped unexpectedly, disk nearly full.

### Task 5 — Docker cleanup
- [x] Daily job per server; what was reclaimed is logged; the disk check raises its event once per day at most.

### Task 6 — UI
- [x] Database page: Backups tab (schedule, retention, storage, "Back up now", list with size and state, restore, delete).
- [x] App page: Scheduled tasks tab (list, add, run now, history with output).
- [x] Settings: S3 storages (add, test, delete) and notification channels (add, test, choose events, delete).
- [x] Tests: validation, team scoping, secrets never rendered back.

## Outcome

- The scheduler, the jobs and the pages are in `internal/ops` and `internal/web`; the plan's `internal/jobs/scheduler.go` became `internal/ops` because the tick needs the database layer, which the queue package does not know.
- `TestBackupWithDocker` (package `backup`): a real PostgreSQL is dumped, changed and restored; the file is uploaded to, listed in and deleted from an S3 server run by `rclone serve s3`.
- By hand through the dashboard against real Docker: a bucket on a local S3 server was added and tested; a PostgreSQL database was backed up (a real `pg_dump` archive, mode 0600, copied to the bucket); a dropped table came back after Restore; with "keep 2" the third backup removed the first from the server and from the bucket; no keys file was left and no key appears in the server's log. A task ran in a real app container and showed its output.
- MongoDB's dump and restore now read the password from a private file made inside the container; the file part was checked against the real `mongo:8` image, the dump itself could not be (MongoDB does not start on this machine's kernel).
- Restore is a job, and its state is recorded on the backup it restores.
- A backup records which storage its copy went to, so a later change of storage still removes old copies from where they are.
- Idle memory on Linux after this phase: server 24.1 MB, proxy 17.6 MB at most (phase 4: 23.6 and 17.6). The scheduler is one goroutine that wakes once a minute.
- Not verifiable here, to check on the VPS: a real S3 provider, a real Discord (or other) channel, email through a real SMTP server, the disk-full notice (Docker Desktop's disk is not visible from the Mac), and a nightly run actually firing at its time over several days.

### Found by the independent review, and fixed

1. **Notification channels reached other people's containers** on the server's Docker networks, the server's own services and some metadata addresses, and showed what they answered. One rule set (`internal/netguard`) now refuses container bridge networks, the server's own addresses other than its web ports, and the metadata services; the answer of anything but the well-known public chat services is no longer shown, nor is how a connection failed.
2. **A bucket's endpoint was not checked at all.** It goes through the same rules, and the name is resolved here and handed to rclone already resolved, so it cannot answer differently a moment later. A redirect issued by the storage server itself is not covered: rclone follows it inside its container.
3. **Long tasks and backups could hold every job worker.** Their job kinds never take the last worker now (`jobs.Queue.Background`). Fairness between teams is for phase 8.
4. **Retention ran under a 15-second limit**, and a bucket that could not be reached kept old files on the server. It has ten minutes, removes the server's file first, and keeps the record until the copy is gone too.
5. **One slow channel delayed everyone's notifications.** Events are sent four at a time, each to its channels in parallel.
6. **The storage keys file had a fixed name**, so an upload and a delete in one directory could remove or read each other's. Each run has its own, and ones left by a crash are removed at start-up.
7. **A schedule saved between a tick's claim and its bookkeeping** got a stray run from the old expression; and a failed store left a schedule silent until a restart. The store only applies to a schedule still claimed, and each tick settles what an earlier one left.
8. **A task on a stopped app notified at every firing.** The run is still recorded as failed; nobody is told.
9. **A run whose task could not be loaded stayed "running".** It is ended.

A schedule missed during downtime fires once when musdash is back. The plan's table said "skipped"; firing once is what a nightly backup should do, and the documentation now says so.

## Review focus

1. **A dump that fails halfway and is kept as a good backup** — the dump test kills the command mid-stream.
2. **A restore into the wrong database** — typed confirmation and team scoping tests.
3. **Storage keys or webhook URLs in a command line, a log or a page** — asserted in the tests.
4. **A notification URL that reaches the dashboard itself** — the address check, including redirects and DNS answers.
5. **The scheduler running a job twice, or never, around a restart** — claim test with two ticks and with a stale `next_run`.

## Self-review of this plan

- Spec coverage for phase 5 rows: scheduled backups, S3, retention, restore (task 2, 6); scheduled tasks and history (3, 6); notifications and events (4, 6); Docker cleanup (5).
- Backups cover PostgreSQL, MySQL, MariaDB, MongoDB and Redis. The other three engines are named as not covered.
- Real S3 and real Discord cannot be reached from the tests; an S3-compatible server and `httptest` stand in, and the real ones are for the VPS.
- Interface names match across tasks.
- No placeholders.

**Approved for implementation.**
