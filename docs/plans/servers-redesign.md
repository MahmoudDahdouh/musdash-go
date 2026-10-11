# Servers: a list, a page for each server, and usage that reads at a glance

Asked for on 2026-10-11: "redesign these pages: `/servers`, `/servers/{id}/metrics`", keeping the taste of the design system.

## What is there now

Looked at with five servers (this machine, and four remote ones: ready, needing attention, not reachable, never checked), a day of samples and three containers.

**`/servers`**

- One card a server, under one another in a column of `max-w-3xl`: five servers are four screens, and the right half of a wide window is empty.
- A card is a key-value list of six to nine rows. What a person looks for first (is it reachable, is Docker there, is the proxy up, how full is it) is spread over the head's pill and two of the rows, and how full it is, is not there at all although Home shows it.
- The way to the usage page is a text link in the list, "Processor, memory and disk"; shared variables are another.
- Every remote card has a primary **Check** button, so the page has as many primary buttons as servers, plus **Add server**.
- Proxy, host key and removal are loose buttons at the foot of each card. The public key and where to put it are repeated in every card that is not ready.
- This machine's card is laid out differently from a remote one's.

**`/servers/{id}/metrics`**

- The page is its server's name and three cards. It does not say whether the server is reachable, and leads nowhere else of the server's.
- **In use now** starts as one line ("Reading what is in use…") and becomes a card five times as high a second later: everything under it jumps.
- Its four meters are a two by two grid of unequal cells (Load has no bar, Memory no note).
- The containers are listed by container name (`musdash-kq3…-d41`), which nobody knows a thing by.
- **Sampling** is a third card at the bottom, and the empty **Over time** card points at it ("It is switched on below"). Switching it off removes a day of samples on one click.

## What the ui-ux-pro-max searches gave

Asked for a design system for an infrastructure dashboard, for charts of usage over time and of capacity, and for five UX rules. The palette and fonts it proposes (dark, Fira) are not taken: the design system here is the one to keep. What is used:

| From | Rule | Where it lands |
|---|---|---|
| pattern "Real-Time / Operations" | Say that telemetry is live, how often it is read, and when it stopped; give a way to go on | The Now card's head: unchanged wording, kept next to the figures |
| chart "Trend over time" | A line with a faint fill, one series; the latest, average and highest written as text | `ui.Chart` already is this; unchanged |
| chart "Real-time streaming" | Not below one update a minute: use a chart that is refreshed, not a streaming one | Samples are one a minute: the charts stay as drawn, no live chart |
| ux "Color only" | Never colour alone | A full meter keeps its word ("High", "Nearly full"); a server's state is a pill with an icon and a word |
| ux "Loading indicators" | A stable skeleton with `aria-busy`, no jump | The Now card is drawn at its full size before the first reading |
| ux "Table handling" | A table scrolls sideways on a small screen | `ui.Table` does; the containers' table keeps to it |

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | `/servers` is a grid of tiles (`ui.Tiles`), one a server, the whole tile a link to the server's page | "Lists of things a person opens are tiles." Projects, environments and resources are; servers were the exception |
| 2 | A server gets a page: `GET /servers/{id}`, **Overview**. With `/servers/{id}/metrics`, **Usage**, it is two tabs under one header (`serverFrame`) | A server has nine facts, four actions, a check's report and a key to install: that is a page, not a card. The usage page gets a frame that says which server it is and how it is doing |
| 3 | The header is the server's name, its state as a pill, and under it its address in mono (`root@203.0.113.20:22`, or "This machine") | The shape of an app's header |
| 4 | A tile shows: the name, the state pill, the address, three meters (processor, memory, disk) from the last stored sample when it is at most five minutes old, and a bottom row of how many things run there, Docker's version and the proxy | What a person looks for first, at a glance. The meters are Home's (`db.LatestServerSamples`): the page asks no remote server anything, as before |
| 5 | A tile without a recent sample says "Usage is not recorded" or "No recent reading" in the meters' place, as a line | A missing value stays a word (`empty-states.md`, decision 8) |
| 6 | This machine's tile is the first and says "This machine" where a remote one has its address. Its state is Docker's: "Ready", or "Docker not reachable" | One layout for both kinds. The badge "This machine" is on its page |
| 7 | The tile's pill is `ui.StatePill` with the server's own word. `TileProps` gets `StateText` for it, and `Body` for what stands between the text and the bottom row | A server's states are not a resource's ("Needs attention", "Not checked yet"). Extending the component, not a tile of the page's own |
| 8 | **Overview** is cards in two columns from `lg`: **Connection** (address, checked, host key, data directory; **Check** in its head) and **Software** (Docker, proxy with its install button, what runs here, public IP address with **Change**). Under them **Shared variables** as a row that leads to the page, and for a remote server **Remove**, which holds Forget host key and Remove server | Each action next to what it is about. Check is the one primary button, and only while the server is not ready |
| 9 | What a check found, a stored problem, and the key to install stand above the cards, in that order | They are what the person came for after pressing Check or adding the server |
| 10 | Shared variables stay a page of their own that the Overview leads to, not a third tab | As a project's Settings leads to its variables. Its template is being changed in another session today; a tab can follow. Its trail names the server and leads back to its page |
| 11 | What acts on a server leads back to its page: Check renders it with the report; the proxy, the host key and the address redirect to `/servers/{id}`. Removing leads to `/servers`. Adding a server leads to the new server's page | The person is where the thing is. After adding, the next step (install the key, Check) is on that page |
| 12 | The search's row for a server leads to `/servers/{id}`; the `#server-…` anchors go | A server has a page now |
| 13 | Home's rows keep leading to a server's usage | They show meters; the page behind them is the meters' |
| 14 | **In use now** is a strip of four cells across (two from `sm`, one below): label, the figure in mono, the bar, a note. New `ui.Stats` and `ui.Stat`, which take `ui.MeterProps` | Equal cells whatever each has. `ui.Meter` stays for a narrow column (Home, the tiles) |
| 15 | Before the first reading the card is drawn whole: the same four cells with a dash for a figure, `aria-busy`, and "Reading what is in use…" as its status | No jump (`UsagePending` takes the labels) |
| 16 | A server's containers are listed by what they belong to: the kind's icon, the resource's name as the link, the container's name under it in mono. Sorted by that name. Figures are right-aligned, tabular | People know "storefront", not `musdash-kq3…`. A fixed order: a table sorted by use would reshuffle every ten seconds |
| 17 | The name comes with the place in one query (`db.NamedPlace`, beside `db.PlaceOf`), a query a container as today, at most `maxContainers` | No cache of rows |
| 18 | **Over time** holds sampling: its foot says what sampling does and has the switch for an Admin. Off, the card is the empty state with **Switch sampling on** in it. The third card goes | One card for one subject, and the empty state's action is in the state (`empty-states.md`, decision 6) |
| 19 | Switching sampling off asks first, saying that the kept samples are removed | "Whatever stops, removes or replaces something asks first" |
| 20 | The stretch of time stays three small buttons in the card's head | It is a choice of three, not navigation between pages |
| 21 | The charts are unchanged | They are what the searches recommend, and they are shared with every resource's Metrics tab |
| 22 | The resources' Metrics tabs get the same Now strip and placeholder, since they share the templates | One look for one thing. Their four figures (processor, memory, network, disk) fit the strip |
| 23 | Nothing new is asked of a server, no script, no new dependency, no new icon | The RAM rules; every icon needed is in `ui.Icon` |

## Not done

- A list of what runs on a server, on its Overview. It is a count today and stays one: the Usage tab lists the containers with links.
- Renaming a server, or changing its address. Neither exists; a redesign adds no feature.
- Shared variables as a third tab (decision 10).

## Files

- `internal/web/server.go`: `handle("GET /servers/{id}", member, s.serverShow)`.
- `internal/web/handlers_servers.go`: `renderServers` builds tiles (stored samples, this machine's Docker); new `renderServer` and `serverShow`; `serverCheck` renders the server's page; the redirects of decision 11.
- `internal/web/handlers_metrics.go`: `serverMetrics` hands the frame its `ServerInfo`; `serverMetricsNow` names containers by their resource; `UsagePending` gets its labels.
- `internal/web/handlers_home.go`: the meters of a stored sample become `sampleMeters`, shared with the Servers page.
- `internal/db/projects.go`: `NamedPlace`.
- `internal/web/pages/servers.templ`: `Servers` (tiles), `ServerShow`, `serverFrame`, `serverTabs`; the two cards go.
- `internal/web/pages/metrics.templ`: `UsagePending`, `UsageNow`, `historyCard`, `ServerMetrics`.
- `internal/web/pages/search.templ`: the server's row.
- `internal/web/ui/display.templ`: `TileProps.StateText`, `TileProps.Body`.
- `internal/web/ui/chart.templ`: `Stats`, `Stat`.
- `internal/web/assets/input.css`: `.stats`, `.stat*`, `.tile-body`.
- `internal/web/pages/gallery.templ`: the strip and a server's tile in the component gallery.
- Tests: `servers_test.go`, `metrics_test.go`, `apps_test.go`, `dns_test.go`, `search_test.go`, `team_test.go`, `web_test.go` (the list of signed-in pages), `db` for `NamedPlace`.
- `docs/servers.md`, `CLAUDE.md`.

## Tests, written first

1. `/servers` holds a tile a server that links to `/servers/{id}`, and none of the forms that act on one.
2. `/servers/{id}` shows a remote server's facts, key and actions; another team's server and an unknown id are 404; a Member sees no forms.
3. Check answers with the server's page and the report; proxy, host key and address lead to `/servers/{id}`; removing leads to `/servers`.
4. A tile has meters when a sample is recent and the line when it is not.
5. The usage page has both tabs, the placeholder strip with its four labels, and the sampling switch inside Over time; switching off carries a question.
6. A reading lists a container under its resource's name with the link; a stranger's stays a name without one.
7. The search leads to `/servers/{id}`.
8. `db.NamedPlace` finds nothing for another team's resource and for a kind that is not one.

## Review of the plan

Read again against the code before building.

- **Decision 4 and the local server.** `serverInfo` asks this machine's Docker and proxy with an eight second limit on every view of the list. That is today's behaviour and stays; the tile needs it for its state. Remote servers are still shown as stored.
- **Decision 7.** `ui.Tile` passes `State` to `StatusPill`, which words it itself. With `StateText` set it must call `StatePill(State, StateText)`, and a server never checked needs a pill too: its state is `"unknown"`, which `stateTone` gives the neutral tone. `data-state` stays the state.
- **Decision 8, Check as primary.** `TestCriticalFormsAsk` reads forms at `/proxy`, `/forget-host-key` and `/delete`: all three keep `ui.Ask`. `/check` is not critical and has none.
- **Decision 11, adding a server.** `text_test.go` and `servers_test.go` post the form and expect a redirect; only the target changes. `CreateServer` returns the row, so the id is at hand.
- **Decision 11, the address.** The Change dialog posts to `/servers/{id}` and, refused, must render the server's page with the dialog open, not the list. `addServerOpen` then no longer has to tell the two forms apart: the list has one form.
- **Decision 14, long figures.** A resource's network figure is "1.2 MiB in · 18.4 MiB out", wider than a cell of four. The figure wraps (`overflow-wrap`), and the strip's columns are `auto-fit` with a least width, so a narrow card has two columns before a figure is squeezed.
- **Decision 15.** The placeholder must not be read out as figures: the dashes are `aria-hidden` and the status line is what a screen reader gets.
- **Decision 16, what the server says.** A container's labels are the server's word. The name shown is the database's, found by an id that looks like one, for the signed-in team; a container whose label finds nothing keeps its own name and gets no link, as now. Nothing of the label reaches the page.
- **Decision 18, a Member.** The switch is an Admin's. A Member reads "An Admin or an Owner switches sampling on and off." in the foot or the empty state.
- **Decision 19.** The form posts to `/servers/{id}/metrics`, which is not one of the critical addresses of `TestCriticalFormsAsk`; the question is added all the same and the test for it is its own.
- **Polling.** `UsageNow` replaces itself (`outerHTML`) every ten seconds. The strip's cells keep their size between readings, so nothing moves; a button is not under the pointer in that card except "Read again", which appears only when the polling has stopped.
- **`TestEmptyListsUseTheComponent`** fails for `<p class="muted…">No…`. The tile's line "No recent reading" is written as Home's is, which the test allows by its sentence; the new sentence "Usage is not recorded." does not start with one of its words.
- **`TestSignedInPagesHaveNoInlineScriptOrStyle`** gets `/servers/{id}` and `/servers/{id}/metrics`; ids on a page must be single: the Change dialog's field is `ip` once a page.

## Found on the way

- **The placeholder never asked twice.** `UsagePending` asked for its reading on `load` and at no other time. While all four readings are in use the answer is a 204, for which nothing is swapped in, and the page then said "Reading what is in use…" until it was reloaded. It asks again every ten seconds until it is replaced.
- **A check was sent again by Refresh.** The page after Check is the answer to a POST. It now gives the server's own address (`Shell.Address`), so Refresh fetches the page.
- **The Over time head on a phone.** The title's line was squeezed into three by the buttons beside it; they go under it where they do not fit.
- **Lines that doubled.** A strip under the card's head has the head's line and none of its own; one under a notice, and the containers' table under either, get one.
- **A stored failure started small.** "dial tcp …: i/o timeout" goes through `pages.Said` like every other stored failure.

## Looked at

In a second musdash on its own data directory, with a Docker and a `/proc` that are made up: five servers (this machine; ready and sampled; needing attention; not reachable; never checked), three containers, a day of samples. At 1440 and 375 pixels: the list, a remote server's Overview in each state, this machine's, Usage with a reading, without one (the server did not answer), with sampling on and off, an app's Metrics tab, and the gallery. No page scrolls sideways at 375. A check was run against the server that is never there, for the report's card.

Not looked at for real: a check that succeeds and installs a proxy, on a real remote server. The tests go through both against an SSH server of their own.

## Review of the code

Read by a second reviewer against the plan and `CLAUDE.md`: authorization and team scoping, behaviour in every state of a server, the conventions the tests do not catch, and the new markup's accessibility. Nothing was found in any of the four. Two things below its bar were taken:

- Two messages still sent a person to "the Servers page" for what is on a server's own page now (a server that was never checked, a proxy to install again). They name the server's page.
- The placeholder asks every ten seconds, and a reading may take twelve. A question asked while one is out is dropped (`hx-sync="this:drop"`), said in the markup and not left to htmx's default.

My own reading of the diff found the two lines of CSS under "Found on the way" (a strip under a notice, a table under a head) and nothing else that changes behaviour.
