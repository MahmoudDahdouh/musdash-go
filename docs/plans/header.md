# Header: the team first, the person last, no container

Asked for on 2026-10-06. Four things:

1. The bar at the top has no container. Its content starts where the bar starts, not where the page's centred column does.
2. The first step of every trail is the root: the team, as a switcher. A person is in the default team unless they chose another.
3. The sidebar no longer says "musdash is using N MB".
4. The bar ends with the person: a button that opens a menu (the account, Settings, Sign out).

## What is there now

- `.topbar` holds `.topbar-inner`, which is 68rem wide and centred like `.page`, "so the trail starts where the page's content does". On a wide window the trail therefore starts well right of the sidebar.
- The trail is what the handler gave `s.shell` (`Projects / Shop / production / web`) or, without crumbs, the page's title. Nothing in it names the team.
- The sidebar's foot has a link to `/account` with the person's name, the Sign out form and the memory figure, which asks `/sys/mem` again every 30 seconds.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | `.topbar` is the row itself; `.topbar-inner` goes. Padding 12px, 16px from 64rem up | "No container". The steps have 6px of their own, so the first label sits at about the sidebar's inset |
| 2 | The page body keeps its 68rem centred column | Only the header was asked about; text lines wider than that read badly |
| 3 | The team's name comes with the session: `SessionByHash` joins `teams` and fills `Session.TeamName` | Every page needs it. One more join on a primary key in a query that already runs for every request; no second query, nothing cached |
| 4 | The layout puts the team in front of whatever the page's trail is (`ui.trail`): handlers and their crumb helpers do not change | One place. A page cannot forget it, and `Shell.Team` empty (a test, the gallery's own demo) simply leaves it out |
| 5 | The team step is a switcher like the environment's: options fetched from `GET /switch/teams` when first opened. Today it answers with the one team (current, leading to Home), a rule, and "Manage team" to `/team` | An install has one team (CLAUDE.md). The step is where a second team would be listed; multi-team itself is not part of this |
| 6 | `/switch/teams` is a Member's route, not under `/team` | It only names the team the person is in. `/team` GETs are a Member's already, but keeping it out of that prefix keeps `TestTeamOwnedRoutesNeedAnAdmin`'s rule simple |
| 7 | A page without crumbs is `Team / Title` (Home is `Default team / Home`) | The bar still says where you are |
| 8 | The person's menu is a `data-select` box with a popover, steered by the same `app.js` code as a switcher. Its head says name and email; its options are **Account** (`/account`), **Settings** (`/settings`, for an Admin, as in the sidebar) and **Sign out** | No new script behaviour to learn. "Account" and not "Profile": the item is named after the page it opens |
| 9 | Sign out is still a POST form with the CSRF token and still asks first (`data-confirm`); the form sits inside the menu with its button | `TestCriticalFormsAsk` reads the form block and wants the question in it |
| 10 | No link to `/keys` in the person's menu | CLAUDE.md: no pointer to that page besides the sidebar and a form's hint |
| 11 | The sidebar's foot goes whole: the account link and Sign out moved to the bar, the memory line is removed | Two places for the same two actions would be one too many |
| 12 | With the line go `ui.MemReadout`, `Shell.MemMB`, `GET /sys/mem`, its handler and test, and `internal/sysmem`, which nothing else uses | A route that only fed the line is dead once the line is gone. `make rss` measures from outside and is untouched |
| 13 | The menu of a button at the right end opens flush with the button's right edge: `data-menu-end` on the popover, read by `place` | Placed by its left edge it is pushed back in by the window and hangs off-line with its button |
| 14 | The avatar is the person's initial in a small round mark, drawn with a class. No image, no per-person colour | Nothing to store or fetch, and a colour per person would need a `style` attribute, which the CSP forbids |
| 15 | Under 40rem the person's button is the avatar alone | The trail needs the width; it scrolls sideways already |

## Found on the way

`app.js` decides whether a chosen option belongs to a `Select` by looking for *any* hidden input inside the box. A form inside a menu (Sign out's CSRF token) is such an input, and the code would go on to write into a label that is not there. It now asks for the box's own input (`:scope > input[type=hidden]`), which is where `selectBox` puts it.

The review of the code found a second one. The confirm dialog gives the focus back to what had it when it opened. For Sign out that is a button in a menu, and the menu has shut by then, so the focus would be left on nothing. Before it opens the dialog, the `data-confirm` handler now moves the focus to the button of the menu the asking button is in, if it is in one.

## Changes

**Database** (`internal/db/users.go`)
- `Session.TeamName`, filled by `SessionByHash`.

**Server** (`internal/web/server.go`, `handlers_resources.go`, `pages/resources.templ`)
- `shell` sets `Team`; `MemMB`, `memReadout` and the `/sys/mem` route go.
- `GET /switch/teams` (member) → `switchTeams` → `pages.TeamOptions`.

**Layout** (`ui/layout.templ`)
- `Shell.Team`; `trail` puts the team first.
- `topbar` without the inner box: menu button, trail, `userMenu`.
- `userMenu(s)`, `initial(s)`; the sidebar's foot and `MemReadout` go.

**Style** (`assets/input.css`)
- `.topbar` as the row, `.topbar-inner` removed, `.crumbs` takes the room that is left.
- `.avatar`, `.user-button`, `.menu-head`.

**Script** (`static/app.js`)
- `place` reads `data-menu-end`; the option click looks at the box's own hidden input.

**Removed**: `internal/sysmem`.

**Docs**: CLAUDE.md (the trail starts with the team; the person's menu; `data-menu-end`).

## Tests

- `db`: a session carries its team's name, and the new one after `RenameTeam`.
- `web`: every signed-in page's trail starts with the team switcher; the bar holds the account link and the Sign out form, the sidebar neither; a Member's menu has no Settings, an Admin's has; `/switch/teams` names the team and leads to `/team`; `/sys/mem` is gone.
- `TestSignedInPagesHaveNoInlineScriptOrStyle` (no id twice, no inline style), `TestCriticalFormsAsk` (Sign out asks) and `TestRouteTableRefusesByRole` keep holding.
- In the browser: the bar at a wide and a narrow window, both menus by pointer and by keyboard, Sign out through its question.

## Second pass: the menu's items

Asked for the same day, after looking at it: the Account item always looked active, and the menu needs a variant for dangerous items, in soft colours; Sign out's question should have a danger button.

**Why Account looked active.** It was not the item. `app.js` puts the focus on the first option of any menu it opens, and a focused item is drawn like one under the pointer. That is right for a `Select` or a switcher, which open on the option that is chosen, and wrong for a menu where nothing is.

| # | Decision | Why |
|---|---|---|
| 16 | The menu is a component of its own, `ui.Menu`, with `ui.MenuItem` for what is in it. The person's menu is built from them; `MenuAction` is a `MenuItem` | Asked for as a component with variants, and the gallery needs one to show |
| 17 | A `Menu`'s popover is marked `data-menu-actions`. Opened with the pointer it marks nothing and the focus stays on its button; ArrowDown and ArrowUp go in from there. Opened from the keyboard (arrows, Enter, Space) the focus goes to the first item, or the last for ArrowUp | Somebody using the keys needs to be somewhere; somebody using the pointer must not see an item lit that they did not point at |
| 18 | Tab from the button of an open menu shuts it | The focus is no longer inside the list, where Tab already did |
| 19 | `MenuItem.Danger`: red text and icon, `danger-soft` under the pointer and the focus. Sign out is one | "Soft colours": a red block in a menu would be all that is read of it |
| 20 | `MenuItem.Current`: semibold with the sidebar's rail, and no fill | The fill is what read as "active". Only the Account and Settings pages mark anything |
| 21 | Sign out's question uses `ui.Ask(…, danger)`, so its confirm button is the red one | Asked for |
| 22 | Roles stay `listbox` and `option` | Every menu here is steered by the same code through them; nothing is ever `aria-selected` in a `Menu` |

Found while checking it in the browser: `a.menu-item` sets a link's colour and outranked `.menu-item-danger`, so a danger item that is a link was black. The danger rules name both classes.
