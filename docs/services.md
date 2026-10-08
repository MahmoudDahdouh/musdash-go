# Services

[← All documentation](../README.md#documentation)

One-click services from the catalogue, a Docker Compose file of your own, and stacks kept in a Git repository.

## The catalogue and your own Compose file

A service is a stack of containers described by a Docker Compose file. **Add resource** offers a catalogue of more than 600 services, "Your own Compose file" and a Compose file in a Git repository.

The catalogue is sorted into categories (AI, Analytics, Databases, Monitoring, Storage and so on); a service can be in up to three. The chips above the list narrow it to one category, and the field beside them finds a service by name or by what it does. Both work together.

Six of the templates were written for musdash (n8n, WordPress, Ghost, Uptime Kuma, MinIO, Cloudflare Tunnel) and are started for real by its tests. The rest are the templates of [Coolify](https://coolify.io/services) and [Dokploy](https://github.com/Dokploy/templates), turned into musdash's form by `tools/catalog` (the licences and what was changed are in `internal/catalog/services.LICENSE`). What to know about those:

- Each is held to the same rules as a file of your own, and the tests load every one the way a deployment does. They are not each started: a template is its makers' Compose file, and one that does not come up says why on its Deployments page, where its Compose file can be changed.
- What is reached through the proxy publishes no port. A template publishes the ports its makers did for what the proxy cannot carry (SSH for a Git server, MQTT, a game's own port), written in its Compose tab. One from 20000 to 29999, which is musdash's own range, is moved up by 10000 (Gitea's SSH is on 32222, Minecraft on 35565). A second copy of such a template on one server needs another port there before it starts.
- A port under 1024 cannot be published, so a mail or DNS server from the catalogue has its web address and no port 25 or 53.
- A value its makers left for you to set (an API key, a mail server) starts empty. It is listed under Variables.
- A dozen keep a database password their makers wrote into the file, where the other end of it is the image's own default. Such a password is reachable only inside the stack's network.
- A template asks on its form for what only you can give it: an address to sign in with, or the password of the app's first account where its makers had written one into the file for everybody.
- About 110 services of the two catalogues are not there. Most need what a stack may not have (the Docker socket, the server's own network or directories, extra capabilities); some are reached on a port of their own rather than a web address (a game server, a VPN). [catalogue-left-out.md](catalogue-left-out.md) lists each with the reason.

- Give a service of the stack a web address by adding `SERVICE_FQDN_<NAME>_<PORT>` to its environment: `NAME` is the Compose service, `PORT` the port it listens on. musdash gives it a domain (a generated one at first; change it under Settings) and routes it.
- `SERVICE_URL_<NAME>` is the same address with its scheme, `SERVICE_HTTPS_<NAME>` is `true` or `false`.
- `SERVICE_PASSWORD_<ID>`, `SERVICE_USER_<ID>`, `SERVICE_BASE64_<ID>` and `SERVICE_HEX_<ID>` are filled with values generated once per service. They are listed on the service's Overview page.
- Any other `${NAME}` in the file is yours to set, in the Variables box.

These names follow Coolify's convention, so a template written for it can be pasted.

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
