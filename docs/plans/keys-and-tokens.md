# Keys & tokens: one page, permissions, and forms that act once

Asked for on 2026-10-06. Four things:

1. Nothing about tokens or API keys on other pages (the "From a pipeline" card of a tag, "Deploy from outside" in an app's and a service's Settings, and the like).
2. Everything at `/keys`, in two tabs, **Private Keys** and **API Tokens**, each one table.
3. "New API token" stays a dialog: name, expires in (7, 30, 60, 90 days, 1 year, never), and permissions as checkboxes (Read, Write, Root, Deploy, Read sensitive data). Root disables the others.
4. Refreshing the page after making a key sends the form again and makes another. Find out why and fix it.

## What is there now

- `/keys` is four cards: a person's API tokens, deploy tokens (every app and service, with or without one), webhook secrets (the same again), SSH keys.
- A token has one `ability`, `read` or `deploy`, chosen in a select. `deploy` also reads, starts and stops.
- Other pages point at `/keys`: Account ("API tokens"), Sources ("SSH keys"), an app's and a service's Settings ("Deploy from outside"), and a tag's page shows a `curl` line with `$MUSDASH_TOKEN`.

## The refresh bug

A secret that is shown once is rendered in the response to the POST that made it; CLAUDE.md forbids putting it in a redirect, a cookie or a later GET, and that stays. The cost is that the page a person is looking at *is* a POST response, and a browser's Refresh sends a POST again. Each of these handlers does its work on every POST it gets:

| Handler | What a refresh does |
|---|---|
| `tokenCreate` | a second API token of the same name and permissions |
| `sshKeyCreate` | a second key pair of the same name |
| `appDeployToken`, `serviceDeployToken` | replaces the token that was just shown: the one the person copied stops working |
| `memberReset` | a second reset link |
| `invitationCreate` | refused by the unique address, but the dialog opens with an error |

Two-step set-up and new recovery codes are not affected: each needs a TOTP code, which works once.

The fix has two halves, and the first is the one that is relied on:

- **The server acts on a form once.** Such a form carries a hidden `_once` value, random per rendering (`ui.Once()`). Right before it writes, the handler calls `s.sentBefore(w, r, back)`, which records the person's id with the value in a limiter of one per day (`auth.Limiter`, bounded at 4096 keys, no new type). A value seen before means the form was sent again: nothing is made, and the answer is a redirect to `back` with a note. The check and the record are one step under the limiter's lock, so two POSTs at the same moment cannot both pass. A form without the value is acted on as before (a script, a test); CSRF is what keeps strangers out, this only keeps a browser from repeating itself. Recorded at the write, not at the top: a form refused for a wrong password has spent nothing.
- **The browser stops offering to send it again.** A page rendered after such a POST says its own address (`<body data-address="/keys/tokens">`, from `ui.Shell.Address`), and `app.js` puts it in the address bar with `history.replaceState`. Refresh is then a GET of the page, without the token, which was shown once. To be checked in the browser pane; if a reload still POSTs, the server half answers it.

Not kept across a restart: the limiter is in memory. A refresh of a stale POST page after a restart, in a browser without script, would act again. Accepted; a table for it is not worth its writes.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | Tabs are paths: `/keys` is Private Keys, `/keys/tokens` is API Tokens | The order asked for; `/keys#ssh-keys` links from forms keep working; the rule here is that where you are is in the path |
| 2 | The API Tokens table holds three kinds of row: a person's API tokens, deploy tokens, webhook secrets, told apart by a Type column | "All in one table". Deploy tokens and webhook secrets are still needed (one resource only; a Git host's secret), and with their Settings pointers gone this page is the only place for them |
| 3 | Only tokens and secrets that exist are rows. One is made in a dialog that picks the resource: `POST /keys/deploy-tokens`, `POST /keys/webhook-secrets` | Listing every app twice with "None" is what made the page long. The per-resource routes stay for Replace and Revoke |
| 4 | A token has a set of permissions, stored as a comma-separated list in `api_tokens.abilities` (migration 0016: add the column, fill it, drop `ability`) | Five checkboxes |
| 5 | Existing tokens: `read` stays `read`; `deploy` becomes `read,write,deploy` | Exactly what each could do before |
| 6 | What each permission is: **Read** every GET. **Write** start and stop (`apps/{id}/stop`, `databases/{id}/start|stop`, `services/{id}/stop`). **Deploy** start a deployment (`apps/{id}/deploy`, `services/{id}/deploy`, `/api/v1/deploy`). **Read sensitive data** reading, with secret values in the answers. **Root** all of them, and what later versions add | Each box must gate something real. They are independent (a deploy-only token for a pipeline reads nothing), except that sensitive includes read and root includes all |
| 7 | Read sensitive data unlocks two things, both of which a Member already sees in the dashboard: an app's variables with values (`GET /api/v1/apps/{id}/envs`, new: names for Read, values for sensitive) and a database's password in `GET /api/v1/databases/{id}` | Without something behind it the checkbox would be a label. `TestAPINeverReturnsSecrets` keeps holding for every other token |
| 8 | Root is stored alone (`root`); a form with Root and others checked is Root | The disabled boxes are not sent anyway |
| 9 | A token still never does what its person may not: permissions narrow the person's role, they do not add to it | Unchanged rule; every API route is still a Member's |
| 10 | The dialog keeps "Current password" | CLAUDE.md: making an API token asks for the password again. Four fields: name, expires, permissions, password |
| 11 | `/api/v1/me` answers `"abilities": [...]` in place of `"ability"` | The old field has no single value any more |
| 12 | Kept elsewhere: sentences that say what an action does to tokens (changing a password revokes them, a role change ends them) and form hints that say where a key is added | Those are consequences and directions, not token management |

## Changes

**Database** (`migrations/0016_token_abilities.sql`, `internal/db/access.go`)
- Constants `AbilityRead`, `AbilityWrite`, `AbilityDeploy`, `AbilitySensitive` (`read:sensitive`), `AbilityRoot`.
- `NormalAbilities([]string) (string, error)`: known names only, no duplicates, fixed order, root alone, sensitive brings read; none is an error.
- `APIToken.Abilities string`, `May(ability) bool`, `AbilityList() []string`. `MayDeploy` goes.

**API** (`api.go`, `server.go`, `handlers_webhooks.go`)
- `s.api(needs, h)` refuses with 403 naming the permission that is missing.
- Routes get `read`, `write` or `deploy` as in decision 6.
- `apiAppEnvs`, the password in `apiDatabase`, `abilities` in `apiMe`.

**Acting once** (`server.go`, `ui/controls.templ`, `ui/layout.templ`, `static/app.js`)
- `ui.Once()`, `s.sent`, `s.sentBefore`, `Shell.Address`, `data-address`.
- Used by `tokenCreate`, `sshKeyCreate`, both deploy-token handlers, `POST /keys/deploy-tokens`, `memberReset`, `invitationCreate`. A webhook secret is made with a redirect already and needs none.

**Keys page** (`handlers_keys.go`, `handlers_tokens.go`, `pages/keys.templ`, `pages/access.templ`)
- `GET /keys` (Private Keys), `GET /keys/tokens` (API Tokens), `ui.Tabs`.
- Private Keys: one table (name, public key, added, Copy and Delete).
- API Tokens: one table (name, type, scope, last used, expires, actions); three dialogs. Scope is the permissions of an API token, and one line for the other two kinds; a webhook secret's Show is with its actions.
- New token dialog: `perm_read`, `perm_write`, `perm_root`, `perm_deploy`, `perm_sensitive`; `data-alone` on Root disables the rest of its group (`app.js`).
- Lifetimes `7`, `30`, `60`, `90`, `365`, `never`.
- Redirects go to `/keys` or `/keys/tokens` instead of fragments.

**Other pages**
- Removed: the tag page's "From a pipeline" card and "or with one API call"; `keysLink` from an app's and a service's Settings; Account's "API tokens" card; Sources' "SSH keys" card.

**Tests**
- `db`: abilities normalised; `May`; the migration carries `deploy` over as `read,write,deploy`.
- `web`: each permission against each kind of route; sensitive values only for a sensitive token; a refused dialog comes back open with its boxes; a form sent twice makes one token, one key, and leaves a deploy token alone; the two tabs; the new dialogs' routes refuse another team's resource and a preview; no other page holds the removed cards.
- `test/vps` posts `token_ability`; those scripts are updated to the new fields where the file is not being worked on by another session.

**Docs**: README (the API table, the token paragraph), CLAUDE.md (Keys page, permissions, acting once, `data-alone`).

## Not done

- No route to edit a token's permissions: revoke and make another.
- No removal of per-resource deploy tokens in favour of API tokens with Deploy: pipelines use them today.

## Review of this plan (2026-10-06)

Read against the code before starting. Approved with these corrections, which are folded in above or apply as written here:

- **The two picker routes choose from the page's own list.** `POST /keys/deploy-tokens` and `/keys/webhook-secrets` look the id up in `s.keyOwners`, the list the dialog was drawn from, and not by a query of their own: that list is already the team's, leaves previews out, and knows which resources come from Git. An id that is not in it, or that already has what is asked for, is refused.
- **A column called "May" does not fit a webhook secret.** It is "Scope", and the secret's Show sits with the row's actions.
- **A preview's variables in the API are its parent's** (`App.ConfigOwner`), which is what it runs with; the dashboard has no page for them at all.
- **A double click is the one case where the guard costs something**: the browser shows the answer to the second POST, which is the refusal, so the token the first one made is never seen. The note says so and says what to do (revoke it, make another). Before, the person got two tokens and saw one.
- **Checked, no change needed:** a replayed two-step form fails on its used code; `DROP COLUMN` is allowed for a column whose only CHECK is its own (the migration test proves it on the real driver); htmx keeps no history snapshots (`historyCacheSize: 0`), so the address change puts no token into storage; every new route is a Member's, like the per-resource ones it stands beside.

## Review of the code (2026-10-06)

Read by a second reviewer against CLAUDE.md's rules, after the tests were green and the page had been used in a browser (a token, a deploy token, a webhook secret and a key made; Refresh after each is a GET and makes nothing). No hole in what a token may do, in what an answer holds, or in whose resource a dialog can name. Changed after it:

- **A form that was let through and then made nothing gets its value back** (`s.notSent`): a write that fails, as many tokens as a person may have, a dialog refused for its choice. Sent again, it is a first time, and nobody is told that something was made when nothing was.
- **`POST /keys/deploy-tokens` asks whether the form was sent before it looks at the choice.** Sent again, the form names an app that has a token by now; it was refused for that, in a dialog that then offered some other app.
- The README no longer says that a token that only deploys reads nothing at all: a deploy call answers with the ids it queued.

Left as it is: two people who make the first deploy token of one app in the same instant are both shown one, and the later write is the one that works. That is what two Replaces at once do as well, and both people may replace.
