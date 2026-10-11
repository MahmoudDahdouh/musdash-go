# Docker image and Compose deployments, a second pass

Asked for on 2026-10-11: "double check the UX/UI for Docker and Docker Compose deployment, also fix the issues", every tab, menu and feature, with Coolify and Dokploy read first; "at GitHub at Build with add a Docker Compose option"; and test projects, small and large, to try it with afterwards. Planned, the plan reviewed, built and the code reviewed, in that order. The first pass is `docker-deploys.md`.

## How it was looked at

A fresh install in a container of its own (Docker in Docker), four Docker image apps (two meant to fail: an image that does not exist, a port nothing listens on), the three stacks of `test/testdata/stacks`, and a stack from a public repository (`docker/awesome-compose`, `nginx-golang/compose.yaml`). Every tab of an app and of a stack was opened at 1024 and 1280 pixels. Coolify (`af589f6`) and Dokploy (`5be17d3`) were read at their source for what each offers in the same places.

## What was found

| # | Found | Where |
|---|---|---|
| 1 | A Compose file in a repository is offered only as a card of its own. The Git form's Build with, where both other products have it (Coolify's build pack list), has no Docker Compose | New app from Git |
| 2 | A stack from a repository whose file publishes `80:80` fails: "the port 80 of the server may not be published". That is what most files written for plain `docker compose` do with their web server, and a repository's file cannot be edited here | Deployment of a stack |
| 3 | The header of a stack from Git says "Your own Compose file"; the card below names the repository | Stack header |
| 4 | A stack can be created, and Deploy pressed, while a variable its file requires (`${NAME:?}`) has no value. The deployment can only fail. Both other products block it | New service, Deploy |
| 5 | With no variables stored, the Compose tab still hides the Variables box behind "Show and edit", which shows an empty box | Compose tab |
| 6 | The terminal lists a stack's containers by their full names, in no order (`musdash-p27…-gateway-1`); Logs lists the same stack by service, sorted | Terminal tab |
| 7 | A container of a stack that hangs cannot be restarted: Redeploy leaves an unchanged container alone. Both other products restart one container | Containers |
| 8 | The Stack card does not say which server the stack runs on; an app's card does | Stack overview |
| 9 | A Docker image app's card has a row "Commit: none" | App overview |
| 10 | At 1024 pixels an app's tab bar is wider than the page and cut in the middle of a word, with nothing to say that it scrolls | Tabs |
| 11 | A stack's deployment log repeats one line of Compose's progress up to twenty times ("821d9dafb26d Downloading 1.049MB") | Deployment log |
| 12 | A failure is shown as it was wrapped: "the health check did not pass…", "the Compose file could not be read…", lower case at the start of a notice | Notices |
| 13 | The new-service form from Git is one ungrouped list with the name first, where the app's Git form has three groups and takes the name from the repository | New service from Git |
| 14 | A private image fails with the registry's "denied" and nothing about what to do | Docker image app |
| 15 | A file that names its containers (`container_name`) is refused. Found when stacks from public repositories were tried, after the plan was written: one of five failed on it | Deployment of a stack |
| 16 | Several stacks made together are deployed one after another, and each one waiting shows "Deploying" over an empty log | Stack overview |
| 17 | Compose prints a line of progress for every megabyte of a layer: a pull of Postgres is hundreds of lines | Deployment log |
| 18 | A stack whose file publishes a port that is taken (`8000:8000`, beside the dashboard on 8000) was reported as deployed, with the port not published at all. Found when the stacks that had named their containers were deployed again | Deployment of a stack |

What was looked at and left: the Docker image form, the Domains dialogs of both kinds, Storage, Tasks, Environment, Metrics, the delete dialogs, the Compose reads list, logs by service, containers with their states. They do what the first pass meant them to.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | The Git form's Build with gets **Docker Compose**, on the new-app form only. Choosing it swaps the app's fields (Port, Domain, Dockerfile) for the stack's (Compose file, Variables, the environment's network), and the form makes a service from Git, the same thing the card "Compose file in a Git repository" makes. A line under the menu says so | One place where a repository is turned into something that runs. What it makes is a stack, with a stack's page; an app's page has no room for several containers. An existing app's Source form does not offer it: an app does not become a stack |
| 2 | The Compose file is named from the base directory, and what is stored is the two joined (`services.compose_path`) | The form has a base directory already, and Coolify has the same pair. No new column |
| 3 | The repository is looked at for a Compose file too, where the form offers Compose: it is chosen when the folder has one and nothing that says how an app is built (a Dockerfile, `railpack.json`, `nixpacks.toml`). The Compose file field takes the name that was found, unless the person has typed one of their own | A Dockerfile beside a Compose file is the usual shape of an app whose Compose file is for the developer's machine. The names are Compose's own, in its order |
| 4 | Ports 80 and 443 of the server are the proxy's. A stack's file that publishes one, over TCP or UDP, is deployed without that publication, the service keeps the container port among the ones it names (so the Domains dialog offers it), and the log says so and where a domain is given. Every other port a stack may not publish is refused as before | Refusing asks for a change to a file that is often not the person's. Leaving the line out publishes less, never more. Nothing is given a domain on its own: a domain that comes back after it was removed would be worse than one line in the log |
| 5 | A stack whose pasted file insists on a variable (`${NAME:?}`, `${NAME?}`) that has no value is not deployed: the new-service form refuses with the names when Deploy is ticked, and the Deploy button leads to the Compose tab with a note. A stack from Git is not held back: its file is read at the deployment, and what is stored may be an older one | The deployment can only fail, a minute later and further from the field |
| 6 | The Variables box of the Compose tab is shown when nothing is stored | There is nothing to keep from the page |
| 7 | One container of a stack can be restarted from the Containers table: `docker restart` of a container the server lists for this stack, under the stack's own lock, asked first. It applies no change; the question says so | What Redeploy does not do. The name in the request is only a choice among the stack's own containers, as the terminal's is |
| 8 | The terminal's choices are named as Logs names them: the service, with the number where a service has several containers, sorted | One stack, one set of names |
| 9 | The tab bar fades at an edge that has more behind it | The bar already scrolls to the open tab; what was missing is the sign that it scrolls |
| 10 | A line of a stack's pull or start that repeats the one before it is written once | The log is read by a person |
| 11 | A stored failure is shown with a capital first letter | It is a sentence in a notice |
| 12 | A pull that the registry refused says what to do, if signing in on the server is found to work when tried: `docker login` there, as the account musdash runs as | The store of registry credentials is still the next thing missing; until then the way that works is said where the failure is |
| 13 | A `container_name` is taken out as the proxy's ports are, and said in the log: containers keep Compose's names under the stack's project. The name becomes an alias of the service on the stack's own networks | The reason for refusing it stands (two stacks could claim one name), and the file is often not the person's. A container answers to its name on its networks, which is all the rest of a file can rely on, and an alias on the stack's own networks is already something a stack may have |
| 14 | A stack's actions for one container (logs, terminal, restart) are a menu at the end of its row | Three buttons a row left the image column of a stack of twelve the width of five letters |
| 15 | A first deployment that waits its turn says so where its log will be | "Deploying" over an empty box for the minutes the stacks before it take reads as stuck |
| 16 | Of a layer's progress a line is written every five seconds | Enough to see that it moves |
| 17 | A start that fails for a taken port is tried again only when the port is in musdash's loopback range. A taken port outside it is the file's own: the deployment ends, names the port and the service that publishes it, and removes the container that was made and not started | The second try was written for a loopback port that something took between the choice and the start. For the file's own port it cannot help, and it did harm: Docker starts the container it could not give the port, without the port, and Compose reports success |

**Not in this piece.** A store of registry credentials with a page of its own. A history of a stack's deployments (a stack has its latest log). A banner for a Compose file saved and not yet deployed. A view of the resolved Compose document (it holds every secret). Restart for an app (its Redeploy replaces the container without a gap). The header's buttons, which another piece of work (cancelling a deployment) is changing.

## Tasks

Each ends with `go vet ./... && go test -short ./...` green, a look at the diff, and a commit.

### 1. Build with: Docker Compose

- `internal/deploy/detect.go`: `GuessStack(files []string) string`, the Compose file of a folder that has one and none of Dockerfile, `railpack.json`, `nixpacks.toml`; the names in Compose's order (`compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml`). Test in `detect_test.go`: each name; the order; a Dockerfile beside it wins; `package.json` beside it does not.
- `internal/web/pages/apps.templ`: `PackCompose = "compose"`; `gitWhereFields` and `gitBuildFields` take `stack bool`. With it: the option; a hidden `stack=1` inside `#git-where` and `#compose_path` in its `hx-include`; a `<fieldset data-when="build_pack" data-is="compose">` with the Compose file, Variables and the network box; the app's Port and Domain in a `<fieldset data-when="build_pack" data-is="dockerfile static nixpacks railpack">` (a fieldset, so the required Port is disabled, not only hidden).
- `internal/web/pages/sources.templ`, `handlers_sources.go` (`gitDetect`): with `stack=1`, `GuessStack` is asked first; the answer picks `compose` and, out of band, replaces the Compose file field when what the form sent for it is empty or one of the four names.
- `internal/web/handlers_apps.go` (`appCreate`), `handlers_services.go`: a form with `build_pack=compose` is read as a service from Git (`parseRepoForm`, base directory, Compose file, variables, network) and stored by the code `serviceCreate` uses, taken out into one function; a refusal renders the app form again with the fields as sent.
- Tests (`internal/web/git_test.go`): the form offers the option and an app's Source form does not; a post makes a service with the joined path, variables and source, queues its deployment and leads to its page; a bad path, a bad repository and a taken name are refused on the app form; detection picks Compose and sends the field, and leaves a typed path alone; another team's source is refused.

### 2. A stack's forms

- `handlers_services.go`: `serviceCreate` and task 1's path refuse `deploy=1` with required variables missing (pasted file only); `serviceDeploy` leads to the Compose tab with a note instead of queueing; `renderServiceCompose` shows the box when nothing is stored.
- `pages/services.templ`: the Git form in two groups, Repository and Stack, the name after the repository with `data-suggest-here`.
- Tests (`services_test.go`, `gitservices_test.go`): the refusal names the variables and keeps the form; unticked Deploy creates; Deploy with a missing variable queues nothing; with a value it queues; the Compose tab of a stack with no variables has the textarea and no `variables_kept`.

### 3. A stack's page

- `pages/services.templ`: the header's line is `stackSource`; the Stack card has Server; a Restart form in each row that has a container, while the stack is not being deployed.
- `internal/docker/docker.go`: `Restart(ctx, name)`. `internal/deploy/service.go`: `RestartServiceContainer(ctx, serviceID, container)`, under the service's lock, the name looked for in `StackContainers`. `server.go`: `service("POST", "/containers/restart", member, …)`. `confirm_test.go`: `restart` joins the critical addresses.
- `handlers_terminal.go`, `pages/terminal.templ`: choices with a label, sorted.
- `pages`: `Said(text)` capitalises a stored failure; used for a service's `LastError` and a deployment's `Error`.
- Tests: the header of a Git stack; the restart of a listed container runs `docker restart` with that name, of one that is not listed nothing, of another team's stack 404, during a deployment a note; the terminal's labels and order; `Said`.

### 4. Ports 80 and 443

- `internal/compose/override.go`: `Unpublish(ports ...int) []Unpublished` removes the TCP publications of those server ports from every service, adds each one's container port to `expose`, and returns what it removed, sorted. `internal/deploy/service.go` calls it before `Validate` and writes a line a removal to the log.
- Tests (`compose_test.go`, `service_test.go`): long form, `host_ip`, UDP taken out too and said in its own words, the port in the layout, a file with 8080 untouched; the deployment with the scripted Runner comes up, logs the line and writes no `"published":"80"` into the resolved file.

### 5. Apps, tabs and the log

- `pages/apps.templ`: Commit only for an app from Git; `Said` for errors.
- `assets/input.css`, `static/app.js`: the tab bar's fading edges (`data-more`), set on load, scroll and resize.
- `internal/deploy`: a writer that drops a line equal to the one before it, around a stack's pull and start.
- Decision 12, after it has been tried in the test install with a registry that asks for a password: the hint under a refused pull, and the guide.
- Tests: the overview of an image app has no Commit; the writer (split writes, a last line with no end); `TestSignedInPagesHaveNoInlineScriptOrStyle` still passes.

### 6. Test projects and guides

- `test/testdata/stacks/proxy-ports.yaml`: a web server that publishes 80 and 443 and a worker beside it. `TestStacksWithDocker` deploys it and asks for the page through a domain.
- `test/testdata/projects/`: a Dockerfile project and a Compose project that builds it with a database beside it, each a directory a repository is made from.
- In the test install: the new form with Compose from a public repository, small (`docker/awesome-compose`, one service) and large (several services with builds); the stacks pasted; Docker image apps; every decision above gone through in a browser, at laptop and phone widths.
- `docs/deploying.md`, `docs/services.md`, `CLAUDE.md`.

## Review of the plan

Read against the code before anything was written. What changed:

- **Port and Domain cannot simply be hidden.** `data-when` on a `div` hides, and a hidden `required` field stops the form without a word (CLAUDE.md says so of the Git forms). They go in a `fieldset`, which the script disables as well.
- **Detection must not change an app's Source form.** `GuessPack` is asked there too, and an answer of `compose` would name a build that form does not have. The stack is looked for only when the form says it offers it (`stack=1`), and in a function of its own.
- **The first draft set the Compose file field from the answer always.** A person who typed `deploy/compose.yaml` and then chose another branch would lose it. The form sends what the field holds, and only an empty field or one of the four standard names is replaced.
- **The first draft gave a generated domain to a service whose port 80 was left out.** `SyncEndpoints` and a person's own removals would then disagree about who made it: removed by hand, it would be back at the next deployment. The log line and the Domains tab are enough (decision 4).
- **Holding back Deploy for a stack from Git** would trust a file that the next deployment replaces. Only a pasted file is held to its variables (decision 5).
- **Restart was going to take the container's name from the form and run it.** It is looked for among what the server lists for this stack, the way the terminal chooses, and the stack's lock is taken so it cannot run into a deployment.
- **The header's buttons are left alone**: the branch `cancel` changes them, and the same lines edited here would have to be merged by hand there. The one line of the header that changes is its second, which that branch does not touch.

## Review of the code

Read as a diff by a second reader with nothing but the code, this plan and CLAUDE.md. Nothing was found that crosses a team, reaches a command line with a value of a person's or a server's, or lets a path leave a repository. Two things were wrong, and nine smaller ones changed.

- **A stack was held back for variables Compose is content without.** The check used "has no default", which is what the warning on the Overview is drawn from: a file with `SENTRY_DSN: ${SENTRY_DSN}` and no value could not be deployed at all, though Compose reads it with nothing in its place. Only `${NAME:?…}` and `${NAME?…}` hold a stack back now (`catalog.VarRef.Insists`), and the second is content with a variable set to nothing.
- **Restart could restart another container than the one asked about.** Every row's form has the same address, and the list is replaced when the stack's state changes. With the question open at that moment, the dialog looked for "the form with this address" and took the first row's. Which container is meant is now the button's own name and value, the dialog looks through every form with the address for that button, and where there is none nothing is sent.
- A publication over UDP of port 80 or 443 was taken out with the words for TCP, which told the person to give it a domain. It has a sentence of its own.
- The hint for a refused pull was given for any output with "denied" in it, which is also how Docker says its own socket may not be used. The words are whole phrases now.
- "Waiting for its turn: one deployment at a time is read and built on a server" was true of stacks from Git only. It says that it waits.
- A long line with no end that arrived in equal pieces lost the second piece to "said before".
- The Compose file's path was checked in its two parts and not as what is stored: `.` and two long parts passed the form and failed at the deployment.
- A container name the file asked for went into the log as it was. Only one that passed as a plain name is written there.
- A second restart while the first waited was told the stack "is being deployed". And a finished container, which the menu does not offer to restart, was restarted for a request that asked anyway: the deployer refuses it.
- Two containers of one service had the same row name and the same question. They are called by Compose's own names.
- Three tests said more than they proved or were missing: another team's stack, a finished container, and a source "that is not ours" which was one the form never offered.

Left as it is: after a publication is taken out, the service's ports are listed with the ones it still publishes first, so for a service that publishes both 9000 and 80 the Domains dialog starts at 9000. The tab bar's fade is written with `to right`, which the right-to-left work will have to turn.

## Tried on a server

A fresh install inside a container of its own (Docker in Docker), gone through in a browser at 1024 pixels and at a phone's width, and the two tests with real Docker (`TestStacksWithDocker`, `TestProjectsWithDocker`) run inside it.

- **The test projects** (`test/testdata/projects`): the Dockerfile project built and answered as an app; the small Compose project was built by Compose, came up without port 80 and answered through a domain; the large one came up with ten services (two built, the job "Finished"), refused first for the variable it insists on, and its gateway, the repository's mounted file, the API built with the argument and one of the five that share an extension field were reached through domains.
- **Stacks from a public repository** (`docker/awesome-compose`, through the Git form with Docker Compose chosen), six of them: `nginx-golang` (two services, one built, port 80) and `wordpress-mysql` came up; `fastapi` and `prometheus-grafana` were refused for `container_name`, which is what decision 13 answers (see the last point); `nginx-golang-postgres` was built and started, and its database ended with Postgres's own account of a volume that does not suit version 18, which is the repository's to fix; `react-express-mongodb` mounts its source over the image for development, and was refused with a message that says which mount and why.
- **Five stacks made together** waited one after another, each under "Deploying" and an empty log: finding 16.
- **A registry that asks for a password** was run beside it. An image from it was refused ("no basic auth credentials"), with the hint; after `docker login` on the server the same deployment pulled it. That the installed service finds the sign-in of its own account was read from the unit (`User=musdash`, home in the data directory) and not tried on an installed server.
- One container of the twelve-service stack was restarted from its menu and was up again in seconds, the others untouched.
- **After the names were taken out**, a stack of two was pasted whose second service asks the first for a page by the first's container name: it was answered, every five seconds. `prometheus-grafana` was deployed again and read without complaint, its log naming both containers; its images were still coming down a slow line when the trial ended, so it was not seen up. `fastapi` was deployed again too. It publishes `8000:8000`, which in this install is the dashboard's own port: its log said "address already in use", then "trying others", then "Deployed", and the container was up with no port published. That is finding 18. Deployed once more after decision 17, it ends with "port 8000 of the server, which the Compose file publishes for api, is in use by something else on the server", and nothing of it is left half made.
- **Restart, with the list replaced under the open question** (the stack's state changing at that moment, made to happen by hand): the container that was asked about was the one restarted. It took a minute, for a container that does not listen to being asked to stop: the restart waited as long as a database is given. It waits as long as the container's own file says now, or Docker's ten seconds.
