# Add resource: found by the server, 48 at a time

Asked for on 2026-10-08, for `/projects/{project}/env/{env}/new`
(`pages.ResourceNew`), after it was tried on a VPS:

1. pagination, 48 a page, the next page loaded when the list is scrolled;
2. the search term and the filters are in the address;
3. the search waits until the typing pauses (debounce);
4. scrolling is laggy, and a search does not find what has "not loaded yet".

## What is there now

- The page holds every card: 7 ways to make an app, the engines, and 625
  service templates. 784 KB of HTML, sent uncompressed, and 638 `<img
  loading="lazy">`, each a request of its own as it comes near the window.
- The field and the Category menu narrow in the browser (`app.js`
  `filterList`): cards are hidden, nothing is asked of the server, and the
  address never changes. A filter is gone with Refresh.
- So on a slow line the list is still arriving while a person types (a card
  that has not arrived is not found), and scrolling starts dozens of image
  requests at once. That is 4.

## What it becomes

The server finds and cuts; the browser shows what it is given.

**Address.** `…/new?q=wordpress&category=cms&category=analytics`. `q` is the
field, `category` once for each one chosen. A page drawn from such an
address has the field filled in, the categories ticked and the list
narrowed, so Refresh, Back and a link that was sent to somebody all show
the same thing. `page=N` is how far the list goes (see "Afterwards").

**What is paged.** The services, 48 a page, in the catalogue's order (by
name). Apps (7) and Databases (the engines) are short lists that are not
paged: they are shown whole, above the services, when they match. 48 fills a
row at two, three and four columns.

**Finding** (`pages.NewResource.Find`):

- the text is cut to 64 characters, lowercased and split into words; every
  word has to be somewhere in the card: its name, its line, its categories'
  names, or the words it is also known by (`"service " + key`, an app
  card's own list). One word finds what it found before; several narrow,
  as they do in the bar's search;
- categories: a card stays when it has **any** of those chosen (as now).
  Only keys of `catalog.Categories` count, each once; anything else in the
  address is dropped. An app card has no category, an engine has
  `database`;
- a group with nothing left is not drawn; with nothing left at all the page
  says "Nothing matches" (`ui.EmptyState`), with Clear filters and the link
  to a person's own Compose file;
- "Services (N)": N is how many were found, not how many are shown.

No cache: 625 headers are walked for each request, which is some tens of
kilobytes of short-lived strings and nothing that stays.

**Three answers from the one route** (`GET …/new`, a Member's):

| Request | Answer |
| --- | --- |
| a browser's own | the whole page, first 48 services of what the address finds |
| htmx, target `kinds` (the form) | the groups for `#kinds`, the Clear button out of band, and `HX-Replace-Url` with the address in its simplest form |
| htmx, `page` ≥ 2 (the list's last row) | that page's cards and, if there are more, the next last row |

The third does not ask the database for the team's Git sources: it draws no
app card. A `page` past the end answers 200 with nothing.

**The form.** The row above the list becomes `<form method="get">` with

```
hx-get="…/new" hx-target="#kinds" hx-swap="innerHTML" hx-sync="this:replace"
hx-trigger="input delay:300ms from:#kind-filter, search from:#kind-filter, change from:#category-list, submit"
```

`search` is what a search field says on Enter (Chrome, Safari): asked at
once. The form has two text fields, its own and the menu's, so a browser
does not send it on Enter by itself.

`from:` because the Category menu has a filter field of its own, whose
typing must ask nothing. `this:replace` drops an answer that a newer
question has overtaken. While a question is out, `#kinds` is dimmed
(`hx-indicator`, one rule).

The address is replaced, not pushed: Back leaves the page, as it does now,
and does not walk back through every letter typed.

**Clear filters** is a link to the bare address, by the field and in the
empty state: a plain page load, which empties the field, the ticks and the
menu's count at once. By the field it is there only while something
narrows; an htmx answer brings its new state out of band (`#kinds-clear`).

**The last row.** While there are more services, the list ends with

```html
<li class="offers-more" hx-get="…/new?q=…&page=2" hx-trigger="intersect once, click" hx-swap="outerHTML">
  <button class="btn btn-secondary" type="button">Show more</button>
</li>
```

The answer takes its place: 48 cards and the next such row. It reaches
40rem up over the cards before it (a negative margin and as much padding,
with no pointer events of its own), so the next page is asked for before
the end of the list is on the screen. The button is for when the request
failed: a press asks again.

**Removed.** The browser's own narrowing has no user left: `filterList` and
`data-filter`, `data-filter-pick`, `data-filter-clear`, `data-filter-count`,
`data-filter-empty`, `data-filter-group` in `app.js`; `data-search`,
`data-search-text` and `data-tags` on a card, and `OfferProps.Search` and
`Tags` (a card no longer carries what finds it: the server knows).
`syncMulti` on `pageshow` stays, for the menu's count.

## Files

- `internal/web/pages/resource_new.go` (new): `NewResource`, `Find`, the
  app cards as data, the address builder. Moved out of `resources.templ`.
- `internal/web/pages/resources.templ`: `ResourceNew` (page),
  `ResourceFound` (answer 2), `ResourceMore` (answer 3).
- `internal/web/handlers_resources.go`: `resourceNew` reads `q`, `category`,
  `page` and chooses the answer.
- `internal/web/ui/offer.templ`, `controls.templ` (a comment), `input.css`
  (`.offers-more`, the dimming), `static/app.js`.
- Tests: `TestServiceCatalogueAndTemplateForm` (the page is the first 48,
  under 200 KB), a new `TestAddResourceIsFoundAndPaged`, `TestOffer`,
  the databases test's card lookup.
- `CLAUDE.md`, `docs/services.md`.

## Tests, written first

`TestAddResourceIsFoundAndPaged`:

- the page has 48 service cards, a last row asking for `page=2`, and
  "Services (625)";
- walking `page=2…` with `HX-Request` gives every template once, in order,
  and the last answer has no last row; a page past the end is empty;
- `?q=wordpress`: the field holds it, only matching cards, the count is the
  matches, and the last row (if any) carries `q`;
- several words narrow; capitals do not matter; a category's name finds;
- `?category=cms`: ticked in the menu, the button says 1, no app card, only
  CMS cards; `category=cms&category=analytics` is either; `category=nope`
  is as if not sent;
- `?category=database` keeps the engines;
- `?q=zzzz`: no group, the empty state, Clear filters;
- the htmx answer for `kinds` has no `<html`, has `HX-Replace-Url` in its
  simplest form (no empty `q`, no `page`, unknown categories gone), and the
  out-of-band Clear;
- what was typed comes back escaped (`q="><script>`).

## Review of the plan

- _48 of what?_ Of services only. The other two groups are 7 and about ten
  cards and are the top of the page; paging them with the services would
  make the first page "7 apps, the engines and 31 services" and move a
  heading into the middle of a page. Kept; said in the report.
- _Every word, not the whole phrase._ "wordpress cms" found nothing before
  (the phrase is in no card); now it finds WordPress. One word is unchanged.
- _An answer that arrives late._ The form's are replaced (`hx-sync`). A
  last row's answer for a list that has since been replaced has no target
  left and htmx drops it.
- _Back._ Pages are `no-store`, so Back asks the address again, which now
  says what was narrowed. The field has `autocomplete="off"`; the browser
  restores the ticks, which are the address's own.
- _`HX-Target` decides the answer._ A request that only claims to be htmx
  gets a fragment of the same data the page holds: nothing to gain.
  htmx's history request sends no target and gets the whole page.
- _The count on the Category button_ after an htmx answer: the menu is
  outside `#kinds` and `app.js` already keeps it on `change`. Good.
- _Options' counts in the menu_ stay the catalogue's totals, not what the
  text has left. Unchanged from now; a count that moved while typing would
  need the menu swapped too. Not asked for.
- _Inline style or script?_ None: the last row is CSS, the form is
  attributes. `allowEval` is off and nothing here needs it.
- _The reach of the last row_ covers cards with a box that takes no
  pointer. The button inside must take it again. **Fixed in the plan**:
  `.offers-more > * { pointer-events: auto }`, and to look at in a browser
  that a card under it can still be pressed.
- _A full page with `page=3` in its address_ (somebody copied the last
  row's address): `page` is ignored and the first 48 are drawn.
  **Fixed in the plan**: said under "Address", tested.
- _HTML is still sent uncompressed._ The page goes from 784 KB to about
  100 KB, so it stops mattering here; compressing pages is a change to
  every answer and not part of this.

Approved with the two fixes.

## Afterwards: the page is in the address too

Found in the review of the code, before the commit. With the list in
pieces, Back from a card's form drew the first 48 again: a person who had
scrolled to the 150th card and opened it came back to the top. Before this
change the whole list was in the page and the browser put it where it was.

So the address follows the list. The answer to the last row sets
`HX-Replace-Url` to `…/new?…&page=N`, and a browser's own request with
`page=N` draws the list from its start up to that page (`Find(…, whole)`),
with the last row asking for N+1. Back and Refresh then get a page as long
as the one that was left, and the browser restores the scroll. The form's
answer has no page: what it finds is another list, from its start. A number
past the end is the whole list; what is no number is the first page.

This takes back one fix of the plan's review ("a full page with `page=3`
is the first 48"). The cost is that Refresh far down the list draws that
much of it at once: at the very end, the 623 cards the page always drew
before, now about 700 KB.

## Review of the code

- **In a browser** (a second musdash on a port of its own, the whole
  catalogue):
  - the page is 105 KB for 63 cards (7 apps, 8 engines, 48 services); it was
    784 KB for 638;
  - "wordpress" typed in one go asks once, 300ms after the last letter:
    Services (7), the address `?q=wordpress`, Clear filters by the field,
    the focus still in the field;
  - CMS and Analytics ticked: the menu stays open, the button says 2,
    Services (79), `?category=analytics&category=cms`; that address loaded
    again has both ticked and the field filled;
  - scrolled down, page 2 arrives (51 KB, 5ms here) before the end of the
    first is on the screen, and the address becomes `?page=2`; a card under
    the last row's reach is still the thing a press lands on; Show more,
    pressed, brings the next page;
  - a card opened from the 96th and Back: 96 cards, the same scroll
    position, the field as it was;
  - at 375px nothing scrolls sideways.
- _The form's answer came second to the list's._ The first version asked
  "is there a page?" before "is it the form?", so a form request that
  carried a page got cards and no address. The form is asked first now, and
  tested with a page in its request.
- _A page past the end._ The clamp was one too low and gave the last page
  again for every number after it. It is the page after the last, which is
  empty; `Atoi` gives the largest number for one too long, which the clamp
  takes as well.
- _Work for nothing._ The categories' names were joined for every template
  even with no text to find. Now only when there is text.
- _`OfferProps.Search` and `Tags`_ are gone with the attributes, and
  `TestOffer` holds a card to carrying no `data-`.
- _The databases test_ looked for three services' logos on the page that
  listed everything. It asks for each by name now, which is also a test of
  the field.
- _`EmptyProps.Attrs`_ has no user left in the pages. Kept: it is a
  component's general way in, and its test still uses it.
- _Not done:_ the menu's counts do not follow the text; the list's changes
  are not announced to a screen reader (they were not before); HTML is
  still sent uncompressed.
- _To see on the VPS:_ how long a page of 48 takes over the real line. Here
  it is 5ms and 51 KB; the first page is one request of 105 KB and 63
  images, where it was 784 KB and up to 638.
