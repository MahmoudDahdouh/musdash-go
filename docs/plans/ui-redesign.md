# UI redesign — implementation plan

**Goal:** the fourteen changes asked for on 2026-10-05: projects and resources as cards, the environment in the address and in a switcher, one page to add any resource, icons on tabs, dialogs for what is critical and for short forms, tags that exist on their own, one page for keys and tokens, a Domains tab, and environment variables that are not shown until asked for.

**Builds on:** `3deafc0` (Primer light theme, Hugeicons, Home, popover selects). Nothing in `internal/deploy`, `internal/proxy` or the job queue changes. One migration (`0015_tags.sql`).

**Done when:** every item of the request list below is visible in the dashboard, `go vet ./... && go test -short ./...` is green, and each page was looked at in a browser at desktop and phone width.

## Global constraints

- Earlier constraints hold: three modules, no inline script or `style` attribute (CSP), tokens only, `handle(pattern, who, h)` for every route, loaders answer 404, a one-time secret is in the response to its POST and nowhere else.
- **No new asset and no new library.** `app.js` grows by behaviours driven by `data-*` attributes; `app.css` by component classes.
- A confirm dialog is opened by script; the server still checks everything it checked before (typed names, roles, the form token). A short form now sits in a dialog, so the dashboard needs script to be used, as it already did for selects.
- Idle memory does not move: no cache, no new goroutine. A switcher's options are fetched when it is opened, not on every page.
- Addresses that change keep no alias unless a test or the API depends on them: the dashboard has no outside links to itself. The JSON API (`/api/v1`) and webhook addresses do not change at all.

## The request, item by item

| # | Asked | Task |
|---|---|---|
| 1 | Projects as cards: name, description (3 lines), number of environments and resources; `/e/<env>` in the path, first environment by default | 2 |
| 2 | Environments in a switcher, not tabs; a header instead of the breadcrumb, with a switcher between resources | 1, 2 |
| 3 | App, database, service are all resources: one "Add resource" page listing every kind and source | 3 |
| 4 | Icons on tabs | 1 |
| 5 | New project in a dialog | 2 |
| 6 | Resources of a project as cards | 2 |
| 7 | Card style from Primer's Card | 1 |
| 8 | Project cards show icons: AppWindow (app), Database, Layers01 (service) | 1, 2 |
| 9 | A dialog before every critical action: sign out, delete, change of critical data | 4 |
| 10 | Tags can exist empty; CRUD for tags | 5 |
| 11 | API tokens, keys, deploy tokens and webhook secrets on one page, not per project | 6 |
| 12 | A Domains tab in a project | 7 |
| 13 | Any create or update with up to five inputs is a dialog | 8 |
| 14 | Environment variables are secret: shown only when asked | 9 |

## Decisions

| Topic | Decision | Why |
|---|---|---|
| Card | `ui.Card`-style tile `.tile`: 12px radius, 1px border, 16px padding; an icon in a 32px grey square, a semibold heading, a muted description clamped to three lines, a metadata row at the bottom (16px icon + figure), an optional action top right. The heading is the link and its `::after` covers the tile, so the whole tile is clickable with one tab stop and one name; a control inside sits above it. Hover darkens the border | Primer's Card anatomy (Icon, Heading, Description, Metadata, Action), written as our class. The existing `.card` (settings panels with head and foot) keeps its name and look |
| Tile grid | `.tiles`: `repeat(auto-fill, minmax(17rem, 1fr))`, a `<ul>` with an `aria-label`, each tile an `<li>` | Primer's guidance for a collection of cards |
| Environment in the path | `GET /projects/{id}/e/{env}` is the environment's page. `GET /projects/{id}` shows the first environment (by creation, which is `production` unless it was deleted) without a redirect. An environment of another project is a 404 | "by default use the first one" |
| New-resource addresses | `GET /projects/{id}/e/{env}/new` (the one page), `…/e/{env}/apps/new`, `…/e/{env}/databases/new`, `…/e/{env}/services/new`, and `POST` to `…/e/{env}/apps`, `/databases`, `/services`. The variant stays a query value (`?source=git`, `?engine=postgres`, `?template=wordpress`): it picks a form, it names nothing. `databases/new` without an engine and `services/new` without a template redirect to `…/new` | No `?env=` anywhere; the `env` hidden field goes too |
| Header | The breadcrumb becomes a bar at the top of every page (`.topbar`, now on every width; on a phone it also holds the menu button). A step of it can be a switcher: a button naming where you are and a popover of the places beside it | "create a header instead of breadcrumb" |
| Switchers | Two: environments of the project, and resources of the environment (apps, databases, services, each with its icon and state, then "Add resource"). Shown on the project's environment page, the new-resource pages and every resource page. Options are fetched when first opened (`GET /projects/{id}/switch/environments?at=<env>`, `GET /environments/{id}/switch/resources?at=<kind>:<id>`), as the repository picker's are; an option is a link | A page costs no extra query until somebody opens the menu; the existing Select script places the popover and moves through options |
| `ui.Crumb` | Gains `Icon` and `Menu` (the address its options come from) and `Filter` (a filter field: resources yes, environments no) | One trail type for links and switchers |
| Project tabs | Resources (`/projects/{id}/e/{env}`), Domains (`/projects/{id}/domains`), Settings (`/projects/{id}/settings`). Domains and Settings are the project's, across environments; on them the header ends at the project | An environment's address in a settings page would say the settings are that environment's |
| Environments | Created from a dialog (the switcher's "New environment" entry and the Settings card); deleted from Settings with the typed-name dialog as now | #13 |
| Add resource page | Three groups under one filter field: **Apps** (Public repository, Private repository with a GitHub App, Private repository with a deploy key, Docker image), **Databases** (the engines of `catalog.Databases()`), **Services** (Your own Compose file, Compose file in a Git repository, then `catalog.Services()`). Each is a compact tile linking to the existing form. The private-repository tiles preselect the first access of that kind (`?access=app` / `?access=key`); with none set up they say so and link to where one is added | Coolify's "New resource" page. The forms themselves are unchanged |
| Filter field | `data-filter="<container id>"` on an input hides children of the container whose `data-search` does not contain the text, and a group left empty | One behaviour for the add-resource page and long lists |
| Tabs | `ui.Tab` gains `Icon`; every tab bar gets icons | #4 |
| Icons added | app-window, chart, file, dashboard, eye, eye-off, search, unfold, github, archive, code, more, variable, shield, webhook, rocket (Hugeicons Stroke Rounded, as the rest) | Named by the request (AppWindow), or needed by tabs and switchers |
| Confirm dialog | One `<dialog id="confirm">` in the layout. A submit button with `data-confirm="<question>"` (and optional `data-confirm-title`, `data-confirm-submit`, `data-confirm-tone="danger"`) opens it instead of submitting; its confirm button submits the form with that button as submitter. Text is set with `textContent` | One dialog element a page instead of one a row; a critical action is marked by one attribute |
| What is critical | Sign out; every delete, remove and revoke; stop (app, database, service); rollback; restore a backup; replace a secret or token; forget a host key; change a role; reset a member's password; turn a member's second step off; deploy everything with a tag. Delete of a project, environment, app, database or service keeps the typed name | #9 |
| Form dialog | `ui.FormDialog`: a trigger button and a `<dialog>` holding a form (fields, Cancel, submit). `Open: !f.OK()` reopens it when the server sends the page back with errors (`data-autoopen`). Used for every create or update of up to five inputs | #13. Handlers already re-render the page with the failed form; only where the form sits changes |
| Forms that become dialogs | New project; project details; add environment; profile; change password; create API token; rename team; invite; add server; add deploy key; add GitHub App; add storage (app); add task; app tags; build server; previews; add S3 storage; dashboard address; backup schedule; add domain (project tab: resource, domain, path, HTTPS); edit a tag; new tag. A card that held such a form shows the stored values and an Edit or Add button | #13 |
| Forms that stay on the page | More than five inputs, or an editor: new app, new database, new service, app General settings, app Source, add domain with all its options (app's Domains tab), notification channel, database settings, Compose editor, variables editor, shared variables editor | The rule's own limit; a textarea of many lines is not a dialog's job |
| Tags | A table `tags (team_id, tag, created_at)`, primary key `(team_id, tag)`. Migration 0015 fills it from `resource_tags`. `SetTags` adds a missing tag; `ListTags` is `tags LEFT JOIN resource_tags`; a tag's page exists while its row does. New: `CreateTag`, `RenameTag` (both tables, one transaction; a taken name is refused), `DeleteTag` (both tables). Routes `POST /tags`, `POST /tags/{tag}` (rename), `POST /tags/{tag}/delete`, all `member` | Tags group what is in projects, which is a Member's work; the tag pages and `resource_tags` are already a Member's |
| Tags and the API | `GET /api/v1/tags` lists empty tags too, with zero counts. `POST /api/v1/deploy?tag=` on an empty tag queues nothing, as on a tag nothing waits behind | Same query |
| Keys & tokens page | `GET /keys` (nav: Administration, key icon). Sections: **API tokens** (the person's own; moved from Account), **Deploy tokens** (every app and service of the team: has one or not, create/replace/revoke), **Webhook secrets** (every Git app and Git service not on a GitHub App: address, secret hidden until asked, create/replace), **SSH keys** (moved from Sources; Admin to add or delete). The existing POST routes keep their addresses and now answer on this page | #11. A new token is still shown once, in the response to its POST |
| Per-resource triggers card | Removed from app and service Settings; in its place one line linking to `/keys#deploy-tokens` | "not per project" |
| A webhook secret on the page | Not in the HTML. `GET /keys/webhook/{kind}/{id}` answers the row with the secret, on a button press | Same rule as #14 |
| Domains tab (project) | `GET /projects/{id}/domains`: every domain of the project's apps and service endpoints, with environment, resource, HTTPS and other badges, Remove (confirm) for app domains. "Add domain" dialog for an app of the project: app, domain, path, HTTPS. `POST /projects/{id}/domains` runs the same checks as the app's own route | #12 |
| Variables are secret | An app's Environment tab lists names with the value as bullets; no value is in the page. "Show values" asks for them (`GET /apps/{id}/environment/values`, a fragment; "Hide" asks for the list again). "Edit" opens the editor (`GET /apps/{id}/environment/edit`), which is the textarea as now. The same for shared variables (`…/variables`, `/values`, `/edit`) and a service's variables on its Compose tab (shown as a count and a "Show" button until asked) | #14. Asking is a GET by the signed-in member: the route's level is what guards it, as it guards the editor today |
| `Cache-Control` | Already `no-store` on every rendered page (`render`) | A revealed value is not kept by the browser |

## Data model (migration `0015_tags.sql`)

```sql
CREATE TABLE tags (
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (team_id, tag)
) STRICT;
INSERT INTO tags (team_id, tag, created_at)
    SELECT DISTINCT team_id, tag, unixepoch() FROM resource_tags;
```

No foreign key from `resource_tags` to `tags`: `SetTags`, `RenameTag` and `DeleteTag` keep the two in step inside one transaction each, and a rename would otherwise need `ON UPDATE CASCADE` on a table whose rows are written by resource deletes too.

## Tasks

Each ends with `make generate`, `go vet ./... && go test -short ./...` green, a read of the diff, and a commit `UI redesign task N: …`.

1. **Components.** `.tile`/`.tiles`, the header bar and `ui.Crumb` switchers (`ui.Switcher`, `ui.SwitchOption`), `ui.Tab.Icon`, the new icons, the confirm dialog and `data-confirm`, `ui.FormDialog` and `data-autoopen`, `data-filter`, a secret row (`ui.Masked`). Gallery entries for each. Tests: the gallery renders; `TestSecurityHeadersAndStatic` still passes (no inline script).
2. **Projects.** `db.ListProjects` counts apps (previews left out), databases and services. Projects page as tiles with the three icons; new project dialog (`GET /projects/new` goes). `/projects/{id}/e/{env}`; the resource tiles; the two switcher endpoints; crumbs of every resource page through one helper (`resourceCrumbs`). Tab icons everywhere. Tests: env in path, wrong project's env is 404, switcher fragments list only the team's, counts.
3. **Add resource.** The one page and the moved new-resource routes; every `?env=` in code and tests replaced. Tests: the page lists every engine and template; old chooser addresses redirect; create still lands in the right environment.
4. **Confirm dialogs.** `data-confirm` on every critical form (list above). Test: a walk over rendered pages asserting that each form whose action ends in `/delete`, `/stop`, `/rollback`, `/restore`, `/revoke`… has a `data-confirm` or `data-match` control.
5. **Tags.** Migration, queries, routes, pages (list with New tag; a tag's page with Rename and Delete; empty tag state). Tests: empty tag listed and reachable, rename moves resources, rename onto an existing name refused, delete removes from resources, another team's tag is a 404, migration fills from existing rows.
6. **Keys & tokens.** The page, moved sections, removed cards, webhook reveal. Tests: `TestAPINeverReturnsSecrets` untouched; the page never contains a webhook secret; a Member cannot add an SSH key; token shown once.
7. **Domains.** Project tab and app tab. Tests: lists only the project's; add through the project route obeys the app route's checks; a preview is refused (`ownSettings` rule).
8. **Form dialogs.** The conversions listed above. Tests: existing form tests keep passing (same actions and field names); a failed POST answers with the dialog marked open.
9. **Secret variables.** App, shared and service variables. Tests: the tab's HTML has no value; the values fragment has them; the editor round-trips.
10. **Wrap-up.** CLAUDE.md and README; a browser pass at 1280 and 375 wide with screenshots; independent review of the whole diff and its fixes.

## Changes after the plan's review

An independent read of the plan against the code found these; each replaces what the tables above say.

| Found | Now |
|---|---|
| Team and server variables are readable by a Member only as names: the values are withheld in the handler (`CanEdit`), not by the route | `…/variables/values` and `…/variables/edit` are `admin` routes for the team's and a server's variables, `member` for a project's and an environment's. `TestTeamOwnedRoutesNeedAnAdmin` is taught these two GETs |
| The preview rule was not carried to the new routes | `GET /apps/{id}/environment/values` and `/edit` are wrapped in `ownSettings`. `POST /projects/{id}/domains` loads the app for the team, refuses one that is not in the project and refuses a preview. A webhook secret is revealed by `GET /apps/{id}/webhook-secret` (wrapped) and `GET /services/{id}/webhook-secret`, not by a `/keys/…` route with a kind in it |
| `sum()` over no rows is NULL | `ListTags` uses `coalesce(sum(…), 0)` and joins on team and tag |
| A tag that has nothing can only be given to something by typing its name | The tags dialog of an app or a service lists the team's tags as checkboxes, with a field for new ones |
| Tag edge cases | `loadTag` asks for the row; renaming a tag to itself is no error; `SetTags` adds missing tags in its transaction; deploying an empty tag says nothing has the tag; a team has at most 200 tags |
| Critical actions left out | Also: turning your own second step off, changing the dashboard address, reinstalling the proxy |
| The app's own Domains tab | Cut: the request is for a tab in the project. The card stays in the app's Settings. Removing a domain from the project's tab comes back to that tab |
| Tasks 5 to 7 would build forms that task 8 then converts | Every form these tasks add or touch is a dialog from the start; task 8 converts what is left |
| "New environment" in the switcher | The switcher's last entry is "Manage environments", a link to the project's Settings, where the dialog is. A created environment is where the browser goes next |
| A switcher whose options cannot be fetched | The endpoint answers 200 with a note, as the repository picker does. Space chooses the focused option; the current one carries `aria-current` |
| Tests outside `-short` | `test/deploy_test.go` and `test/rss_test.go` are moved to the new addresses with the rest. The Docker test is compiled and not run here: a second musdash on this machine's Docker would remove the containers of the instance already running |
| The security-header test reads only `/setup` | It also reads the gallery and a signed-in page |

## As built

- Task 4's test (`TestCriticalFormsAsk`) reads the templates instead of walking rendered pages: a branch that no test renders (a server with a proxy, a database that is running) is checked too.
- "Up to five fields" was taken as the least a dialog is used for, not the most: Add server, Add bucket, Add domain and Add storage have more and are dialogs as well, so that every list is added to the same way. An app's General and Source settings and a service's Compose form stay on the page.
- The independent review of tasks 5 to 9 found a checkbox that sent the wrong field name (a service address's HTTPS box, older than this work), ids that two elements shared (`previews`, `tags`, `new-token`, `content`), and a form that lost a tick when it was refused. All are fixed, and the page walk now fails for a duplicate id.

## What is deliberately not done

- No change to the JSON API's shape or to webhook addresses.
- Deploy tokens stay one per resource (they are looked up by resource id in `/api/v1/deploy?uuid=`); the page only gathers them.
- No per-variable editing: the editor is still one text box of `NAME=value` lines.
- No dark mode, no drag-and-drop ordering of tiles, no bulk actions.
