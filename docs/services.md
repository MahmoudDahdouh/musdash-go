# Services

[← All documentation](../README.md#documentation)

One-click services from the catalogue, a Docker Compose file of your own with a domain for any of its services, and stacks kept in a Git repository.

## The catalogue

A service is a stack of containers described by a Docker Compose file. **Add resource** offers a catalogue of more than 600 services, "Your own Compose file" and a Compose file in a Git repository.

Every card has two links: **Website** is the project's own site, and **Docs** is the page to read before you run it, the project's self-hosting or Docker guide where it has one.

The catalogue is sorted into categories (AI, Analytics, Databases, Monitoring, Storage and so on); a service can be in up to three. The **Category** menu above the list narrows it to the categories you tick, and the field beside it finds a service by name or by what it does; several words narrow further (`wordpress mysql`). Both work together, and both are in the page's address, so a narrowed list can be refreshed, bookmarked or sent to somebody. The list shows 48 services at a time and brings the next 48 as you scroll; the heading says how many there are in all, and Back from a service's form returns to where you were in the list.

Six of the templates were written for musdash (n8n, WordPress, Ghost, Uptime Kuma, MinIO, Cloudflare Tunnel) and are started for real by its tests. Most of the rest are the templates of [Coolify](https://coolify.io/services) and [Dokploy](https://github.com/Dokploy/templates), turned into musdash's form by `tools/catalog` (the licences and what was changed are in `internal/catalog/services.LICENSE`). The others were written for musdash too, for services neither of the two has: each from what the project's own makers say about running it with Docker, and each started on a real Docker once before it was added, where it had to come up, answer on its address, survive a redeploy and go away with its volumes. Which services to write one for was taken from the directory at [selfh.st/apps](https://selfh.st/apps/).

Open a new service's address as soon as it is deployed. With many apps the first person to arrive makes the administrator's account (WordPress, Uptime Kuma, n8n and Gitea are like that), and a service has a public address from the moment it is up. Where an app can be given its first account from outside, the template does that with a generated name and password, which are under Variables. A service that would start with a sign-in everybody knows, or with none while it holds something private, is not in the catalogue.

What to know about the templates made from Coolify's and Dokploy's:

- Each is held to the same rules as a file of your own, and the tests load every one the way a deployment does. They are not each started: a template is its makers' Compose file, and one that does not come up says why on its Deployments page, where its Compose file can be changed.
- What is reached through the proxy publishes no port. A template publishes the ports its makers did for what the proxy cannot carry (SSH for a Git server, MQTT, a game's own port), written in its Compose tab. One from 20000 to 29999, which is musdash's own range, is moved up by 10000 (Gitea's SSH is on 32222, Minecraft on 35565). A second copy of such a template on one server needs another port there before it starts.
- A port under 1024 cannot be published, so a mail or DNS server from the catalogue has its web address and no port 25 or 53.
- A value its makers left for you to set (an API key, a mail server) starts empty. It is listed under Variables.
- A dozen keep a database password their makers wrote into the file, where the other end of it is the image's own default. Such a password is reachable only inside the stack's network.
- A template asks on its form for what only you can give it: an address to sign in with, or the password of the app's first account where its makers had written one into the file for everybody.
- About 130 services of the two catalogues are not there. Most need what a stack may not have (the Docker socket, the server's own network or directories, extra capabilities); some are reached on a port of their own rather than a web address (a game server, a VPN); and some thirty are projects that are over: archived by their makers, given up, or replaced by another that the catalogue has. [catalogue-left-out.md](catalogue-left-out.md) lists each with the reason. A service you made from a template that has since left the catalogue keeps running: it has its own copy of the Compose file.

## Your own Compose file

Paste the file as you would run it with `docker compose`. The form lists what the text reads while you type: the variables that need a value from you, the ones the file has a default for, the values musdash generates and the services that get a domain. Nothing is started until the service is deployed, and the first deployment says what Docker made of the file.

- Any `${NAME}` in the file is yours to set, in the Variables box (one `NAME=value` a line, stored encrypted). One the file gives no default for (`${NAME}` or `${NAME:?message}`) is listed under "Needs a value".
- `SERVICE_PASSWORD_<ID>`, `SERVICE_USER_<ID>`, `SERVICE_BASE64_<ID>` and `SERVICE_HEX_<ID>` are filled with values generated once per service. They are listed on the service's Overview page.
- A template written for Coolify can be pasted as it is: `SERVICE_FQDN_<NAME>_<PORT>` in a service's environment gets that service a domain on that port (a generated one at first), `SERVICE_URL_<NAME>` is the same address with its scheme, and `SERVICE_HTTPS_<NAME>` is `true` or `false`.

## Domains

A service of the stack is given a domain on the stack's **Domains** tab, whatever the file says: **Add domain** asks for the service, the domain and the port the service listens on inside its container. Once the stack has been deployed the services are listed with the ports the file names (`ports:` and `expose:`), and choosing one fills in its port; before that, type the service's name as the file has it.

- A stack can have many domains: several services, several ports of one service, several names for the same port.
- A container publishes what it was started with. A domain for a port that no domain led to before is served from the next deployment on, and its row says "Redeploy to serve" until then; one more name for a port that is already served works at once.
- A domain that leads to a service the file no longer has stops the deployment, with the domain and the service named, and what was running keeps running. Remove the domain or add it again for the right service.
- An address the file itself asks for with `SERVICE_FQDN_…` is in the same list, marked "From the file". It can be changed there and goes when the file stops naming it. The stack is told its own address when it starts, so redeploy after changing one.
- **Generate domain** makes an address that needs no DNS, served over plain HTTP. A domain of your own needs a DNS record that leads to the server, which the tab names; **Check DNS** in a domain's row says whether it is there. A stack's domain takes the whole name: paths and passwords are an app's.

## What is running

The Overview lists the stack's containers one by one as the server reports them: each service's state (running, unhealthy, starting, exited, or finished for one that ran once and ended well, as a migration does), its image, and the ports its file publishes on the server itself. A service the file has and the server has no container for is named too. The list is read when the page opens and when the stack's state changes; **Refresh** asks again.

**Logs** shows every container's output together, or one service's: the stack's services are above the log. **Terminal** opens a shell in the container you choose.

A stack is held to the same limits as an app. Its file is read inside a container that has no network and sees nothing of the server, then checked: privileged mode, the host's network or process namespaces, devices, extra capabilities, mounts of system directories, of the Docker socket or of the stack's own directory, networks with a subnet of their own, and volumes or networks that belong to something else are refused, with a message naming the line. Files the Compose file names (`env_file`, `include`, `extends`) are not read: a stack is one file plus its variables. The first deployment on a server downloads the `docker:<version>-cli` image used for that check.

A stack runs on its own network. Tick "Connect to the environment's network" if its containers and the apps and databases of the environment need to reach each other by name; names must then be unique across them.

To publish a port that is not HTTP (a mail server, a game server), use an ordinary `ports:` entry with a host port from 1024 to 65535, outside 20000 to 29999.

## Stacks from a Git repository

A service also takes a Compose file that lives in a repository ("Compose file in a Git repository"), read through the same GitHub Apps, GitLab sources and deploy keys as apps. At every deployment musdash clones the branch, reads the file in the sandbox with only the checkout in view, and applies the same checks as to a pasted file. Differences from a pasted stack:

- `build:` is allowed for contexts inside the repository. Images are built with `docker compose build`, one build at a time per server, and named by musdash.
- Files of the repository can be mounted into containers (`./nginx.conf:/etc/nginx/nginx.conf`). They are mounted read-only, and a path that is a symbolic link in the repository is refused. Data that a container writes belongs in a named volume.
- `include`, `extends` and `env_file` may name files of the repository.
- A push to the branch redeploys it, through the GitHub App or through a webhook you add to the repository; a deploy token does the same for a CI pipeline. Both are made on the Keys & tokens page, under API Tokens.
- Only the Compose file itself is scanned for `SERVICE_…` variables, not files it includes.
- Domains are given on the Domains tab as for a pasted file. The services it offers are those of the file as it was last deployed: deploy first when the repository has a new one.
