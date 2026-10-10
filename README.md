<div align="center">

<img src="internal/web/static/favicon.svg" alt="musdash logo" width="88" />

# musdash

**Deploy apps, databases and services on your own server, from a dashboard that idles in under 50 MB of memory.**

[![Latest release](https://img.shields.io/github/v/release/MahmoudDahdouh/musdash-go?label=release&color=1a4fd8)](https://github.com/MahmoudDahdouh/musdash-go/releases/latest)

[Install](#quick-start) · [Features](#features) · [Documentation](#documentation) · [Report a problem](https://github.com/MahmoudDahdouh/musdash-go/issues)

</div>

## What is musdash?

musdash is a self-hosted alternative to Heroku, Vercel and Netlify. Give it a Docker image or a Git repository, and it builds your app, runs it on your server and serves it on your domain over HTTPS. Databases and ready-made services are a few clicks away.

It is built to be small. musdash is one program: there is no Node.js, PostgreSQL, Redis or separate proxy to install beside it, and its dashboard and proxy together idle in under 50 MB of memory. A small server keeps its memory for your apps.

Your apps are ordinary Docker containers on a server you control. The proxy that serves them runs apart from the dashboard, so they keep serving while the dashboard restarts.

## Features

- **Apps from an image or from Git.** Deploy a Docker image, or build from a repository on GitHub, GitLab, Bitbucket, Gitea or Forgejo with a Dockerfile, Nixpacks, Railpack, or as a static site.
- **Deploys that drop no requests.** A new version takes over only after it passes its health check. If it fails, the old one keeps serving. Roll back to any of the last five deployments.
- **A deploy on every push.** Through a GitHub App or a webhook, and a preview of every pull request at an address of its own.
- **Eight databases.** PostgreSQL, MySQL, MariaDB, MongoDB, Redis, KeyDB, Dragonfly and ClickHouse, each with a generated password and a connection string ready to copy.
- **More than 600 one-click services.** n8n, WordPress, Ghost, Uptime Kuma, MinIO and hundreds more, or a Docker Compose file of your own, with a domain for any of its services.
- **Domains and HTTPS.** Certificates from Let's Encrypt are issued on their own. Route by path, or put a password in front of a domain.
- **Backups.** Scheduled database backups with copies to S3-compatible storage, retention and restore. [Which engines](docs/databases.md#backups).
- **Scheduled tasks and notifications.** Run commands on a schedule, and hear about deployments, backups and stopped containers on Discord, Slack, Mattermost, Telegram, Pushover, email or a webhook.
- **More servers.** Deploy to other machines over SSH with no agent to install on them, or build on one server and run on another.
- **Logs, a terminal and metrics.** Live logs, a shell in any container, and charts of what servers and containers use.
- **A team.** Owner, Admin and Member roles, invitations, two-step sign-in, shared variables and tags.
- **An API.** Tokens with their own permissions for scripts, and deploy tokens for CI pipelines.

## Quick start

You need a Linux server with systemd (`amd64` or `arm64`) and root access. Run this on it:

```bash
curl -fsSL https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/install.sh | sudo sh
```

The installer sets up Docker when the server has none, downloads musdash, starts it, and ends with the address of your dashboard. Then:

1. Open that address and create the owner account.
2. Create a project, choose **Add resource**, and deploy a Docker image such as `nginx:alpine`. It gets an address that works at once.
3. Point a domain of your own at the server and add it on the app's **Domains** tab. Its HTTPS certificate is issued on the first request.

Ports 80 and 443 must be open for your apps, and port 8000 for the dashboard until you give it a domain under **Settings**.

The [installation guide](docs/install.md) has the requirements, each step the installer takes, and how to read the script before you run it.

## Upgrading

Run the same command again:

```bash
curl -fsSL https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/install.sh | sudo sh
```

Your apps keep running. The proxy restarts with the upgrade, so new connections are refused for about a second. [More about upgrading](docs/install.md#upgrading).

## Documentation

| Guide | What it covers |
|---|---|
| [Installing musdash](docs/install.md) | Requirements, upgrading, commands, settings, removing musdash |
| [Deploying apps](docs/deploying.md) | How a deploy works, rolling back, domains, deploying from Git, previews |
| [Databases](docs/databases.md) | Creating a database, connecting apps to it, backups and restore |
| [Services](docs/services.md) | The catalogue, your own Compose file, stacks from a Git repository |
| [Servers](docs/servers.md) | Adding servers over SSH, the terminal, metrics |
| [Scheduled tasks, notifications and clean-up](docs/operations.md) | Commands on a schedule, notification channels, what is cleaned up |
| [Team, access and finding things](docs/team.md) | Roles, two-step sign-in, shared variables, tags, search |
| [The API](docs/api.md) | Tokens, permissions and routes |
| [Developing musdash](docs/development.md) | Building from source, tests, how the code is laid out |

## Common questions

**How large a server do I need?**
musdash itself needs very little. Size the server for your apps, and for building them: with less than 2 GB of memory, add swap, which the installer shows you how to do.

**I forgot my password.**
Run this on the server. It prints a one-time link to choose a new one:

```bash
sudo -u musdash musdash reset-password you@example.com
```

**My account is locked after too many wrong passwords.**

```bash
sudo -u musdash musdash unlock you@example.com
```

**I lost my phone and my recovery codes.**
An Owner or an Admin can turn off your second step on the Team page. For the only Owner:

```bash
sudo -u musdash musdash disable-2fa you@example.com
```

**What should I back up?**
`/var/lib/musdash/musdash.db` and `/var/lib/musdash/master.key`, together. Without the key, the secrets stored in the database cannot be read.

**A build fails with "Could not resolve host".**
Docker's builds have no DNS on that server. [The fix is one line in Docker's settings](docs/deploying.md#deploying-from-git).

**Where are the logs?**
`journalctl -u musdash-server -f` for the dashboard and `journalctl -u musdash-proxy -f` for the proxy. An app's logs are on its **Logs** tab.

## Building from source

musdash is written in Go and builds with nothing but the Go toolchain:

```bash
make build
```

[Developing musdash](docs/development.md) has the rest: running it locally, the tests, and how the code is laid out. Found a bug, or miss something? [Open an issue](https://github.com/MahmoudDahdouh/musdash-go/issues).

## Credits

- Most of the service catalogue is made from the templates of [Coolify](https://coolify.io/services) and [Dokploy](https://github.com/Dokploy/templates); the rest were written for musdash, for services neither has. Their licences and what was changed are in `internal/catalog/services.LICENSE`.
- Icons are from [Hugeicons](https://hugeicons.com), and logos from the [selfh.st icon collection](https://selfh.st/icons). `internal/web/static/logos.LICENSE` says which is which.
