# Phase 6 — Multi-server: implementation plan

**Goal:** add a second server to the dashboard and deploy apps, databases and services to it exactly as to the first, with its own proxy and certificates; optionally build images on one server and run them on another.

**Spec:** `docs/spec.md` sections 2 (agentless, one Runner), 4.5 (remote servers, validation, build server), 6.5 (proxy on remote servers) and 8 (lazy SSH connections). Builds on phases 0–5.

**Done when:** the same app deploys to a second VPS from the UI.

## Global constraints

- Earlier constraints hold. No new modules: SSH is `golang.org/x/crypto/ssh`, already a dependency.
- Nothing is installed on a remote server except Docker (which must be there), the musdash proxy, and a data directory.
- Everything sent to a remote shell goes through `runner.Quote`. A secret never appears on a remote command line: environment variables for a remote command travel in a `0600` file that the command's own shell reads and removes.
- A remote server's host key is recorded the first time it is seen and compared ever after.

## Decisions that differ from, or add to, the spec

| Topic | Decision | Why |
|---|---|---|
| How a command's environment reaches a remote server | A `0600` file in the server's work directory, sourced and removed by the remote shell before it `exec`s the command | sshd refuses `Setenv` for all but a few names, and `env NAME=value cmd` would put clone tokens and passwords in the remote process list |
| Stopping a remote command | The session is sent `KILL` and closed when the context ends | Without a terminal sshd does not stop a command whose client went away; a cancelled build would run on |
| Connections | One per server, opened on first use, kept while anything uses it, closed after five idle minutes | The spec's rule. A server whose containers are being watched always has its events stream open, so its connection stays; that is one connection, about 200 KB |
| Host keys | Trust on first use: the key seen at "Check server" is stored with the server, shown as a fingerprint, and required from then on. A changed key fails every command with a message that says so; "Forget host key" on the server's page accepts a new one | No way to know a new server's key in advance; a silent change is what an attacker in the path looks like |
| Who musdash connects as | Any user that can run `docker`. Installing the proxy needs root, directly or through `sudo -n` | Root is not required for deployments. The proxy binds 80 and 443 and is a systemd service |
| Where things live on a remote server | A data directory per server, `/var/lib/musdash` for root and `~/.musdash` otherwise, changeable. `config.Config.On(server)` gives the paths for a server; code that writes through a Runner uses it | Until now one data directory served for both the control plane's own files and the files written on "the server", which were the same machine |
| Health checks | Through the Runner: `Runner.Dial` opens a connection as seen from the server (a forwarded channel over SSH), and the probe uses it | A container's port is on the remote server's loopback interface |
| The proxy binary on a remote server | The control plane's own executable, copied over, when the remote is Linux of the same architecture; otherwise the file `<data>/dist/musdash-linux-<arch>` if the operator put one there, else a message saying which file is missing | One binary; no download from the internet at install time, no compiler on the server |
| Proxy service on a remote server | `musdash-proxy.service`, written by the dashboard, `Restart=always`, running as the connecting user with `AmbientCapabilities=CAP_NET_BIND_SERVICE` | As on the first server |
| Build server | An app may name another server to build on. The image is built there and moved with `docker save` piped into `docker load` on the target, through the control plane as a stream | The spec's registry needs one to exist and credentials for it on both sides. A pipe needs nothing, costs no memory, and is correct; a registry can be offered later for large images |
| Stacks from Git | Built on their own server | `docker compose build` and the checkout the stack mounts from belong together |
| Dumps of a remote database | Streamed through the control plane to the server's backup directory | Memory stays constant. Running gzip on the remote side would need a shell pipeline per engine image; later, if bandwidth matters |
| Choosing a server | "New app", "New database" and "New service" ask which server when the team has more than one | With one server nothing changes |
| Deleting a server | Only when nothing runs on it. The proxy service and data directory are left on the machine, and the page says so | musdash does not delete things on a machine it is about to lose access to without being asked |

## Data model (migration `0011_remote_servers.sql`)

`servers` gains: `ssh_key_id` (the key musdash signs in with), `host_key` (the server's public key, once seen), `data_dir`, `arch`, `status` (`unknown`, `ok`, `unreachable`), `status_detail`, `checked_at`, `proxy` (`none`, `installed`), `docker_version`.

`apps` gains `build_server_id`.

## File map

```
migrations/0011_remote_servers.sql
internal/runner/ssh.go            SSHRunner
internal/runner/ssh_test.go       against an SSH server started in the test
internal/runner/runner.go         Dial added to the interface
internal/servers/servers.go       the pool: connections, idle close, host keys
internal/servers/check.go         validation of a server, proxy install
internal/config/config.go         On(server)
internal/deploy/*                 paths through Cfg.On(server); probe through the Runner; build server
internal/web/handlers_servers.go  add, check, install proxy, forget host key, delete
internal/web/pages/servers.templ
```

## Interfaces

```go
// internal/runner
type Runner interface {
	// … as before …
	Dial(ctx context.Context, network, address string) (net.Conn, error) // as seen from the server
}
type SSHConfig struct {
	Host string; Port int; User string
	Signer  ssh.Signer
	HostKey ssh.PublicKey                 // nil: accept and report through Seen
	Seen    func(key ssh.PublicKey) error // called with the key the server presented
	WorkDir string                        // for environment files
}
func DialSSH(ctx context.Context, cfg SSHConfig) (*SSHRunner, error)

// internal/servers
func (p *Pool) Runner(ctx context.Context, s db.Server) (runner.Runner, error)
func (p *Pool) Check(ctx context.Context, s db.Server) (Report, error)
func (p *Pool) InstallProxy(ctx context.Context, s db.Server) error
func (p *Pool) Forget(serverID string) // drop a pooled connection

// internal/config
func (c Config) On(s db.Server) Config // the same layout under the server's data directory
```

## Tasks

### Task 1 — SSHRunner
- [x] `SSHRunner`: `Run`, `Output`, `WriteFile` (temporary file then `mv`, mode applied before content), `ReadFile`, `MkdirAll`, `RemoveAll`, `Dial`, `Close`.
- [x] Environment through a removed `0600` file; cancellation kills the remote command; host key pinning.
- [x] Tests against an SSH server started inside the test (the `x/crypto/ssh` server side, running commands with the local shell): output and exit codes, stdin, quoting of hostile arguments and file names, a secret in `Env` absent from every command line, a cancelled command gone from the process list, a wrong host key refused, atomic write, `Dial` reaching a listener on the "server".

### Task 2 — Paths and probes per server
- [x] `Config.On(server)`; every path given to a Runner goes through it. Logs the control plane writes with `os` stay on the control plane.
- [x] `Runner.Dial`; the probe uses the server's Runner.
- [x] The pool: one connection per server, idle close, reconnect after a drop, a changed host key reported as such.
- [x] Tests: the whole deploy suite still passes on the local path; a deploy through an `SSHRunner` to this machine with a different data directory writes nothing into the control plane's.

### Task 3 — Servers in the dashboard
- [x] Add a server (name, address, port, user, key: an existing one or a new one whose public key is shown to be installed); check it (reachable, host key recorded, Docker and its version, Compose plugin, git, architecture, memory, disk, data directory created); status on the list.
- [x] Forget host key; delete a server that has nothing on it.
- [x] Server choice in the New app, database and service forms.
- [x] Tests: validation, team scoping, a server of another team never offered or reachable.

### Task 4 — Proxy on a remote server
- [x] Copy the binary, write the unit, start it, write the first routes file; report a missing binary for another architecture clearly.
- [x] Routes and certificates per server already follow from `SyncRoutes(server)`.
- [x] Tests with the scripted Runner: the commands, `sudo -n` only when not root, nothing done twice.

### Task 5 — Build server
- [x] `apps.build_server_id`; build there under that server's build lock, `docker save` → `docker load` on the target, remove the image from the build server afterwards.
- [x] Tests: order of commands on the two servers; a failed transfer leaves the old version serving.

### Task 6 — End to end
- [ ] With `MUSDASH_DOCKER_TEST=1`: this machine added as a "remote" server through an SSH server started by the test, an app deployed to it and answering, its routes written under the remote data directory.
- [ ] Independent review; README; RSS on Linux with one remote server connected.

## Review focus

1. **A value a person typed reaching the remote shell unquoted** — host names, users, paths, branch names, container names; the SSH tests use hostile strings for every argument position.
2. **A secret on a remote command line or left in a remote file** — the environment file's life is asserted in the tests.
3. **A changed host key accepted quietly** — pinned after first use; the test swaps the server's key.
4. **A connection that died being used for ever** — the pool drops it and reconnects; a test kills the server mid-session.
5. **Files of the control plane's data directory used as if they were on the remote server, or the reverse** — the task 2 test deploys with two different data directories and checks each.

## Self-review of this plan

- Spec coverage for phase 6 rows: remote servers over SSH and key management (tasks 1–3), validation (3; installing Docker itself is left to the person, with the command shown, because it needs root and a choice of package source), build server (5, by a pipe instead of a registry), remote proxy (4).
- The scheduler, backups, tasks, clean-up and notifications of phase 5 take their Runner from the same pool and need paths through `Config.On`; task 2 covers them.
- What cannot be verified here: a real second machine. The tests use this machine over a real SSH connection, which exercises the protocol, quoting, paths and port forwarding but not a different architecture, a firewall, or systemd.
- Interface names match across tasks.

**Approved for implementation.**
