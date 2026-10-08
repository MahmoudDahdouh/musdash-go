# Installing musdash

[← All documentation](../README.md#documentation)

Installing, upgrading and removing musdash on a server, and the commands and settings of the binary.

## What you need

- A Linux server with systemd, on `amd64` or `arm64`. musdash is tested on Ubuntu 24.04.
- Root access, with `sudo` or as root.
- Ports 80 and 443 reachable from the internet, for your apps and their certificates, and port 8000 for the dashboard until you give it a domain.
- Nothing else. Docker is installed for you when the server has none, and so is git where `apt-get`, `dnf` or `yum` is there.

musdash itself idles under 50 MB of memory. Building images is what needs memory: on a server with less than 2 GB and no swap, the installer shows the commands that add swap.

## Install

```bash
curl -fsSL https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/install.sh | sudo sh
```

The installer shows each step as it runs:

1. **Checking this server**: that it is Linux with systemd, on a supported architecture.
2. **Installing Docker and git**: only what is missing. Docker comes from Docker's own install script.
3. **Downloading musdash**: the binary for the server's architecture, checked against the release's checksums before it is used.
4. **Creating the musdash user**: a system user that runs both services and is a member of the `docker` group.
5. **Installing the services**: the binary at `/usr/local/bin/musdash` and two systemd services, `musdash-server` and `musdash-proxy`.
6. **Starting musdash.**
7. **Waiting for the dashboard**, so that the address it ends with works when you open it. It also watches the proxy for three seconds: when another web server already has port 80 or 443, the proxy cannot start, and the installer says so instead of calling the install done.

To read the script before it runs as root, download it first:

```bash
curl -fsSL -o install.sh https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/install.sh
```

```bash
sudo sh install.sh
```

When a step fails, the installer names it and prints the end of that step's output. The whole output is in `/var/log/musdash-install.log`.

In a terminal the steps are drawn as a list that fills in. Where the output is not a terminal (a file, a pipe, cloud-init), or the window is smaller than 20 rows by 70 columns, each step is a plain line. Three settings change what the installer does:

| Variable | What it does |
|---|---|
| `MUSDASH_ADDRESS` | The address shown for the dashboard at the end. Without it the installer uses the server's own, and asks one public service (`ipv4.icanhazip.com`) when the server only knows a private one |
| `MUSDASH_VERSION` | Installs that release (`v0.1.0`) and not the latest |
| `NO_COLOR` | Any value turns colour off |

They go between `sudo` and `sh`, with `env`:

```bash
curl -fsSL https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/install.sh | sudo env MUSDASH_ADDRESS=203.0.113.10 sh
```

## First steps

1. Open `http://<server address>:8000` and create the owner account.
2. Check **Servers**: Docker and the proxy should both show as running, and the public IP address should be the server's.
3. Create a project, then **Add resource** and **Docker image**, with an image such as `nginx:alpine`. The generated `sslip.io` address works at once over HTTP.
4. For HTTPS, point a domain's DNS at the server and add it on the app's **Domains** tab. The certificate is issued on the first request.
5. To put the dashboard itself on a domain with HTTPS, set it under **Settings**.

Ports 80 and 443 must be reachable from the internet for certificates to be issued.

## Upgrading

Run the install command again. It installs the latest release and says which version replaced which.

Apps keep running, and keep serving while the control plane restarts. The proxy is the same binary and is restarted too, so new connections to apps are refused for about a second, and for up to fifteen when a long request (a download, a log stream) is under way, which the proxy lets finish first. Upgrade when that is acceptable. Running the command when there is nothing newer restarts nothing that is running; a service that is down is started. An upgrade that was cut off before the restart (a dropped connection) is finished by running the command again.

To install one release and not the latest, take that release's installer:

```bash
curl -fsSL https://github.com/MahmoudDahdouh/musdash-go/releases/download/v0.1.0/install.sh | sudo sh
```

## A remote server of another architecture

A server you add under **Servers** gets its proxy from the dashboard's own binary. When that server has another architecture than the dashboard's (an ARM server added to a dashboard on `amd64`, say), put that architecture's binary where the dashboard looks for it, then choose **Install proxy**:

```bash
sudo -u musdash mkdir -p /var/lib/musdash/dist
```

```bash
sudo -u musdash curl -fsSL -o /var/lib/musdash/dist/musdash-linux-arm64 https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/musdash-linux-arm64
```

Use `musdash-linux-amd64` in both places for the other direction. More about remote servers is in [Servers](servers.md).

## Installing a build of your own

The installer also takes a binary that is already on the server and downloads nothing. Build it as [Developing musdash](development.md#build) says, copy it over with the `install` folder, and run the installer with it:

```bash
scp -r dist/musdash-linux-amd64 install root@your-server:/root/
```

```bash
ssh root@your-server 'cd /root && ./install/install.sh ./musdash-linux-amd64'
```

Use `musdash-linux-arm64` on ARM servers. Everything else is as with the command above: Docker and git are installed when missing, and nothing is downloaded but those.

## Commands

Both services run as the `musdash` user, and so should a command that reads the data directory:

```bash
sudo -u musdash musdash reset-password you@example.com
```

The link it prints starts with `http://localhost:8000` unless `MUSDASH_URL` is set: put your dashboard's address in its place.

| Command | What it does |
|---|---|
| `musdash server` | Runs the control plane |
| `musdash proxy` | Runs the edge proxy |
| `musdash migrate` | Applies database migrations and exits |
| `musdash reset-password <email>` | Prints a one-time link to choose a new password |
| `musdash unlock <email>` | Lets an account that was locked for too many wrong passwords or codes try again at once |
| `musdash disable-2fa <email>` | Turns off an account's two-step sign-in, for somebody who lost their phone and their recovery codes |
| `musdash version` | Prints the version |

## Settings

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `-data` | `MUSDASH_DATA` | `/var/lib/musdash` | Directory for the database, keys, logs and backups |
| `-listen` | `MUSDASH_LISTEN` | `:8000` | Address the UI listens on (`server`) |
| `-http` | `MUSDASH_PROXY_HTTP` | `:80` | Address the proxy serves HTTP on (`proxy`) |
| `-https` | `MUSDASH_PROXY_HTTPS` | `:443` | Address the proxy serves HTTPS on; empty turns HTTPS off (`proxy`) |
| `-dev` | `MUSDASH_DEV=1` | off | Request logging and the component gallery at `/_ui` |
| `-pprof` | | off | Serves `/debug/pprof` to loopback clients only |
| | `MUSDASH_MASTER_KEY` | | Base64 of the 32-byte key that encrypts stored secrets. When unset, the key is created at `<data>/master.key` |
| | `MUSDASH_URL` | `http://localhost:8000` | Address used in links printed by `reset-password` |

Back up `<data>/master.key` together with the database. Without the key, stored secrets cannot be decrypted.

The two services get their settings from their systemd units, `/etc/systemd/system/musdash-server.service` and `musdash-proxy.service`. The installer writes both again when their text changes with a release, so a setting changed by hand belongs in a drop-in (`systemctl edit musdash-server`), which it leaves alone.

## Where things are

| What | Where |
|---|---|
| The binary | `/usr/local/bin/musdash` |
| The services | `musdash-server` (dashboard, webhooks, jobs) and `musdash-proxy` (ports 80 and 443) |
| Data: the database, the master key, logs, backups, certificates | `/var/lib/musdash` |
| The dashboard's log | `journalctl -u musdash-server -f` |
| The proxy's log | `journalctl -u musdash-proxy -f` |
| The installer's output | `/var/log/musdash-install.log` |

Back up `/var/lib/musdash/musdash.db` and `/var/lib/musdash/master.key` together. One is of no use without the other.

## Removing musdash

Stop and remove the two services and the binary:

```bash
sudo systemctl disable --now musdash-server musdash-proxy
```

```bash
sudo rm /etc/systemd/system/musdash-server.service /etc/systemd/system/musdash-proxy.service /usr/local/bin/musdash
```

```bash
sudo systemctl daemon-reload
```

The containers musdash started are Docker's and keep running, but with the proxy gone nothing leads a domain to them. Remove them with `docker` when you no longer want them.

The data directory holds the database, the master key and your backups. Removing it cannot be undone, so copy what you want to keep first:

```bash
sudo rm -rf /var/lib/musdash
```

```bash
sudo userdel musdash
```
