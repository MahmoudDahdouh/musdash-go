# Team: Invitations is a tab of its own

Asked for on 2026-10-07: "at /team page, add a new tab for Invitations."

## What is there now

- The Team page has two tabs (`pages.TeamTabs`): **Members** (`/team`) and **Shared variables** (`/team/variables`).
- Members holds two cards under one another: the members table, and the invitations that are waiting (`<section id="invitations">`, rows, an empty state inside the card).
- The header of Members has **Rename** and **Invite** for an Admin. Both open a dialog on that page.
- `POST /team/invitations` answers with the Members page: with the dialog open when the form is refused, with the link (shown once) when it is made. `POST /team/invitations/{id}/delete` leads to `/team#invitations`.
- A member's reset link is made from the members table and shown once on the same page.

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | A third tab, **Invitations**, at `GET /team/invitations`, between Members and Shared variables | Who is in the team and who is about to be belong next to each other; variables are something else |
| 2 | The route is `member`, as `/team` is | A Member sees who is invited today; the tab moves the list and takes nothing away. What a Member cannot use (Invite, Cancel) is left out of the page, and the POST routes stay `admin` |
| 3 | The invitations leave the Members tab: it holds the members table and nothing else. The `#invitations` anchor goes | One place for each thing |
| 4 | The list is a table: Email, Role, Invited by, Made, Expires, and Cancel for an Admin | It is the page's list now, as Sources and Notifications are one table. An invitation lasts seven days, and when it ends was stored and never shown (`ui.Until`) |
| 5 | Empty, the tab is a default-size `ui.EmptyState` with the Invite button in it for an Admin; the header has the button only while the list has something in it | The empty-state rule (`docs/plans/empty-states.md`, decision 6) |
| 6 | **Invite** stays in the header of Members too | Members is where a person lands on Team, and inviting is how a member is added; taking the button away would hide the page's main action behind a tab. The Members table is never empty, so the rule of decision 5 does not touch it |
| 7 | Whatever `POST /team/invitations` answers is the Invitations tab: the refused form (dialog open, errors in place), the link that is shown once, and the note for a form sent twice (`sentBefore` leads to `/team/invitations`). The page that shows the link gives `/team/invitations` as its address | The new invitation is in the list right under its link. A person who sent the form from Members is moved to the tab, with the dialog open as they left it |
| 8 | Cancelling leads back to `/team/invitations` | Where the row was |
| 9 | **Rename** stays on Members only | Its refusal renders Members; a second copy would move a person between tabs for a team's name |
| 10 | The reset link of a member is still shown on Members, the invitation link on Invitations. `oneTimeLink` is shared | Each next to what it is about |
| 11 | `pages.TeamView` stays one struct for both tabs; each renderer loads only what its tab shows (members or invitations) | One query less a page, no second type for two fields |
| 12 | The tab's icon is a new one, `mail` (Hugeicons Mail01, the set every other icon is from) | `users` is Members, and `user` reads as the account. An envelope is what an invitation looks like everywhere |
| 13 | No count on the tab | `ui.Tab` has none, and the tabs are also drawn by the shared variables handler, which would need a query for it |
| 14 | The search is unchanged: "invitations" leads to Team | The search offers the sidebar's pages and no others |

## Files

- `internal/web/server.go`: `handle("GET /team/invitations", member, s.invitationsPage)`.
- `internal/web/handlers_team.go`: `renderTeam` loads members only; new `renderInvitations` and `invitationsPage`; `invitationCreate` renders the tab, `invitationDelete` and `sentBefore` lead to it.
- `internal/web/pages/team.templ`: `Team` loses the invitations card; new `TeamInvitations`; the header's Invite and the dialog are shared pieces (`inviteButton`, `inviteDialog`).
- `internal/web/pages/shared.templ`: the tab in `TeamTabs`.
- `internal/web/ui/icons.templ`: `mail`.
- Tests: `team_test.go`, `keys_test.go`, `web_test.go` (the list of signed-in pages).
- `CLAUDE.md`: a line on the Team page's tabs.

## Tests, written first

1. `TestInvitations` (existing): the invitation is listed on `/team/invitations` without its link, and `/team` does not list it; cancel leads to `/team/invitations`; a Member's tab has neither Invite nor Cancel.
2. New `TestTeamTabs`: all three tabs are on each of the three pages and the right one is `aria-current`; empty, the Invitations tab has one Invite button (in the state); with an invitation it has one (in the header) and a Cancel form that asks first; a Member reads the list (200) and is offered neither.
3. `TestFormsThatShowASecretActOnce` in `keys_test.go` (existing): the invitation's page gives `/team/invitations` as its address and a repeat leads there.
4. `TestSignedInPagesHaveNoInlineScriptOrStyle`: `/team/invitations` in its list.
5. `TestRouteTableRefusesByRole`, `TestTeamOwnedRoutesNeedAnAdmin`, `TestCriticalFormsAsk`, `TestEmptyListsUseTheComponent` read the route table and the templates and must stay green as they are.

## Review of the plan

Read again against the code before writing any.

- **Decision 6 against "one place for each thing".** The dialog is on two pages. Weighed against the alternatives: no Invite on Members (the main action of Team is then one tab away, and a team of one sees a page with nothing to do on it), or a link from Members that opens the dialog on the other tab (a query that opens a dialog again on every Refresh, and new machinery for one button). Two buttons that open the same dialog and end on the same page are the least surprising. **Kept.**
- **Decision 7, moving a person from Members to Invitations on a refused form.** The same happens today for nothing; checked that `ui.FormDialog` with `Open` works on any page that holds it, and the Invitations tab always holds it for an Admin. **Kept.**
- **Decision 2, a Member reading the list.** No new exposure: the same rows are in the Members page a Member opens today, and no token is in them (`ListInvitations` reads the hash, the page does not print it). **Kept.** The page must not print `TokenHash`: a line in `TestTeamTabs` for it. **Added.**
- **Decision 5, the header button while empty.** The Members header keeps Invite regardless; only the Invitations tab hides it while empty. `TestTeamTabs` counts `data-open="invite"` on the tab in both states. **Kept.**
- **Field ids.** The dialog's field is `email` on both pages, and each page has one dialog with it; the invitations page has no other `email` or `role` id. `TestSignedInPagesHaveNoInlineScriptOrStyle` checks it for the new page. **Fine.**
- **`mux` patterns.** `GET /team/invitations` and `POST /team/invitations` are two patterns of one path; `POST /team/invitations/{id}/delete` is longer. No conflict. **Fine.**
- **Decision 12, an icon from outside.** One path set from the set's own package, same licence file. It is drawn in the browser before it is committed, since a wrong digit in a path is a wrong picture and no test sees it. **Kept, with that check.**
- **Missed in the first draft: the expired invitations.** `ListInvitations` leaves them out already (`expires_at > now`), so "Expires" is never "now" for longer than a request. **Fine.**
- **Missed in the first draft: what else names the old place.** `grep` for `/team#invitations` and `id="invitations"`: the handler's redirect and one test. Nothing in `app.js`, the search or a notification. **Fine.**

Approved with the one addition.

## Review of the code

The diff read again after `go vet` and the tests were green, and the pages looked at in a browser (an instance of its own, a test owner, one invitation).

| # | Found | Done |
|---|---|---|
| 1 | At a phone's width the email broke into slivers of three letters: `break-all` in a table that scrolls lets the column shrink to nothing | The column has a least width (`min-w-56`); a long address still breaks instead of pushing Cancel out of reach |
| 2 | "Made" read "just now" beside "In 7 days" | Both start with a capital, as the API tokens table writes its times |
| 3 | `renderInvitations` set `Me` and `Role`, which only the members table reads | Removed; the struct says whose they are |
| 4 | The first draft of the empty state explained what an invitation is and no longer said what to do | "Invite someone with a link. Whoever opens it chooses a name and a password and becomes a member." |
| 5 | The new test looked for "Admin" and "Owner" anywhere in the page, where the sidebar and the person's menu have them too | It reads the invitation's own row |
| 6 | The page's title in the browser was "Invitations" on one tab and "Team" on the others | "Team" on all three, as both tabs of Keys & tokens share theirs |
| 7 | The intro sentence was written out in three places | `pages.TeamIntro` |

Checked and left as they are:

- `renderTeam` and `renderInvitations` both load the team: two short functions read better than one with a switch for which list to load.
- `GET /team/invitations` is `member`. The row holds the address, the role, who invited and two times; the test asserts that neither the token nor its hash is in the page, for an Owner and for a Member.
- The dialog on Members is always given an empty form, since only the Invitations tab answers its POST. Its field ids (`email`, `role`) are one of each on either page.
- Nothing else named the old place: `/team#invitations` was in the handler and one test.
- The `mail` icon is drawn from two paths of Hugeicons' Mail01; looked at in the tab and in the empty state.
- Not done: a count of waiting invitations on the tab (decision 13).

`TestCreateServiceFromTheCatalogue` failed in one of three full runs with "the endpoint is not routed", as it did before this change in a run on `main` the same day; it passes alone. It is not touched here.
