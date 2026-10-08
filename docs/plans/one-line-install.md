# One-line install: a command that ends with musdash running

Asked for on 2026-10-08: "one link to install, like Coolify and Dokploy", with every step showing a loading animation and its state, a huge "Musdash" when the installation succeeds, and the dashboard's address in a rectangle with a pointer. This is the design; the tasks are in `one-line-install-plan.md`.

```sh
curl -fsSL https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/install.sh | sudo sh
```

## What there is today, and what is missing

Installing takes three steps on two machines: `make build-linux` on one's own, `scp` of the binary and the `install` folder, and `install/install.sh <binary>` on the server. It needs Go and a checkout.

- The repository has no tag, no release and no workflow, so there is no binary a script could fetch.
- `install.sh` takes the path of a binary and reads the two unit files from its own folder, so it cannot be piped from `curl`.
- It stops when Docker is not installed.
- It prints plain lines.

## Who it is for

Somebody with a fresh Linux server and no checkout. They paste one command and end with the dashboard's address. The same command, run again later, upgrades.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | The command fetches `install.sh` from the latest release (`releases/latest/download/install.sh`), not from `main` | Several sessions commit to `main`: a half-finished change to the script there would reach every new install the moment it is pushed. The release's copy is frozen with its binary, so the two always match. Chosen by Mahmoud over the raw `main` address |
| 2 | The address is GitHub's. No domain of our own | Nothing to host. A domain later is a redirect and changes nothing in the script |
| 3 | A release is made by a GitHub Actions workflow when a tag `v*` is pushed: vet, the short tests, both Linux binaries, `checksums.txt`, and `install.sh` with the tag written into it | A release nobody has to remember the steps of. The first is `v0.1.0`, pushed only when Mahmoud says so |
| 4 | The workflow can also be started by hand, and then builds everything and publishes nothing | The tests have never run on GitHub's machines. A failed first tag would have to be deleted and pushed again |
| 5 | `make release` makes the four files in `dist/`; the workflow runs it | One way to make a release, whether a person or the workflow does it |
| 6 | Everything is in one POSIX shell script. No `musdash install` subcommand, no tarball | What runs as root is one file that can be read before it is piped into a shell. Nothing is added to the binary. Chosen over a bootstrap script plus a subcommand, which would have drawn better and been easier to test |
| 7 | The two unit files become text inside the script, and `install/*.service` are removed | A piped script has no folder beside it. One source for the units, not two that drift |
| 8 | The whole script is functions, called on its last line | A connection that drops while the script downloads leaves a shell with half a file: it must run nothing |
| 9 | Which release: `MUSDASH_VERSION` if set, else the tag written into the script, else the latest. A version must look like `v1.2.3` | A release's script installs that release. The copy in the repository has no tag in it and takes the latest. The value becomes part of a URL, so it is checked against a pattern |
| 10 | The binary is checked against `checksums.txt` of the same release with `sha256sum`, and then run once (`musdash version`). No checksum file, or no match, and nothing is installed | A truncated or damaged download must not become the binary. It does not protect against a release that was replaced: both files come from the same place, and the script says no more than that |
| 11 | `MUSDASH_RELEASE_URL` replaces the address the files come from | A mirror, and what the tests use (`file://`) so they need no network |
| 12 | When Docker is missing it is installed with Docker's own script (`get.docker.com`), fetched into a file and then run, and the service is enabled and started. A server that has Docker is left as it is. Chosen by Mahmoud | This is what makes it one command on a fresh server; Coolify and Dokploy do the same. Docker's script knows the distributions, which this one should not have to |
| 13 | When git is missing it is installed where `apt-get`, `dnf` or `yum` is there; elsewhere the last screen says deploying from Git needs it | Deploys from Git fail without it, and a fresh Debian image has none. Three package managers cover what Docker's script covers |
| 14 | `install.sh <binary>` still installs a binary that is already on the machine and downloads nothing | A local build, and the VPS test harness, which calls it this way |
| 15 | Run again, it upgrades, and the last screen says which version replaced which. When the binary and both units are already what it would install, and both services are running, nothing is restarted | The proxy's restart refuses connections for about a second (README). Pasting the command twice must not cost that |
| 16 | The last step waits until the dashboard answers on its port, up to thirty seconds | The address is shown when it works, not a moment before |
| 17 | The address shown is the one the server's outgoing route uses. When that one is private (10/8, 172.16/12, 192.168/16, 100.64/10), one public "what is my address" service is asked, with three seconds to answer, and its answer is used only if it is an IPv4 address | A cloud server behind NAT knows only its private address, which is of no use in a browser. What the service answers is data and is checked before it is printed |
| 18 | The output of every command goes to `/var/log/musdash-install.log` (`0600`), not to the screen | The screen is the checklist. What Docker's script prints is a hundred lines |
| 19 | A step that fails turns red, the steps after it stay as they were, the last fifteen lines of that step's output are printed under the list with the log's path, and the script exits 1 | The person sees which step and why without opening a file |
| 20 | Look: the checklist (decision 21) with the heavy box (decision 23). Chosen by Mahmoud from three animated mockups | |
| 21 | All seven steps are listed from the start: `○` waiting, a spinner on the one running, `✓` done, `✗` failed, `–` not needed. The running step shows its seconds at the end of its row; a finished one shows a detail (a version, the system's name). Under the list a bar and a percentage | "Always add loading and state." The seconds were not in the mockup: they are added so that the long Docker step is seen to be alive |
| 22 | The bar moves when a step ends, by that step's share (Docker and the download have the large ones). It never creeps on its own | A bar that moves by the clock says something that is not known |
| 23 | On success the name is drawn in five lines of solid blocks, fading in from grey to blue, and under it a box of heavy lines with `❯❯❯` running toward "Dashboard" and the address for a second and a half, then still | "A huge text when installation success", "the link in a rectangle with a pointer". The script has to end, so the pointer cannot run for ever |
| 24 | The banner says MUSDASH | The name is musdash; "Mushdash" in the request was a slip |
| 25 | One background loop draws, ten times a second, from a state file the steps write. The steps run in the script's own shell | A step sets what later steps read (the architecture, the version), which a step in a subshell could not. The loop is stopped, and the cursor shown again, on exit and on Ctrl-C (`trap`) |
| 26 | Animation only when the output is a terminal of at least 20 rows and 66 columns that is not `dumb`. Otherwise one plain line when a step starts and one when it ends, and no escape sequence at all | A log file, CI and cloud-init get text they can keep. The checklist redraws eleven lines at once, which a short window scrolls into a mess |
| 27 | Colour is off with `NO_COLOR` or when not a terminal. Blues are from the 256-colour table where the terminal has it, the plain blue otherwise | no-color.org. The fade needs more than one blue |
| 28 | Block letters, the spinner and the box are drawn unless the locale names a character set that is not UTF-8 or the terminal is the Linux console (`TERM=linux`). Then: `[ok]`, `[..]`, a bar of `#`, the name in slanted ASCII letters, a box of `+`, `-` and `|` with `>>>` | A server's `LANG` is often `C` while the window the person looks at draws UTF-8, so `C` alone must not take the look away. The console's font has no braille |
| 29 | Colours are blue for what is running and for the brand, green for done, red for failed, grey for waiting. State is always a sign as well as a colour | The dashboard's own rule |

## The steps

| # | Row | What it does | Detail shown when done |
|---|---|---|---|
| 1 | Checking this server | Root, Linux, systemd, `amd64` or `arm64`, `curl` or `wget` | The system's name and the architecture |
| 2 | Installing Docker and git | Decisions 12 and 13. Makes sure the `docker` group exists | Their versions |
| 3 | Downloading musdash | The binary and `checksums.txt`; decision 10. `–` with a local binary | The version |
| 4 | Creating the musdash user | The system user, a member of `docker`; the data directory, `0700` | "already there" on an upgrade |
| 5 | Installing the services | The binary to `/usr/local/bin/musdash` by rename, the two units, `daemon-reload`, `enable` | |
| 6 | Starting musdash | Restarts the proxy, then the server, unless decision 15 says nothing changed | |
| 7 | Waiting for the dashboard | Decision 16 | The port |

Before the list is drawn the script only looks: whether it is root, and what kind of terminal it has. Not being root is said in one plain line with the command to use.

## What it looks like

Running:

```
  musdash installer   step 3 of 7

  ✓  Checking this server        Ubuntu 24.04, amd64
  ✓  Installing Docker and git   Docker 29.8.0, git 2.43.0
  ⠹  Downloading musdash         v0.1.0                          4s
  ○  Creating the musdash user
  ○  Installing the services
  ○  Starting musdash
  ○  Waiting for the dashboard

  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  58%
```

Done:

```
  ███    ███ ██    ██ ███████ ██████   █████  ███████ ██   ██
  ████  ████ ██    ██ ██      ██   ██ ██   ██ ██      ██   ██
  ██ ████ ██ ██    ██ ███████ ██   ██ ███████ ███████ ███████
  ██  ██  ██ ██    ██      ██ ██   ██ ██   ██      ██ ██   ██
  ██      ██  ██████  ███████ ██████  ██   ██ ███████ ██   ██

  ┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
  ┃                                                ┃
  ┃   ❯❯❯  Dashboard    http://203.0.113.10:8000   ┃
  ┃                                                ┃
  ┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛

  Open it and create the owner account.

  Ports    80 and 443 for your apps, 8000 for the dashboard
  Status   systemctl status musdash-server musdash-proxy
  Logs     journalctl -u musdash-server -f
  Data     /var/lib/musdash  (back up musdash.db and master.key together)
```

After an upgrade the line under the box says which version replaced which, and that there is nothing to set up again. The notes that the script prints today stay, under these: no swap on a small server, and git where it could not be installed.

Failed:

```
  ✓  Checking this server        Ubuntu 24.04, amd64
  ✗  Installing Docker and git   failed after 12s
  ○  Downloading musdash
  …

  E: Unable to locate package docker-ce
  (the last fifteen lines of that step)

  The whole output is in /var/log/musdash-install.log
```

Not a terminal:

```
musdash installer
[1/7] Checking this server
[1/7] done: Ubuntu 24.04, amd64
[2/7] Installing Docker and git
…
musdash v0.1.0 is running.
Dashboard: http://203.0.113.10:8000
```

## Files

| File | Change |
|---|---|
| `install/install.sh` | Written again |
| `install/musdash-server.service`, `install/musdash-proxy.service` | Removed; their text is in the script |
| `.github/workflows/release.yml` | New |
| `Makefile` | `release` |
| `test/install_test.go` | New |
| `README.md` | The install section leads with the command; where the other architecture's binary is downloaded for "Install proxy". The rewrite of the whole README for end users is its own piece of work, after this |
| `CLAUDE.md` | `make release`, and what holds for the script: one file, units inside it, steps and the drawing loop, no escape sequence when not a terminal |

`test/vps` calls `install/install.sh <binary>` and needs no change.

## Tests

- Always, on any machine: the script parses (`sh -n`). Its functions, called from a shell that sourced it without running it: the architecture from `uname -m`, a version that is and is not allowed, the line of `checksums.txt` for a file, which addresses are private, what a "what is my address" answer is accepted as, and the drawing: a frame of the checklist at a given state, the banner and the box (every line of the box the same width), in the UTF-8 and the ASCII look. The plain output holds no escape byte.
- With Docker (`MUSDASH_DOCKER_TEST=1`), in a Debian container, with the release's files in a directory and `MUSDASH_RELEASE_URL=file://…`:
  - a first install: the user, the binary (which runs), both units with the text expected, the services started proxy first, the dashboard answering;
  - the same again: nothing restarted;
  - a newer binary: restarted, and the versions named;
  - a wrong checksum, and no checksum file: exit 1, nothing installed;
  - an architecture that is not supported;
  - no Docker: the stand-in for Docker's script is run;
  - a local binary as the argument;
  - under a terminal (`script`): the cursor is hidden and shown again, and the last screen holds the name and the box.
- What stands in for the real thing there, since a container has no systemd: `systemctl` (which, asked to restart the server, starts the binary, so the last step waits on a real dashboard), `docker`, and `curl` for the one address of Docker's script.
- Not covered by the above, to do on a server: real systemd units, Docker's real script on a machine without Docker, the look in a real window, and the published command end to end once `v0.1.0` exists. The test VPS is shared with other sessions and is used for this only when Mahmoud says so.

## Not in this

- A domain of our own for the address.
- The other architecture's binary for "Install proxy" on a remote server: a one-line install has no `dist` folder. The README says where to download the file and where to put it.
- Removing musdash from a server.
- The DNS check for builds that the VPS test asked of the installer (finding F6): `docs/plans/test-fixes.md` left the script alone for it, and this does too.
- Releases for anything but Linux on `amd64` and `arm64`.
