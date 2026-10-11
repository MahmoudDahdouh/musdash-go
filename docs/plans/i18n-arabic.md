# Two languages: English and Arabic

Asked for on 2026-10-10: the dashboard in two languages, English (left to right) and Arabic (right to left, Modern Standard Arabic), with a component that switches between them. Everything a person reads in the dashboard is translated. Work is on the branch `feat/i18n-arabic-rtl`.

The owner gave the glossary the same day (`tech-glossary-en-ar.md`, terms taken from cranl.com/ar). The terms the dashboard uses are in "Glossary" below and every Arabic text follows them. The full sentences of the dashboard will be given by the owner later; until then the catalogue holds a draft written with that glossary (decision 21).

This is the second version of the plan. The first was reviewed against the code on 2026-10-10; what the review found is folded into the decisions, and listed at the end ("What the review changed") so the reasons stay with it.

The answers given before this plan was written:

| Question | Answer |
|---|---|
| Library or our own | Our own, standard library only (see "Why not a library") |
| Addresses | English is the default and has no prefix; Arabic is the same page under `/ar` |
| Scope | Everything the dashboard shows |
| Left in English | The API, the CLI and installer, notifications, the catalogue's descriptions, what Docker and servers answer, the guides in `docs/` |
| Digits | Western (0–9) in both languages |
| Font | Rubik, for both languages |
| Switcher | In the person's menu, and in a corner of the signed-out pages |
| Charts | Mirrored in Arabic |
| Words | The owner's glossary (2026-10-10) |

## What was looked at

- **templ's own guide** passes the locale in the request context and looks strings up from components, which every templ component can do since each receives `ctx`. Its library (`ctxi18n`) reads YAML.
- **CLDR plural rules**: Arabic uses all six categories. For whole numbers: zero (0), one (1), two (2), few (n % 100 in 3..10), many (n % 100 in 11..99), other (everything else: 100, 101, 102, …). English has one and other.
- **RTL on the web**: `lang` and `dir` on `<html>`; CSS logical properties (`margin-inline-start`, `inset-inline-end`, `text-align: start`) and Tailwind's `ms-`/`me-`/`ps-`/`pe-`/`start-`/`end-`/`text-start` utilities, so one stylesheet serves both directions; text that is left to right by nature (domains, code, logs) is isolated inside the right-to-left page. In a right-to-left box `scrollLeft` runs from 0 at the start to negative values, not from 0 up.
- **Language switchers**: each language named in itself ("English", "العربية"), no flags (a flag is a country, not a language), each option marked with its `lang`; a remembered choice is checked against the list of known languages before it is used.
- **Fetch metadata**: every current browser sends `Sec-Fetch-Mode: navigate` on a page load and something else on `fetch`, `EventSource`, a WebSocket handshake or an image. htmx 2.0.11 (the copy in `static/`) sends `HX-Current-URL` with every request and fires `htmx:pushedIntoHistory` and `htmx:replacedInHistory`.
- **Rubik on Fontsource**: subsets `arabic`, `cyrillic`, `cyrillic-ext`, `hebrew`, `latin`, `latin-ext`; a variable font, weights 300–900; SIL OFL 1.1.
- **The code as it is** (2026-10-10): 28 templates; 13 rules in `input.css` and 9 classes in templates name a side (`layout.templ:156`, `apps.templ:839,865`, `servers.templ:121,124,131,136,253`); no handler reads `r.URL.Path` or `RequestURI` to build an address; the chain is `s.recoverer(secureHeaders(mux))` (`server.go:376`); `Referrer-Policy: same-origin`; `plainText` (`text.go`) refuses U+2066–206F; the delete forms compare the typed name in `app.js:136` and in four handlers; `ui.Ago`, `Until`, `Took` and `ui.Destinations` take no context; `db.DefaultEnvironment` is `"production"` and environment names are lowercase ASCII (`envNameRE`).

### Why not a library

`ctxi18n` and `go-i18n` need a YAML or TOML parser and `golang.org/x/text`: three or four modules for a project that allows three in all. What musdash needs is small: look a text up, put values into it, and pick one of six plural forms, and the rules for that are fifteen lines. Written ourselves, the catalogue is constant data in the binary (decision 5) and costs next to nothing at run time.

## Decisions

### The package and the catalogue

| # | Decision | Why |
|---|---|---|
| 1 | A package of its own, `internal/i18n`, with no dependency on anything in musdash. It holds the languages, the catalogue, the plural rules and the context keys | Every layer can ask it (`ui`, `pages`, `web`, and a lower package that words an error or a label a person reads, decisions 18 and 36) without a cycle |
| 2 | Two languages, `i18n.En` and `i18n.Ar`, in a fixed list. A language is `{Code, Name, Dir}`: `en`, `English`, `ltr`; `ar`, `العربية`, `rtl` | A third language is a line in the list and a catalogue file |
| 3 | The key of a text is its English wording: `t(ctx, "Add environment")`. English has no catalogue; a key is its own English. A text with no Arabic shows in English | The templates keep reading as they do now, a missing translation is never a blank, and the tests that look for English text keep passing |
| 4 | The same English that needs two Arabic texts gets a context: `tc(ctx, "service", "Stopped")`, keyed `service\x04Stopped` (gettext's separator). Used only where the Arabic differs. The usual case is agreement: an app is masculine and a database or a service feminine, so "Stopped" is متوقف for an app and متوقفة for a database or a service | The glossary uses one word for the verb and the noun (نشر for Deploy and Deployment), so the context is not for verb and noun; it is for what Arabic agrees with and English does not |
| 5 | The Arabic catalogue is Go source: `internal/i18n/ar_*.go`, one file per area of the dashboard (`ar_layout.go`, `ar_apps.go`, …), each a package-level slice of `{En, Ar string}` literals, all listed in one `var areas = [][]entry{arLayout, arApps, …}`. The index is built once at start: a `[]ref` sorted by key, where `ref` is `struct{ area, pos uint16 }` (4 bytes), so about 8 KB for two thousand texts. A lookup is a binary search over it. The slices themselves are never copied | A slice of constant strings is static data in the binary and is not copied to the heap; a map of two thousand entries would be a few hundred KB of heap for the life of the process (RAM rule 8). One file per area lets the pages be translated side by side without touching the same file. The index has to say which area a position is in, since there is one slice per file |
| 6 | Values go into a text as `{0}`, `{1}`, … and a count as `{n}`, not as `fmt` verbs: `t(ctx, "Deleted {0}.", name)`. A translation may use a placeholder in another place, twice, or not at all | Arabic orders a sentence differently, and its forms for one and two leave the number out (دقيقة, دقيقتان): `fmt` would print `%!(EXTRA …)` |
| 7 | Plurals: `tn(ctx, n, "{n} minute", "{n} minutes")`. The Arabic entry is keyed by the English for one and has six forms, chosen by the CLDR rules for `n` | The rules are the CLDR chart's, tested against its examples |
| 8 | In an Arabic text, every value put in is wrapped in FSI … PDI (U+2068, U+2069). Such a text is for reading only: the output of `t`, `tc` and `tn` never goes into `data-copy`, `data-match`, an input's `value` or anything else a person copies or a script compares; those get the bare value. What a person types back is read with `typed(r, name)` (decision 9), and `app.js` drops the same two characters before it compares a `data-match` | A name or a domain inside a right-to-left sentence otherwise takes the punctuation next to it to the wrong side. But a person who copies a name out of such a sentence copies the two invisible characters with it: pasted into the delete confirmation, the name never matches (`app.js:136`, `handlers_apps.go:1125` and three more), and pasted into a name field `plainText` refuses it |
| 9 | `typed(r, name)` in `web` (`text.go`) is `strings.TrimSpace` of the form value with U+2068 and U+2069 taken out, and nothing else. Every handler that reads a name, a domain or a delete confirmation uses it in place of `strings.TrimSpace(r.PostFormValue(…))`. Other invisible and reordering characters are still refused by `plainText` as now | Only the two characters musdash itself puts into a page are taken out. A reordering character a person typed is still refused, which is what `plainText` is for |
| 36 | A text that is held in a Go list and shown later (a label in a table of options, a state's name, a role's name, a page in `ui.Destinations`, a notification event's label, a channel field's label and hint, a category's label, the heading and line of a `kindGroup`) is written `i18n.Mark("Running")` where the list is, and translated where it is shown: `t(ctx, v.Label)`. `Mark` returns its argument; it exists so the tests can find the key. A text held in a package-level string constant (`envNameRule`, `notShown`) is found by the tests through the constant, so a constant needs no `Mark` | Without it the test that every text has Arabic (decision 34) sees only literal arguments of `t`, and the lists (`notify.Events`, `notify.Kinds`, `pages.RoleLabel`, `kindGroup`, `eventLabel`, `catalog.Categories`, `ui.Destinations`) would pass it in English |

### Addresses and the remembered choice

| # | Decision | Why |
|---|---|---|
| 10 | Arabic is the same address under `/ar`: `/ar/projects/…` is `/projects/…`. One wrapper in front of the router (`localePrefix`) takes `/ar` off `r.URL.Path` (and `RawPath`), puts Arabic in the context and passes the request on; the routes are registered once. `/ar` alone leads to `/ar/`. The chain becomes `s.recoverer(secureHeaders(s.localePrefix(mux)))` | The owner's choice. One route table, one set of handlers and of role checks (`routes.go`) for both languages. The mux's own redirects (a cleaned path, a trailing slash) are made from the stripped path and get `/ar` back from decision 12 |
| 11 | `/ar` is for pages only. Under `/ar`, a path that starts with `/static/`, `/api/`, `/webhooks/`, `/lang/` or `/_ui`, or is `/healthz`, answers 404. The test for "starts with" is by whole segment here and everywhere in this plan (`/ar` and `/ar/…`, never `/archive`) | One address for each of those. What other machines call is not in a language; `/lang/` decides the language itself and must not be inside one (decision 14); the gallery stays English (decision 32) |
| 12 | Redirects need no change in the handlers. Under `/ar` the response writer is wrapped (`localeWriter`) and, before the headers go out, puts `/ar` in front of `Location`, `HX-Redirect`, `HX-Location` and `HX-Replace-Url` when the value is a path of this site: it starts with one `/`, does not start with `/static/`, `/api/`, `/webhooks/`, `/lang/` or `/_ui`, and is not already `/ar` or under `/ar/`. It changes headers only, never the body, and has `Unwrap`, so `Flush` (SSE) and `Hijack` (the terminal) reach the real writer | A body rewrite would buffer the page (RAM rule 8). `redirect`, `http.Redirect` and the htmx headers are used in a hundred places that then stay as they are |
| 13 | Addresses in a page go through `ui.At(ctx, path)`, which puts `/ar` in front under Arabic: `href`, `action`, `hx-get`/`hx-post`/…, `sse-connect`, `ui.Poll`'s URL, `data-address`, `data-terminal` and every other attribute holding an address of a page. `pages.EnvPath`, `ResourcePath` and `Path()` stay as they are, and the template wraps what they give: `ui.At(ctx, v.Path())`. `ui.At` leaves alone what decision 12 leaves alone (`/static/`, `/lang/`, …), so it can be called on any address | One place that knows the prefix. The builders of paths are also used for redirects, where decision 12 adds the prefix, and would add it twice. The terminal's WebSocket is opened from `data-terminal` and a browser does not follow a redirect on a WebSocket handshake |
| 14 | The choice is remembered in a cookie, `musdash_lang` (`en` or `ar`, a year, `Path=/`, `HttpOnly`, `SameSite=Lax`, `Secure` under HTTPS). The switcher's links go to `GET /lang/{code}?next=<path>` (registered `open`), which sets it and leads to `next` in that language. A request without `/ar` is led to the same address under `/ar` when the cookie is `ar` **and** all of these hold: the method is `GET` or `HEAD`; `Sec-Fetch-Mode` is `navigate` (absent counts as not); it is not an htmx request; and the path is not one of decision 11's. With no cookie the language is English: `Accept-Language` is not read | A link from outside (a notification's short address, a bookmark) brings an Arabic reader to Arabic. Only a page load may be redirected: a plain form `POST` would lose its body to a 303, a WebSocket handshake fails on a redirect, and an `EventSource` (the SSE extension sends no `HX-Request`) or a download gains nothing from one |
| 15 | `next` must be a path of this site: one leading `/`, no `//`, no `\`, no control character, no scheme; `/ar` (as a segment) is taken off it first. Anything else leads to `/` | It is an address in a link, and must not be one to another site |
| 37 | An htmx request whose own path has no `/ar` but whose `HX-Current-URL` is a page of this site under `/ar/` is answered in Arabic, as if it had the prefix (the language goes into the context and `localeWriter` wraps the writer). Only the language is taken from the header; nothing is routed by it | A safety net for an `hx-*` address that missed `ui.At`: without it the fragment comes back in English inside an Arabic page. The header is the client's word, but a language is not something to protect |

### The switcher and the page

| # | Decision | Why |
|---|---|---|
| 16 | `ui.LanguageSwitcher` is a `ui.Menu` with one `ui.MenuItem` for each language: the language's own name, `lang` and `hreflang` on the link, `Current` on the one in use. Its button shows a globe icon and the current language's name. It is in the person's menu (`userMenu`, between Settings and Sign out) and at the top end of `AuthLayout` (sign in, set up, reset, invitation, second step). The gallery (`/_ui`) shows it. Its links are never wrapped in `ui.At` (decision 13 leaves `/lang/` alone) | Where a person looks for a setting of their own, and before signing in. A `Menu`, not a `Select`: choosing goes to another page |
| 38 | `next` in the switcher's links is where the reader is, without `/ar`. `localePrefix` puts the request's path and query (after the prefix is taken off) into the context (`i18n.WithPlace`, `i18n.Place`). The switcher uses it for a `GET`. For a page that answers another method it uses `ui.Shell.Address` when the page set one, else the path of a same-origin `Referer` (sent, since `Referrer-Policy` is `same-origin`), else `/`. The links carry `data-lang-next`, and `app.js` rewrites their `next` from `location` (without `/ar`) on `htmx:pushedIntoHistory` and `htmx:replacedInHistory` | A page that answers a `POST` has the `POST`'s address, and a `GET` of it is a 405. The Add resource page changes its address with `HX-Replace-Url` as the list is narrowed and scrolled, after the switcher was drawn |
| 17 | Everything a person reads is translated: text in templates, `aria-label`, `title`, `placeholder`, `alt`, page titles, crumbs, empty states, dialogs, the confirm questions (`ui.Ask`, `ui.Confirm`), flash notes, form errors, the search's pages and their keywords (an Arabic word finds them too), relative times, the few words `app.js` writes | "Everything". A screen reader reads the attributes |
| 18 | Errors: a message a handler makes is translated where it is made (`tr(r, …)`, which reads the context). An error from a lower package that a page shows (`err.Error()` in a flash or a form, twenty places in the handlers) is translated by `i18n.Error(ctx, err)`: an `*i18n.Message` found with `errors.As` anywhere in the chain is put into the language with its values; any other error is looked up by its whole text, and shown as it is when the text is not in the catalogue. A lower package makes an error with values as `i18n.Errorf("port {0} is in use", p)`, whose `Error()` is the English. What a server or Docker answered stays as it came, after the translated sentence that introduces it | The handler knows the request; the lower package does not and should not. A wrapped error (`fmt.Errorf("…: %w", err)`) has a text no catalogue holds, so it is found by its type, not its text. Docker's words are not ours to translate |
| 19 | Flash notes are put into words when they are set, in the language of the request that sets them | The cookie already holds UTF-8 (base64) and is read once by the next page, which is in the same language |
| 20 | `app.js` has no words of its own: the few it writes ("Copied", "Loading…", the search's notes) are given to it by the layout as `data-t-*` attributes on `<body>` | No catalogue in the script, and the page's language decides |
| 21 | Until the owner's sentences come, the Arabic catalogue holds a draft in Modern Standard Arabic, written with the owner's glossary and the writing rules below. A draft entry is marked by being in a file under `// draft` and listed by `make i18n-report`. The owner's text replaces it in the same files | Every page can be seen and tested in Arabic now; the owner's wording is a change of the catalogue only |
| 22 | `<html lang dir>` comes from the context in `Layout`, `AuthLayout` and the error pages (`pages.NotFound`, `pages.Message`): `lang="ar" dir="rtl"` or `lang="en" dir="ltr"` | The browser mirrors the layout, the screen reader picks the voice, the font picks its glyphs |
| 39 | Names that are data are never translated. An environment's name is lowercase ASCII (`envNameRE`) and the first one is `production` (`db.DefaultEnvironment`); it is shown as stored, in the mono stack, in both languages. The glossary's بيئة الإنتاج and بيئة ما قبل الإنتاج are for sentences about environments, never for a name | A name translated on one page and not on another (the search, the rename form, the switcher, the server's network) reads as two environments |

### Right to left

| # | Decision | Why |
|---|---|---|
| 23 | The stylesheet and the templates use only logical properties and utilities: `margin-inline-start` not `margin-left`, `inset-inline-end` not `right`, `text-align: start`, `border-inline-end`, `ms-`/`me-`/`ps-`/`pe-`/`start-`/`end-`/`text-start`/`rounded-s-`. The 13 rules in `input.css` and 9 classes in templates that name a side are changed. A test keeps it so (decision 34) | One stylesheet for both directions, and nothing for a later page to forget |
| 24 | What moves sideways is turned with the direction. The sidebar drawer slides in from the start (`translateX(-100%)` and `border-right` at `input.css:1735-1736` follow `dir`; `margin-left: var(--size-sidebar)` at `:1817` becomes `margin-inline-start`). A popover list is put by its button's start edge, `menuEnd` by its end edge: `app.js:246` reads the direction of the button (`getComputedStyle(button).direction`) and, under `rtl`, takes `r.right - width` for the start and `r.left` for the end; the list is still placed with `style.left`, which is a coordinate of the window, not a side. The trail is scrolled to its end: `app.js:52` sets `scrollLeft` to `-scrollWidth` under `rtl`. The open tab's centring (`app.js:55-57`) adds a difference of two positions and works in both directions as it is | In a right-to-left box `scrollLeft` is 0 at the start and negative towards the end, so `trail.scrollLeft = trail.scrollWidth` is held at 0 and shows the start of the trail, not where the reader is |
| 25 | Icons that point (`chevron-right`, `arrow-right`, back arrows) are mirrored under `dir="rtl"` by a class, `.icon-dir` (`scaleX(-1)`), set by `ui.Icon` for the names in a short list. Icons that do not point (a clock, a check, `chevron-down`, a logo) are not | A chevron that points away from the next step reads as "back" |
| 26 | Left to right inside a right-to-left page: whatever is in the mono stack (domains, images, ports, hashes, ids, cron lines, environment names), code and logs, the terminal, the Compose and variable editors, and fields for such values. `.font-mono`, `code`, `pre`, `kbd`, `.log`, `.terminal` and `.input-mono` get `direction: ltr; unicode-bidi: isolate`, and `ui.Input`/`ui.Textarea` give `dir="ltr"` to a `Mono` field. Names typed by people that may be Arabic (projects, teams, people, channels, the labels `plainText` checks) are shown in `<bdi>` and their fields have `dir="auto"`. App, database and service names are DNS-safe ASCII and are shown in the mono stack | A domain read right to left is another domain. A name may be Arabic or English and has to be shown in its own direction |
| 27 | Charts are mirrored in Arabic: time runs from the right. `ui/chart.go` reads the direction from the context and draws x from the other side; the labels stay text, not mirrored | The owner's choice. Drawing it the other way is cheaper and sharper than flipping the SVG, which would mirror the labels |
| 28 | Times: `ui.Ago`, `ui.Until` and `ui.Took` take the context as their first argument and use `tn` (منذ 5 دقائق, خلال ساعتين, 2د 5ث). Dates are written with the language's month names (`2 يناير 2006`). Digits are Western in both languages. Clock times stay `15:04` | The owner's choice of digits. One set of month names (يناير…ديسمبر), the one most read across the Arab world in software |

### The font

| # | Decision | Why |
|---|---|---|
| 29 | Font: Rubik, the variable font (weights 300–900), in three subsets: Latin, Latin Extended and Arabic, as the WOFF2 files of `@fontsource-variable/rubik` (the same files Google Fonts serves), in `internal/web/static` with its licence (SIL OFL 1.1, `fonts.LICENSE`). There is no Node here: `make fonts` downloads the three files from `cdn.jsdelivr.net/npm/@fontsource-variable/rubik@<version>/files/` at a pinned version at least two weeks old, checks each against a SHA-256 written in the `Makefile`, and they are committed like `app.css`, so a build needs only Go. The `unicode-range` of each subset is copied from the package's own CSS, not written by hand. `@font-face` has `font-display: swap` | The owner's choice. Served by musdash and not by Google: the CSP (`default-src 'self'`) already allows it, a server with no internet still has its font, and no reader's address goes to a third party. A page in English fetches no Arabic file. Pinned and checked like the builders' releases |
| 30 | The font files are served like a logo: hashed and answered from the embedded file, never held on the heap and never gzipped (WOFF2 is compressed already). The `logo` flag of `static.asset` (`static.go:69`) becomes `streamed`, true for a name that starts with `logo-` or `rubik-` | About 100 KB that would otherwise stay in memory once asked for |
| 31 | `--font-sans` becomes Rubik and then the system stack as now; the mono stack is unchanged. Under `:lang(ar)` letter spacing is 0 (`tracking-tight` and the like breaks joined Arabic letters) and the line height of body text is a little taller. Rubik changes the English dashboard too (it is wider than the system fonts at 14px), so the look-through of tasks 6 and 22 covers English as much as Arabic | Arabic is joined writing, and its marks need room above and below. The English pages were laid out with the system fonts |

### What stays English, names, and the tests

| # | Decision | Why |
|---|---|---|
| 32 | What stays English: `/api/v1`, the CLI, the installer, notifications (Slack, email, …), the catalogue's names and descriptions of services and engines (several hundred, from Coolify and Dokploy), what Docker, git and servers answer, logs, the guides in `docs/`, the component gallery (`/_ui`, development only), and the plain-text 404 of a fragment (`http.NotFound` in four handlers). The catalogue's **categories** are not its text: they are the dashboard's own filter (`catalog.Categories`) and are translated (decision 36), sorted by the label in the reader's language | The owner's choice. A template's text is data owned by its upstream, and the API is read by programs |
| 33 | Product names are not translated: musdash, Docker, Compose, Git, GitHub, GitLab, Bitbucket, Nixpacks, Railpack, S3, cron, TLS, DNS, and engine names. Where the glossary gives a technical term in Arabic with the Latin in brackets (HTTPS → بروتوكول النقل الآمن (HTTPS), SSH → الاتصال الآمن عن بُعد (SSH), Webhook → خطّاف ويب (Webhook)), that form is used where the term stands alone (a heading, a label, a column); inside a compound or a sentence that has named it already, the Latin alone (مفتاح SSH, رابط Webhook). A value a person picks or types (the scheme `https`, a port, an example in a field) stays as the machine writes it | The owner's glossary decides the words. The bracketed form is long, and a compound with it twice is not read |
| 34 | Tests that keep it whole (details under "Tests"): the plural rules against CLDR; a catalogue with no duplicate key and no placeholder lost; no key in code without an Arabic entry and no entry without a key; no text in the generated templates outside `t(…)`; every page under `/ar` has `lang="ar" dir="rtl"` and no address of a page without `/ar`; no side named in the stylesheet, the templates or `app.js`; the redirects of decision 14 only on page loads | A new page or button added later in English only fails a test instead of shipping half translated |
| 35 | Short names in Go: `t`, `tc`, `tn` in `ui` and `pages` (one small file each, calling `i18n`), `tr`/`trn` in `web` (from `r.Context()`). The tests find the keys by these names, `i18n.T`, `i18n.Errorf`, `i18n.Mark`, and string constants passed to any of them | `{ t(ctx, "Deploy") }` reads in a template; `{ i18n.T(ctx, "Deploy") }` two thousand times does not |

Left for later, on purpose: a third language; a language for the API's error texts; notifications in the team's language; a language stored with the account (the cookie and the address cover it; an account setting would be a migration and a third place to look); Eastern Arabic digits; folding Arabic letter forms (أ إ آ → ا, ة → ه, ى → ي) in the database search, where names are matched as typed. The search's own pages are matched with that folding (task 8), since their words are ours.

## How a request finds its language

```
GET /ar/projects/abc              GET /projects/abc
  │                                 cookie musdash_lang=ar, Sec-Fetch-Mode: navigate, no HX-Request
  ▼ localePrefix                    ▼ localePrefix
  ctx: ar, place /projects/abc      303 → /ar/projects/abc
  w = localeWriter(w)
  │                                 POST /projects/abc/settings (cookie ar, no /ar)  → not redirected
  ▼ router (one table)              GET  …/terminal/ws (WebSocket)                   → not redirected
  │   ui.At(ctx, "/projects/abc/settings") = "/ar/projects/abc/settings"
  │   redirect(w, r, "/projects/abc") → Location: /ar/projects/abc   (by localeWriter)
  ▼
<html lang="ar" dir="rtl"> …

GET /lang/ar?next=/projects/abc   → Set-Cookie musdash_lang=ar; 303 → /ar/projects/abc
GET /lang/en?next=/projects/abc   → Set-Cookie musdash_lang=en; 303 → /projects/abc
GET /ar/lang/en?next=…            → 404 (the switcher's links are never under /ar)

GET /projects/abc/switch/environments   HX-Request: true, HX-Current-URL: https://host/ar/projects/abc
  → answered in Arabic (decision 37)
```

The order of wrappers: `recoverer` → `secureHeaders` → `localePrefix` → router. The session, CSRF and role checks are inside the router and see the path without `/ar`, so they do not change.

## The i18n package

```go
package i18n

type Lang struct{ Code, Name, Dir string }

var En = Lang{"en", "English", "ltr"}
var Ar = Lang{"ar", "العربية", "rtl"}
var All = []Lang{En, Ar}

func WithLang(ctx context.Context, l Lang) context.Context
func From(ctx context.Context) Lang              // En when none
func Parse(code string) (Lang, bool)             // only codes in All
func Prefix(ctx context.Context) string          // "" or "/ar"

func WithPlace(ctx context.Context, pathAndQuery string) context.Context
func Place(ctx context.Context) string           // "" when none

func T(ctx context.Context, en string, args ...any) string
func TC(ctx context.Context, context, en string, args ...any) string
func N(ctx context.Context, n int, one, other string, args ...any) string
func Mark(en string) string                      // returns en; marks a key held in a list

type Message struct{ /* English text and its values */ }
func Errorf(en string, args ...any) error        // *Message
func Error(ctx context.Context, err error) string // errors.As for *Message, else the whole text

func pluralAr(n int) int                         // 0..5: zero one two few many other
```

A catalogue file:

```go
package i18n

// draft
// The pages of an app: apps.templ, handlers_apps.go.
var arApps = []entry{
	{"Add domain", "إضافة نطاق"},
	{"Deploy", "نشر"},
	{"Redeploy", "إعادة النشر"},
	{"Rollback", "التراجع عن النشر"},
	{"Deleted {0}.", "تم حذف {0}."},
	{"app\x04Stopped", "متوقف"},
}

var arAppsPlural = []plural{
	{"{n} app", [6]string{"لا تطبيقات", "تطبيق واحد", "تطبيقان", "{n} تطبيقات", "{n} تطبيقًا", "{n} تطبيق"}},
}
```

And `i18n.go` lists them: `var areas = [][]entry{arLayout, arAuth, arProjects, …}`; plurals likewise.

## Glossary

The owner's glossary decides. Below are the terms of it the dashboard uses, as the owner wrote them; one English word has one Arabic word everywhere. The second table is what the dashboard needs that the glossary does not have: a draft, for the owner to confirm.

### From the owner's glossary

| English | العربية |
|---|---|
| Dashboard | لوحة التحكم |
| Platform | منصة |
| Account | حساب |
| Project | مشروع |
| Team | فريق |
| Roles | أدوار |
| Admin | مدير |
| Member | عضو |
| Session | جلسة |
| Deploy / Deployment | نشر |
| Redeploy | إعادة النشر |
| Auto-deploy on every push | نشر تلقائي مع كل دفع (push) |
| Rollback | التراجع عن النشر |
| Deploy history | سجل النشر |
| Failed deploy | نشر فاشل |
| Build | بناء |
| Build log | سجل البناء |
| Build folder | مجلد البناء |
| Start command | أمر التشغيل |
| Production environment | بيئة الإنتاج |
| Staging environment | بيئة ما قبل الإنتاج |
| Hosting | استضافة |
| Server | خادم |
| Domain | نطاق |
| Custom domain | نطاق مخصص |
| Automatic SSL | شهادة SSL تلقائية |
| HTTPS | بروتوكول النقل الآمن (HTTPS) |
| Port | منفذ |
| Environment variables | متغيرات البيئة |
| .env file | ملف متغيرات البيئة (.env) |
| Cron job / Scheduled task | مهمة مجدولة |
| Volume | وحدة تخزين |
| Container | حاوية |
| Docker image | صورة Docker |
| Container registry | سجل الحاويات |
| Public / Private registry | سجل عام / سجل خاص |
| Terminal | الطرفية |
| CLI | واجهة سطر الأوامر |
| SSH | الاتصال الآمن عن بُعد (SSH) |
| CPU | المعالج |
| Memory (RAM) | الذاكرة |
| Disk | القرص |
| Logs | السجلات |
| Live logs | سجلات مباشرة |
| Metrics | المقاييس |
| Alerts | تنبيهات |
| Notifications | إشعارات |
| Service status | حالة الخدمة |
| Event | حدث |
| App down / App back up | توقف التطبيق / عودة التطبيق |
| Webhook | خطّاف ويب (Webhook) |
| Signed webhook | Webhook موقّع |
| Diagnose | تشخيص |
| Rate limiting | تحديد معدل الطلبات |
| Repository | مستودع |
| Private repository | مستودع خاص |
| Branch | فرع |
| Push | دفع |
| Pull request | طلب سحب |
| Git URL | رابط Git |
| Source | المصدر |
| Preview | معاينة |
| Endpoint | نقطة نهاية |
| Route | مسار |
| Documentation | التوثيق |
| Templates | قوالب |
| API | واجهة برمجة التطبيقات |
| API key | مفتاح API |
| Token | رمز |
| Permissions | صلاحيات |
| Full access | وصول كامل |
| Read-only | قراءة فقط |
| Read-write | قراءة وكتابة |
| Secrets | الأسرار |
| Expiry date | تاريخ الانتهاء |
| Confirmation | تأكيد |
| Activity log | سجل النشاط |
| Restore | استعادة |
| Login | تسجيل الدخول |
| Database | قاعدة بيانات |
| Storage bucket | حاوية تخزين |
| S3-compatible | متوافق مع S3 |
| Channel | قناة |
| Right-to-left (RTL) | من اليمين إلى اليسار |

How the glossary's words fall on musdash's pages, where it was not obvious:

- An app's **Deployments** tab is سجل النشر (Deploy history), its **Logs** tab السجلات. A deployment that failed is نشر فاشل.
- **Token** is رمز and is always named with what it is: رمز API, رمز النشر (deploy token). A second-step code is رمز التحقق and recovery codes رموز الاسترداد, so رمز alone is never on a page.
- **Container** (حاوية) and **Storage bucket** (حاوية تخزين) are close: the backup's S3 form says حاوية تخزين S3 in full every time, and حاوية alone is only ever a container.
- **Admin** is مدير (not مسؤول). **Source** is a Git source (المصدر); musdash shows no "origin".
- A domain's **Path** is المسار, the glossary's word for a route, which is what it is.

### Not in the glossary (draft, for the owner)

| English | Arabic (draft) | Note |
|---|---|---|
| Owner | المالك | the role |
| Environment | بيئة | a name of one stays as stored (decision 39) |
| App | تطبيق | |
| Service | خدمة | |
| Backup | نسخة احتياطية | |
| Health check | فحص السلامة | |
| Shared variables | المتغيرات المشتركة | |
| Tag | وسم | |
| Category | فئة | the catalogue's filter |
| Invitation | دعوة | |
| Sign out | تسجيل الخروج | |
| Password | كلمة المرور | |
| Two-step sign-in | التحقق بخطوتين | |
| Private key | مفتاح خاص | |
| Settings | الإعدادات | |
| Search | بحث | the dialog; the glossary's لوحة الأوامر is a command palette, which it is not |
| Language | اللغة | |
| Running / Stopped / Failed / Queued | قيد التشغيل / متوقف / فشل / في الانتظار | متوقفة for a database or a service (decision 4) |
| Add / Edit / Delete / Save / Cancel | إضافة / تعديل / حذف / حفظ / إلغاء | |

Writing rules for the Arabic: Modern Standard Arabic; buttons are verbal nouns (نشر، حفظ، حذف، إضافة) as in the glossary and most Arabic software, never the imperative; no tashkeel except where a word is ambiguous without it; the Arabic comma (،) and question mark (؟); Western digits; product names in Latin (decision 33).

## Tests

- `i18n`: `pluralAr` against the CLDR chart's examples (0, 1, 2, 3, 10, 11, 99, 100, 101, 102, 103, 111, 1000); `T` with a missing key gives the English; placeholders in any order, repeated or absent; FSI/PDI around values only in Arabic; `N` in English and Arabic; `TC` falls back to the plain key's Arabic and then the English; `Error` translates a `*Message` wrapped twice with `%w`, translates a known whole text, and leaves an unknown one as it is; the index finds an entry of every area.
- `TestCatalogueIsWhole` (`i18n`): no key twice across the areas; every `{0}`/`{n}` of the English is in the Arabic, and the Arabic has none the English lacks; every Arabic plural has six forms.
- `TestEveryTextHasArabic` (`web`): walks the Go files of `ui`, `pages`, `web`, `notify`, `catalog` and the lower packages of task 20 (the generated `*_templ.go` included) with `go/parser`, collects the first text argument of `t`, `tc`, `tn`, `tr`, `trn`, `i18n.T`, `i18n.TC`, `i18n.N`, `i18n.Errorf`, `i18n.Mark` (a literal, or a package-level string constant of the same package, looked up in its declaration), and fails for one with no Arabic entry. Fails too for an entry no code uses, so the catalogue does not rot.
- `TestNoTextOutsideT` (`web`): reads the generated `*_templ.go` with `go/parser`, not the `.templ` files. Static markup is in the string literals given to `templ_7745c5c3_Buffer.WriteString`: tags are taken out, and what is left between tags, and the values of `aria-label`, `title`, `placeholder` and `alt`, fail when they hold a Latin letter. Literals given to `templ.EscapeString` / `templ.JoinStringErrs`, and literals in the `Label`, `Title`, `Text`, `Hint`, `Placeholder`, `Question`, `About` and `Filter` fields of composite literals, fail the same way unless they are a call of `t`/`tc`/`tn`. An allow-list holds the product names of decision 33, and `gallery_templ.go` is skipped. Reading the generated Go rather than templ's syntax needs only the standard library and is not thrown by `if`, `for`, `switch` or Go blocks.
- `TestArabicPages` (`web`): renders every page of `TestSignedInPagesHaveNoInlineScriptOrStyle`'s list, and the signed-out ones, under `/ar`: `<html lang="ar" dir="rtl">`; every `href`, `action`, `hx-*`, `sse-connect`, `data-address` and `data-terminal` value that is a path of this site starts with `/ar/`, except `/static/` and `/lang/`; every `/lang/` link carries a `next` without `/ar`.
- `TestLocalePrefix` (`web`): `/ar` → `/ar/`; `/ar/static/…`, `/ar/api/v1/me`, `/ar/webhooks/…`, `/ar/lang/en`, `/ar/_ui`, `/ar/healthz` → 404, and `/archive` is not taken for `/ar`; a redirect, an `HX-Redirect` and an `HX-Replace-Url` under `/ar` carry it, once; a `Location` of `/static/…`, `/lang/…` or another site is left alone; the mux's own redirect of an unclean path keeps `/ar`; SSE flushes and the terminal hijacks through the wrapper; an htmx request to `/projects/…` with `HX-Current-URL` under `/ar/` is answered in Arabic and one with an `HX-Current-URL` of another site is not.
- `TestLanguageCookie` (`web`): `/lang/ar?next=…` sets the cookie and leads to the `/ar` address; `/lang/en?next=/projects/abc` leads to `/projects/abc` and a page there is English; `next` of `//evil`, `https://evil`, `/\evil`, a control character → `/`; `next=/ar/projects/abc` with `/lang/en` → `/projects/abc`; an unknown code → 404. With the cookie at `ar`, a `GET` with `Sec-Fetch-Mode: navigate` to a page without `/ar` leads to `/ar`; none of these does: an htmx request, a `POST` form (the handler runs with its body), a `GET` without `Sec-Fetch-Mode`, a WebSocket handshake to the terminal, an `Accept: text/event-stream` request, `/static/…`, `/api/…`, `/webhooks/…`, `/lang/…`.
- `TestTypedDropsIsolates` (`web`): deleting a project, an app and a database with the name pasted as it is in an Arabic page (U+2068 name U+2069) succeeds; renaming a project to a name with them stores the name without them; a name with U+202E is still refused.
- `TestStylesAreLogical` (`web`): `input.css` names no `left`/`right` property (`margin-left`, `padding-right`, `left:`, `right:`, `border-left`, `text-align: left`, …) and the templates and `app.js` no `ml-`/`mr-`/`pl-`/`pr-`/`left-`/`right-`/`text-left`/`text-right`/`rounded-l`/`border-r` class, outside a short allow-list with the reason for each (`app.js`'s `style.left` for a popover, which is a window coordinate; `scrollLeft`).
- `ui`: `Ago`, `Until`, `Took` and dates in both languages; a chart drawn under Arabic has its first point at the right; `LanguageSwitcher` marks the current language, gives each link its `lang`, `hreflang` and `data-lang-next`, and uses `Shell.Address` on a page that answers a `POST`; `ui.Destinations` under Arabic lists Arabic titles, and the search finds a page by an Arabic word written with or without hamza (`اعدادات` finds الإعدادات).
- `static`: the font files are served with immutable caching, are not held and are not gzipped (as `TestALogoIsNotKept`).
- The tests that exist keep passing in English unchanged.
- `make rss-linux` before and after: the idle figure must not grow by more than the index (about 8 KB).

## Review focus

The cases a person meets that the decisions imply and no other test above would see first; each has its test in the task that owns the code:

1. A reader in Arabic picks English and must get English, not the same Arabic page (task 2: `TestLanguageCookie`, `/lang/en` and `/ar/lang/en`).
2. A reader with the cookie at `ar` sends a plain form, opens a terminal or follows live logs, and nothing is redirected (task 2: `TestLanguageCookie`'s list of requests that are not).
3. A reader copies a name out of an Arabic sentence into the delete confirmation or a name field (task 3: `TestTypedDropsIsolates`; task 5: `app.js` `data-match`).
4. An `hx-get` that missed `ui.At` answers inside an Arabic page (task 2: `TestLocalePrefix`, `HX-Current-URL`).
5. The trail of a deep page under Arabic shows its end, and a menu opens by its button's start edge (task 5: looked at in Chromium at 360px, the trail of a resource's page and the person's menu).

## File map

| File | What |
|---|---|
| `internal/i18n/i18n.go` | `Lang`, context, place, `T`/`TC`/`N`/`Mark`, placeholders, isolation, the areas and the index |
| `internal/i18n/plural.go` | `pluralAr` |
| `internal/i18n/message.go` | `Message`, `Errorf`, `Error` |
| `internal/i18n/ar_*.go` | the Arabic catalogue, one file per area |
| `internal/i18n/*_test.go` | the package's tests and `TestCatalogueIsWhole` |
| `internal/web/locale.go` | `localePrefix`, `localeWriter`, `GET /lang/{code}`, `tr`, `trn` |
| `internal/web/text.go` | `typed` |
| `internal/web/locale_test.go` | `TestLocalePrefix`, `TestLanguageCookie`, `TestArabicPages`, `TestTypedDropsIsolates` |
| `internal/web/i18n_lint_test.go` | `TestEveryTextHasArabic`, `TestNoTextOutsideT`, `TestStylesAreLogical` |
| `internal/web/testdata/i18n-pending/<area>.txt` | the files and route prefixes of an area not translated yet (see "Working side by side") |
| `internal/web/ui/t.go`, `internal/web/pages/t.go` | `t`, `tc`, `tn`, and `ui.At` |
| `internal/web/ui/language.templ` | `LanguageSwitcher` |
| `internal/web/ui/layout.templ` | `lang`/`dir`, the switcher in `userMenu` and `AuthLayout`, `data-t-*` |
| `internal/web/ui/format.go`, `chart.go` | times, dates and the chart in both directions |
| `internal/web/ui/search.templ` | `Destinations(ctx, s)`, titles and keywords through `Mark` |
| `internal/web/assets/input.css` | logical properties, `@font-face`, `.icon-dir`, left-to-right islands, `:lang(ar)` |
| `internal/web/static/rubik-*.woff2`, `fonts.LICENSE`, `static.go` | the font and how it is served (`streamed`) |
| `internal/web/static/app.js` | words from `data-t-*`, popover placement and the trail by direction, `data-match` without isolates, `data-lang-next` |
| `internal/notify/notify.go`, `internal/catalog/services.go`, `internal/web/pages/team.templ`, `resources.templ`, `home.templ` | the lists of decision 36 through `i18n.Mark` |
| every `.templ` in `ui` and `pages`, every `handlers_*.go` | the texts in `t(…)`/`tr(…)`, the addresses in `ui.At`, names read with `typed` |
| `internal/web/server.go`, `routes.go` | `localePrefix` in the chain, `GET /lang/{code}` as `open` |
| `Makefile` | `make fonts`, `make i18n-report` (keys with no Arabic, and draft entries) |
| `CLAUDE.md`, `README.md`, `docs/team.md` | the rules (no more "no web fonts"; a new text goes through `t`, a listed one through `Mark`; an address through `ui.At`; a typed name through `typed`; sides are logical), a line under Features, a paragraph on the language |

## Working side by side

Tasks 9–19 can be done side by side once tasks 1–8 are in, each in a worktree of its own (`.claude/worktrees/i18n-<area>`, branched from `feat/i18n-arabic-rtl` and merged back into it). So that two of them never edit the same file:

- Each area has its own catalogue file (`ar_<area>.go`).
- The lint tests' allow-list is a directory, not a list in a test file: `internal/web/testdata/i18n-pending/<area>.txt` names the files of that area not translated yet and the route prefixes of its pages. The lint tests skip those files; `TestArabicPages` skips the pages under those prefixes. A task deletes its area's file; it touches no other.
- `app.css` is generated: a merge that conflicts in it takes either side and runs `make generate` again.
- Task 8 (the `ui` components and the frame) goes first, alone: every page is drawn inside it.

Another session may be working in the shared checkout at the same time (it holds uncommitted catalogue work on 2026-10-10): stage by path, never check out over its files.

## Tasks

Each task ends with `go vet ./... && go test -short ./...` green, `make generate` where templates or CSS changed, a review of its own diff, and a commit on `feat/i18n-arabic-rtl` with the subject `i18n task N: …`. Tests are written first where the task names them.

The foundation (tasks 1–8) is done in order. The pages (tasks 9–19) touch different files and can be done side by side (see above); each follows the glossary.

### Foundation

- [ ] **1. The `i18n` package.** `Lang`, context and place, `T`/`TC`/`N`/`Mark`, placeholders, FSI/PDI, the areas and the `ref` index, `pluralAr`, `Message`/`Errorf`/`Error` with `errors.As`. Tests first: the plural chart, placeholders, fallback, isolation, `Error` through `%w`, `TestCatalogueIsWhole` on a small catalogue.
- [ ] **2. `/ar` and the remembered choice.** `localePrefix` (prefix by segment, the place in the context, `HX-Current-URL` for htmx), `localeWriter` (headers only, `Unwrap`), the 404s of decision 11 (with `/lang/` and `/_ui`), `GET /lang/{code}` with the `next` checks, the cookie redirect for page loads only (decision 14). `tr`/`trn`. The chain in `server.go`. Tests first: `TestLocalePrefix`, `TestLanguageCookie` (review focus 1, 2, 4).
- [ ] **3. `ui.At`, `typed` and the frame.** `t`/`tc`/`tn`/`At` in `ui` and `pages`; `typed` in `web`, used by the four delete confirmations and every handler that reads a name or a domain; `<html lang dir>` in `Layout`, `AuthLayout`, `pages.NotFound` and `pages.Message`; `data-t-*` on `<body>` and `app.js` reading them; `TestArabicPages` over the frame's links. Tests first: `TestTypedDropsIsolates` (review focus 3).
- [ ] **4. The switcher.** `ui.LanguageSwitcher`, in `userMenu` and `AuthLayout`, a globe icon in `ui.Icon`, the gallery entry; `next` from `i18n.Place`, `Shell.Address` or the `Referer` (decision 38); `data-lang-next` and its rewrite in `app.js` on the two htmx history events. Test of its markup, and of `next` on a page that answers a `POST`.
- [ ] **5. Right to left.** Logical properties through `input.css` and the templates (the 13 rules and 9 classes); the drawer, popover placement by direction (`app.js:246`), the trail's `scrollLeft` (`app.js:52`); `app.js` drops U+2068/U+2069 before a `data-match` compare; `.icon-dir` and its list of icons; the left-to-right islands, `dir="ltr"` on `Mono` fields and `<bdi>`/`dir="auto"` for names (decision 26); `:lang(ar)` spacing. `TestStylesAreLogical`. Looked at in Chromium in both directions at 360px and 1280px (the sidebar, a drawer, a menu by its start and by its end, a dialog, a table, a form, the trail of a resource's page, the terminal) (review focus 3, 5).
- [ ] **6. Rubik.** `make fonts` with the pinned version and checksums; the three WOFF2 subsets and the licence; `@font-face` with the package's `unicode-range`; `--font-sans`; `streamed` in `static.go`. Static test. The English pages looked at again at 360px and 1280px, since the font is new to them. `make rss-linux`.
- [ ] **7. Times, dates, charts, and the lint tests.** `Ago`/`Until`/`Took` with the context (and their callers); dates; the chart mirrored; `TestEveryTextHasArabic` and `TestNoTextOutsideT` over the generated Go, with `testdata/i18n-pending/` holding an entry for every area of tasks 8–20. `make i18n-report`.
- [ ] **8. The `ui` components and the frame:** layout, sidebar (`ui.Nav`; `ui.Destinations(ctx, s)` with its titles and keywords through `Mark`, Arabic keywords, and the search's page matching with letter forms folded), trail and switchers, search dialog, confirm dialog, empty states, pills and their states (through `Mark`, with the contexts of decision 4), `Ask`/`Confirm`, form components, toasts. `ar_layout.go`. Deletes `i18n-pending/layout.txt`.

### The pages

Each: every text in `t(…)` (in the template) or `tr(…)` (in the handler: flash notes, form errors, titles, crumbs), every text held in a list through `i18n.Mark`, every address in `ui.At`, every typed name through `typed`, the Arabic draft in its `ar_*.go` following the glossary, its `i18n-pending/<area>.txt` deleted, its pages looked at under `/ar`.

- [ ] **9. Signed out and the account:** setup, sign in, second step, reset, invitation, account page, two-step setup and recovery codes, error and not-found pages. `ar_auth.go`.
- [ ] **10. Home, projects, environments, tags, search results** (`eventLabel` through `Mark`; environment names as stored, decision 39). `ar_projects.go`.
- [ ] **11. Add resource** (`resource_new`): the groups (`kindGroup` through `Mark`), the Category menu (`catalog.Categories` labels through `Mark`, sorted in the reader's language), filters, paging, offers (the catalogue's own names and descriptions stay English, decision 32). `ar_new.go`.
- [ ] **12. Apps:** the image and Git forms and their pickers, settings, domains and the DNS check, deployments, logs, previews, storage, health check, options. `ar_apps.go`.
- [ ] **13. Variables:** the editors and lists of an app, a service and the shared ones. `ar_variables.go`.
- [ ] **14. Databases.** `ar_databases.go`.
- [ ] **15. Services** (a template's or a person's own Compose, pasted or from Git). `ar_services.go`.
- [ ] **16. Servers, metrics and the terminal** (`data-terminal` through `ui.At`). `ar_servers.go`.
- [ ] **17. Operations:** backups (the S3 form's wording, see the glossary notes), restores, scheduled tasks, schedules, clean-up. `ar_operations.go`.
- [ ] **18. Sources, keys and tokens.** `ar_sources.go`.
- [ ] **19. Team, invitations, notifications, settings** (`pages.RoleLabel` and the role options, `notify.Events` labels, `notify.Kinds` field labels and hints through `Mark`). `ar_team.go`.
- [ ] **20. Errors from lower packages** (decision 18): the texts a page shows from `db`, `deploy`, `compose`, `source`, `auth`, as known texts or `i18n.Errorf`. `ar_errors.go`. `testdata/i18n-pending/` is empty after this task, and the directory is removed with the code that reads it.

### Closing

- [ ] **21. Docs.** `CLAUDE.md` (the i18n package, `t`/`tr`/`Mark`/`ui.At`/`typed`, logical sides, the font, what stays English, the glossary as the source of words), a line under Features in `README.md`, a paragraph on the language and its address in `docs/team.md`.
- [ ] **22. Review.** `/code-review` over the branch; every finding fixed or answered. Every page looked at in Arabic and in English in Chromium at 360px and 1280px; `make rss-linux` against `main`.
- [ ] **23. The owner's sentences** (when they come): replace the draft entries, and confirm or replace the "Not in the glossary" table; `make i18n-report` shows none left.

## Risks

- **A link without `/ar`** throws an Arabic reader into English. `TestArabicPages` covers the pages in its list; for a page load the cookie redirect (decision 14) brings them back, and for an htmx request `HX-Current-URL` does (decision 37). A plain form `POST` without `/ar` is the case neither covers: its answer is English. `TestArabicPages` checks every `action`.
- **A key changed in English** leaves its Arabic behind. `TestEveryTextHasArabic` fails for the new key and for the entry nothing uses.
- **A text held in a list** that is not written with `Mark` is not seen by the tests. `TestNoTextOutsideT` catches the props fields it names; a review of each page task looks at the lists the page draws from.
- **Longer Arabic text** in a button or a column. Arabic is often shorter than English, but not always; the look-through of tasks 5 and 22 at 360px is where it is found.
- **Rubik in English** changes widths the English pages were laid out for. Tasks 6 and 22 look at the English pages too.
- **Size of the work**: about two thousand texts across 28 templates and the handlers. Tasks 9–19 are independent, so they can be done side by side.

## What the review changed

The first version of this plan was checked against the code on 2026-10-10. What was found, and where it is answered:

| Found | Answered by |
|---|---|
| The switcher's English link would have been `/ar/lang/en`, whose redirect `localeWriter` put back under `/ar`: no way out of Arabic | Decisions 11, 12, 13, 16; `TestLocalePrefix`, `TestLanguageCookie` |
| The cookie redirect took any request that was not htmx: a plain form `POST` (body lost), the terminal's WebSocket (a handshake does not follow redirects), `EventSource` (sends no `HX-Request`) | Decision 14; `TestLanguageCookie` |
| FSI/PDI copied with a name broke the delete confirmation (`app.js:136`, four handlers) and made `plainText` refuse a pasted name | Decisions 8, 9; `TestTypedDropsIsolates` |
| "production → الإنتاج" would have translated a stored name (`db.DefaultEnvironment`) | Decision 39 |
| The index of positions did not say which file's slice a position is in, and joining the slices would have copied them to the heap | Decision 5 |
| Texts held in Go lists (`notify.Events`, `notify.Kinds`, `RoleLabel`, `kindGroup`, `eventLabel`, `catalog.Categories`, `ui.Destinations`) were invisible to both lint tests | Decision 36; `TestEveryTextHasArabic` |
| Reading `.templ` syntax for the text test would be fragile | `TestNoTextOutsideT` reads the generated Go |
| The switcher had no source for `next`, and a page that answers a `POST` or changes its address with `HX-Replace-Url` would have had a wrong one | Decision 38 |
| `trail.scrollLeft = trail.scrollWidth` (`app.js:52`) stays at the start under `rtl`; the "tab underline" named before is centring that already works | Decision 24 |
| No safety net for an `hx-*` address that missed `ui.At` | Decision 37 |
| `i18n.Error` by text missed wrapped errors | Decision 18 |
| "Has no `/ar` already" was a string test, which `/archive` passes | Decision 11 (by segment everywhere) |
| The verb/noun example (`انشر`/`النشر`) disagreed with the glossary, which uses نشر for both and verbal nouns on buttons | Decision 4 (contexts are for agreement); the glossary |
| The page tasks shared the lint allow-list, the page list and `app.css`, so they could not be done side by side | "Working side by side" |
| Rubik changes the English dashboard as well, and no step said how to fetch it without Node | Decisions 29, 31; task 6 |
| Counts were off (25 classes in templates; there are 9) | "What was looked at" |
