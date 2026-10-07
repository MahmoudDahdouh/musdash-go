# Service catalogue: categories, and the services Coolify and Dokploy have

The Add resource page (`/projects/{project}/env/{env}/new`) offers six service
templates. This plan gives every template one or more categories, and then
fills the catalogue from the two catalogues people know: Coolify's
(https://coolify.io/services) and Dokploy's (https://github.com/Dokploy/templates).

## What is there to import

Counted on 2026-10-07 from a checkout of each.

| | Coolify | Dokploy |
|---|---|---|
| Templates | 382 (`templates/compose/*.yaml`), 27 of them marked `ignore: true` | 532 (`blueprints/<id>/`) |
| Licence | Apache-2.0 | MIT |
| Format | A Compose file with a comment header: `documentation`, `slogan`, `category`, `tags`, `logo`, `port` | `docker-compose.yml`, `template.toml` (variables, domains, env, file mounts) and `meta.json` (name, description, links, tags) |
| Variables | `SERVICE_FQDN_*`, `SERVICE_URL_*`, `SERVICE_PASSWORD_*`, …: the convention musdash already reads | Its own helpers: `${domain}`, `${password:32}`, `${base64:64}`, `${hash:16}`, `${jwt:…}` |
| Categories | One `category` per file (38 spellings of about 25 ideas) | Free tags, 636 different ones |
| Logos | 234 SVG, 114 raster | 252 SVG, 279 raster |

188 services are in both. Together they are 726 different ones.

Not all of them can run under musdash, and that is by design
(`internal/compose/validate.go`): a stack may not mount the Docker socket
(22 Coolify and 29 Dokploy templates do), be privileged, use the host's
network or devices, or mount a directory next to its Compose file. "All
services" in this plan therefore means: every one that passes musdash's own
rules after a mechanical conversion. The ones left out are listed with the
reason, in a file that is committed.

## Decisions

| Question | Decision | Why |
|---|---|---|
| Where a template's categories are written | In its header: `# categories: analytics, monitoring`. One or more keys from a fixed list in `catalog` (`catalog.Categories`: key and label) | The header is where the rest of what the catalogue says about a template is. A fixed list keeps 636 free tags from becoming 636 filter buttons |
| The list | About 25 categories, made from Coolify's `category` values and the Dokploy tags that many templates share (the table below) | Coolify's are already close to a usable set; the spellings are merged (`RSS`/`rss`, `Mail`/`email`, `devtools`/`development`/`developer-tools`) |
| How a template gets several | Its source's category, and every category its source's tags map to, at most three. Where nothing maps, a hand-written line in the importer's overrides | The request: a service can have many categories. Three keeps a card's line short |
| The filter | Radio buttons drawn as chips above the lists, "All" first, each with its count. With the text field they narrow together. Database engines carry `database`, so that category shows them too | Radios are form state: the browser brings the choice back when a person returns from a template's form. No request, no page of its own |
| Whose template wins when both have one | Coolify's | Its variable convention is musdash's own, so the conversion changes less |
| The six templates written for musdash | Kept as they are, never overwritten | They are the ones started for real against Docker, and services that exist name their keys |
| Coolify templates marked `ignore: true` | Left out, unless Dokploy has the service | Coolify does not offer them itself |
| The converter | A Go program in a module of its own, `tools/catalog` (its `go.mod` has `gopkg.in/yaml.v3` and a TOML reader). Nothing of it is built into musdash | A converter needs a YAML parser, and musdash's module must not get one. yaml.v3's node tree keeps a file's own quoting and scalars, so a value is never re-typed on the way through. Committed, because 500 files nobody can regenerate cannot be reviewed or brought up to date |
| What decides that a template is in | `TestCatalogueLoadsInTheSandbox`: loaded twice in the sandbox, `Validate`, every address variable resolves to a service and a port | It is the check a deployment makes before anything is started |
| Are imported templates started for real | Not all: it would pull several hundred gigabytes. A sample of each kind of conversion is, through `TestCatalogueWithDocker` | Said plainly in the page's own text and in the README: a ready-made template is its maker's Compose file, checked against musdash's rules |
| `Services()` and memory | `Services()` returns what the header says and no Compose text; `Service(key)` reads the one file. | 500 Compose files held after the first page view would be about 1.5 MB of heap for good. The headers are about 150 KB |
| Logos and memory | A logo is never kept on the heap: its hash is computed once from the embedded file, and a request is answered from the embedded file directly, not gzipped | `static` keeps every asset it has served, raw and gzipped. That is right for five scripts and wrong for 500 images |
| Which logos | SVG only, at most 6 KB, with the rules `TestEveryOfferHasALogo` already has. First the selfh.st collection (CC BY 4.0, what the 25 logos there are came from), then the source's own SVG. A service with none has its icon in the brand's blue, as the component already does | A raster logo is 20 to 500 KB each |
| `docs` and `website` | `docs` or `website` is required, not both | Coolify's header has one address |
| Ports | A template publishes none. `ports:` is removed on import; a service that serves HTTP gets its address from `SERVICE_FQDN_<NAME>_<PORT>` | The catalogue's rule: two copies of one template must not collide on a server. A template that is nothing without a raw port (a game server) is left out |

### Categories

`ai`, `analytics`, `auth` (Authentication), `automation`, `backend`,
`business` (CRM, ERP, marketing), `cms`, `communication` (chat, forums,
video calls), `database` (Databases and their tools), `devtools` (Developer
tools), `docs` (Documentation and notes), `ecommerce`, `email`, `finance`,
`games`, `git` (Git and CI), `home` (Home and family), `media`,
`monitoring`, `networking` (proxies, VPN, DNS), `productivity`, `rss` (RSS
and reading), `search`, `security`, `storage` (files, backup, S3),
`support` (help desk).

The list is final when the import has run: a category nothing has is
removed, and one that most of "productivity" would rather be in is added.

## The conversion

Both sources go through the same last steps; a Dokploy blueprint is first
turned into the shape of a Coolify template.

**Dokploy only**

1. `[variables]` and `config.env` are resolved by substitution into the
   Compose text: a helper becomes a magic variable (`${password:N}` is
   `SERVICE_PASSWORD_<NAME>` or `SERVICE_PASSWORD_64_<NAME>`, `${base64:N}`
   is `SERVICE_BASE64_*`, `${hash:N}` is `SERVICE_HEX_*`, `${username}` is
   `SERVICE_USER_*`), a domain becomes `SERVICE_FQDN_<SERVICE>` of the
   service its `config.domains` entry names, and a fixed value becomes the
   default of the variable (`${NAME:-value}`), which the person can change.
2. Each `config.domains` entry adds `SERVICE_FQDN_<SERVICE>_<PORT>` to that
   service's environment.
3. `config.mounts` (a file with content) becomes a top-level `configs`
   entry with `content`, mounted where the Compose file mounted
   `../files/<path>`.
4. Left out: a blueprint that uses `${jwt}`, `${randomPort}`, `env_file`,
   or a mount of a directory.

**Both**

5. The header is rewritten: `name`, `about`, `docs`, `website`,
   `categories`, `source`.
6. Generated values keep their meaning. Coolify's `SERVICE_BASE64_*` is a
   random string, not base64: it becomes `SERVICE_PASSWORD_*`.
   `SERVICE_REALBASE64_*` becomes `SERVICE_BASE64_*`. A template with a
   kind musdash has no equal of (`SERVICE_SUPABASEANON_*`) is left out.
7. `ports:`, `container_name`, `exclude_from_hc` and Coolify's own keys are
   removed; an image with no tag gets `:latest`.
8. A file mount with `content:` (Coolify) becomes a `configs` entry.
   A `$` in its content is doubled, since Compose fills variables in there.
9. `./name:/path` with no content becomes a named volume.
10. A service that ends by itself (`restart: "no"`, or `exclude_from_hc`
    with a restart policy that does not restart) gets a
    `service_completed_successfully` dependency from the template's main
    service when nothing waits for it: `docker compose up --wait` fails
    for a container that exits, also with 0, unless something waits for
    its end (checked against Compose 5.5.1).
11. A template that still has what `Validate` refuses is left out, with
    the reason.

## Work

1. **Catalogue shape** (`internal/catalog`). `Categories`, `categories` and
   `source` header keys, `ServiceTemplate` without `Compose` in
   `Services()`, `Service(key)` reading the file. The six get their
   categories. Tests: every template has one to three known categories;
   `Services()` holds no Compose text.
2. **Logos off the heap** (`internal/web/static`). Test: after every logo
   was asked for, the package holds none of their bodies.
3. **The filter** (`ui/offer.templ`, `pages/resources.templ`, `app.js`,
   `input.css`). Chips, a line of categories on a card, the count beside
   the Services heading. Tests in `internal/web`: the page has a chip for
   every category in use and none for an empty one; every service offer
   carries its categories.
4. **The converter** (`tools/catalog`), with its tag table and overrides.
5. **The import**: run, test in the sandbox, fix the converter or record
   the reason, until the test is green. Commit the templates, the logos,
   `internal/catalog/services.LICENSE` and the list of what was left out.
6. **Tests for a large catalogue**: `TestServiceCatalogue` holds every
   template to the rules that can be read from text (no `ports:`, an image
   has a tag, a header that is whole, no password written out);
   `TestCatalogueLoadsInTheSandbox` runs in parallel; `TestEveryOfferHasALogo`
   checks every logo there is and requires one of the six, the engines and
   the app kinds.
7. **Check**: `go vet ./... && go test ./...`; the sandbox test with
   Docker; a sample started for real; idle RSS and the heap after the page
   was opened; the page in a browser, narrow and wide.
8. **Docs**: README, CLAUDE.md.

## Review of this plan

Read again on 2026-10-07 before any code. Changed after it:

- *Memory was only half thought of.* The first draft kept `Compose` in the
  list. Added "`Services()` and memory" and the logo decision: the page
  that shows 500 offers is the one that would have loaded 500 files and
  500 images onto the heap for good.
- *"All" was a promise the allow-list cannot keep.* Said at the top what
  it means here and that the rest is listed with reasons, rather than
  loosening `Validate` to get the number up. No rule of `Validate` changes
  in this work.
- *One-shot containers.* Found by trying it: `up --wait` fails for a
  container that exits with 0. Step 10 is the answer; a template whose
  one-shot cannot be told from its text will fail when deployed, which the
  sample run is there to estimate.
- *Coolify's `BASE64` is not base64.* A straight copy would have given
  apps a 44-character value with `+` and `/` where their maker tested a
  32-character one. Step 6.
- *The page's weight.* 500 offers is about half a megabyte of HTML. Kept
  as one page, since filtering in the browser is what makes search across
  everything immediate; the card's markup is measured in step 7 and
  trimmed if the page is over 600 KB.
- *Left as it is:* tags are not kept as search text. The name, the line
  about it and its categories are what the field looks in.

## What was built

Written after the work, on 2026-10-07.

**The numbers.** 611 templates: the six written for musdash, 303 of
Coolify's and 302 of Dokploy's. 26 categories, all in use. 419 of the 605
imported templates have a logo; the rest are drawn with the service icon.
112 services of the two catalogues are not there
(`docs/catalogue-left-out.md`): 39 for the Docker socket, 17 that Coolify
itself does not offer, 17 for a capability, network mode or security
option a stack may not have, 8 for a directory that is the server's own,
6 that are reached on a port of their own, 6 that build from source, and
the rest in ones and twos.

**What changed against the plan**

- *One-shot containers* are only given a waiter where the file says they
  end (`restart: "no"` or `on-failure`) and their name or Coolify's
  `exclude_from_hc` says they are a job. A service that is already waited
  for with `service_completed_successfully` gets `restart: "no"`, since
  musdash's default policy would run it again for ever.
- *A password written out* is replaced only where the file shows two ends
  of it, and in every place or none. The first version replaced each one it
  found, which would have locked Plausible out of its own database: its
  image connects as `postgres:postgres` unless told otherwise, and that
  default is not in the file. The catalogue's test therefore holds only the
  six own templates to "a password is always generated".
- *A variable with no default gets an empty one.* The catalogue's form asks
  for every variable that has none, and Coolify's templates have about a
  thousand such references (a mail server, an API key) that both platforms
  start a stack without.
- *A directory of the server that is only a place for data* (`/opt/app`)
  becomes a named volume rather than a reason to leave the template out.
  `/`, `/etc`, `/run`, `/var/lib` and the like still are one.
- *An address that only Coolify's rule can place* (it belongs to the
  service that names it with a port) is renamed to that service's own name.
- *One service under two file names* (`gitea-with-postgresql`,
  `gitea-postgres`) is one template, Coolify's, and two templates never
  share a name. A service template of an engine musdash runs as a database
  (ClickHouse, KeyDB, Dragonfly) is left out.
- *A template with no web address* is kept where the apps beside it are
  who it is for, with `connect: true`, and left out where it is reached on
  a port of its own. The plan had said so for game servers; the first
  import kept them with their ports gone, and the second reader caught it.
- *`Validate` did change, in one place.* Compose 5 writes an empty
  `placement` into every `deploy` block of the normalised form, and the
  allow-list refused it: no file with `deploy.resources.limits` could be
  deployed at all. An empty `placement` is now passed; one that names a
  constraint is refused as before. No rule is looser for it.
- *The page.* Some 620 offers were 1.5 MB of HTML. The icons of a card's foot
  are now drawn once on the page and referred to (`ui.IconDefs`,
  `ui.IconRef`), and the filter reads a card's own text instead of a copy
  of it in an attribute: 760 KB, rendered in 3 to 19 ms. Under 40rem the
  chips are one row that scrolls.
- *Logos never overwrite one musdash has* (the engines', the notification
  channels'): a template of the same key is shown with that one.

**What was checked**

- `go vet ./... && go test ./...`, and the converter's own tests
  (`tools/catalog`).
- `TestCatalogueLoadsInTheSandbox` for all 611, against Docker 29 and
  Compose 5.5.1.
- Twenty imported templates started for real with
  `TestCatalogueWithDocker`, chosen to have one of each kind of conversion
  (a file mount, a job that ends, a generated address, a password that was
  written out, Dokploy's variables). Seventeen of them are in the catalogue
  and come up, answer on their address, keep their values across a
  redeploy and are removed with their volumes: adminer, cockpit,
  docker-registry, filebrowser, flatnotes, garage, gatus,
  gitea-with-postgresql, glance, it-tools, listmonk, mailpit, opengist,
  plausible, shiori, umami, vaultwarden. The other three are what "Review
  of the code" says was found by running it: one that its makers' own file
  no longer starts, one that shipped a fixed administrator password, and
  one that was merged into another. Two bugs of the converter were found
  the same way and fixed (an empty file, a dollar sign in a variable's
  text).
- Memory, on this machine: 22 MB resident before the page was opened, 33 MB
  at the most while it was rendered three times, 22 MB twenty seconds
  after eight pages and every logo twice. The binary is 1.9 MB larger.
- The page in a browser at 1280 and 375 pixels: the chips, the field and
  the two together.

**What is not checked.** The other 588 imported templates were never
started. Each loads and passes musdash's rules, which is what a deployment
checks before it starts anything. Whether its containers then come up is
found out by deploying it, and going by the twenty that were tried, some
will not: for a reason in their makers' file (as with Hoppscotch) or for
one in the conversion that no check of the text can see (as with the empty
file, which Compose accepts when it reads a file and refuses when it
starts it).

## Review of the code

Two reviews on 2026-10-07: my own, with the page open in a browser and
templates started on Docker, and a second reader's of the diff. What they
found, and what was done about each.

**Found by running it**

- *`app.js` declared `narrow` twice*, which is a syntax error that stops the
  whole script: no dialog, no menu, no filter, on every page. No test runs
  JavaScript. Renamed, and `TestScriptsDeclareEachNameOnce` now looks for
  the one mistake that costs everything.
- *The import overwrote three logos musdash already had* (ClickHouse,
  GitLab, Mattermost) and would have deleted them on its next run. The
  converter now replaces only what its own list says it wrote.
- *A password hash in a Dokploy variable was mangled* (`$argon2id$v=19$…`
  became `${argon2id:-}${v:-}…`): a variable's own text was put into a file
  without its dollar signs escaped. Fixed in `expand`. The same template
  showed what the hash was for: a fixed administrator password, the same on
  every install. A blueprint that ships a password hash is now left out,
  and a sign-in password written out for the app itself is asked for on
  the template's form instead (`${ADMIN_PASSWORD:?…}`): not generated,
  since an app may have rules for a password and the person has to know it
  anyway.
- *Compose refuses a `deploy` block*: see "`Validate` did change" above.
- *An empty file could not be started.* Coolify writes a file with no
  content for the app to fill; as a config, Compose accepts it when it
  reads the file and refuses it when the stack is started. It is now one
  line end, and the sandbox test fails for a config with no content.
- *One template is larger than a Compose file may be* (300 KB of
  configuration files against the 128 KB a person may save): left out, and
  the test holds every template to that size.
- *Hoppscotch does not start*, as its makers wrote it: its migration job
  runs a command that the tool it downloads no longer has. Left out
  (`tools/catalog/rejected.txt`).

**Found by the second reader**

- *A game server whose port is closed is of use to nobody.* The import
  dropped every `ports:` and kept the template. Now a template with no web
  address is kept only if the apps beside it are who it is for (a database,
  a cache: `connect: true`) or it never listened for the outside (a tunnel,
  a bot); six that are reached on a port of their own are left out. A
  Coolify template that names its port only in its header gets its address
  from it, as Coolify gives it one. Both tests of the catalogue fail for a
  template that has neither an address nor `connect: true`.
- *The job's waiter could be the database the job needs.* The service with
  the web address is now tried first.
- *An address could go to SSH*, when a service published port 22 before its
  web port and its variable named none. The header's port is taken first.
- *`links` with a second name* (`backend:api`) lost the name; it is kept as
  an alias on the network. No template relied on a `container_name`.
- *The converter's own tests are outside `go test ./...`*: `make
  catalog-test`.
- *A failed run left the catalogue half empty*, and a run from another
  directory read no `rejected.txt` and said nothing. The earlier output is
  removed only when the new one is ready, and a missing list is an error.
- Smaller: two ways the converter could panic on odd input instead of
  leaving a template out; `utm_` parameters cut from the middle of a query;
  a logo with an event handler or a remote `url(…)` is refused, though
  neither does anything in an `<img>` under the dashboard's policy; hashing
  a logo allocated a 32 KB buffer for each of several hundred.

**Left as it is**

- The page is not compressed. It is 760 KB, down from 1.5 MB; a gzip writer
  is a megabyte of memory while it works, for a page opened to add
  something.
- Default texts of their makers' platform in a few values
  (`INSTANCE_NAME=${INSTANCE_NAME:-coolify}`): a value may mean something
  to the app, and the converter does not guess.

