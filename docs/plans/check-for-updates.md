# Check for updates

Asked for by Mahmoud on 2026-10-11: a **Check for updates** button on Settings that only the Owner sees and presses, and a badge in the sidebar, shown to everybody, while a newer version exists. Coolify and Dokploy were named as the examples.

## What the others do

- Coolify looks for a new version on a schedule, says so in its navigation, and can upgrade itself, by hand or automatically.
- Dokploy has a button that looks, and another that pulls the new image and restarts.

Both upgrade themselves because they are containers with the Docker socket. musdash is a binary under systemd whose server runs as the `musdash` user: it cannot replace `/usr/local/bin/musdash` or restart its units.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | musdash finds out and says so. It does not upgrade itself; the card shows the install command to copy. Chosen by Mahmoud | Upgrading from the dashboard needs a root-owned helper and changes to the installer: a piece of work of its own |
| 2 | The latest release is read from where `…/releases/latest` redirects to, with one `HEAD` | It is the address the installer uses. No API, so no hourly limit for an address and no JSON to read; no body at all |
| 3 | Only a tag of the form `vN.N.N` is taken from the answer | What GitHub says is data. It is stored, shown, and put into a link to the release |
| 4 | Asked once in 24 hours from the tick, counted from the install's last try, besides the button | A badge that needs a button pressed first is seldom seen. Counting from the install's own time spreads the questions |
| 5 | The daily question can be switched off, by the Owner | It is a request a server makes on its own |
| 6 | A build that is no release asks nothing by itself and is never told to upgrade | `dev` cannot be compared with a tag |
| 7 | The answer is in `settings`; the sidebar reads that one value | No memory is held, and no page waits on GitHub |
| 8 | The card is on the Settings page every Admin reads; the button and the switch are the Owner's, in the page and in the route table | What was asked for. An Admin who opens Settings from the badge still sees what to do |
| 9 | The badge is at the sidebar's foot, not on the Settings entry | A Member has no Settings entry, and the badge is for everybody |
| 10 | A failed check tells the person that GitHub could not be asked. The reason goes to the log | An error of the network names addresses of the server's own |

## What was built

- `internal/update`: `Latest`, `Newer`, `Release`, `NotesURL`, `InstallCommand`.
- `internal/ops/update.go`: `Update` (the state), `CheckUpdate`, `SetUpdateAuto`, `updateIfDue` in `Tick`.
- `internal/web`: `handlers_update.go`, `pages/update.templ`, `ui.Shell.Update` and `updateNote` in the layout, `.nav-update` in `input.css`.
- Routes: `POST /settings/updates/check` and `POST /settings/updates/auto`, both `owner`.

## Tests

- `update`: the comparison (numbers, not text; what is not a tag), the redirect read from a stand-in server.
- `ops`: once a day, never for `dev`, a failure keeps what was known and is not tried again at once, off means off.
- `web`: the card before and after a check, the note for an Owner, an Admin and a Member, the two routes refused below Owner, a failed check, up to date after the upgrade.

## Not proven here

- The request against GitHub from a real server (the tests use a stand-in; the redirect was looked at by hand on 2026-10-12 and answered `302` to `…/releases/tag/v0.1.2`).
- The look on a narrow screen.

## Later

An **Update now** button: a root-owned unit the server may start, which runs the installer. Its own plan.
