# The Git form: one repository picker, a branch picker, and a build that is worked out

Asked for on 2026-10-08, of the page a new app from a Git repository is made on (`…/app/new?source=git&access=app`): make it easier; the Repository field and the "Choose from" button under it are the same thing twice, the picker alone is enough; the branch should be a picker too, `main` unless the repository says otherwise; and "Build with" should be worked out.

## What was there

Thirteen controls in one column, in the order the columns of `apps` happen to have. With a GitHub App the repository was typed as an address or filled in by a small button under the field, the branch was typed, and Build with was chosen by hand with three fields under it of which at most one applies (Dockerfile path, Folder to serve, the single-page box).

## The plan

| # | Decision | Why |
|---|---|---|
| 1 | Through a source (a GitHub App, a GitLab token) the repository is one control, `ui.Picker`, which now holds a value: a button as wide as the field that names what is chosen, a hidden input with the address, and the list fetched when it is first opened. No text field beside it | It is what was asked. The list is everything the source can read, so there is nothing to type that is not in it (but see 3) |
| 2 | With no source (a public repository, a deploy key) the repository and the branch stay text fields | There is nobody to ask for a list: a deploy key is on any host, and a public repository has no credentials |
| 3 | A picker's list ends with a typed option (`ui.PickerTyped`): what is in the filter field, offered as "Use acme/shop" when no option is exactly that | A list is cut at 300, and a GitLab token reads public projects it is no member of. Without this the picker alone could not name them, and the text field would have to stay |
| 4 | The form holds one variant of the two fields for each of the team's sources, and the typed one, each a `<fieldset>`. Only the one "Read the repository through" names is shown and enabled (`data-when`, `data-is`); the others are `hidden` and `disabled`, so the form sends one `repo` and one `branch` | Switching the way in keeps what was chosen under each, needs no request, and the server reads the same two names as before. A disabled fieldset's fields are not sent, by the browser or by htmx |
| 5 | The branch is a `ui.Picker` too, `main` to begin with. Its list is `GET /sources/<host>/{id}/branches?repo=…`, asked when it is opened. Choosing a repository sets the branch to that repository's default branch and empties the branch list (`data-sets` on the option) | The default branch is in the repository list already, so the common case needs no second request. A list from the repository before would be wrong |
| 6 | Build with is worked out: `GET /sources/detect?access=&repo=&branch=&base_dir=` lists the folder the app is built from and `deploy.GuessPack` reads the names: a `Dockerfile` is the Dockerfile build, `railpack.json` Railpack, a file a builder knows (`package.json`, `go.mod`, `requirements.txt`, …) Nixpacks, an `index.html` and nothing to build a static site. The answer is a line under Build with that says what was found and sets the menu (`data-pick`) | Named, so the person sees why and can still choose otherwise. Only names are compared; nothing a repository holds is read or shown |
| 7 | It is asked again when the way in, the repository, the branch or the base directory changes, 150 ms after the change, a newer request replacing one in flight. Between two such changes the person's own choice of Build with stays | Each of those is another codebase. The delay lets `data-when` enable the right variant before the values are read, and spares a request per key |
| 8 | Nothing is worked out without a source. The line is empty and the menu is as it was | The same reason as 2 |
| 9 | The fields under Build with are shown for the build they belong to (`data-when`): the Dockerfile path for Dockerfile, Folder to serve and the single-page box for a static site. They are optional fields, and they are still sent. The Port stays whatever is chosen | A `required` field that is hidden and empty stops the browser from sending the form, and says nothing. Only optional fields are hidden |
| 10 | The New app page is three groups under small headings: **Repository** (the way in, repository, branch, base directory), **Build** (Build with and its fields, deploy on push), **App** (name, port, server, domain, deploy now). The image form keeps its one group | Twelve controls in one column have no order to read them in. The repository comes first because the rest follows from it |
| 11 | Choosing a repository puts its name in the Name field while that is empty or still holds the last suggestion (`data-suggest` on the option, `data-suggest-here` on the field) | One field less to think about. What a person typed is never replaced, and the app's Settings, where the name exists, has no such field |
| 12 | `ui.Picker`'s first form, a button that fills another field (`data-fill`, `ui.PickerOption`), goes: the repository field was its one use | No dead component |
| 13 | The app's Source settings and the service-from-Git forms use the same fields, so they get the pickers; detection is the app forms' only | They share `gitRepoFields` already. A service has no build pack |
| 14 | GitLab gets the same: `GitLab.Branches`, `GitLab.Files` | One form for both hosts, not a picker for one and a text field for the other |

## What reaches a Git host, and what comes back

- A member asks; the source is the team's (`db.GitSource` by team id, 404 otherwise) and of the kind the route names.
- The repository is parsed by `source.ParseRepo` and must be on the source's own host (`sourceHost`), as the form's own check has it: a source's credentials go nowhere else.
- GitHub is asked with a token for that one repository and `contents: read`, the permission the clone uses. The branch is `source.ValidBranch`, the base directory `source.ValidRelPath`, each path segment escaped.
- What comes back is data: a branch name that is not `ValidBranch` is left out, and a file name is only compared with a fixed set. Lists are cut at 300 branches and one folder's listing; nothing is kept between requests.
- Each request has ten seconds.

## Tests

- `source`: branches and a folder's names from stand-ins for both hosts; the token asked for is for the one repository and reads contents; a name that is not a branch is dropped; a file where a folder was asked for is an error.
- `deploy.GuessPack`: a table.
- `web`: the form has a picker for each source and no "Choose from"; the variant that is not chosen is hidden and disabled; the repository list carries the address, the label, the default branch and the name; the branch list needs a repository on the source's host; another team's source is 404; detection names the Dockerfile and sets the menu, and says nothing without a source; an app is created from what the pickers send.

## Review of the plan

- **A race on changing the way in.** `data-when` runs on the same `change` the detection listens for, and htmx's listener is nearer the field, so it would read the variant that is about to be disabled. Fixed in 7 with the delay, which was wanted anyway.
- **A hidden required field.** The first draft hid the Port for a static site. A person who had emptied it could then not send the form and would not be told why. 9 now hides optional fields only.
- **Detection against a person's choice.** Considered remembering that Build with was chosen by hand and never touching it again. Refused: after another repository is chosen the earlier choice is about another codebase, and the line says what was set and why.
- **The picker alone.** Without 3 it loses repositories the text field could name. 3 stays.
- **The name suggestion on Settings.** The General form there has a field with the id `name`. The suggestion therefore goes by a mark on the field, not by an id, and only the New app form has the mark.
- **RAM.** Two more handlers and no state; a Git host's answer is read under the existing 4 MiB bound and dropped.

Approved with those changes.

## Review of the code

Looked at in a browser against a stand-in for GitHub, and read once more.

- **`form.elements` is not a lookup by name.** `data-when` first asked the form for the field named `access` that way and got two things, the hidden input and the Select's button, whose id is the same word: no value, so every variant was hidden. It now asks for `[name="…"]`.
- **htmx hands attributes down.** The pickers sit inside the element that asks for the detection, and their own requests took its `hx-include`, `hx-sync` and `hx-indicator`: the repository list was asked for with the whole form in its address. `hx-disinherit="*"` on that element.
- **A filter's `change`.** Typing into a picker's filter field and leaving it is a `change` that bubbles, and would have asked for the detection again. The filter has no name and is no part of the form; `app.js` keeps its `change` from the page.
- **The typed option was offered too early.** "Use shop" appeared while `acme/shop` was the only match, and would have named a repository with no owner. `PickerTyped` takes what the text must hold first: a slash for a repository, nothing for a branch.
- **Pasting an address.** A whole `https://github.com/acme/shop` typed into the repository filter would have had the prefix put in front of it again. The prefix is taken off first.
- **The hint under Build with** explained four builds a person no longer has to choose between. It now says where the choice comes from.

Not done: nothing is worked out for a public repository, which GitHub would answer for without credentials, at sixty questions an hour for the whole server's address. It would work on a quiet day and fail on a busy one.
