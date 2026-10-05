# Phase 7 — Deploy polish: implementation plan

**Goal:** roll an app back to an earlier deployment; route by path as well as by host; put a password in front of a domain; and give each pull request of a Git app its own preview that goes away when the pull request is closed.

**Spec:** `docs/spec.md` sections 4.1 (rollback, pull-request previews) and 4.5 (path routing, basic auth). Builds on phases 0–6.

**Done when:** opening a pull request creates a preview address; closing it removes it.

## Global constraints

- Earlier constraints hold. No new modules: bcrypt for the proxy's passwords is already in `golang.org/x/crypto`.
- The proxy stays small: no per-request allocation that grows with the number of routes, no log by default.
- A preview runs code that is not yet merged. It must not be a way for someone who cannot push to the repository to run code with the app's secrets.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| What a rollback runs | The image of a chosen successful deployment, as it is on the server, with the app's settings of today | Settings and variables are not versioned. The page says so |
| Keeping images to roll back to | Every deployment's image gets a tag of the app's own (`musdash/<app>:<commit>` for builds, `musdash/<app>:d-<deployment>` for pulled images); the newest five are kept | A pulled tag such as `nginx:latest` moves. The image it pointed at becomes untagged and the daily clean-up would remove it |
| Which images are the newest five | Asked of the deployments table, not of Docker | Docker orders images by when they were made. A pulled image, or one that was rolled back to, can be old and still be what ran last |
| Two deployments of one commit | Share one image name; a rebuild of the commit replaces what the earlier deployment would roll back to | The name is the commit. A rebuild with other build variables is rare, and the alternative is an image per deployment |
| A rollback whose image is gone | Fails at once and says the image is no longer on the server | Rebuilding an old commit is a deployment of that commit, not a rollback; it is not done silently |
| Path routing | A domain row gets a path prefix. The proxy picks the longest prefix that matches on a segment boundary: `/api` matches `/api` and `/api/x`, not `/apix`. The path is passed on unchanged unless "strip prefix" is set | The usual meaning. Stripping is a switch because apps differ in whether they expect it |
| One host, several apps | Allowed when the paths differ; `(host, path)` is unique instead of `host` | That is what path routing is for |
| Certificates | Per host, as before. A host is requested when any of its paths wants TLS | One certificate serves all paths of a host |
| Basic auth | A user name and a bcrypt hash on the domain row; the proxy asks for them before passing a request on | The spec's "fields on the route" |
| Cost of bcrypt per request | The proxy remembers credentials it has verified for five minutes, as a hash, in a table of at most 256 entries | A bcrypt comparison takes tens of milliseconds of CPU; a page with thirty assets would otherwise cost seconds |
| Who may share a host | Resources of one team, on one server. Another team is told the domain is taken, whatever the path; the same team on another server is told the domain is routed there | Two teams on one host could take each other's traffic by choosing a longer path. Two servers cannot both be where the host's DNS points |
| The dashboard's own domain | No path of it can be given to an app | A page under it would be the dashboard's origin to a browser |
| A path that is not in its simplest form | On a host where the path decides the route or a password guards one, `/public/../admin` and `//admin` are redirected to `/admin` first | What the proxy matches is then what the app receives |
| The password after the check | The `Authorization` header is not passed on to the app | The password is the proxy's; an app's logs are not the place for it |
| Guessing passwords | One bcrypt comparison at a time across the proxy; a request waits up to five seconds for its turn | A flood of wrong passwords must not take the CPU from every site |
| Service endpoints | Keep a whole host, without a path or a password, for now | Their domain form replaces the row; it is a separate change |
| The routes file | Written `0600` | It now holds password hashes. The proxy runs as the account that writes it |
| What a preview is | An app of its own, a child of the app it previews: same repository and build settings, the pull request's branch, a copy of the parent's variables, its own container and address. It is listed under the parent, not among the environment's apps | The whole pipeline (clone, build, health check, routes, logs, delete) then applies unchanged |
| Which pull requests get a preview | Only those whose branch is in the same repository. A pull request from a fork gets none | A fork's code would be built and run with the app's variables by someone who cannot push to the repository |
| A preview's address | `pr-<number>-<app>.<base>` where `<base>` is the app's preview domain if one is set, otherwise a generated address on the server's IP | A wildcard DNS record `*.preview.example.com` gives every pull request a name without touching DNS again |
| Variables and files | Not copied: a preview is built and run with its parent's, as they are at each deployment (`App.ConfigOwner`). `MUSDASH_PREVIEW=1` and `MUSDASH_PULL_REQUEST=<n>` are added | No second copy of the secrets to keep in step or to clean up. It also means a preview talks to the same database unless the app looks at `MUSDASH_PREVIEW` |
| Volumes and server directories | A preview gets none of its parent's | A directory of the server would be production's own data; a volume would be one more thing to remove with every pull request |
| Which branch a pull request must go into | The app's own | A preview shows what the app would become |
| A preview's own settings | None. Its pages for variables, storage, tasks, domains and settings are closed; it can be deployed, stopped, rolled back, read and removed | Everything it runs with is its parent's |
| Removing a preview | A job under the preview's own deployment lock, so it waits for a build that is running | A pull request is often closed while its last push is still building |
| Deleting an app | Its previews go first, and the row cannot be deleted while one exists | A preview without a parent would be a running app that no page lists |
| Telling the pull request | A comment with the address when the first deployment succeeds, updated on later ones, through the GitHub App when it may write to pull requests; otherwise nothing | Best effort: an App created before this phase lacks the permission. The page explains how to grant it |
| Events | `pull_request` with action `opened`, `reopened`, `synchronize` deploys; `closed` destroys | From the GitHub App's webhook, and from a webhook added by hand that sends pull request events |
| Limits | At most ten previews per app at once | A burst of pull requests must not fill the server |

## Data model (migration `0012_deploy_polish.sql`)

- `domains` is rebuilt: `host` is no longer unique by itself; new columns `path` (default `''`), `strip_prefix`, `auth_user`, `auth_hash`; unique `(host, path)`.
- `apps` gains `preview_of` (the parent app, `''` for a normal app), `pr_number`, `previews` (whether pull requests get previews), `preview_domain`, `pr_comment_id`.
- `deployments` gains `rollback_of` (the deployment whose image is run again).

## File map

```
migrations/0012_deploy_polish.sql
internal/proxy/table.go, proxy.go     path lookup, basic auth, the verified-credentials table
internal/deploy/routes.go             path, strip, auth into routes.json
internal/deploy/pipeline.go           rollback; image tags of the app's own
internal/deploy/preview.go            create, update and destroy previews
internal/source/webhook.go            pull request events
internal/source/github.go             comment on a pull request
internal/web/handlers_apps.go, handlers_webhooks.go, pages/apps.templ
```

## Interfaces

```go
// internal/proxy
type Route struct {
	Host, Target, RedirectTo string
	TLS bool
	Path        string // "" or "/prefix"
	StripPrefix bool
	AuthUser    string
	AuthHash    string // bcrypt
}
func (t *Table) Lookup(hostport, path string) (Route, bool)

// internal/deploy
func (d *Deployer) Rollback(ctx context.Context, app db.App, to db.Deployment) (db.Deployment, error)
func (d *Deployer) SyncPreview(ctx context.Context, parent db.App, pr source.PullRequest) (db.App, error)
func (d *Deployer) ClosePreview(ctx context.Context, parent db.App, number int) error

// internal/source
type PullRequest struct{ Repo string; Number int; Action, Branch, HeadRepo, Commit string }
func ReadEvent(body io.Reader, secret []byte, header, event string) (Push, PullRequest, bool)
```

## Tasks

### Task 1 — Rollback
- [x] Tag every deployed image under the app's repository; keep five.
- [x] `Rollback`: a deployment with `rollback_of`; the pipeline skips pull and build, checks the image is there, and runs it through the same health check and switch.
- [x] Deployment page: "Roll back" on successful deployments other than the one serving.
- [x] Tests: order of commands (no pull, no build), the image gone, team scoping, a rollback of a rollback.

### Task 2 — Path routing and basic auth
- [x] Migration; domain form fields; uniqueness on `(host, path)`; validation of the prefix.
- [x] Proxy: longest-prefix lookup on segment boundaries, strip, `WWW-Authenticate`, the verified-credentials table with its bound and expiry.
- [x] Tests: lookup table cases; a request with no, wrong and right credentials; the hash never in a response or a log; the routes file's mode; certificates requested once per host.

### Task 3 — Pull request previews
- [x] `ReadEvent` reads push and pull request events from one signed body.
- [x] `SyncPreview` / `ClosePreview`; previews under the parent app's page; the parent's delete removes its previews.
- [x] Comment on the pull request through the GitHub App.
- [x] Tests: opened → a child app with the branch, variables and address; synchronize → redeploy, not a second child; closed → destroyed; a fork → nothing; the limit; another repository's event with a valid signature → nothing.

### Task 4 — End
- [ ] Independent review; README; RSS on Linux for the proxy with basic auth routes.

## Review focus

1. **A preview for code from outside the repository** — the head repository must equal the base repository; tested with a fork's payload.
2. **A path prefix that matches more or less than it says**, or lets `/admin` be reached as `/admin/../x` — lookup on the cleaned path, segment boundaries, table tests.
3. **Basic auth that can be skipped** — by a different host header, an HTTP request to a TLS host, a path trick, or the cache answering for another route.
4. **A rollback that runs an image of another app** — the image must be in the app's own repository and belong to a deployment of that app.
5. **Password hashes or preview secrets where they do not belong** — the routes file's mode, logs, pages.

## Self-review of this plan

- Spec coverage for phase 7 rows: rollback (task 1), path routing and basic auth (2), pull request previews (3).
- The proxy's memory: the credentials table is bounded at 256 entries of 32 bytes plus a time.
- Previews on a remote server follow from phase 6: a preview is an app.
- What cannot be verified here: a real GitHub pull request and its comment. The webhook payloads are GitHub's documented ones.

**Approved for implementation.**
