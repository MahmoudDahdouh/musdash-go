# Servers

[← All documentation](../README.md#documentation)

Deploying to more than one server, opening a terminal in a container, and seeing what servers and containers use.

## More servers

Servers, "Add a server" takes another machine that has Docker and is reached over SSH. Nothing of musdash runs on it except its containers and, if you install it, the proxy that serves their domains.

1. Add the server: its address, SSH port and account, and a key (a new one is made for it unless you pick an existing one). The account must be able to run `docker`: root, or a member of the `docker` group. The server's sshd must forward connections for that account (`AllowTcpForwarding yes` or `local`, which is OpenSSH's default but not Alpine's, and not that of every hardening guide): that is how musdash checks that a new container of an app answers on its port.
2. Put the public key the page shows into that account's `~/.ssh/authorized_keys` on the server.
3. Choose **Check**. The first check records the server's host key and shows its fingerprint with the kind of key it is, to compare with the server's own: for `ED25519 SHA256:…`, what `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` prints there. musdash asks for the Ed25519 key first, then ECDSA, then RSA, as `ssh` does, and the page names the file. From then on musdash talks only to a machine that presents this key; if the server is reinstalled, "Forget host key" and check again. The check also reports Docker, the Compose plugin, git, memory, whether sshd forwards connections, and whether the proxy can be installed.
4. Choose **Install proxy** to serve domains from that server. It copies the musdash binary into the server's data directory and starts it as the systemd service `musdash-proxy`, which needs root or `sudo` without a password. For a server of another architecture than the dashboard's, put that architecture's binary at `<data>/dist/musdash-linux-<arch>` first: [Installing musdash](install.md#a-remote-server-of-another-architecture) has the command that downloads it, and `make build-linux` produces both from source.

With more than one server, the form a resource is added with asks which one. Point an app's domain at the server it runs on.

- A remote server keeps musdash's files (env files, Compose stacks, backups, routes, certificates) in its own data directory: `/var/lib/musdash` for root, `~/.musdash` otherwise, or what you entered. It has to be a directory for musdash alone, which makes directories in it and clears out what its own builds left: a system directory or a home directory is refused, and so is a directory that already holds other things when the server is first checked. A check leaves the file `.owned-by-musdash` in it, which is how musdash knows the directory later.
- A command's secrets never appear in the server's process list: they travel in a private file that the command's shell reads and removes.
- One SSH connection per server is opened when first needed and closed after five idle minutes. A server with running containers is watched for their state, so its connection stays open.
- **Build server.** A Git app's Settings can name another server to build on. The image is built there and moved with `docker save` piped into `docker load` on the app's server: no registry is needed. Use it to keep builds off a small server.
- Removing a server from the dashboard changes nothing on the machine: its proxy service and data directory stay until you remove them.

## Terminal

Apps, databases and services have a Terminal tab: a shell in the running container (`bash` where the image has it, `sh` otherwise), as the user the image runs as. It is a shell in the container, never on the server. It ends when you leave the page.

- It is drawn by a small script of musdash's own, not by a terminal library: shells and full-screen programs (`vi`, `less`, `top`, `psql`) work, with colours, a scrollback of 2000 lines and copy by selecting. The mouse is not passed to programs, and double-width characters (CJK, emoji) take one column, so a line that holds them is misaligned.
- At most eight terminals are open at once, and one that carries nothing for thirty minutes is closed.
- Behind a proxy of your own in front of musdash's, WebSocket upgrades must be passed on.

## Metrics

The Metrics tab of an app, a database or a service shows what its containers use now (processor, memory, network, disk), read from `docker stats` while the page is open and not otherwise. Servers, "Processor, memory and disk" shows the same for a server (from `/proc` and `df`; Linux only) and for every container on it.

History is off until you switch **sampling** on for a server on that page. Then, once a minute, one sample of the server and of each app, database and service on it is stored, and kept for a day; the pages draw it as charts over 1, 6 or 24 hours. Switching it off removes the samples.
