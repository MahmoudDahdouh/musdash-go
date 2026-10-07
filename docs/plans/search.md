# Search: one field for everything the team has

Asked for on 2026-10-07: a search in the bar at the top, right before the person's menu, that looks in all the projects and what they hold ("project, services and everything"), built the way search is best built. Coolify's was looked at first.

## What Coolify does, and what is taken from it

Coolify's `GlobalSearch` (Livewire) opens a dialog on `/` or Cmd+K, loads every searchable thing of the team into a cache for five minutes (apps, services, databases, servers, projects, environments, and its own pages with keywords), and filters that list as the person types. A hit is a name, a kind and where it is, and Enter opens it.

Taken: the dialog and its two keys, the kinds, a hit that says what it is and where, pages with keywords, matching more than the name (domains, images, repositories).

Not taken: the cache. A list of every row held in memory is what RAM rule 8 forbids, and here it is not needed: the database is a local file and one query answers in well under a millisecond. Nor "create new …" commands in the search: adding a resource has its one page already.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | The bar ends with `.topbar-end`, which now holds the search button and then the person's menu | "At the top right before the user drop down" |
| 2 | In the bar it is a button that looks like a field: the search icon, "Search", and the key `/`. Under 48rem it is the icon alone | The trail needs the width, and a real field in the bar would have to show its results in a menu hung from it, which a phone has no room for. A button that opens a dialog works the same at every width |
| 3 | The search itself is a modal `<dialog>` near the top of the window: a field, the results under it, the keys at its foot | The browser gives a dialog its focus trap, Escape and backdrop. Near the top, not centred: the list grows and shrinks as it is typed in and the field must not move |
| 4 | It opens on a click, on `/` outside a field, and on Cmd+K (Mac) or Ctrl+K (elsewhere), which also closes it. Never over another dialog, and never when another script took the key | `/` and Cmd/Ctrl+K are what GitHub, Coolify, Linear and Vercel taught. On a Mac Ctrl+K deletes to the end of a line in every text field, so only Cmd+K is taken there. The terminal handles Ctrl+K itself (`preventDefault`), which is the "another script took it" |
| 5 | Results come from the server as it is typed: `GET /search?q=` answers the options as HTML, asked for by htmx 150 ms after the last key, a newer request replacing one still under way (`hx-sync="this:replace"`) | No list of the team's rows in the page or in memory. 150 ms keeps a fast typist from sending a request per letter and is not felt as a wait |
| 6 | One SQL statement, a `UNION ALL` with the team's id in every arm, sorted and cut in the database. No index, no FTS table | A team has tens or hundreds of rows. FTS5 would mean triggers on eight tables to save microseconds |
| 7 | Searched: **projects** (name, description), **environments** (name), **apps** (name, image, repository, branch), **databases** (name, engine, image), **services** (name, template, repository), **domains** (the host; leads to what it points at), **servers** (name, host, address), **tags**, and the dashboard's own **pages**. A project, environment, app, database or service is also found by its id, alone or inside what is typed | "Everything" a person can open. The id is what a container, network or volume name on the server holds (`musdash-<id>-…`), so a line of `docker ps` can be pasted here. Only the whole id counts: part of a random id matches by accident |
| 8 | Not searched: variables and their values, Compose text, keys and tokens, people, deployments, logs | Stored secrets are not in a page until asked for (CLAUDE.md), and a search that matched inside them would say what they hold. Deployments and logs are found from their app |
| 9 | Previews are left out | A preview is listed with its parent and by no other query (CLAUDE.md) |
| 10 | The text is cut to 100 characters and split at spaces into at most 5 words. Every word must be found in the row: in its own text (decision 7) or in where it is (its project's and environment's names). At least one word must be in its own text | `shop web` finds the app `web` in the project Shop. Without the last rule `production` would list everything in every production environment |
| 11 | Case is ignored as SQLite's `LIKE` ignores it: for ASCII letters. `%`, `_` and `\` in the text are taken as themselves | Resource names are DNS-safe, so ASCII. A `%` typed must not match everything |
| 12 | Order: the name is the text (0), the name starts with a word (1), the name holds a word (2), found elsewhere (3); then pages, projects, environments, apps, databases, services, domains, servers, tags; then by name | Enter opens the first row, so the first row has to be the best guess. One flat list, each row saying its kind, and not groups by kind: with groups the best hit is wherever its group happens to be |
| 13 | 20 rows. The query asks for one more, and when there is one the list ends with "Showing the first 20. Keep typing to narrow it down." | A list longer than that is not read; it is narrowed |
| 14 | With nothing typed the list is the pages the person can open ("Go to"), from no query | The dialog is of use the moment it opens, and it teaches that pages are found here |
| 15 | Pages are the sidebar's entries for the person's role (`ui.Nav` through `navGroups`) and Account, each with a few more words it is found by (`ssh` finds Keys & tokens, `members` finds Team) | One list of pages, so a Member is not offered Settings. The search points at `/keys` only as the sidebar does, typed instead of clicked: no page gains a link to it |
| 16 | A row is a link (`<a role="option">`): icon of the kind, the name with the matched part marked (`<mark>`), under it the kind in words, where it is and one fact in mono (image, repository, engine, host), and the state as a pill for what has one | State is text as well as colour; machine facts are mono. A link can be opened in a new tab with the pointer |
| 17 | The keys are a filtered menu's: focus stays in the field, the arrows move the marker (`data-active`, `aria-activedescendant`), Enter opens the marked row. The first row is marked when results arrive | The same code path as `menuFilter` (`setActive`), and the same look |
| 18 | Enter pressed while the list is older than the text (the request is on its way) waits for the answer and opens its first row | Type and Enter, fast, must open what was typed and not what was listed a moment before. Enter that ends a composed character (an input method) is not one |
| 19 | The dialog opens empty every time. When it closes, the field is emptied and the pages are fetched again behind it, so the next opening shows them at once. A page the browser brings back whole (Back) has it closed | No stale results under an empty field, and no "Loading…" flash on every opening |
| 20 | A count ("3 results", "No results") is put in a `role="status"` line by an out-of-band swap of its content; "No results for …" is also what the list shows | A reader who does not see the list hears that it changed. The region itself stays, or the change is not announced |
| 21 | A failed query answers 200 with a note, as the switchers do. A request that got no answer shows "Search is not answering." from the script | htmx swaps nothing else in, and old results under new text would be a lie |
| 22 | `/search` is a Member's route. An expired session answers with `HX-Redirect` to the sign-in page, as every htmx request here does | It lists only what the team's pages list |
| 23 | Servers lead to `/servers#server-<id>`, a domain to its app's Domains tab or its service's page, a tag to `/tags/<tag>`, an environment to `/projects/<p>/e/<e>` | A server has no page of its own; the list has an anchor per server |
| 24 | New code is in files of its own (`ui/search.templ`, `pages/search.templ`, `handlers_search.go`, `db/search.go`); `layout.templ` gains two lines | Another session has uncommitted work in `layout.templ`, `app.js` and `server.go` |

Left for later, on purpose: `/api/v1/search`, remembering recent searches, forgiving typos, searching logs.

## What it looks like

```
bar:   ☰  Default team / Shop / production          [🔍 Search        /]  (M) Mahmoud ⌄

dialog:
┌───────────────────────────────────────────────────────────┐
│ 🔍  Search projects, apps, domains, servers…        [esc] │
├───────────────────────────────────────────────────────────┤
│▌▢ web                                          ● Running  │
│   App · Shop / production · nginx:1.27                    │
│  🌐 web.example.com                                        │
│   Domain · web · Shop / production                        │
│  ▤ Servers                                                │
│   Page                                                    │
├───────────────────────────────────────────────────────────┤
│ ↑ ↓ to move   ↵ to open                                   │
└───────────────────────────────────────────────────────────┘
```

## Changes

**Database** (`internal/db/search.go`, new)
- `Hit{Kind, ID, Name, Detail, Status, ProjectID, Project, EnvID, Env, OwnerKind, OwnerID, Owner, Rank}` and the `Hit*` kind constants.
- `Search(ctx, teamID string, words []string, limit int) ([]Hit, error)`: the statement of decisions 6 to 12. Words are escaped for `LIKE` here, so a caller cannot forget.

**Server** (`internal/web/handlers_search.go`, new; `server.go`)
- `GET /search` (member) → `s.search`: `searchWords(q)` (cut, split), pages that match, `DB.Search`, merge by rank, cut to 20, render.
- `searchWords` and the merge are plain functions with tests of their own.

**Pages** (`internal/web/pages/search.templ` + `search.go`, new)
- `SearchRow{Href, Icon, Name, Kind, Where, Detail, Pill}`, built from a `db.Hit` or a page here, where the state helpers of each kind are; `SearchResults{Query, Rows, More, Problem}`.
- `SearchOptions(v)`: the rows, the note for none or more, and the status line's out-of-band text.
- `marked(name, words)`: the name in three parts, the middle one the match.

**Layout** (`internal/web/ui/search.templ` + `search.go`, new; `layout.templ`)
- `searchButton()`, `searchDialog()`; `Destinations(s Shell) []NavItem` (the sidebar's entries for this role, and Account) and `NavWords(key)`.
- `Layout`: `.topbar-end` holds `@searchButton()` and `@userMenu(s)`; `@searchDialog()` after the confirm dialog. `userMenu` loses its own wrapper.

**Style** (`assets/input.css`)
- `.topbar-end` as a row; `.search-button`; `.kbd`; `.search` (the dialog), `.search-field`, `.search-foot`, `.search-hit` and its `mark`.

**Script** (`static/app.js`)
- `data-search-open` opens the dialog; the two keys; `data-search-field` steers the list; Enter that waits; the note when no answer comes; refresh on close.
- `setActive` finds its field by either attribute.

**Docs**: CLAUDE.md (the search, its attributes, what is and is not searched), this plan.

## Review of this plan (2026-10-07)

Read against the code before any of it was written. Changed: ids are found inside what is typed and not only alone (a container's name is what a person has); Cmd/Ctrl+K closes as well as opens; Enter during composition; the page brought back by Back; 100 characters, not 64, so a long host name pasted whole is not cut short of a match. Checked and kept: `domains` has no `team_id`, so its arms reach the team through the app or the service's endpoint; an htmx request with no session is answered with `HX-Redirect` (`redirect` in `server.go`); the terminal's field calls `preventDefault` for Ctrl+K before the document hears of it; `narrow` in `app.js` filters in the browser and so cannot be given this field. Approved.

## Found on the way

- A list is not stale only until its request is sent. The first version compared the field with the text last *asked* for, so Enter between the request leaving and its answer arriving opened a row of the search before. There are now two texts: `asked` (sent) and `shown` (answered); the list is stale while the field differs from `shown`.
- htmx 2.0.11 reports a settled swap on the target (`#search-results`), not on each element it put in. The first version waited for the list's first child and so never marked a row; found in the browser, where no Go test looks.
- The pointer marks a row on `mousemove`, not `mouseover`: a new list arriving under a pointer that is not moving fires `mouseover`, which took the marker from the first row.
- Sorting pages among hits: `web` also finds the page Keys & tokens, by `webhook`. It is last (found elsewhere than in the name), which is where a guess that weak belongs.

- A dialog's `close` event comes with the browser's next frame, not when it closes. When that frame was late, the event arrived after the dialog had been opened again and emptied the field under the person's typing. Opening now makes the dialog fresh itself, and a `close` that finds the dialog open does nothing.

## Review of the code (2026-10-07)

An independent reading of the diff found nothing in the query (team in every arm, parameters only, no sealed column in the text) and these in the script and the pages, all fixed:

- The text a list answers was taken from the request last *sent*. htmx settles 20 ms after it swaps, and the next request can leave in between, so a list could be marked as answering a text it did not. It is now read from the answer's own request.
- A row that leads to a place on the page it is opened from (a server, on the list of servers) loads nothing, and the dialog stayed open over it. A row that is opened closes the dialog.
- The local server's card had no `id`, so its row led to the top of the list.
- Safari sends the Enter that ends a composed character after the composition, with nothing but the key's code (229) to tell it by.
- A browser filling in a form sends key events that name no key.
- A request that got no answer was shown and not said: the status line kept the last count.
- The lines in the list that are not rows are hidden from who does not see them (the status line says the same), and the marked row is the list's selected one.

## Tests (written before the code they test)

`internal/db/search_test.go`
- Each kind is found by its own text, and leads back to the right owner (a domain of an app, a domain of a service's endpoint).
- Another team's project, app, server, domain and tag are never returned, whatever is typed.
- Order: exact name before prefix before inside before other fields.
- Two words: `shop web` finds `web` in Shop; `production` alone does not list what is merely in a production environment.
- `%` and `_` match only themselves; a preview is not returned; the limit holds; an id finds its row, alone and inside a container's name, and half an id finds nothing.

`internal/web/search_test.go`
- Every signed-in page has the button in the bar before the person's menu and one dialog; signed-out pages have neither.
- `/search?q=` lists links to the right addresses for each kind, marks the match, shows the state, and escapes what is typed.
- Nothing typed: the pages. A Member is not offered Settings or Notifications; an Admin is.
- No match: the note and "No results". More than 20: the closing note. Over-long text is cut, not refused.
- Another team's rows are not in the answer. Signed out: redirect.
- Variable names and values are not found.

The route walk (`TestRouteTableRefusesByRole`) and the inline-script and duplicate-id test cover the new route and the dialog on every page they list.

## Not verified by tests

How it feels: the keys, the wait before a request, Enter while a request is on its way, focus going back to the button. Checked by hand in a browser at the end, at phone width too.
