# Empty states: one component, everywhere a list can be empty

Asked for on 2026-10-06: update every empty state of the project, taking Primer's pattern (<https://primer.style/product/ui-patterns/empty-states/>) as the inspiration.

## What Primer says, and what of it is used

- An empty state has a **graphic**, a **primary text** (a title that says what the empty space is for), an optional **secondary text** (brief, not a repeat of the title, the next step), one **primary action**, an optional **secondary action** (a text link under the first), and an optional **border** for when it shares the page with other content.
- Three kinds: a first use, a feature that is empty, an error. An error has the alert icon, says what is wrong in plain words, and leads to a way out.
- It is used on a whole page and inside a container.

Nothing of Primer's is imported; the sizes are ours.

## What is there now

- `ui.EmptyState(icon, title, description)` with the action as children: 22 uses, all page-level, always bordered, a 20px icon and a 16px title.
- About 25 lists inside cards say they are empty with one grey sentence (`<p class="card-body muted">No bucket yet. …</p>`): no icon, no title, and the action is a small button in the card's head, away from the sentence.
- Two lists say nothing when empty (an app's previews).

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | One component, `ui.EmptyState(ui.EmptyProps)`, children are the primary action | The codebase's idiom for a component with options (`ButtonProps`, `TileProps`); three sizes and a second action do not fit in positional arguments |
| 2 | Three sizes. **Default**: stands where a page's list would be, under a header or tabs; bordered, 24px icon, 16px title, 14px text. **`EmptySmall`**: inside a card, where its table or rows would be; no border (the card has one), 20px icon, 14px title, 13px text. **`EmptyPage`**: all a page holds (not found, not allowed); no border, more room, 32px icon, 20px title | Primer's border rule: added when it shares the page with other content. The title never outgrows what is above it: a page's `h1` is 20px, a card's title 14px |
| 3 | The title is a heading of the level its place has: `h1` for `EmptyPage` (the page has no other), `h2` by default, `h3` for `EmptySmall` (under the card's `h2`) | A screen reader's outline stays true |
| 4 | The icon is decorative (`aria-hidden`, as every `ui.Icon`), plain and muted, no tile behind it | Primer's; the title says everything the icon does |
| 5 | Text is centred and at most 30rem wide | Primer's narrow blankslate; a centred line across a 1200px card cannot be read |
| 6 | The action in an empty state is the primary button (`Small` inside a card). Where the same action is in the page's header or the card's head, it is there only while the list has something in it | One call to action, next to the sentence that explains it. The page-level states already work this way (`ProjectList` hides "New project" from the header while empty) |
| 7 | A second action is a text link under the first (`EmptyProps.Link`, `LinkHref`), used only where there is somewhere useful to go | Primer's secondary action. No link for the sake of one |
| 8 | An empty state is for a **list** that has nothing (a table, rows, tiles, a log, charts) or a page that cannot be shown. A single missing **value** stays a word in its place: "none" in a key-value list, "Never" in a table cell, "No domain" on an endpoint's row, "No recent reading" on a server's row of Home | A blankslate inside a table cell or a row is noise |
| 9 | Menus keep their one line (`ui.MenuNote`, "Nothing matches.") | A menu is a narrow popover of one-line items; Primer's own menus say it in a line too |
| 10 | Home's "Getting started" stays as it is | It is the first-use experience already: steps, each with its action |
| 11 | `Text` is plain text | Two places had a `code` span in the sentence (`SERVICE_FQDN_NAME_PORT`); the name reads as what it is without it, and one children slot is kept for the action |
| 12 | `EmptyProps.Attrs` goes on the element, for the one a filter shows and hides (`data-filter-empty`, `hidden`) | The Add resource page's "Nothing matches" |
| 13 | Wording: the title says what is missing in a few words, without a full stop; the text says why it matters or what to do next and does not repeat the title. Existing sentences are kept wherever they already do that, split at their own seam | Primer's content rules, and the page tests read these sentences |
| 14 | No new icon, no new dependency, no script | Every icon needed is in `ui.Icon` |

## The component

`internal/web/ui/display.templ`:

```go
const (
	EmptySmall = "sm"   // inside a card
	EmptyPage  = "page" // all a page holds
)

type EmptyProps struct {
	Icon, Title, Text string
	Size              string
	Link, LinkHref    string
	Attrs             templ.Attributes
}

templ EmptyState(p EmptyProps) { … children: the primary action … }
```

`internal/web/assets/input.css`, replacing the present `.empty` block: `.empty` (grid, centred, 32px 24px, border), `.empty-sm`, `.empty-page`, `.empty-title`, `.empty-text`, `.empty-actions` (hidden when it has no element in it, with the `:not(:has(*))` the menus use), `.empty-link`. `.chart-empty` goes.

## Every empty state

Page-level (default size, already `EmptyState`; new props, same words unless noted):

| Where | Icon | Title | Text | Action |
|---|---|---|---|---|
| Projects | folder | No projects yet | Create a project, then deploy apps, databases and services into it. | New project |
| A project's environment | box | Nothing is in *env* yet | as now | Add resource |
| A project's domains, with apps | globe | No domain yet | as now | Add domain |
| A project's domains, no apps | globe | No domain yet | as now | **Go to Resources** (new: the way forward) |
| Tags | tag | No tags yet | as now | New tag |
| One tag, nothing has it | tag | Nothing has this tag yet | as now | link: Go to Projects (new) |
| An app's deployments | rocket | No deployments yet | Choose Deploy to start the app. Each deployment is listed here with its log. | none: Deploy is in the header |
| Logs of an app, a database, a service | file (was terminal) | Nothing is running | as now | app only, link: See its deployments (new) |
| Usage of an app, a database, a service | chart (was server) | Nothing is running | as now | app only, link: See its deployments |
| Terminal of an app, a database, a service | terminal | Nothing is running | as now | app only, link: See its deployments |
| Add resource, filter matches nothing | search | Nothing matches | A service that is not listed can be run from its own Compose file. | link: Use your own Compose file |

Whole page (`EmptyPage`):

| Where | Icon | Title | Text | Action |
|---|---|---|---|---|
| Not found | alert | Page not found | as now | Go to Home |
| Not allowed | lock | Your role does not allow this | as now | See who is in the team |

Inside a card (`EmptySmall`; today a grey sentence):

| Where | Icon | Title | Text | Action |
|---|---|---|---|---|
| Home, recent activity | clock | No activity yet | Nothing has been deployed, backed up or run yet. | none |
| Home, projects | folder | No projects yet | A project groups the apps, databases and services of one product. | none: Getting started has it |
| App overview, domains | globe | No domain yet | Add one to reach the app from outside. | Add domain (to Settings) |
| A preview's overview, no address | globe | No address | This preview could not be given one: the address it would have may be taken. | none: a preview has no settings |
| App overview, latest deployment | rocket | Not deployed yet | Choose Deploy to start the app. | none |
| App storage | drive | Nothing is mounted | Without storage, files the app writes are lost when it is redeployed. | Add storage (from the head) |
| App settings, domains | globe | No domain yet | The app answers at its generated address only. | Add domain (from the head) |
| App settings, previews on and none (new) | git-branch | No previews at the moment | A pull request into *branch* gets one when it is opened or pushed to. | none |
| App and service settings, tags | tag | No tags | Give it a tag to deploy it together with everything else that has it. | Add tags (from the head) |
| App tasks | clock | No tasks yet | A task is the place for what a cron job would do: … | Add task (from the head) |
| A task's runs | clock | This task has not run yet | Each run is listed here with its output. | none |
| A run's output | terminal | This run wrote nothing | | none |
| Database backups, schedule | clock | No schedule yet | This database is backed up only when you choose Back up now. | Set up (from the head) |
| Database backups, list | archive | No backup has been made yet | Backups are listed here as they are made. | none |
| Settings, backup storage | drive | No bucket yet | Until one is added, backups are kept on the server only, and are lost with it. | Add bucket (from the head) |
| Settings, notification channels | bell | No channel yet | Without one, a failed backup or deployment is only visible here in the dashboard. | none: "Add a channel" is the card below |
| Keys, API tokens | key | No tokens yet | Make an API token for a script of your own. The deploy tokens and webhook secrets of apps and services are listed here too. | none: the three kinds stay in the head |
| Keys, private keys | key | No keys yet | Admin: Create one to deploy from a private repository or to reach a remote server. Others: An Admin can create one. | New key, for an Admin (from the head) |
| Sources, GitHub Apps | github | No GitHub App yet | Admin: Create one to deploy from private repositories, and on every push. Others: An Admin can create one. | New GitHub App, for an Admin (from the head) |
| Variables card, one group | variable | No variables yet | (the card's own words) | Add variables (the Edit link, from the head) |
| Variables card, a group of two | variable | None yet (a plain title) | | none: Edit stays in the head |
| Shared variables, read-only list | variable | No variables yet | An Admin has set none. | none |
| Team, invitations | users | Nobody is invited at the moment | Admin: Invite someone to give them a sign-in of their own. Others: An Admin can invite someone. | none: Invite is in the page's header |
| Service overview, addresses | globe | No web address | To give a service one, add SERVICE_FQDN_NAME_PORT to its environment in the Compose file. | Open the Compose file; none for a service from Git, whose file is changed in the repository |
| Service settings, addresses | globe | No web address | the same | the same |
| Service overview, containers | layers | Not deployed yet / The first deployment is under way | Choose Deploy to start the stack. / Its containers are listed here once they are up. | none |
| Service overview, latest deployment | rocket | Nothing has been deployed yet | The deployment's log appears here. | none |
| Service Compose tab, file from Git not fetched | file | The file has not been fetched yet | It is shown here after the first deployment. | none |
| Usage history, sampling off | chart | Sampling is switched off | Nothing is kept for this server, which is how it starts. | Open the server's usage, where there is such a page to go to |
| A chart with no samples | chart | No samples in this stretch of time (a plain title) | | none |

Left as they are, by decisions 8 to 10: `ui.MenuNote` and "Nothing matches." in menus, "none"/"None"/"Never" values, "No address: the one it would have is taken." on a preview's row, "has no domain yet" on an endpoint's row, "Nothing has it yet" on a tag's tile, "No recent reading of what it uses." on a server's row of Home, the warning notice of a usage reading that failed, Getting started, and the signed-out "invitation expired" notice (an `AuthLayout` page, where a notice is the form for it).

## Tests

- `internal/web/ui/empty_test.go` (the package's first test): renders `EmptyState` in each size and checks the heading level, the classes, that the link and the attributes are there when given and absent when not.
- `TestSignedInPagesHaveNoInlineScriptOrStyle` and the page tests keep passing; `keys_test.go` reads "There is no token yet." and gets the new title.
- A test over the templates, in the manner of `TestCriticalFormsAsk` (`TestEmptyListsUseTheComponent`): a card whose body is one grey sentence, or a grey paragraph that opens with "No", "Nothing", "None", "Nobody" or "There is no", fails unless the test names it as a note, so the next empty list is written with the component.

## Other things touched

- `pages/gallery.templ`: the three sizes, with and without an action and a link.
- `CLAUDE.md`: one bullet in the `internal/web` list on when a list is empty.
- Generated: `*_templ.go`, `static/app.css`.

## Working around the other session

The main checkout has uncommitted work of another session (pills with icons) in `display.templ`, `input.css`, `icons.templ`, `home.templ`, `apps.templ`, `operations.templ`, `CLAUDE.md` and the generated files. This is built on the branch `empty-states` in `.claude/worktrees/empty-states` from `main`, and kept off the lines that work changes: no new icon (it adds two at the end of the switch), the CLAUDE.md bullet goes in another list than its bullet, and `home_test.go` is not edited. The generated files will differ and are regenerated by whoever merges second (`make generate`).

## Order of work

1. Component, CSS, its test, gallery.
2. Page-level callers (the 22), then the cards, file by file.
3. The template test, `keys_test.go`, CLAUDE.md.
4. `make generate`, `go vet ./... && go test -short ./...`.
5. Look at it in a browser: a second instance on its own data directory and port, with a stub `docker`, as the notes on parallel sessions say.
6. Review the diff, commit, push.

## Review of this plan (2026-10-06)

Read again against the templates, the tests and Primer's page. Approved with these changes, made above or to be kept to while building:

- **An empty state has the icon of its tab.** The first draft gave logs a terminal and deployments the old deploy icon; the tabs above them show a file, a rocket and a chart. The state now repeats its tab's icon, so the two say the same thing.
- **Decision 6 has two exceptions, both in the table.** The API tokens card keeps its three buttons in the head (which of the three would the state offer?), and the notification channels card has its "Add a channel" card right below.
- **A card's head that loses its button must not lose its meaning.** The heads keep their title and their sentence; only the button moves. Where the head's button changes what is there rather than adding to a list ("Change" on a schedule, "Edit" on tags), the state's button says what it does when there is nothing yet ("Set up", "Add tags").
- **The previews state needs the line the list has.** The card's key-value body has a bottom border only when a list follows it; it has to have it when the state follows too.
- **The action row must not take room when there is no action.** Twelve of the states have none. The row is hidden by CSS when it has no element, and the link sits outside it.
- **`hidden` works on it.** `.empty` is a grid, which would beat the attribute, but `input.css` already has `[hidden] { display: none !important }`.
- **No test in `internal/web/ui` exists yet.** The component's test is that package's first; it renders into a `strings.Builder`, no server needed.
- **How it lands.** `empty-states` is `main` plus these commits, so it is pushed to `origin/main` as a fast-forward from the worktree. The main checkout is not touched: its uncommitted work is another session's. It stays one step behind `origin` until that work is committed and rebased, at which point the generated files are regenerated, not merged by hand.
- **Not done, on purpose.** No "Deploy" button inside "Nothing is running": the header's is on the same screen, a second form for it would be a second critical form to keep asking (`TestCriticalFormsAsk`), and the link to the deployments is the way to why nothing runs.

## Review of the code (2026-10-07)

Looked at in a browser first, on an instance of its own (its own data directory and port, a stub `docker`): Home, Projects, a project's environment and domains, Tags, Keys and tokens, Sources, Team, both Settings lists, every tab of an app and of a database that were never started, a server's usage, the filter on Add resource, a page that is not there, the gallery, and a phone's width. The button in a state opens its dialog; the filter's state is hidden until nothing matches.

Then read by a second reviewer against the plan and CLAUDE.md. Nothing was lost for any role (each moved button is drawn exactly once, in the head or in the state, under the same condition), every link leads to a registered route, and the CSS does what it says. Changed after it:

- **The template test guarded less than it said.** Its pattern caught 19 of the 29 sentences this change replaced: not "There is no token yet.", not "Not deployed yet.", not one with a second class. It now fails for any card whose body is one grey sentence, and for any grey paragraph that opens by saying there is none, unless the test names the sentence as a note (five are). Run against the templates as they were, it finds 29.
- **Two states were headings under a heading that already said it.** A Git app's variables are two groups, each under an `h3`, and "None yet" was a second `h3` under each; a card of charts repeated one `h3` per chart. `EmptyProps.Plain` writes the title as a line of text for those two.
- **Three sentences were not true for every state of the data.** The tokens state promised a deploy token and a webhook secret, whose buttons exist only once an app or a service lacks one. "Open the Compose file" led a service from Git to a page where the file cannot be changed: it now says the file is the repository's and has no button. A preview without an address was told the address is taken, which is one of three reasons.
- **This document's table** had three titles the tests made me keep as they were ("No backup has been made yet", "The file has not been fetched yet") and lacked the preview's row.

Left: the containers table of "In use now" draws nothing for a server with no containers. It is older than this change and stands next to the meters, which are the card's content.

`TestAHangingServerDoesNotHoldTheTick` (`internal/ops`) failed once in a full run under load and passed five times alone and in every later run; nothing in `internal/ops` is touched here.
