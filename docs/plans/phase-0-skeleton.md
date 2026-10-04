# Phase 0 — Skeleton: implementation plan

**Goal:** a `musdash` binary with `server`, `proxy` and `migrate` subcommands where a user can set up the first account, log in and create a project, and a test proves the idle RSS target.

**Spec:** `docs/spec.md` (sections 3, 5, 7, 8, 9, 10).

**Tech stack:** Go 1.27, `modernc.org/sqlite` v1.60.1, `golang.org/x/crypto` v0.57.0, `github.com/a-h/templ` v0.3.1070 (run with `go tool templ`), Tailwind 4.3.3 standalone CLI, htmx 2.0.11, Alpine 3.17.4.

## Global constraints

- Three external modules only. Everything else is the standard library.
- `CGO_ENABLED=0`. Idle RSS: server under 30 MB, proxy under 20 MB.
- Stream, never buffer command output. No in-memory caches of rows.
- Local commands use argument arrays; anything sent to a shell goes through `runner.Quote`.
- All secrets at rest are AES-256-GCM sealed with the master key.
- Light mode only, blue brand, no web fonts, no icon font. Every colour, size and radius comes from a token.
- Generated files (`*_templ.go`, `static/app.css`) are committed, so `go build` needs only Go.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| Name | `musdash` everywhere | User's choice |
| `internal/crypto` | Named `internal/secret` | Avoids shadowing the stdlib `crypto` package |
| `migrations/`, `templates/` | Each is a tiny Go package with an `embed.FS` | `go:embed` cannot reach parent directories |
| Cookie `Secure` flag | Set only when the request arrived over HTTPS | First login on a fresh VPS is `http://<ip>:8000`; an always-`Secure` cookie would make login impossible |
| First user | `/setup` is available only while the `users` table is empty | No default credentials |
| Password reset | `musdash reset-password <email>` prints a one-time link; email delivery arrives with notifications in phase 5 | No SMTP until phase 5 |
| Teams | `teams` and `team_members` exist from phase 0 with one default team | Phase 8 then adds roles without a data migration |
| IDs | 12-character lowercase base32 text ids | Safe in URLs, container names and DNS labels |
| Job serialisation | `jobs.lock_key`: at most one running job per key | Implements "one build per server" (RAM rule 6) |
| Static assets | Served from `embed`, gzip-compressed once at start, URL carries a content hash, `Cache-Control: immutable` | Small over the wire, no per-request work |

## Design system (built first)

Subject: a control panel an operator keeps open all day on a small VPS. The product's thesis is "light", so the interface is quiet, dense and fast, and it shows its own footprint.

- **Signature:** the sidebar footer shows musdash's own live memory use ("musdash · 21 MB"). Everything else stays restrained.
- **State rail:** every resource row and card carries a 3 px left rail coloured by state (running, deploying, stopped, failed). State is always also written as text.
- **Type:** system UI stack for text (zero bytes). `ui-monospace` stack for every machine fact: domains, image tags, ports, commit hashes, logs. Small uppercase tracked labels for section eyebrows. Body 14 px, minimum 12 px.
- **Colour tokens** (defined in `@theme`, default palette disabled):

| Token | Value | Use |
|---|---|---|
| `brand-50 … brand-900` | `#EEF4FF #DCE8FF #BDD2FF #8FB2FF #5A8BFA #2F66F0 #1A4FD8 #153FB0 #14368C #142F6E` | Brand scale; `brand-600` is the primary action |
| `ink` / `ink-soft` / `ink-mute` | `#0B1B33` / `#34445E` / `#5B6B84` | Text levels |
| `canvas` / `paper` / `sunk` | `#F5F7FB` / `#FFFFFF` / `#EDF1F7` | Page, card, inset surfaces |
| `line` / `line-strong` | `#DFE5EF` / `#C5CFDE` | Borders |
| `ok`, `warn`, `danger`, `idle` (+ `-soft` tints) | `#0E8A5F`, `#B45309`, `#C8321F`, `#7A889E` | Status |

- **Other tokens:** spacing on a 4 px base, radius `sm 4 / md 6 / lg 10`, two shadows (`raised`, `overlay`), motion 120 ms and 200 ms with `prefers-reduced-motion` honoured.
- **Components** (`internal/web/ui`, all templ, all reused by later phases): `Layout`, `AuthLayout`, `Sidebar`, `PageHeader`, `Button`/`LinkButton` (primary, secondary, ghost, danger; sizes sm/md), `Field` (label, hint, error), `Input`, `Textarea`, `Select`, `Checkbox`, `Card`, `Section`, `Table`, `Badge`, `StatusPill`, `Tabs`, `Modal`, `ConfirmButton`, `Toast`/`Flash`, `EmptyState`, `Code`, `KeyValue`, `LogView`, `Icon` (inline SVG set), `Breadcrumbs`.
- A gallery page at `/_ui` (only when `--dev`) renders every component for review.

## File map

```
go.mod, Makefile, .gitignore, README.md
cmd/musdash/main.go            subcommand dispatch: server, proxy, migrate, reset-password, version
migrations/embed.go            embed.FS of *.sql
migrations/0001_init.sql       users, sessions, teams, team_members, projects, environments, jobs, password_resets
internal/config/config.go      flags + env → Config; data dir layout; master key load/create
internal/secret/secret.go      Seal, Open, RandomID, RandomToken, RandomBytes
internal/db/db.go              Open (pragmas, pool), Migrate
internal/db/{users,sessions,projects,jobs}.go   queries
internal/auth/auth.go          password hash/verify, session create/lookup/destroy, CSRF, login limiter
internal/runner/runner.go      Runner interface, Cmd, ExitError
internal/runner/local.go       LocalRunner
internal/runner/quote.go       Quote, QuoteJoin
internal/jobs/jobs.go          Queue: Enqueue, Register, Start, Stop; retry + lock_key
internal/proxy/proxy.go        minimal listener (real routing arrives in phase 1)
internal/sysmem/sysmem.go      own RSS (Linux /proc, macOS getrusage)
internal/web/server.go         mux, middleware chain, static handler
internal/web/middleware.go     recover, security headers, session loader, CSRF check, require-login
internal/web/handlers_auth.go  setup, login, logout, reset, account
internal/web/handlers_projects.go  dashboard, projects, environments
internal/web/ui/*.templ        design-system components
internal/web/pages/*.templ     pages
internal/web/static/           app.css (built), htmx.min.js, htmx-sse.js, alpine.min.js, app.js
internal/web/assets/input.css  Tailwind entry with @theme tokens
test/rss_test.go               builds the binary, starts it, asserts RSS
```

## Interfaces later phases rely on

```go
// internal/runner
type Cmd struct {
    Name   string
    Args   []string
    Env    []string   // KEY=VALUE, added to the inherited environment
    Dir    string
    Stdin  io.Reader
    Stdout io.Writer
    Stderr io.Writer
}
type Runner interface {
    Run(ctx context.Context, c Cmd) error                       // streams; *ExitError on non-zero exit
    Output(ctx context.Context, c Cmd) ([]byte, error)          // stdout, capped at 1 MiB
    WriteFile(ctx context.Context, path string, mode fs.FileMode, r io.Reader) error
    ReadFile(ctx context.Context, path string) (io.ReadCloser, error)
    MkdirAll(ctx context.Context, path string, mode fs.FileMode) error
    RemoveAll(ctx context.Context, path string) error
    Close() error
}
func Quote(s string) string

// internal/secret
func New(key []byte) (*Box, error)                 // 32-byte key
func (b *Box) Seal(plain []byte) (string, error)   // base64(nonce|ciphertext)
func (b *Box) Open(sealed string) ([]byte, error)
func RandomID() string                             // 12 chars, [a-z2-7], first char a letter
func RandomToken(nBytes int) string                // URL-safe base64

// internal/jobs
type Handler func(ctx context.Context, payload []byte) error
func (q *Queue) Register(kind string, h Handler)
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, opts ...Option) (id string, err error)
// Options: WithLockKey(string), WithRunAfter(time.Time), WithMaxAttempts(int)
```

## Tasks

Each task ends with `go vet ./... && go test ./...` green, a self-review of the diff, and a commit pushed to `main`.

### Task 1 — Core: module, config, secret, db, runner, jobs
- [x] `go mod init github.com/MahmoudDahdouh/musdash-go`; add the three modules; `go get -tool` templ.
- [x] `secret`: tests first — seal/open round trip, tampered ciphertext fails, wrong key fails, `RandomID` shape and uniqueness over 10 000 draws.
- [x] `config`: master key is read from `MUSDASH_MASTER_KEY` or `<data>/master.key`; created `0600` on first run; a key file with wider permissions is refused.
- [x] `db`: `Open` applies the five pragmas and `SetMaxOpenConns(2)`; `Migrate` applies numbered files once, in order, each in a transaction, recorded in `schema_migrations`. Tests: fresh migrate, re-run is a no-op, pragmas read back, foreign keys enforced.
- [x] `runner`: `Quote` table test including empty string, single quotes, newlines, `$()`, backticks, unicode; `LocalRunner` tests for streaming stdout, exit code, context cancel kills the process, `Output` cap.
- [x] `jobs`: tests — a job runs once; a failing job retries with back-off up to max attempts then is `failed`; two jobs with one `lock_key` never overlap; jobs left `running` by a crash are requeued at start; `Stop` waits for running jobs.

### Task 2 — Design system and web shell
- [x] Vendor htmx, the SSE extension and Alpine; download the Tailwind CLI to `bin/` (git-ignored); `make generate`.
- [x] `input.css` with the tokens above; base layer for focus rings, form controls and reduced motion.
- [x] All components in `internal/web/ui`; `/_ui` gallery.
- [x] Static handler with hashed URLs, gzip, immutable caching. Test: correct `Content-Type`, `Content-Encoding`, 404 for unknown file.
- [x] Screenshot review of the gallery at 1440, 1024 and 375 px; fix what looks wrong.

### Task 3 — Accounts and projects
- [x] `auth`: bcrypt cost 12; passwords 10–72 bytes; sessions are 32-byte random tokens stored as SHA-256 hashes, 30-day sliding expiry; CSRF token per session; login limiter 5 failures per 15 minutes per IP + email.
- [x] Handlers: `/setup`, `/login`, `/logout`, `/reset/{token}`, `/account`, `/`, `/projects/new`, `/projects/{id}`, rename/delete project, add/delete environment. Deleting needs a typed confirmation.
- [x] Middleware order: recover → security headers → session → CSRF → route.
- [x] Tests (`httptest`): setup closes after the first user; wrong password does not reveal whether the email exists; POST without CSRF token is 403; a logged-out request to `/` redirects to `/login`; project and environment CRUD; a user cannot read another team's project (404).

### Task 4 — Proxy stub, memory readout, RSS test, docs
- [x] `proxy` subcommand listens and answers 404 with a plain page (routing in phase 1).
- [x] `sysmem.RSS()` and the sidebar readout, refreshed every 30 s by htmx.
- [x] `test/rss_test.go`: build with the release flags, start `server` and `proxy` with `GOMEMLIMIT`/`GOGC` from spec section 8, request five pages, wait, read RSS, fail above 30 MB / 20 MB. Skipped under `-short`.
- [x] `make rss-linux` runs the same test inside a `golang` Linux container for the authoritative number.
- [x] README: build, run, data directory, environment variables.

## Review focus

Inputs the spec implies but does not spell out; each has a test in the task that owns the code.

1. **Second start on an existing data directory** — must not re-run migrations or regenerate the master key (task 1).
2. **Crash mid-job** — a job stuck in `running` must be picked up again after restart (task 1).
3. **Shell metacharacters in any user-supplied string** — `Quote` must make them inert (task 1).
4. **Session cookie over plain HTTP on first install** — login must work at `http://<ip>:8000` (task 3).
5. **Two browser tabs, one logs out** — the other tab's next POST must redirect to login, not error (task 3).

## Self-review of this plan

- Spec coverage for phase 0: binary + subcommands (task 1, 4), SQLite + migrations (1), login (3), projects/environments (3), templ layout (2), job queue (1), `Runner` (1), RSS test (4). `pprof` behind a flag (RAM rule 8) was missing — added to task 4: `--pprof` exposes `/debug/pprof` on the loopback listener only.
- Types: `Quote`, `Runner`, `Box`, `Queue` names are used consistently above.
- No placeholders remain.

**Approved for implementation.**

## Outcome

- Idle memory, measured by `test/rss_test.go`: server 19.9 MB and proxy 14.1 MB on Linux (arm64 container); 26.7 MB and 19.2 MB on macOS, which counts mapped binary pages differently.
- Alpine.js was left out. The interactive behaviour the UI needs (dialogs, the mobile menu, typed confirmation, copy) is a 4 KB script driven by `data-` attributes. This saves 56 KB of JavaScript and lets the Content-Security-Policy forbid inline script and `unsafe-eval`, which Alpine's standard build needs.
- An independent review of the phase found 13 issues; all were fixed before the commit:
  1. The login limiter checked and counted in two steps, so parallel requests could all pass. It now counts the attempt atomically before the password is checked, and at most two password hashes run at once.
  2. Limiter keys used the full IPv6 address; they now use the /64.
  3. Limiter keys embedded the raw email; they now use a short hash, and an oversized email is truncated.
  4. A signed-in browser got 403 on the reset and sign-in forms; signed-out forms now accept the session's token too.
  5. Cancelling a command only killed the direct child after the grace period; the whole process group is now killed.
  6. `Queue.Stop` could still claim a job; it now checks for stop before every claim.
  7. `MUSDASH_*` variables, including the master key, were inherited by child processes; they are stripped, and the key is removed from the environment after it is read.
  8. Request paths were logged, which would include a password-reset token; logs now carry the route pattern.
  9. A job that crashed the process was requeued forever; one that is out of attempts is now failed at start.
  10. Changing the password had no attempt limit; it shares the limiter, keyed by account.
  11. Form bodies had no read deadline; they now have 30 seconds.
  12. Two processes migrating at once could race; the applied check is repeated inside the transaction.
  13. The memory test depended on the proxy's `-https` flag; both are in the same commit.
