# A service's deployments: a process on the Overview, a tab for the history

Asked for on 2026-10-11. Two things:

1. A service's Overview shows the log of its latest deployment, and that sometimes blocks the page. It should show the deployment's process, not its log.
2. A service has no Deployments tab. It should have one.

## What is there now

- A service has no record of its deployments. `services.status` and `services.last_error` say how the last one ended, and one file, `logs/services/<service id>.log`, holds its output. The next deployment removes the file and writes it again.
- The Overview's last card is a log view that opens `…/deploy-log/stream` as soon as the page is drawn. For a deployment that is over, the stream sends the whole file at once.
- Why that blocks: `sseWriter` sends one event a line, htmx puts each into the `<pre>` on its own, and `app.js` then sets `scrollTop = scrollHeight`, which makes the browser lay the box out again. A stack's pull and start print thousands of lines (the file may be 2 MB), so a page that only wanted to show a service's state does that work thousands of times in a row, each time over a longer text. It happens again whenever the page reloads itself at the end of a deployment (`refreshWhenSettled`). The stream also holds one of the sixteen places for live streams for as long as the Overview is open.
- The same event-a-line is what an app's deployment page and every other log view get. There it is asked for; on the Overview it is not.
- An app has all of this already: a row a deployment (`deployments`), a Deployments tab, a page a deployment with its status and its log, and an Overview that shows the latest one as a row.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | A service's deployments are rows of a table of their own, `service_deployments` (migration `0021`), not rows of `deployments` | `deployments.app_id` refers to `apps`, and what an app's deployment has (image, kept image, rollback) a stack's has not. A table that held both would be rebuilt now and would have half its columns empty for each kind |
| 2 | A row is made when a deployment is queued (`queued`), by `EnqueueService`, which now takes the trigger (`manual`, `push`, `api`) and returns the row. The job's payload names it (`deployment_id`) beside the service | The history says what asked for each deployment and holds the ones that wait. `ServiceDeployWaiting` reads the payload's `id`, which stays |
| 3 | A job whose payload names no deployment (queued by the version before this one, or by a test) makes its row when it runs, with no trigger | An upgrade must not lose a deployment that was waiting |
| 4 | Each deployment has a log of its own, `logs/services/<deployment id>.log` (`ServiceLogPath`, now by the deployment's id). The file under the service's id, from before, is removed by the service's next deployment and with the service | The history can show each log. Nothing has to be moved: the directory is the same |
| 5 | Nothing is made up for the past: a service deployed before this has no row until it is deployed again. Its Overview then says that none is on record, not that it was never deployed | The time and the duration of that deployment are not known, and a row with a guessed time would be shown as fact. What it ended with is still in `last_error` and its notice |
| 6 | A row has a `step`: `clone` (a stack from Git), `read`, `images`, `start`. The deployer writes it when that part begins | This is the process. Four small writes a deployment; reading it costs one indexed row, where finding it in the log would mean reading up to 2 MB a poll |
| 7 | The page turns the step into a list of the deployment's stages (`ui.Stages`): the ones before it are done, it is in progress (or is where it failed), the ones after it have not begun. A deployment that succeeded has every stage done | What "process" means to somebody waiting: where it is and what is left |
| 8 | Building is not a stage of its own: a stack from Git has "Pull and build the images" | Whether a file builds anything is known only once it is read. A stage that is skipped would be drawn as done |
| 9 | The Overview's card is the latest deployment as a row (what it is doing in words, what asked for it, when, how long, its pill) over its stages, and leads to the deployment's page. No log, no stream | Asked for. The page asks the database for one row |
| 10 | The card asks for itself (`ui.Poll`, `…/deployments/latest`): every two seconds while its deployment is in progress, otherwise when the header says the stack's state changed (`stack-changed`) and every fifteen seconds besides | A push starts a deployment while the page is open, and one that began and ended between two of the header's polls changed nothing the header shows. A poll with nothing new is answered 204 |
| 11 | The card does not repeat why a deployment failed | The notice at the top of the Overview says it, from `last_error`. The deployment's own page says it too |
| 12 | Deployments is the service's second tab: one table, newest first, fifty at most, each row leading to the deployment's page (`…/deployments/{dep}`): status with the stages (polled until over), the error, and the log as a stream | As an app's. The log is where somebody asked for it |
| 13 | `…/deploy-log/stream` goes; `…/deployments/{dep}/stream` follows that deployment's file. A queued deployment still says "Waiting for its turn." in the log's place | One log a deployment |
| 14 | `sseWriter.Write` sends one event for all the lines it was handed, not one a line. The event's data is the same lines, each on its own `data:` line | The cause of the block, for every log view: a finished 2 MB log arrives in about 130 events (the follower's buffer is 16 KB), so 130 insertions and layouts instead of 20,000. A live log still arrives as it is written |
| 15 | Fifty finished deployments a service are kept, with their logs (`keepDeployments`, as an app's) | The same bound |
| 16 | A deployment whose service was stopped while it waited ends `failed` with "the service was stopped before this deployment started" | It has to end, and it did not deploy anything. The service itself stays `stopped`, not `failed` |
| 17 | At start, a row that is `queued` or `running` and has no job is failed (`FailStaleServiceDeployments`), after the queue has taken back the jobs a dead process left | As `FailStaleDeployments`. A job that was requeued keeps its row and starts it again |
| 18 | A row is finished before the service's own status is written | The header's poll reloads the page when the status leaves `deploying`; the card must then find a deployment that is over |
| 19 | The card shows the deployment that is running or, with none, the newest (`db.CurrentServiceDeployment`) | Found while looking at the page: with one running and one queued behind it, the newest is the one that has not begun, and the card said "Waiting" while the stack was being deployed |
| 20 | The card stands after Domains and Stack, before the containers | It is what changes while a deployment runs. As a log it was last, because it was long |
| 21 | `app.js` bounds a log view by its text (`maxLogChars`, 512 KB), not by 4000 nodes, and keeps the count beside the log | A node was a line; with 14 it is up to 16 KB of lines, and 4000 of those is 64 MB. Half a megabyte is what 4000 lines came to |
| 22 | `app.js` goes to a log's end once a frame (`requestAnimationFrame`), not once an event | Measured: with 14 alone the 2 MB log still kept the page busy for 7 s, a layout of the whole text for each of 129 events. Once a frame it is laid out when it is drawn |

## What the review found

An independent review of the finished change, each point checked against the code before it was acted on.

| # | Found | Done |
|---|---|---|
| 23 | A write that fails in the middle of the job (marking the deployment as started, with a busy or full database) left the record `queued` and the service `deploying`: no buttons, a card that polls for good, until a restart. A panic did the same | `runServiceJob` has one guard that runs when the job ends without the record having its end: the record fails with the reason and the service leaves `deploying`. A job that shutdown ended is left alone. Tested with a database that refuses the write |
| 24 | A deployment interrupted by shutdown stayed `running` at the part it was in while its job waited again | `db.RequeueServiceDeployment`: it waits as its job does, and names no part |
| 25 | Scrolling up in a log has never paused it: the listener was on `document`, and a scroll does not bubble. Older than this change, and 22 depended on it staying broken: heard a frame late, the scroll that takes a log to its end reads as a scroll away from it | The listener hears the scroll on its way down, and measures against where the log was last taken to (`end`), not the log as it is now. Dropping old output moved into the same once-a-frame step, so nothing moves the reader in between. Checked in a browser: scrolling up pauses and holds its place while output arrives, scrolling back resumes, and 400 small writes five milliseconds apart are followed to the end |
| 26 | The frame was asked for on every event, which in a hidden page (no frames) is a callback an event without bound | Asked for once. A hidden page tends its logs once a second by timer, so that a log left open in a background tab stays small |
| 27 | A deployment that ended without writing anything (stopped while it waited, not queued) showed an empty log | The stream says so |
| 28 | A commit that could not be written failed the deployment; `CreateServiceDeployment` returned an id for a row it had not made | The first is logged, as a step is. The second returns nothing on error |

Left as it is: a job queued by the version before this one, whose record is made when it runs, can be called failed by the start-up sweep while it runs if it is the first thing the queue starts. It ends with its true state; it can happen once, on the first start after the upgrade.

## Not done

- Home's list of what was done lately still holds only apps' deployments. A service's are rows now and could be an arm of `db.RecentEvents`; that is a change to Home, and another session has `db/home.go` open.
- The API lists an app's deployments and not a service's (`/api/v1/services/{id}/deployments` would be the route).
- Cancelling a service's deployment, and rolling one back: a stack is replaced in place.
- Which deployment the running stack is from ("Serving") is not marked in the list.

## Changes

**Database** `migrations/0021_service_deployments.sql`; new `db/service_deployments.go`: `ServiceDeployment` (`Over`), the step names, `CreateServiceDeployment`, `ServiceDeployment`, `ServiceDeploymentByID`, `CurrentServiceDeployment`, `ListServiceDeployments`, `StartServiceDeployment`, `SetServiceDeploymentStep`, `SetServiceDeploymentCommit`, `FinishServiceDeployment`, `FailStaleServiceDeployments`, `PruneServiceDeployments`, `ServiceDeploymentIDs`.

**Deployer** `deploy/service.go`: `EnqueueService` (2), `runServiceJob` (3, 4, 16, 18, pruning), `deployService` (6: the step, and the commit of a stack from Git), `DestroyService` removes the logs with the rows. `cmd/musdash/main.go` (17). `config.go`: `ServiceLogPath`'s words.

**Web** `sse.go` (14). `server.go`: the routes of 10, 12, 13. New `handlers_service_deployments.go`. `handlers_services.go`: the Overview reads the latest row, `serviceDeployLog` goes, the delete removes every log, callers pass a trigger. `handlers_webhooks.go`, `api.go`: triggers. New `ui/stages.templ` and `.stages` in `input.css` (`.steps` is Home's numbered list). New `pages/service_deployments.templ`; `pages/services.templ`: the tab and the card.

**Docs** `docs/services.md`, `CLAUDE.md`.

## Tests

- `db`: a row's life (queued, started, step, finished), the latest and the list in order, pruning returns the ids it removed and keeps unfinished rows, stale rows are failed and one with a job is not, rows go with their service.
- `deploy`: a deployment records its row, its steps and its end; a failure leaves the step it failed in and its reason; the log is the deployment's own and the old file goes; a second deployment queued behind the first has a row of its own; a job without a deployment id makes one; stopped while waiting; pruning removes logs.
- `web`: the Overview holds no stream and names the latest deployment with its stages; before any, it says so; the tab lists them; a deployment's page streams its log and its status answers 204 when nothing changed; another team's are 404; a log of many lines arrives in few events and reads the same.

## Measured

A stack with a deployment whose log is 2 MB (15,210 lines), its page opened in a browser (headless Chromium, the instance on this machine):

| | Events | Until the log was whole | Main thread busy |
|---|---|---|---|
| One event a line (before) | 15,214 | 994 s | 813 s, 452 s of it in one piece |
| One event a piece (14) | 129 | 7.1 s | 7.1 s |
| and the end of the log once a frame (22, 25) | 129 | 0.1 to 0.4 s | 0.1 to 0.4 s |

The Overview opens no stream at all, and loads in under a second with the same deployment as its latest.
