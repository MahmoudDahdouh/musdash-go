# Team, access and finding things

[← All documentation](../README.md#documentation)

Who can do what, signing in with a second step, values shared between apps, tags, and the search.

## People and roles

The account made at setup is the team's first Owner. Others join by invitation: under Team, an Owner or an Admin enters an address and gets a link to send. The link works once, for seven days; whoever opens it chooses a name and a password. There is no outgoing mail for this, so the link travels however you send it.

| | Member | Admin | Owner |
|---|---|---|---|
| Projects, apps, databases, services: create, change, deploy, delete | yes | yes | yes |
| Project and environment variables, tags, their own API tokens | yes | yes | yes |
| See servers, sources and deploy keys | yes | yes | yes |
| Add, change or remove servers, sources and deploy keys | | yes | yes |
| Settings (dashboard domain, backup storage) and Notifications | | yes | yes |
| Team and server variables | use by name | change | change |
| Invite, cancel an invitation, rename the team | | Members | Members and Admins |
| Remove a member, make them a password reset link, turn off their second step | | Members | anybody else |
| Change a role | | | yes |

- Removing a member deletes their account, their sessions and their API tokens, and withdraws the invitations they made. What they deployed stays.
- Giving somebody a higher role signs them out and ends their API tokens and reset links, so nothing made under the lower role carries over. Giving them a lower one withdraws the invitations they made.
- The team always has an Owner: the last one cannot be removed or given another role.
- Roles decide who manages the team and its servers. They do not limit what a container is given: a Member deploys containers on the team's servers under the same rules as everybody.
- One install has one team.

## Two-step sign-in

Under Account, a person can add a second step: after the password, signing in asks for a six-digit code from an authenticator app (TOTP). Setting it up shows a key to enter in the app, or to open with a password manager; there is no QR code. It is on once a code from the app has been entered, and ten recovery codes are shown then, each good for one sign-in without the phone.

- Turning it on or off asks for the password again, and turning it off for a code as well.
- A code works once. Five wrong codes lock the second step of that account for fifteen minutes.
- Lost the phone and the recovery codes: an Owner (or, for a Member, an Admin) turns the second step off from the Team page. For the only Owner, run `musdash disable-2fa <email>` on the server.
- A password reset link does not skip the second step.

## Shared variables

A value that several apps need is written once and taken by name. There are four places to keep them:

| Kept under | Named as | Who changes them |
|---|---|---|
| Team, Shared variables | `{{team.NAME}}` | Admins |
| A project's Settings | `{{project.NAME}}` | everybody |
| An environment's Settings | `{{environment.NAME}}` | everybody |
| Servers, on a server's card | `{{server.NAME}}` | Admins |

An app or a service uses one by naming it in a variable of its own, alone or inside a longer value:

```
DATABASE_URL={{environment.DATABASE_URL}}
MAIL_URL=smtp://{{team.SMTP_HOST}}:587
```

- Nothing reaches a container that did not name it.
- Team and server variables are changed by Admins, and their values are shown only to Admins. That is not secrecy from Members: a Member can name one in an app they deploy, and the app then reads it. Keep there what the whole team may use.
- The name is replaced at each deployment with the value stored then, so a changed value takes effect at the next deploy. A preview resolves the names as its app does.
- A deployment that names a variable which does not exist fails and says which. Saving an app's variables warns about such names.
- Only this exact form is read. `{{ .Name }}` and the like, for a template engine, are left alone. An app that needs this very text for itself puts a backslash before it: `\{{environment.name}}` reaches it as `{{environment.name}}`. A shared variable's own value cannot name another one.

## Tags

A tag is a short name such as `nightly` or `frontend`. The Tags page makes, renames and deletes them, and a tag can exist before anything has it; an app's and a service's Settings page chooses among them or adds a new one. A tag's page lists what has it, and its step of the header switches to the team's other tags. Everything that has a tag is deployed with one [API](api.md) call; something that already has a deployment waiting is not queued twice.

## Search

The bar at the top of every page has a search before your own menu. Press `/` anywhere outside a field, or Cmd+K (Ctrl+K on Windows and Linux), type, and press Enter to open the first result; the arrow keys choose another.

It finds what the team has, by name and by what you would know it by: projects (and their descriptions), environments, apps (image, repository, branch), databases (engine, image), services (template, repository), domains, servers (host, address), tags, and the dashboard's own pages. Several words narrow it: `shop web` is the app `web` of the project Shop. A name from the server can be pasted whole (a container, network or volume called `musdash-<id>-…` finds what it belongs to). Variables, Compose files, keys and tokens are not searched, so a search never shows what a secret holds.

Nothing is indexed or kept in memory for it: each pause in the typing is one query on the database.
