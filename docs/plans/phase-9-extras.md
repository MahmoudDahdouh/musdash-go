# Phase 9 — Extras: implementation plan

**Goal:** deploy from GitLab, Bitbucket and Gitea as well as GitHub; build a repository that has no Dockerfile with Nixpacks or Railpack; see what a server and its containers use; and open a terminal in a running container from the browser.

**Spec:** `docs/spec.md` section 4.1 (Nixpacks, Railpack, container terminal, other Git hosts) and 4.6 (metrics). Builds on phases 0–7. Phase 8 (teams, roles, API) has not been built; the spec says phases 5–9 are independent, and nothing here needs it.

**Done when:** the spec says "as needed". Here: a push webhook from each of the three hosts deploys the app it belongs to; a repository without a Dockerfile is built and served with each of the two builders on real Docker; a server's page shows its load, memory and disk and each resource's page its own use; a shell opened in the browser runs `vi` and `top` in an app's container, locally and over SSH.

**Where it is built:** phase 8 was being built on `main` at the same time, in the same checkout. This phase is built on the branch `phase-9` in a worktree of its own and merged when both are done; its migration is `0014` because phase 8 has `0013`.

## Global constraints

- Earlier constraints hold. **No new modules**: the spec allows `coder/websocket` and `creack/pty` for this phase as optional; both are replaced by a few hundred lines of standard library (see the decisions).
- **No new front-end library.** The terminal is drawn by a script of our own, a few kilobytes, loaded on the terminal page only. The charts are SVG written by the server.
- Idle memory does not move: nothing in this phase runs unless a page is open, a build is running, or sampling was switched on for a server. An asset is read into memory when it is first asked for, not with the others.
- A terminal is a shell in a container, never on the server itself. What is typed goes to that container and nowhere else.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| WebSocket | A server-side implementation of our own in `internal/web/ws.go`: the handshake, masked frames in, unmasked frames out, ping, close, messages of at most 64 KB. No extensions, no compression | The terminal is its only user, and it needs a tenth of what a library offers. A module is avoided |
| A terminal on the local server | `/dev/ptmx` opened with the `syscall` package (Linux and macOS), the command started as the leader of a session of its own with the terminal as its controlling one | What `creack/pty` does, in sixty lines, for the two systems musdash runs on |
| A terminal on a remote server | An SSH session with `RequestPty`; a resize is `WindowChange` | As the spec says |
| xterm.js | Not used. `static/terminal.js` is a terminal emulator of our own: a grid of cells, the escape sequences shells and full-screen programs use (cursor movement, erase, insert and delete, scroll regions, the alternate screen, SGR colours with 256 and 24-bit, bracketed paste, application cursor keys), scrollback of 2000 lines, drawn as rows of text | xterm.js is about 290 KB, three times everything else the dashboard sends, and its renderer writes `<style>` elements, which the content policy forbids. A shell, `vi`, `top`, `psql` and `less` need a small part of it. Text drawn as text can be selected and copied by the browser |
| What the terminal claims to be | `TERM=xterm-256color` | Every image has that entry; the emulator implements what programs use of it. What it does not: mouse reporting, sixel, double-width characters (drawn, but counted as one cell) |
| What a terminal runs | `docker exec -it <container> sh -c '<fixed text>'`, the text choosing `bash` when the image has it and `sh` otherwise | Nothing a person typed is part of the command |
| Which containers | An app's serving container, a database's container, and one container of a service chosen from a list | The spec's "container terminal" |
| Who may open one | A signed-in member of the team that owns the resource. The upgrade request must come from the dashboard's own origin, and the first message must carry the session's CSRF token | A WebSocket is not covered by the form token, and `SameSite=Lax` lets an app on a sister domain send the cookie |
| How many | Eight terminals at once across the dashboard; one that carries nothing for thirty minutes is closed | Each holds a connection, a goroutine pair and a `docker exec` |
| When the browser leaves | The terminal is closed, which hangs up the shell in the container | Docker ends an exec whose terminal went away |
| Metrics on demand | `docker stats --no-stream` for a resource's containers and a fixed shell script reading `/proc` and `df` for the server, run when a page asks and not otherwise; the page asks again every five seconds while it is open | The spec's rule 3 |
| Sampling | Off by default, switched on per server. Once a minute the scheduler takes one sample of the server and of every managed container on it and stores it; samples older than 24 hours are deleted | The spec's "optional 60 s sampling into a ring table" |
| A sample of a resource | The sum over the resource's containers, stored under the resource's id | A deployment replaces the container; the resource stays |
| A server that hangs while sampled | Each server is sampled in a goroutine of its own with a time limit, and a server still being sampled is skipped by the next tick | One server must not hold up the others, or the schedules |
| Charts | Inline SVG written by the server from the samples, at most 120 points a chart (averaged in SQL); no script | No chart library; nothing to load |
| What a server answers | Parsed into numbers by strict patterns, or dropped. Nothing of it is shown as text | It is data from a machine that is not trusted with the dashboard |
| Nixpacks, Railpack: where the binary runs | In a container made from an image musdash builds on first use (`musdash/nixpacks:<version>`, `musdash/railpack:<version>`): the release file is fetched by Docker's `ADD --checksum`, so nothing is installed on the server and nothing needs `curl` | The spec's "installed on first use", without a binary on the host. The builder reads a repository; it does so where it can read nothing else |
| The builder's sandbox | No Docker socket, no capabilities, a read-only root, the checkout mounted read-only. Nixpacks also has no network. Railpack has: it asks the network which versions of a language exist | The plan is made from files somebody else wrote |
| Nixpacks: from plan to image | `nixpacks plan` gives the variables; `nixpacks build --out` writes `.nixpacks/Dockerfile` next to the app, into the checkout, which that one run may write to. The sandbox runs as the account that owns the checkout. Before the build, the server is asked that neither `.nixpacks` nor the Dockerfile in it is a link, in the repository and on disk | With `--out` Nixpacks writes only its own files and expects them inside the app's directory, which is the build context. The checkout is thrown away after the build |
| Railpack: from plan to image | `railpack prepare` prints the plan, which is written next to the checkout; `docker build` with the Railpack frontend (`BUILDKIT_SYNTAX`, pinned to the same version) reads it. Build variables are passed as build secrets by name | Railpack's documented way of building without its own CLI doing the build |
| Variables a plan names that the app does not have | Passed to `docker build` on the command line (`--build-arg NAME=value`): they come from the builder or the repository and are not secret. The app's own build variables keep travelling through Docker's environment by name | A repository must not be able to set an environment variable of the Docker command on the server (`nixpacks.toml` can name any variable) |
| The port | For these two build packs the container gets `PORT=<the app's port>` unless the app sets `PORT` itself | Both builders start apps that listen on `$PORT` |
| Start, install and build commands | Through the builders' own variables (`NIXPACKS_START_CMD`, `RAILPACK_BUILD_CMD`, …) set as build-time variables, or the repository's `nixpacks.toml` / `railpack.json` | No new columns; the page says so |
| Versions | Pinned in the code with the SHA-256 of each release file | A build must not change because a release was replaced |
| GitLab | Push and merge request events, authenticated by the secret in `X-Gitlab-Token`, compared in constant time. GitLab's newer signing token is not accepted | GitLab does not sign with a secret the receiving side chose: a signing token is generated by GitLab, and musdash has nowhere to enter one. The secret token travels inside TLS |
| Bitbucket Cloud | Push and pull request events, authenticated by `X-Hub-Signature: sha256=…` | Bitbucket's header |
| Gitea, Forgejo, Gogs | As GitHub (`X-Hub-Signature-256`), and `X-Gitea-Signature` / `X-Gogs-Signature` (the bare hex) for versions that send only that | Their payloads are GitHub's |
| Which host sent a webhook | Not asked. Every accepted way of proving the secret is tried against the one secret, and the body is read for every shape at once | Headers that name the host or the event are not signed |
| A pull request event without an action (Bitbucket) | An open pull request is deployed when its commit is not the one its preview last deployed; a merged or declined one is closed | Bitbucket says what happened in a header, which is not signed. A comment on a pull request then deploys nothing |
| A push of several branches (Bitbucket) | Each branch in the event is looked at, up to sixteen | One push can move several |
| Private repositories on these hosts | A deploy key, as now. The Sources and Settings pages say where each host wants the key and the webhook | The spec's "deploy key + per-provider webhook parser" |
| Preview comments | GitHub only | The others would need an API token per host |
| Bitbucket Data Center | Not in this phase | Its payloads are a third shape |

## Data model (migration `0014_extras.sql`)

- `servers.metrics` (0/1): whether the server is sampled. The file is `0013_extras.sql` on the branch, where migration numbers must be consecutive, and becomes `0014` when phase 8's `0013` is merged.
- `metric_samples (server_id, resource_id, at, cpu, mem, mem_total, load, disk_used, disk_total)`, primary key `(server_id, resource_id, at)`, `WITHOUT ROWID`. `resource_id` is `''` for the server itself. `cpu` is in hundredths of a percent of one core for a resource and of the whole machine for a server; the rest are bytes, `load` hundredths.
- No change to `apps`: `build_pack` takes two more values.

## File map

```
migrations/0014_extras.sql
internal/source/webhook.go, providers.go     the three shapes of body; the ways of proving the secret
internal/web/handlers_webhooks.go            every way tried; several branches; delivery ids
internal/docker/build.go                     context on standard input; plain build args; secrets
internal/deploy/builders.go                  the builder images, the sandbox, plan to image
internal/metrics/                            host script and its parser, docker stats and its parser
internal/db/metrics.go                       samples: insert, history, prune
internal/ops/metrics.go                      the sampler
internal/web/ui/chart.templ                  the SVG chart
internal/web/handlers_metrics.go, pages/metrics.templ
internal/runner/terminal.go, pty_linux.go, pty_darwin.go, ssh.go
internal/servers/servers.go                  a remote terminal borrows the pooled connection
internal/web/ws.go                           WebSocket
internal/web/handlers_terminal.go, pages/terminal.templ
internal/web/static/terminal.js              the emulator
internal/web/static/static.go                assets read when first asked for
```

## Interfaces

```go
// internal/runner
type Terminal interface {
	io.Reader                     // what the command prints; io.EOF when it has ended
	io.Writer                     // what is typed
	Resize(cols, rows int) error
	Close() error                 // ends the command
}
// Runner gains:
Terminal(ctx context.Context, c Cmd, cols, rows int) (Terminal, error)

// internal/source
type Proof struct{ Hub256, Hub, Bare, Token string }
func ReadHook(body io.Reader, secret []byte, p Proof) (Event, bool)
type Event struct { Push Push; Pushes []Push; PR PullRequest }

// internal/metrics
func Host(ctx context.Context, r runner.Runner, dir string) (HostSample, error)
func Containers(ctx context.Context, r runner.Runner, names ...string) ([]ContainerSample, error)

// internal/web
func acceptWS(w http.ResponseWriter, r *http.Request) (*wsConn, error)
```

## Tasks

### Task 1 — Git hosts
- [x] `source`: bodies of GitLab (push, merge request) and Bitbucket Cloud (push with several changes, pull request) read in the same single pass as GitHub's; `ReadHook` with every way of proving the secret, each compared in constant time, the body always read to its end.
- [x] `web`: the Git webhook accepts all of them; each pushed branch is compared; delivery ids of the three hosts; a pull request event without an action.
- [x] `deploy`: `SyncPreview` skips an event whose commit the preview already deployed, when the event says no more than "open".
- [x] Pages: where each host wants the deploy key and the webhook, and which events to tick.
- [x] Tests: each host's documented payloads; a wrong token, a token for another app, a signature over another body; a GitLab fork; a Bitbucket push of two branches; indistinguishable answers kept.

### Task 2 — Nixpacks and Railpack
- [x] `docker.BuildSpec`: context from standard input, plain build arguments, secrets by name, a frontend.
- [x] `deploy/builders.go`: builder image on first use; the sandbox commands; Nixpacks plan → variables → tar → build; Railpack prepare → plan file → build.
- [x] `PORT` for these packs; the build-pack choice on the forms; hints.
- [x] Tests with the scripted runner: the commands, the sandbox flags, a plan that names `DOCKER_HOST`, a builder that fails, a plan that is not JSON. With real Docker (`MUSDASH_DOCKER_TEST=1`): a small app built and served with each.

### Task 3 — Metrics
- [x] `internal/metrics`: the host script and `docker stats`, parsed strictly; tests with real output, garbage and hostile output.
- [x] Migration, queries, the sampler in `ops` with its per-server guard and the 24-hour ring.
- [x] `ui.Chart`; the Metrics tab of apps, databases and services; a server's usage page with the sampling switch.
- [x] Tests: sampling stores and prunes; a hanging server does not hold the tick; pages of another team's resources answer 404.

### Task 4 — Terminal
- [ ] `runner`: `Terminal` for the local machine, SSH, the pool's remote runner and the test fakes; tests against a real shell locally and through `sshtest` (a resize is seen by `stty size`; closing ends the process).
- [ ] `web/ws.go` with tests against a hand-written client: handshake, masking, fragments, ping, close, an oversized message, an unmasked frame.
- [ ] Handlers: origin, CSRF in the first message, the limit, the idle timeout, shutdown; pages and the tab.
- [ ] `terminal.js` and its styles; assets read lazily.
- [ ] Verified in a browser against real containers: a shell, `vi`, `top`, paste, resize, copy.

### Task 5 — End
- [ ] Independent review; README; CLAUDE.md; idle memory on Linux.

## Review focus

1. **A terminal reached by someone who should not** — another team's resource, a page on another origin, a signed-out browser, a container that is not the resource's.
2. **A webhook accepted without the secret** — the new ways of proving it, an empty secret, a header for one way and a body for another, timing.
3. **A repository steering the build off its path** — plan variables as environment of the Docker command, the tar stream as build context, the sandbox's mounts.
4. **Numbers from a server used as anything but numbers.**
5. **Memory** — what a terminal, a metrics page and the sampler hold; what the idle process holds (nothing new).

## Self-review of this plan

- Spec coverage for the phase 9 row: web terminal (task 4), metrics (3), Nixpacks and Railpack (2), GitLab, Bitbucket, Gitea (1).
- The spec's optional modules are not taken; the two things they would have done are small and tested here against real processes.
- The largest risk is the emulator: it is ours to get right. It is checked in a browser against real programs, and the protocol under it (bytes both ways, a resize message) is what xterm.js would speak, should it ever replace it.
- What cannot be verified here: real GitLab and Bitbucket deliveries (their documented payloads are used), and a terminal through a real sshd (the test server speaks the same protocol).

**Approved for implementation.**
