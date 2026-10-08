# One-line install: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `curl -fsSL https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/install.sh | sudo sh` ends with musdash running and its address on the screen.

**Architecture:** One POSIX shell script, `install/install.sh`, holds everything: the release it downloads from, the two systemd units as text, the seven steps and the drawing. A GitHub Actions workflow builds the four files of a release when a tag is pushed, through `make release`.

**Tech stack:** POSIX `sh` (dash on the servers, bash as `sh` on a Mac), GNU coreutils, systemd, GitHub Actions (`actions/checkout@v7`, `actions/setup-go@v7`, `gh release create`), Go tests that run the real script.

**Spec:** [one-line-install.md](one-line-install.md). Where this plan and the spec disagree, this plan wins; the differences are in "Decisions that extend the spec".

## Global constraints

- No new Go module, and nothing added to the binary (CLAUDE.md, "Hard constraints").
- The script is POSIX `sh`: no arrays, no `local`, no `[[`, no `echo -e`, no `$'…'`. It must parse and behave the same under dash and under bash called as `sh`.
- Values that come from the machine (the system's name, versions, what an address service answers) are data: cut to printable ASCII before they are drawn, matched against a pattern before they become part of a URL or a path. Nothing read from a file or a command is ever `eval`ed or sourced as state.
- Commands are argument vectors with every expansion quoted. The script never builds a command line as a string.
- No escape sequence reaches output that is not a terminal.
- A release's version is `v` and three numbers (`v0.1.0`), and no other shape.
- Tests run the real script with real processes; what stands in for systemd and Docker inside a container is named as a stand-in.
- `go vet ./... && go test -short ./...` is green before every commit. Commits are staged by path. The work is on the branch `one-line-install`, rebased on `origin/main` before it is pushed to `main`.

## Decisions that extend the spec

| # | Decision | Why |
|---|---|---|
| P1 | The last step asks `http://127.0.0.1:8000/healthz`, not `/` | The server has that route for this (`internal/web/server.go`), with no session and no redirect |
| P2 | `MUSDASH_ADDRESS` names the address the last screen shows and skips finding it | A person who knows their address or already has a domain; and the tests, which must not call a public service |
| P3 | A downloaded binary is installed as `/usr/local/bin/musdash.new` before it is run for the first time | `/tmp` is mounted `noexec` on hardened servers, so a binary cannot be tried where it was downloaded |
| P4 | State between the steps and the drawing loop is one small file a step, read with `read -r` | The loop is another process. A file that is sourced would run whatever a version string held |
| P5 | The drawing loop ends only between two frames (it traps `TERM` and finishes the frame it is in), and draws the last frame itself | A loop killed in the middle of a frame leaves the cursor somewhere unknown, and the banner under it would be drawn over the list |
| P6 | Steps run in the script's own shell as functions with their output sent to the log, and every command in a step ends in `\|\| return 1` | `set -e` is switched off inside a function that is called as a condition, which is how a step's failure is caught |
| P7 | The release workflow also runs the installer's container tests | A release whose installer cannot install its own binary must not be published |
| P8 | No pre-release tags. If the workflow fails on `v0.1.0`, nothing is published; the fix is tagged `v0.1.1` | The command asks for the latest release, so the number does not matter, and no tag has to be moved |
| P9 | git is not installed inside the container tests (a stand-in `git` is on the path) | Installing it there needs the network and a package index. The real thing is checked on a server |

## Review focus

What the spec implies and a person will meet, most likely first. Each has its test in the task named.

1. The connection drops while the binary downloads: a short file must not be installed (Task 3, wrong checksum).
2. The command is pasted a second time on a server that serves apps: nothing restarts (Task 3, same again).
3. The output goes to a file or a pipe (`| tee install.log`, cloud-init): no escape byte, and still every step and the address (Task 3, plain run).
4. A system's name or a version holds an escape sequence or is very long: it is drawn as plain, cut text and the rows stay aligned (Task 1, `clean`; Task 2, frame).
5. A step fails: the list ends in its final state with the cursor back, the reason is on the screen, exit 1 (Task 3, unsupported architecture under a terminal).

## Files

| File | Responsibility |
|---|---|
| `install/install.sh` | The installer. Sections in this order: settings, pure functions, the two units, drawing, steps, `main`, the last line that calls it |
| `install/musdash-server.service`, `install/musdash-proxy.service` | Removed |
| `test/install_test.go` | Tests of the script: functions and drawing on any machine, whole installs in a container |
| `Makefile` | `release` |
| `.github/workflows/release.yml` | Vet, tests, `make release`, publish |
| `docs/install.md`, `docs/development.md`, `CLAUDE.md` | What changed for a reader and for the next session |

---

### Task 1: The script's skeleton and its pure functions

**Files:** create `test/install_test.go`; rewrite `install/install.sh` (settings, pure functions, the guard on the last line; `main` only says "not built yet" until Task 3).

**Interfaces produced** (each prints its answer on stdout and returns 0, or returns 1):

| Function | Does |
|---|---|
| `arch_of <uname -m>` | `amd64` for `x86_64`/`amd64`, `arm64` for `aarch64`/`arm64` |
| `valid_version <v>` | 0 only for `v<n>.<n>.<n>` |
| `release_url <file>` | From `BASE`, `VERSION`: `$BASE/<file>`, or `…/releases/download/$VERSION/<file>`, or `…/releases/latest/download/<file>` |
| `checksum_for <file> <checksums.txt>` | The hash of the line whose name is exactly `<file>` (or `*<file>`) |
| `sha256_of <file>` | With `sha256sum`, else `shasum -a 256` |
| `valid_ipv4 <s>` | Four numbers of 0 to 255 and nothing else |
| `is_private <ipv4>` | 10/8, 172.16/12, 192.168/16, 100.64/10, 127/8, 169.254/16 |
| `clean <text> <max>` | Printable ASCII only, cut to `<max>` characters |
| `utf8_ok` | 1 for `TERM=linux` or a locale that names a character set other than UTF-8 |

The script ends with:

```sh
# Everything above is definitions. A download that stopped half way has no
# last line, so it runs nothing.
[ "${MUSDASH_INSTALL_LIB:-}" = 1 ] || main "$@"
```

- [ ] **Step 1:** Write `test/install_test.go` with a helper that runs `sh -c '. ../install/install.sh; <call>'` with `MUSDASH_INSTALL_LIB=1` (and again under `dash` when it is installed), and table tests:
  - `TestInstallScriptParses`: `sh -n`.
  - `TestInstallArch`: `x86_64`→`amd64`, `amd64`→`amd64`, `aarch64`→`arm64`, `arm64`→`arm64`; `riscv64`, `armv7l`, `i686`, empty fail.
  - `TestInstallVersion`: `v0.1.0`, `v10.20.30` pass; `0.1.0`, `v1.2`, `v1.2.3-rc1`, `v1.2.3/../x`, `latest`, `v1.2.3 `, empty fail.
  - `TestInstallReleaseURL`: the three cases.
  - `TestInstallChecksumFor`: picks the exact name out of two lines; a longer name ending the same way (`xmusdash-linux-amd64`) does not match; the `*name` form matches; a missing name prints nothing.
  - `TestInstallAddresses`: private `10.0.0.1`, `172.16.0.1`, `172.31.255.255`, `192.168.1.1`, `100.64.0.1`, `100.127.255.255`, `127.0.0.1`, `169.254.1.1`; public `172.32.0.1`, `100.128.0.1`, `8.8.8.8`, `203.0.113.10`. `valid_ipv4` refuses `1.2.3`, `1.2.3.4.5`, `256.1.1.1`, `a.b.c.d`, `1.2.3.4; id`, `<html>`, empty.
  - `TestInstallClean`: `"Ubuntu\x1b[31m 24.04\n"` with 40 gives `Ubuntu[31m 24.04`; a 100-character text with 34 gives 34 characters.
  - `TestInstallUTF8`: `LANG=C` yes, `LANG=en_US.UTF-8` yes, `LANG=en_US.ISO-8859-1` no, `LC_ALL=de_DE.utf8` yes, `TERM=linux` no.
- [ ] **Step 2:** `go test ./test -run TestInstall -v` fails (no such functions).
- [ ] **Step 3:** Write the script's settings and functions.
- [ ] **Step 4:** The tests pass under `sh` and `dash`.
- [ ] **Step 5:** Commit: `Install: the installer's own functions, tested from a shell that sources it`.

### Task 2: The drawing

**Files:** `install/install.sh` (drawing section), `test/install_test.go`.

**Interfaces produced:**

| Name | Does |
|---|---|
| `row <n>` | Sets `ROW`, the row's name padded to its column, and `WEIGHT`, its share of the bar (5, 40, 25, 5, 5, 10, 10); `name_of <n>` prints the name |
| `set_state <n> <wait\|run\|ok\|fail\|skip> [detail]` | Writes `$TMP/s.<n>` (state on line 1, detail on line 2, the second it started on line 3) by rename |
| `frame <tick> <first>` | Prints the twelve lines of the checklist from the state files; unless `<first>` is 1 it first moves the cursor up twelve lines. Uses `UTF8`, `COLOR` |
| `painter` | The loop of decision P5; started as `painter & PAINTER=$!`, stopped by `stop_painter` |
| `banner_lines` | The name: five lines of blocks, or of slanted ASCII when `UTF8` is empty |
| `banner` | Prints it; with `FANCY`, fades it in over six frames |
| `box <address> <lit>` | The five lines of the box; `<lit>` 0 to 2 is the chevron that is bright, 3 is all of them |
| `plain <text>` | Prints the line only when `FANCY` is empty |

- [ ] **Step 1:** Tests, each sourcing the script with `TMP` set to a directory the test filled:
  - `TestInstallFrame`: states ok, ok, run, wait ×4 with `COLOR=` and `UTF8=1`; the output has twelve lines, `✓`, a spinner character, `○`, "step 3 of 7", and `45%` (5 + 40). With `UTF8=` the same in ASCII (`[ok]`, `[..]`, `[  ]`, `#`), and every byte below 128.
  - `TestInstallFrameRedraws`: `frame 1 0` starts with `ESC[12A`; `frame 0 1` does not.
  - `TestInstallFrameKeepsItsColumns`: a detail of 200 characters and one holding `ESC[2J` leave every row at most 80 columns and without `ESC`.
  - `TestInstallBanner`: five lines, all of one width, at most 62 columns, in both looks.
  - `TestInstallBox`: five lines of one display width (runes counted in Go) in both looks, holding the address; with `UTF8=` only ASCII.
- [ ] **Step 2:** They fail. **Step 3:** Write the drawing. **Step 4:** They pass under both shells.
- [ ] **Step 5:** Run the drawing for the eye: a throwaway script that sources the installer and plays the seven steps with `sleep`, in a real terminal of this machine, in both looks.
- [ ] **Step 6:** Commit: `Install: the checklist, the name and the box`.

### Task 3: The steps

**Files:** `install/install.sh` (units, steps, `main`), remove the two `.service` files, `test/install_test.go`.

**Interfaces produced:** `unit_server`, `unit_proxy` (print the unit's text, byte for byte what the removed files held); `fetch <url> <file>`; `step_check`, `step_docker`, `step_download`, `step_user`, `step_services`, `step_start`, `step_wait`; `run_step <n> <function>`; `public_address`; `main`. A step sets `DETAIL` and, when it had nothing to do, `SKIP=1`.

- [ ] **Step 1:** Container tests in `test/install_test.go`, skipped without `MUSDASH_DOCKER_TEST=1`. The helper builds `musdash` for Linux on the Docker server's architecture with a version given by `-ldflags -X main.version=…`, writes it and its `checksums.txt` into a release directory, writes the stand-ins (`systemctl`, `docker`, `git`; and for single cases `uname`, `curl`), and runs `buildpack-deps:bookworm-curl` with the script, the release and the stand-ins mounted read-only, `MUSDASH_RELEASE_URL=file:///rel`, `MUSDASH_ADDRESS=203.0.113.10`.
  - The stand-in `systemctl` appends its arguments to `/tmp/systemctl.log`; `is-active` answers from a marker file; `restart musdash-server.service` starts `/usr/local/bin/musdash server` as the `musdash` user, so the last step waits on a real dashboard.
  - `TestInstallFresh`: exit 0; `id musdash` is in `docker`; `/usr/local/bin/musdash version` says the version; both units equal the text of the files removed in this task (kept in the test as constants); the log has `restart musdash-proxy.service` before `restart musdash-server.service`; `/var/lib/musdash` is `700 musdash`; the output has `[1/7]` to `[7/7]`, `Dashboard: http://203.0.113.10:8000`, and no `ESC`; `/var/log/musdash-install.log` is `600`.
  - `TestInstallSameAgain`: a second run in the same container exits 0 and adds no `restart` line.
  - `TestInstallUpgrade`: a second run with a release of another version adds both restarts, and the output names both versions.
  - `TestInstallWrongChecksum`, `TestInstallNoChecksums`: exit 1, `/usr/local/bin/musdash` and `/usr/local/bin/musdash.new` do not exist, the output names the step and shows the reason.
  - `TestInstallArchNotSupported`: a stand-in `uname` answers `riscv64`; exit 1, the reason names it.
  - `TestInstallWithoutDocker`: no `docker` on the path, and a stand-in `curl` that answers `https://get.docker.com` with a script that puts a `docker` there and makes the group; exit 0, and that script ran.
  - `TestInstallLocalBinary`: `sh /install.sh /rel/musdash-linux-<arch>` with no `MUSDASH_RELEASE_URL`; exit 0, the download row says it was not needed.
  - `TestInstallNotRoot`: run as `nobody`; exit 1, one line that says to use `sudo`.
  - `TestInstallUnderATerminal`: `script -qec 'stty rows 40 cols 100; sh /install.sh' /dev/null` with `TERM=xterm-256color`; the output holds `ESC[?25l` and, after it, `ESC[?25h`, the block letters, `┏`, `❯`, and the address.
  - `TestInstallFailsUnderATerminal`: the same with the `riscv64` stand-in; `ESC[?25h` is the last cursor sequence, `✗` is there, exit 1.
- [ ] **Step 2:** They fail. **Step 3:** Write the units, the steps and `main`.
- [ ] **Step 4:** They pass. `go vet ./... && go test -short ./...` is green.
- [ ] **Step 5:** Read the diff against the spec's decisions 8 to 19 and 25 to 28, one by one.
- [ ] **Step 6:** Commit: `Install: one command that downloads musdash, installs Docker and starts both services`.

The parts of `main` that are easy to get wrong:

```sh
main() {
  [ "$(id -u)" -eq 0 ] || { echo "install: run this as root: curl -fsSL … | sudo sh" >&2; exit 1; }
  umask 077
  TMP=$(mktemp -d) && : >"$LOG" || { echo "install: cannot write to /tmp or $LOG" >&2; exit 1; }
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  …
}

# run_step runs a step in this shell, so that what it sets is there for the
# next one, with its output in the log. A step is called as a condition,
# which switches set -e off inside it: every command there ends in
# "|| return 1".
run_step() {
  n=$1; DETAIL=""; SKIP=""
  set_state "$n" run
  plain "[$n/$STEPS] $(name_of "$n")"
  printf '\n== step %s: %s\n' "$n" "$(name_of "$n")" >>"$LOG"
  LOGSTART=$(wc -l <"$LOG")
  if "$2" >>"$LOG" 2>&1; then
    if [ -n "$SKIP" ]; then set_state "$n" skip "$DETAIL"; else set_state "$n" ok "$DETAIL"; fi
    plain "[$n/$STEPS] done${DETAIL:+: $DETAIL}"
  else
    fail_step "$n"
  fi
}
```

### Task 4: Releases

**Files:** `Makefile`, `.github/workflows/release.yml`, `test/install_test.go`.

- [ ] **Step 1:** `TestReleaseStampsTheInstaller` (skipped with `-short`, since it builds both binaries): `make release VERSION=v9.8.7` into a copy of the tree's `dist`, then `dist/install.sh` holds `RELEASE="v9.8.7"`, differs from `install/install.sh` in that line only, and `checksums.txt` has a line for each binary that `sha256` of the file confirms. `make release VERSION=dev` fails.
- [ ] **Step 2:** It fails. **Step 3:** Add to the `Makefile`:

```make
## release: the four files of a release in dist/ (VERSION=v1.2.3)
release:
	@expr "x$(VERSION)" : 'xv[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*$$' >/dev/null || { echo "release: VERSION must look like v1.2.3" >&2; exit 1; }
	$(MAKE) build-linux VERSION=$(VERSION) DIST=$(DIST)
	sed 's/^RELEASE=""$$/RELEASE="$(VERSION)"/' install/install.sh > $(DIST)/install.sh
	grep -q '^RELEASE="$(VERSION)"$$' $(DIST)/install.sh
	cd $(DIST) && (sha256sum musdash-linux-amd64 musdash-linux-arm64 2>/dev/null || shasum -a 256 musdash-linux-amd64 musdash-linux-arm64) > checksums.txt
```

- [ ] **Step 4:** Write the workflow: on a pushed tag `v*` and on `workflow_dispatch`; `permissions: contents: write`; checkout, setup-go from `go.mod`; refuse a tag that is not `v<n>.<n>.<n>`; `go vet ./... && go test -short ./...`; `MUSDASH_DOCKER_TEST=1 go test ./test -run TestInstall -count=1`; `make release` (with `v0.0.0` when started by hand); on a tag only, `gh release create "$GITHUB_REF_NAME" dist/musdash-linux-amd64 dist/musdash-linux-arm64 dist/checksums.txt dist/install.sh --verify-tag --generate-notes` with `GH_TOKEN: ${{ github.token }}`.
- [ ] **Step 5:** Run the workflow's commands as GitHub would: in a `golang:1.27` container, as a user that is not root, `go vet ./... && go test -short ./...`. Fix what only fails there.
- [ ] **Step 6:** Commit: `Release: a tag builds the binaries, their checksums and the installer`.

### Task 5: What a reader and the next session are told

**Files:** `docs/install.md`, `docs/development.md`, `CLAUDE.md`, `docs/plans/one-line-install.md`.

- [ ] **Step 1:** Read `docs/install.md` against the script as built and correct what differs (the steps, the log, `MUSDASH_ADDRESS`).
- [ ] **Step 2:** `docs/development.md`: `make release`, "Releasing" (push a tag; what the workflow does; that a failed run publishes nothing), the layout line for `install`, and the installer's tests under Tests.
- [ ] **Step 3:** `CLAUDE.md`: `make release` under Commands, and a paragraph on the script: one file, the units inside it, steps as functions that end their commands in `|| return 1`, the drawing loop and its state files, no escape sequence when not a terminal, and that a release's copy carries its tag.
- [ ] **Step 4:** Note in the spec which decisions this plan extended.
- [ ] **Step 5:** Commit: `Install: the guide and CLAUDE.md say what the installer does`.

### Task 6: v0.1.0

Mahmoud said on 2026-10-08 to publish it once the tests pass.

- [ ] **Step 1:** Rebase on `origin/main`, run vet and the tests again, push the branch to `main`, and bring the shared checkout's `main` up to it.
- [ ] **Step 2:** `git tag v0.1.0` on that commit and push the tag.
- [ ] **Step 3:** Follow the run (`api.github.com/repos/MahmoudDahdouh/musdash-go/actions/runs`). If it fails: fix on `main`, tag `v0.1.1` (decision P8).
- [ ] **Step 4:** Prove the published command: `releases/latest/download/install.sh` answers 200 and holds the tag; each binary's `sha256` is the one in `checksums.txt`; the command itself, run in a container with the stand-ins for systemd and Docker, installs that binary.
- [ ] **Step 5:** Say what was not proven by any of this: real systemd, Docker's own script on a machine without Docker, git's installation, and the look in a window on a real server.
