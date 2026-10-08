# Add resource: categories in a menu, taller cards, a count

Asked for on 2026-10-08, for `/projects/{project}/env/{env}/new`
(`pages.ResourceNew`):

1. the categories are a dropdown, and several can be chosen;
2. a card is at least 450px high;
3. a card names its categories as badges, outlined, in soft colours;
4. "Your own Compose file" and "Compose file in a Git repository" are under Apps;
5. the Services heading says how many there are: "Services (124)";
6. a button clears the filters.

Three steps, each a commit: the filters (1, 6), the cards (2, 3), the groups
(4, 5).

## What is there now

- `ui.Chips` / `ui.Chip`: a row of radio buttons, one category at a time,
  about 26 of them in three rows above the lists. Used by this page only.
- `app.js` `filterList(id)`: an item stays when its text holds the field's
  text and its `data-tags` holds the one chosen value.
- `ui.Offer`: `TagText` ("CMS · Analytics") in a grey line under the
  description. Cards are 143 to 246px high, a row as high as its tallest.
- Services begins with the two Compose cards, and its sentence carries the
  number of templates.
- The page is 767 KB for 625 templates; `TestServiceCatalogueAndTemplateForm`
  refuses more than 800 KB.

## Step 1: the filters

**`ui.MultiSelect`** (`controls.templ`): the Select's button and popover, with
options that are checkboxes.

- The box is `<div class="select" data-select data-select-multi>`; `Attrs` go
  on it, so the page gives it `data-filter-pick="kinds"`.
- The button shows its label ("Category") and, when something is chosen, how
  many (`data-select-count`, a brand badge; " chosen" for a screen reader).
- An option is `<label class="menu-item check" role="option" aria-selected>`
  around a real `<input type="checkbox" name value>`, its label and its
  count. A real checkbox, because the browser then keeps the choice (Back,
  Refresh), a form would send it, and `.check input` draws it already: no
  new CSS for the box.
- The list is `role="listbox" aria-multiselectable="true"`. More than eight
  options get the filter field, as a Select does.

**`app.js`**

- A click on an option of a multi list does not shut it (the handler that
  records a Select's choice returns early). `mousedown` on such an option is
  prevented, so the focus stays in the filter field and the arrow keys go on
  from the marker.
- Enter in the filter field presses the marked option (already so: it calls
  `click()`, and a label's click toggles its checkbox). Without a filter
  field, Enter and Space on a focused label do the same.
- `syncMulti(box)` puts `aria-selected` and the count in step with the
  checkboxes: on `change`, on `pageshow`, after Clear.
- `filterList`: every checked value under `[data-filter-pick]`; an item
  stays when it has **any** of them (one category or the other: choosing a
  second one widens the list, as a second word in a search engine's facet
  does). None chosen keeps all.
- `data-filter-clear="<id>"` on a button: empties the field, unchecks the
  categories, filters again, puts the focus in the field. `filterList` hides
  every such button while there is nothing to clear.

**Page**: one row: the field, the Category menu, **Clear filters**. The
"Nothing matches" state gets Clear filters as its action, above the link to
a person's own Compose file.

**Removed**: `ui.Chips`, `ui.Chip`, `ChipProps`, `.chips`, `.chip`,
`.chip-count`: nothing else uses them.

Tests: `TestMultiSelect` (ui), the catalogue test reads the menu where it
read the chips (an option for every category in use and no other, its
count, none checked) and wants the Clear button; gallery gets a MultiSelect.

## Step 2: the cards

- `--size-offer: 28.125rem` (450px) in `@theme`; `.offer { min-height }`.
  The foot is already pushed to the bottom (`margin-block: auto`).
- `.badge-soft`: an outlined badge in the info tone's soft blue (border
  `info-line`, text `info`), no fill. `ui.Badge(text, ui.ToneSoft)`.
- A card's categories: `<p class="offer-tags">` with one `<span>` each,
  drawn by the badge's own rules (`.badge, .offer-tags > span`). Not a
  class on each: there are about a thousand of them on the page, and the
  page has 33 KB left. `OfferProps.TagText string` becomes
  `TagLabels []string`.
- One soft colour for all categories: the tones (ok, warn, danger) say
  state everywhere else, and 26 categories in five colours would say
  nothing.

Tests: `TestOffer` and the catalogue test read the badges; the page stays
under 800 KB.

## Step 3: the groups

- The two Compose cards move to the end of Apps. Their addresses do not
  change (`/service/new?template=custom`, `…=git`): what they make is still
  a service. Apps' sentence: "Your own code, image or Compose file. …".
- `kindGroup(title, count, about)`: with a count, the heading is
  "Services (<span data-filter-count>625</span>)". `filterList` writes the
  number of cards left in the group there, so the figure is always what is
  listed under it. The sentence loses its number and "your own".
- The count is the templates': the two Compose cards are no longer there.

Tests: the catalogue test wants the Compose cards before the Databases
heading, and the count in the Services heading.

## Review of the plan

- *Any or all?* Any. A template has one to three categories; "all" of two
  chosen would be nearly always empty.
- *A label with `role="option"` around a checkbox.* An option's children
  are presentational, so a screen reader hears one option, selected or not;
  the checkbox is `tabindex="-1"` and never in the tab order. Accepted.
- *The list closes when the page scrolls*, and narrowing the list can make
  the page shorter than where it was scrolled to, which is a scroll. To
  look at in the browser; if it happens, a list that is multi is placed
  again by its button instead of shut for the frames after a change.
  **Fixed in the plan**: added to step 1.
- *450px for cards whose content is 143 to 246px* leaves most of a card
  empty, and 638 cards make a page of about 100,000px. It is what was
  asked, so it is done as one token; said in the report, with a picture.
- *Badges inside `data-search-text`*: the field still finds a card by a
  category's name, as it does now.
- *`TestSignedInPagesHaveNoInlineScriptOrStyle`*: the menu's ids
  (`category`, `category-menu`, `category-list`) are new on the page; no
  `style` attribute anywhere (the list is placed through the CSSOM).
- *CLAUDE.md* names `ui.Chips` twice and lists the `data-*` attributes:
  updated with step 1.

Approved with the one fix.

## Review of the code

### Step 1

- Looked at in a browser, with the whole catalogue (625 templates): two
  categories ticked leave the menu open and the list at what has either;
  the button says 2; Clear filters empties both and goes; Enter in the
  menu's filter field ticks the marked option, Escape gives the focus back
  to the button.
- *The focus.* A click on a label puts the focus on its checkbox (Chrome),
  whatever `mousedown` did, and the arrow keys then started from nowhere.
  The click handler puts it back in the filter field, or on the option.
- *The scroll.* Confirmed: scrolled 100px down, the seven services of
  Support leave a page shorter than that, the browser scrolls to 61 and the
  list shut. Now it is put by its button again (4px under it), and the next
  scroll, the person's, shuts it as before.
- `item.tags` is read once a card, as `findBy` is: 638 cards are split once,
  not at every key.
- A `change` in the menu's own filter field also reaches both handlers. It
  does the same work again and changes nothing.

### Step 2

- Every one of the 638 cards is 450px high; the page is 100,196px (it was
  48,821) and 784 KB (it was 767; the test's limit is 800).
- A badge is 20px high, the info tone's line around it and its blue in the
  text, no fill: `ui.Badge("Analytics", ui.ToneSoft)` in the gallery is the
  same rule as a card's bare span.
- A card's content is 143 to 246px, so between the text and the foot there
  is 200px and more of nothing. Not changed: the height is what was asked
  for, and filling it (the logo on a band across the top, say) is a
  redesign nobody asked for. Reported with a picture.
- `TestOffer` now also holds a category to no class of its own, and a card
  with no categories to no empty row.
