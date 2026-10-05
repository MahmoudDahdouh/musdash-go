# Phase 8 — Access: implementation plan

**Goal:** more than one person can use an install, each with a role; a person can protect their sign-in with a second step; values can be shared between resources instead of being typed into each; resources can be tagged and deployed together; and a script can read and operate an install with a token.

**Spec:** `docs/spec.md` sections 4.7 (teams, roles, invitations, shared variables, two-factor, API tokens and REST API) and 4.1 (tag-based bulk deploys). Builds on phases 0–7.

**Done when:** a Member cannot delete a server; the API can trigger a deploy.

## Global constraints

- Earlier constraints hold. No new modules: TOTP is `crypto/hmac` + `crypto/sha1`, JSON is `encoding/json`.
- No new goroutine and no cache of rows: a role is read with the session on every request, a token with every API call.
- Every new table that belongs to a team carries `team_id`, and every query on it filters on it.
- Nothing that authenticates a browser (the session cookie) is accepted by the API, and nothing that authenticates a script (a bearer token) is accepted by a page: the first would make every API route a CSRF target.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| How many teams | One per install, as now. This phase adds its members | A second team would have no server (the local one belongs to the first) and no say over the install's settings. What it may do is a product question of its own; the data model has carried a team id since phase 0 and still does |
| What an account is | A member of the team. Removing a member deletes the account, with its sessions, tokens and second step | An account in no team could not sign in, and could not be invited again while its email was taken |
| What the roles are for | A Member works with what is in projects: apps, databases, services, their variables, domains, backups, tasks, tags. An Admin also manages what the team itself has: servers, Git sources and deploy keys, backup storage, notification channels, the dashboard's settings, team and server variables, invitations. An Owner also decides who is an Owner | One line a person can remember, and one a route table can show |
| What a Member may read | Everything except Settings. Pages a Member cannot change are drawn without their forms | A Member picks a server, a source and a backup storage by name, and needs a deploy key's public half |
| What roles do not limit | What a container is given. A Member deploys containers on the team's servers, and the rules for what a container may have of a server (phases 1 and 4) are the same for everybody | A role that could deploy but not mount would need every Compose file and every `docker run` option judged by who saved it, and a stack from Git changes with a push |
| Where a role is checked | In the route table: every route is registered with who may call it (`anyone`, `signed out`, `member`, `admin`, `owner`), and the table is kept for the tests | One place to read, and a test can walk it: a Member is refused at every admin route without the list being written twice |
| A refused request | 403 with a page that says which role is needed; nothing is looked up first | Not a 404: the person can see the page exists. The id in the path is never read |
| The last Owner | Cannot be removed or given another role | An install nobody can manage |
| Who changes a role | An Owner, anybody's but their own when they are the last Owner. An Admin changes nobody's, and invites nobody above Member | An Admin who could make Admins could also unmake the one who made them |
| Who removes a member | An Owner, anybody else. An Admin, Members only | Same line |
| Invitations | A row with the email, the role (Member; an Owner may also invite an Admin), a token stored as its hash, and seven days to live. The link is shown once to the person who made it, to send however they like. Opening it asks for a name and a password and creates the account | There is no outgoing mail for the install itself (a password reset is a link printed by the command line), and a notification channel is the team's, not the install's |
| An invitation for an email that has an account | Refused when it is made: with one team, that person is already a member | — |
| A forgotten password | An Owner (for anybody else) or an Admin (for Members) makes a one-hour reset link from the Team page, shown once. The command line still can | Until now it needed a shell on the server |
| Second step | TOTP (RFC 6238: SHA-1, six digits, thirty seconds), one step either side accepted. The secret is sealed with the master key. A code is accepted once: the step it belongs to is stored, and only a later one is taken | Standard, and it is what every authenticator app speaks. A code seen over a shoulder cannot be used again |
| Setting it up | The page shows the key as text and as an `otpauth://` link. No QR code | A QR encoder is three hundred lines that could not be checked against a real scanner here. Authenticator apps and password managers take the key or the link |
| Turning it on and off | On: the current password, then a code from the app. Off: the password and a code | A session left open must not be enough to put somebody else's phone on the account, or take it off |
| Recovery codes | Ten, shown once when the second step is turned on, stored as hashes, each used once. They are long enough (60 bits) that a hash without a work factor is sound | For a lost phone |
| Between the password and the code | A sealed cookie that names the account and expires in five minutes. No session exists until the code is right | No table, and nothing to clean up |
| Guessing codes | Five tries in fifteen minutes for an account, counted by account and not by address | A code has a million values and three are right at any moment; by address, an attacker with the password and many addresses could try them all |
| A lost phone and no recovery codes | An Owner turns another member's second step off from the Team page (an Admin, a Member's); `musdash disable-2fa <email>` does it from the server | — |
| Shared variables | Used by naming them: `{{team.NAME}}`, `{{project.NAME}}`, `{{environment.NAME}}`, `{{server.NAME}}` inside the value of an app's or a service's variable. Nothing is handed to a container that did not ask for it | Coolify's convention. The other way (everything in a project inherits its variables) gives every container every secret and needs a rule for which one wins |
| When they are resolved | At each deployment, from what is stored then. A name that does not exist fails the deployment and says which | The spec's "resolved at deploy time". An empty value in its place would be found only when the app misbehaves |
| What is expanded | Only that exact form, in values. A shared variable's own value is taken as it is, so one cannot name another | No loops, and `{{ .Something }}` in a value for a template engine is left alone |
| Who sees a shared value | Whoever may change it. A Member sees the names of team and server variables | A Member can deploy a container that prints them, so this is not a wall; it is that a page should not show what its reader cannot edit |
| Tags | A tag is a short name on an app or a service. There is no table of tags by themselves: a tag exists while something has it | Nothing to create first and nothing left over |
| What a tag deploys | Every app and service that has it, each as the push webhook would: nothing new is queued behind a deployment that is already waiting | One rule for every way of asking |
| Databases and previews | Not tagged | A database is started, not deployed; a preview has no settings of its own |
| API tokens | A person's own, for the team: a name, `read` or `deploy`, an optional end date. Stored as SHA-256, shown once. It acts as its person: when they are removed it is gone, and it never does what their role may not | The spec. A token is 32 random bytes, so a plain hash is sound |
| `read` and `deploy` | `read`: every GET. `deploy`: also deploy, stop and start | What a pipeline needs is the second; what a status board needs is the first |
| What the API covers | What the pages show (servers, projects, apps, deployments, databases, services, tags) and what their buttons do (deploy, stop, start, deploy by tag). Not their forms: creating and configuring stay in the dashboard | The spec says "the same handlers with JSON responses". The pages' handlers are form handlers whose answer is a page; the API shares what is underneath them (loaders, queries, the deployer) and has small handlers of its own |
| What the API never returns | A sealed value, a hash, or a variable's value | Its structs are written out field by field; a row is never marshalled as it is |
| The deploy endpoint from phase 2 | Stays. `POST /api/v1/deploy?uuid=…` also takes an API token, several ids, and `tag=` | Existing pipelines keep working |
| Rate limits | By address before anything is looked up (600 calls a minute), and 120 calls a minute per token after | A stolen token cannot be used to keep the server busy, and guesses cost nothing to refuse. The address's allowance is the larger one: pipelines share addresses, and with equal numbers a token's own limit could never be reached |

## Who may do what

| | Member | Admin | Owner |
|---|---|---|---|
| Projects, environments, apps, databases, services: create, change, deploy, delete | yes | yes | yes |
| Project and environment variables; tags; own API tokens; own second step | yes | yes | yes |
| Servers, sources, deploy keys: see | yes | yes | yes |
| Servers, sources, deploy keys: add, change, remove | — | yes | yes |
| Settings (dashboard, backup storage, notifications) | — | yes | yes |
| Team and server variables: change, and see values | — | yes | yes |
| Invite; cancel an invitation; rename the team | — | yes (invites Members) | yes (invites Members and Admins) |
| Remove a member, make a reset link, turn a second step off | — | Members | anybody else |
| Change a role | — | — | yes |

## Data model (migration `0013_access.sql`)

- `users` gains `totp_secret` and `totp_pending` (sealed, `''` when unset) and `totp_step` (the last step a code was accepted for).
- `recovery_codes (user_id, code_hash)`, primary key both, cascade from `users`. A used code is deleted.
- `invitations (id, team_id, email, role, token_hash, invited_by, created_at, expires_at)`: `token_hash` unique; one per `(team_id, email)`.
- `api_tokens (id, user_id, team_id, name, token_hash, ability, created_at, last_used_at, expires_at)`: `token_hash` unique; cascade from `users` and `teams`.
- `shared_vars (id, team_id, scope, scope_id, key, value)`: `scope` is one of `team`, `project`, `environment`, `server`; unique `(scope, scope_id, key)`. Rows of a project, an environment or a server go when it does, by triggers: the cascade from a project to its environments is the database's, and no code of ours runs for it.
- `resource_tags (team_id, tag, resource_kind, resource_id)`, primary key all four. Removed with the app or the service in the same transaction as `domains` and `env_vars`.

## File map

```
migrations/0013_access.sql
internal/auth/totp.go                    codes, the key, the otpauth link
internal/db/team.go                      members, invitations, removal
internal/db/access.go                    second step, recovery codes, API tokens
internal/db/shared.go, tags.go
internal/deploy/shared.go                {{scope.NAME}}: find, expand
internal/deploy/pipeline.go, build.go, service.go   expand where variables are written
internal/web/routes.go                   the route table and who may call each
internal/web/handlers_team.go            members, invitations, the accept page
internal/web/handlers_twostep.go         set up, sign in with a code, recovery codes
internal/web/handlers_shared.go, handlers_tags.go, handlers_tokens.go
internal/web/api.go                      /api/v1: token check, limits, JSON handlers
internal/web/pages/team.templ, access.templ, tags.templ
cmd/musdash/main.go                      disable-2fa
```

## Interfaces

```go
// internal/auth
func NewTOTPKey() string                                   // base32, 160 bits
func TOTPLink(issuer, account, key string) string          // otpauth://totp/…
func TOTPCheck(key, code string, now time.Time, after int64) (step int64, ok bool)

// internal/db
func (d *DB) ListMembers(ctx, teamID) ([]Member, error)
func (d *DB) SetRole(ctx, teamID, userID, role string) error      // ErrLastOwner
func (d *DB) RemoveMember(ctx, teamID, userID string) error       // ErrLastOwner; deletes the account
func (d *DB) CreateInvitation(ctx, Invitation) (Invitation, error) // ErrIsMember, unique → already invited
func (d *DB) AcceptInvitation(ctx, tokenHash, name, passwordHash string) (User, string, error)
func (d *DB) UseTOTPStep(ctx, userID string, step int64) error     // ErrNotFound when the step was used
func (d *DB) UseRecoveryCode(ctx, userID, codeHash string) error
func (d *DB) TokenByHash(ctx, hash string) (APIToken, error)       // with its person's role, live only
func (d *DB) SharedVars(ctx, teamID, scope, scopeID string) ([]EnvVar, error)
func (d *DB) SharedFor(ctx, teamID, projectID, environmentID, serverID string) (map[string]map[string]string, error) // sealed
func (d *DB) SetTags(ctx, teamID, kind, id string, tags []string) error
func (d *DB) Tagged(ctx, teamID, tag string) ([]App, []Service, error)

// internal/deploy
func SharedRefs(value string) []SharedRef                          // {Scope, Name}
func ExpandShared(value string, lookup func(scope, name string) (string, bool)) (string, []SharedRef /* missing */)
func (d *Deployer) DeployTag(ctx context.Context, teamID, tag, trigger string) (queued int, err error)

// internal/web
type access int // open, signedOut, member, admin, owner
func (s *Server) handle(mux *http.ServeMux, pattern string, who access, h http.HandlerFunc)
```

## Tasks

### Task 1 — Roles, members, invitations
- [x] Migration `0013` (the whole phase's tables).
- [x] The route table: every route registered with who may call it. `admin` and `owner` answer 403 before the handler runs.
- [x] `Shell.Role`; Servers, Sources and Settings drawn for a Member without what they cannot use; Settings left out of their navigation.
- [x] Team page: members with roles, change a role, remove, reset link, invitations (make, cancel), rename.
- [x] `/invite/{token}`: the form, the account, the session.
- [x] Tests: a Member is refused at every admin route in the table (the done-when: deleting a server), and reaches every member route; every route under Servers, Sources, Settings and Team that changes something asks for an Admin; role changes and removal by each role; the last Owner; an invitation used twice, expired, for a member's email, cancelled; a removed member's session and tokens are gone; the invitation token never in a log.

### Task 2 — Second step
- [x] `auth` TOTP with the RFC 6238 test vectors; a code is not accepted twice; a code outside the window.
- [x] Account page: turn on (password → key → code → recovery codes), turn off, new recovery codes.
- [x] Sign-in: password → code or recovery code → session. The sealed cookie between the two.
- [x] Team page: turn another member's off. `musdash disable-2fa`.
- [x] Tests: no session before the code; the limit on guesses; a recovery code once; the cookie expired, tampered with, or another account's; turning on needs the password; other sessions end when it is turned on.

### Task 3 — Shared variables
- [x] `SharedRefs` / `ExpandShared`, table-tested first: the exact form, several in one value, an unknown scope left alone, a missing name reported.
- [x] The four pages (team, project, environment, server), one template; names only for a reader who may not change them.
- [x] Expansion where an app's env file and build arguments are written, and where a service's variables are; a missing name fails the deployment with its name.
- [x] Saving an app's or a service's variables warns about names that do not exist yet. A shared variable's own value may not name another: refused when it is saved.
- [x] Tests: each scope resolves for an app and for a service; a preview resolves as its parent; another team's variable cannot be named; rows go with their project, environment and server.

### Task 4 — Tags
- [x] Tags on an app's and a service's settings page; the Tags page; a tag's page with "Deploy all".
- [x] `DeployTag`.
- [x] Tests: the name rule; a tag's page lists only the team's; deploy all queues one deployment each and none behind a waiting one; tags go with the resource; a preview cannot be tagged.

### Task 5 — API tokens and the API
- [x] Account page: tokens (make, shown once, revoke).
- [x] `/api/v1`: the token check, both limits, the handlers, JSON errors.
- [x] `POST /api/v1/deploy` with an API token, several ids, a tag.
- [x] Tests: no token, a wrong one, an expired one, a removed person's → 401, indistinguishable; a `read` token cannot deploy; a session cookie is not accepted by the API and a token is not accepted by a page; another team's id → 404; no response carries a sealed value or a hash; the per-token limit; the done-when: a deploy through the API.

### Task 6 — End
- [ ] Independent review; README and CLAUDE.md; RSS on Linux.

## Review focus

1. **A route a Member can reach that the table says they cannot** — the wrapper runs before the handler; the test walks the table. And the other way round: a route registered outside the table.
2. **Becoming more than one's role** — an Admin making an Owner, removing an Owner, resetting an Owner's password or second step; a Member inviting; a role in a form field.
3. **Signing in without the second step** — through the session cookie being set early, the sealed cookie being forged or reused for another account, a code used twice, a recovery code used twice, the password-reset link, or an invitation.
4. **A token that outlives its person, does more than its ability, or works as a session** — and a session that works as a token.
5. **A shared variable of another team, project or server reaching a container** — the lookup is keyed by the app's own team, project, environment and server.
6. **Secrets in the wrong place** — invitation, reset and API tokens in logs or redirects; TOTP keys and recovery codes after they were first shown; sealed values in JSON.

## Self-review of this plan

- Spec coverage for phase 8 rows: teams, invitations and roles (task 1), two-factor (2), shared variables at team, project, environment and server scope (3), tags and bulk deploys (4), API tokens, REST API and rate limiting (5).
- Memory: one limiter table more (bounded at 4096 entries, like the others); no goroutine; no cache.
- Checked against itself: an Admin could invite an Admin in the first draft while not being allowed to make one by changing a role; invitations now follow the same line.
- Narrower than the spec, on purpose: one team; the API does not create or configure. Both are in the table above with their reasons and listed again in the outcome.
- What cannot be verified here: an authenticator app reading the key (the codes are checked against the RFC's vectors).

**Approved for implementation.**
