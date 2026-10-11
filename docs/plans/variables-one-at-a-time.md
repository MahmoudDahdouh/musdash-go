# Variables, one at a time

2026-10-11. What was asked: show and hide a variable's value by key, or work
in a developer view; keep every value encrypted with the key and decrypt it
when a person asks to see it; a better page. Coolify and Dokploy are the
models.

## What is there today

- `env_vars.value`, `shared_vars.value` and `services.variables` are sealed
  with the master key (`secret.Box`, AES-256-GCM) and nothing else is stored.
  That part of the request is already true and stays as it is.
- A page lists names. **Show values** opens every value of the card at once
  (`…/values`), and `…/edit` is one text box of `NAME=value` lines.
- There is no way to see, add, change or delete one variable: the only write
  is the whole set (`ReplaceEnvVars`, `ReplaceSharedVars`).

## What the other two do

- **Coolify** lists a row a variable that holds the name and never the value.
  The value is decrypted when its dialog is opened; the row has a copy button
  and an edit button. A button switches between "Normal view" and "Developer
  view", which is the `.env` text.
- **Dokploy** has the `.env` text only, behind one eye that hides all of it.

musdash's list is Coolify's normal view with a value that can be looked at in
its row; its editor is the developer view.

## Decisions

| | Decision | Why |
|---|---|---|
| 1 | Names stay in plain text; values stay sealed. | A name is what the page lists, what `UNIQUE` and `ORDER BY` work on and what a deployment's error may say. A sealed name could be none of those (every sealing differs). |
| 2 | A variable is addressed by its name: `…/vars/{name}`. | A name is `[A-Za-z_][A-Za-z0-9_]*`: safe in a path and in an element id, and it stays the same when the developer view replaces the set, which gives every row a new id. |
| 3 | `…/vars/{name}`, never `…/{name}`. | `values` and `edit` are names a variable may have. |
| 4 | Showing one value opens one value. | The row's request decrypts that row and nothing else. |
| 5 | A value is hidden again by asking the server for the row without it. | The row in the page is replaced, so no value is left behind it, and it needs no script. |
| 6 | Show all and Hide all are both in the card; the stylesheet shows each while it has something to do (`:has`). | Rows are shown and hidden one at a time, so only the rows know the state. |
| 7 | Add and Edit are dialogs (`ui.FormDialog`); Edit's is fetched. | It holds the value, which is not in the page until asked for. It leaves the page when it is closed. |
| 8 | Copy is on a shown row only. | Copying after a fetch is not allowed by every browser's clipboard; `data-copy` on a value that is there always works. |
| 9 | The developer view is `…/edit`, as it was. | It is that already. It gets the name and a way back. |
| 10 | A value typed in a dialog is stored as typed. `FormatEnv` quotes a value that reading the line back would change. | Otherwise a value with a space at an end, or in quotes, is changed by the next save in the developer view. |
| 11 | A value cannot hold a line break. | An env file cannot carry one, and neither can a line of the developer view. A break at the end of what was pasted is dropped. |
| 12 | A service's variables stay in its Compose form. | They are the answers to the file's `${NAME}`s and are saved with the file. Not part of this. |
| 13 | No lock (Coolify's write-only value), no filter, no comments. | Not asked for. |
| 14 | No new API route. | Not asked for; a route there needs its own permission and tests. |

## Addresses

`Base` is `…/app/{id}/environment` for an app and `…/variables` for the team,
a project, an environment and a server.

| | | Who |
|---|---|---|
| `GET Base` | the list, names only | as today |
| `GET Base/values[?hide=1]` | the card with every value, or none | as today |
| `GET Base/edit`, `POST Base` | the developer view and its save | as today |
| `GET Base/vars/{name}[?hide=1]` | one row with its value, or without | may change them |
| `GET Base/vars/{name}/edit` | the dialog that changes it, with its value | may change them |
| `POST Base/vars` | add one (`key`, `value`, `build`) | may change them |
| `POST Base/vars/{name}` | change one; `key` may rename it | may change them |
| `POST Base/vars/{name}/delete` | delete one, after `ui.Ask` | may change them |

"May change them": a Member for an app's (behind `s.ownSettings`, so a preview
is turned away), a project's and an environment's; an Admin for the team's and
a server's. `TestTeamOwnedRoutesNeedAnAdmin` learns the two new `GET`s.

A write answers with a redirect to `Base` and a note. A refused one draws the
page again with its dialog open and the errors in place (422).

## The page

```
Environment variables                [Show all] [Hide all] [Developer view] [+ Add variable]
Stored encrypted. …
┌──────────────────────────────────────────────────────────────────────┐
│ Name            Value                                                │
│ DATABASE_URL    ••••••••                                    (eye) (⋯) │
│ NODE_ENV        production                          (copy) (eye) (⋯) │
└──────────────────────────────────────────────────────────────────────┘
```

- The eye is a ghost icon button with a name of its own ("Show value of
  DATABASE_URL", "Hide value of …") and the same id in both states, so the
  focus stays on it when the row is replaced. A shown value that is empty says
  "Empty".
- ⋯ is a `ui.Menu`: **Edit** (fetches the dialog), **Delete** (danger, asks).
  The eye and Delete are kept apart on purpose.
- An app from Git keeps its two groups, "Given to the running app" and "Given
  to the build". The dialogs have a **Build-time variable** box there.
- With nothing stored the card is the empty state: **Add variable** and a link
  to the developer view for pasting a `.env` file.
- The developer view is titled so, says what it is, and has **List view** in
  its head beside Cancel in its foot.
- A Member's page of the team's and a server's variables is unchanged: names.

## Storage

`internal/db`:

- `EnvVarByKey(kind, id, key)`, `AddEnvVar`, `UpdateEnvVar(kind, id, key, v)`,
  `DeleteEnvVar`. A name that is taken is `ErrVarExists`; one that is not there
  `ErrNotFound`.
- `SharedVar(team, scope, scopeID, key)`, `AddSharedVar(…, limit)`,
  `UpdateSharedVar`, `DeleteSharedVar`, every one filtering on the team.
  More than the limit is `ErrTooManyVars`, counted in the transaction that adds.
- No migration: the tables are what they need to be.

`internal/deploy`: `ValidEnvName`; `FormatEnv` quotes where needed.

## Script and style

- `app.js`: a dialog that arrives by htmx with `data-autoopen` is opened, and
  one inside `[data-fetched]` is removed when it closes. After Show all or
  Hide all the focus goes to the one of the two that is left to see.
- `ui.MenuItem` gets `Fetches` and `Into`: a button that asks for an address
  and puts the answer in an element, and sends no form (the menu sits in the
  delete form).
- `input.css`: the two `:has` rules of decision 6.

## Tests, written first

- `db`: add, change, rename onto a taken name, delete, another resource's and
  another team's rows untouched, the limits.
- `deploy`: `ParseEnv(FormatEnv(x)) == x` for values with quotes and spaces.
- `web`: an app's variables one at a time (sealed in the table; the page and
  the other rows hold no value; one row's answer holds one value and is no
  page; `?hide=1` holds none; the dialog; rename; a taken name; a line break;
  a reserved build argument; delete; an unknown name is 404; a preview and
  another team's app are turned away). The same for the four shared scopes,
  and by role. The existing tests for Show values and the editor keep passing.

## Review of the plan

Read against the code and CLAUDE.md before anything was written. Approved
with these changes, which are part of the plan:

- **The focus after Show all and Hide all.** The card replaces itself and the
  button that was pressed goes with it. Planned: the button that will be
  visible in the card that arrives carries an id, which htmx puts the focus
  back on. The review of the code found the case that misses (below), and a
  line of script does it now.
- **The mask is read aloud.** Eight bullets are said as eight bullets. The
  cell holds them `aria-hidden` and the word "Hidden" for a screen reader.
- **The question matches the handler.** Deleting an app's variable takes
  effect at the next deploy; deleting a shared one makes what names it fail
  to deploy. Each question says its own.
- **`FormatEnv` has four callers and all of them fill a box `ParseEnv` reads**
  (an app's two, a scope's, a service's), so quoting in it is safe for all.
- **The card's head has four buttons.** It wraps under its title on a narrow
  screen; to be looked at in a browser at 375px.
- **The API is left alone** (decision 14): `/api/v1` has no route that writes
  a variable today, and `GET /apps/{id}/envs` reads them as before.
- **Roles are the route table's.** The two new `GET`s under `/team` and
  `/servers` are `admin`; a Member's page there still has names and no
  button. Nothing in a handler decides a role.
- **Previews.** Every new app route is behind `s.ownSettings`; the test that
  walks a preview's addresses gets the new ones.

## Steps

1. `db` and `deploy`, with their tests.
2. `ui.MenuItem`, `app.js`, `input.css`.
3. The card, the row and the dialogs (`pages/variables.templ`); app handlers
   and routes.
4. Shared handlers and routes.
5. Guides (`docs/deploying.md`, `docs/team.md`), CLAUDE.md, a look at the
   pages in a browser, review of the diff.

## What building it changed

- **A button that asks is not disabled while it does.** `hx-disabled-elt`
  was on the eye and on Show all. A disabled button gives up the focus, so
  there was none for htmx to put back on the button that replaced it. Seen in
  a browser with the keyboard; removed.
- **The row's text is level with its buttons at either size.** Buttons are
  44px under a finger (`pointer: coarse`) and 28px otherwise. The text cells'
  padding is worked out from the button's height and the line's (`1lh`), not
  written as a number.
- **Two lists in one card keep one edge.** The name column has a least width,
  so the values of "Given to the running app" and "Given to the build" start
  at the same place for names of the usual length.
- **A refused dialog's page says its address** (`ui.Shell.Address`): the
  answer to `POST …/vars` is drawn at an address that has no page, and Refresh
  would send the form again.
- **A whole line pasted into Name is taken apart** (`NAME=value` with the
  value box empty), as the developer view would read it.
- **The browser sends a dialog's `close` with its next frame**, so a fetched
  dialog in a tab that is not shown leaves the page when the tab is shown
  again. Nobody is looking until then.

## Review of the code

The diff was read by a second reviewer who had the code and not this plan's
conclusions. Nothing was found about who gets a value or where one ends up.
What was found, and done:

- **An app's variables had no end.** A scope holds 200; an app, added to one
  at a time, held as many as were sent, and all of them are read for the tab
  and at every deployment. An app now holds 500 (`maxAppVars`), counted in
  the transaction that adds (`AddEnvVar`) and when the developer view saves.
- **The focus was lost in one case.** Card drawn without values, one row
  shown, Hide all pressed: that button had no id to come back to. The id is
  gone; the script puts the focus on whichever of the two is left to see.
- **Tests that were missing.** Another team's project, environment and
  server by the new addresses; ids on a tab with two lists and on a scope's
  page with rows; the limit; the address a refused dialog's page says.

Left as it is: a value near 1 MB typed in a dialog is a line the developer
view then refuses for its length. A form is at most 1 MB, so the two limits
are a few bytes apart.
