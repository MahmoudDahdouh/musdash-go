# Docker image and Compose deployments: domains for any service, and what was missing around them

Asked for on 2026-10-09: "update the UX/UI for Docker and Docker Compose deployment, fix the issues, and add the missing parts, like add domains: I cannot see it when I deploy using Docker", with Coolify and Dokploy to be read first, and test projects, small and large, to try it with afterwards. Planned, the plan reviewed, built and the code reviewed, in that order.

## What was there

A Docker image is an app, and an app has a Domains tab with everything on it. A Compose stack is a service, and a service had no way to be given a domain at all: an address existed only where the file itself used `SERVICE_FQDN_<NAME>_<PORT>`. The stack this was asked about is two services, `static` and `nextjs`, each with a `ports:` line and neither with a magic variable. It deployed, and its page said "No web address. To give a service one, add SERVICE_FQDN_NAME_PORT to its environment in the Compose file." That is Coolify's convention, and a file written for plain `docker compose` does not know it.

Both products were read at their source (Coolify `3f953d1`, Dokploy `5be17d3`). What they share, and musdash lacked:

- A domain is given to **one service of the stack** in a dialog: a service picker that lists what the file has, the domain, the container port. Coolify groups a stack's domains by service; Dokploy puts a service badge on each. Dokploy fails a deployment whose domain names a service the file no longer has, with both names in the message.
- The stack's page lists its **containers one by one** with a state each (Coolify's "Compose resources", Dokploy's Containers tab), and logs and the terminal choose a container.
- Variables the file reads are found for the person (Coolify makes a row for each `${VAR}` and marks the required ones).
- Both say "redeploy to apply" after a change that a running stack cannot take.

What musdash already does that they do not, and keeps: the file is loaded in a sandbox and what runs is the checked document; nothing of the person's is handed to Compose on the server.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | A domain can be given to any service of a stack, on any port, from the dashboard. It is an endpoint like the ones the file names, with `manual = 1`: its Compose service and port are what the person chose, and it has its one domain | The endpoint is already what a domain of a service hangs from (routes, search, deletion, the DNS record). A second model beside it would have every one of those twice |
| 2 | `SyncEndpoints` leaves a manual endpoint alone: it adds and removes only what the file names | Saving the file must not take a domain away that the file never knew of |
| 3 | A deployment publishes each target (service and port) once on a loopback port, however many endpoints lead to it, of either kind | Two domains for one program are two names, not two ports |
| 4 | A manual endpoint whose service the file does not have fails the deployment before anything is started, naming the domain and the service | The alternative is a domain that silently answers nothing. What ran before keeps running |
| 5 | What a stack's file holds is recorded when a deployment has read it (`services.layout`, JSON: each service's name, image, container ports, whether it is built). Saving a different text empties it | The dialog offers the services and their ports from it, and the Containers card names what is missing. It describes the stored text or nothing: a list that is out of date would refuse a service the person has just added |
| 6 | While the layout is known the dialog's Service is a Select of its services, and choosing one puts its first port into the Port field (`data-suggest`, which the Picker has already); a name that is not in the list is refused. While it is not known (never deployed, or the text was changed since) Service is a text field and the deployment is what checks it | No YAML parser: the file is only ever read by Compose, in the sandbox, at a deployment. The usual path, create and deploy, has the list by the time anybody looks for domains |
| 7 | A stack's domains are a tab of their own, **Domains**, as an app's are: one table (domain, where it leads, how it is served), Add domain, Change, Remove, Check DNS. Settings loses its Addresses card, and the Overview's card links to the tab | One place for each thing, and the place a person looks is the one an app has |
| 8 | An endpoint the file names is changed there and not removed: its row says that it is the file's | It would be back at the next save |
| 9 | A domain added to a target that is already published is routed at once. Otherwise the answer says to redeploy, and which port is not published yet | A running container publishes what it was started with, as an app's domain with a port of its own already says |
| 10 | A service's domain takes a whole host, with no path and no password | As before. An app has those; a stack's endpoint did not, and the proxy's rules for a shared host are the app's |
| 11 | The Overview lists the stack's containers as the server reports them, each with its state in a pill and Docker's own sentence, and what the file publishes on the server. It is asked when the page is opened, when the header has a new state to show (`HX-Trigger: stack-changed`) and when Refresh is pressed; never on a timer, and never stored | "Running" for a stack of fifteen says nothing about which one is restarting. Each answer is a command run on the server, so nothing asks for it while nothing has changed. What a server answers is data: a name is matched against the service's own, the rest is cut and shown as text |
| 12 | Logs can be narrowed to one Compose service, chosen among the stored members | The merged output of a large stack cannot be read |
| 13 | A failed pull says why: the end of what Docker printed is in the error, as it is for a stack that did not come up | "docker exited with status 1" was all the page had |
| 14 | The new-service form and the Compose tab list the variables the text reads as it is typed (required, with a default, generated, an address), from `catalog.ScanVariables` and `ScanMagic`, with no Docker involved | The person learns before the deployment which values are theirs to give |
| 15 | The form's hint stops teaching the magic variables first: a domain is added on the Domains tab; the variables stay for templates | It was the only way and is now the second one |

**Not in this piece.** Credentials for a private registry (neither a Docker image app nor a stack can pull from one that needs a login; it is a store of its own, with a page, and is the next thing missing). A path, a password or a www redirect on a service's domain. Restarting one container, or a stack without deploying it. A preview of the resolved Compose document (it holds every secret).

## Data model (migration `0020_stack_domains.sql`)

- `service_endpoints.manual INTEGER NOT NULL DEFAULT 0`. A manual endpoint's `name` is `~` and its own id: no file can name it (a magic variable's name is upper-case letters, digits and underscores), and `UNIQUE (service_id, name)` holds.
- `services.layout TEXT NOT NULL DEFAULT ''`.

## Tasks

1. **Model and deployment.** The migration; `db.Endpoint.Manual`; `AddEndpoint`, `DeleteEndpoint` (manual only), `SetServiceLayout`, `UpdateServiceCompose` emptying the layout with a changed text; `compose.Project.Layout`; the deployment's targets. Tests: the queries; with the scripted Runner, one publication per target, the failure for a service that is gone, the layout recorded; the layout of documents with `expose`, long and short `ports`, UDP left out.
2. **The Domains tab.** Routes `GET …/domains`, `POST …/domains`, `POST …/domains/{eid}`, `POST …/domains/{eid}/delete`, `GET …/domains/{eid}/dns`; `pages.ServiceDomains`; `ui.Option.Suggest`. Tests: add, change, remove, the refusals (a service the layout does not have, a port out of range, a generated address with https, a host that is taken), another team's service, the DNS check, the pages' lists.
3. **The stack's page.** Containers with their states; logs by service; the pull's error.
4. **The forms.** What the text reads, on the new-service form and the Compose tab; the hints; what the Docker image form turns out to need when it is used.
5. **Test projects.** `test/testdata/stacks/`: one service with no port named; a web service with a database, variables and a health check; a stack of twelve; the edge cases (a required variable, `expose` only, a port from a variable, a container that runs once and ends). `test/stacks_test.go` deploys each with real Docker under `MUSDASH_DOCKER_TEST=1`, gives one a domain from the dashboard's side and asks for it through the loopback port. The same files are pasted into a running instance and gone through in a browser, with Docker image apps beside them.
6. **Guides.** `docs/services.md`, `docs/deploying.md`, CLAUDE.md.

## Review of the plan

Read against the code before anything was written. Five things changed.

- The first draft ran the sandbox when the dialog was opened, to list the services of a file that had not been deployed. That is a `docker run` from a GET, and for a stack from Git it needs a checkout that only a deployment makes. The layout is recorded by the deployment instead (5), and the dialog has a text field until there is one (6).
- A manual endpoint was going to carry a name made from its service and port. Two domains for one target would then collide on `UNIQUE (service_id, name)`; the name is the row's own id behind a character no file can write.
- `assign` in `deployService` keyed host ports by endpoint name and published one port an endpoint. With (3) it keys by target, and an endpoint that joins a published target takes that port: otherwise the retry after "port is already allocated" would move one of two endpoints and leave the other pointing at a port nothing listens on.
- The route of a domain is the endpoint's `host_port`, and a stopped or never-started stack is left out by `RoutesForServer` already, so (9) needs no rule of its own: copying the port of a published target is enough.
- Removing a manual endpoint leaves its loopback port published until the next deployment. Nothing routes to it and the port is the stack's own; it is said in the plan and not worked around.

What stays as it was planned: the tab, the fragment for containers, the scan.

## Review of the code

Read as a diff by a second reader with nothing but the code and this plan, and tried on a server. Nothing was found that crosses a team or reaches a command line. Six things changed.

- **One port, one target.** `assign` kept each endpoint's stored port for its target without asking whether another target had kept the same one. Two endpoints that shared a target share its port; when the file changes and they part (`SERVICE_FQDN_FRONT_3000` becomes `…_8080` while a person's domain still leads to 3000), both targets were published on one loopback port and `up` failed. A kept port now goes to the first target that claims it and the other gets a new one (`TestOneLoopbackPortGoesToOneTarget`).
- **A domain removed during a deployment** made `SetEndpointTarget` answer "not found" and failed the deployment, on a retry after containers had been replaced. It is skipped: the domain is gone, and that is all.
- **A stack from Git kept its layout** when its repository, branch or path was changed, so the dialog offered the services of another file. `UpdateServiceSource` empties it, and the refusal of an unknown name says that the list is the last deployment's.
- **The container list asked every 15 seconds for as long as a tab was open**, a `docker ps` on the server each time, holding one of the dashboard's four readings. It asks when the page opens, when the header's poll has a new state to show, and when Refresh is pressed (decision 11 was rewritten). A first request that finds every reading taken answers with a line that says so, where it used to answer nothing and leave "Asking the server…" standing.
- **The layout stored an image name as long as the server printed it.** It is cut, as are the number of services and of ports.
- **A test said more than it proved:** the removal of another team's endpoint was tried with one the file names, which is refused for being the file's. It is tried with one of each kind now.

## Tried on a server

The three stacks of `test/testdata/stacks` were pasted into a fresh install running inside a container of its own (Docker in Docker, so its clean-up at start could not touch anything else on the machine), with four Docker image apps beside them, and gone through in a browser. `TestStacksWithDocker` does the same from the deployer's side. What that found:

- **Three stacks made together on a new server all failed**, each with "pull docker:29-cli: docker exited with status 1". Each first deployment pulled the image that reads Compose files, side by side, and Docker's image store fails pulls of one image that overlap ("lease does not exist"). One deployment a server fetches it at a time now (`fetchSandbox`).
- **"docker exited with status 1" was all a failed pull said**, for a stack's images, for the sandbox image and for an app's image. The error ends with Docker's last lines: "pull access denied", "error from registry: denied". They are shown on lines of their own.
- **An app's tab bar was wider than the page on a laptop**, and its last tab, Settings, was cut off with nothing to say that the bar scrolls. Below 84rem a bar of nine tabs or more shows the words without their icons, and fits.
- A stack's containers stopped on purpose were listed as "Exited" in red. They are "Stopped".

What worked as planned: a domain added to `web` of the two-service stack answered through the proxy after a redeployment and a second name for the same port at once; a domain for the cache waited with "Redeploy to serve"; the twelve-service stack came up with eleven running and the migration "Finished", four domains on three ports; the stack without its required variable failed with the file's own message; the stack with no port named was reached on port 80 of its one service.
