// musdash UI behaviours. Everything is driven by data attributes so pages
// carry no inline script and the Content-Security-Policy can forbid it.
(() => {
  "use strict";

  const on = (type, selector, fn) =>
    document.addEventListener(type, (e) => {
      const el = e.target instanceof Element ? e.target.closest(selector) : null;
      if (el) fn(el, e);
    });

  // Dialogs: data-open="<dialog id>" opens, data-close closes the nearest one.
  on("click", "[data-open]", (el) => {
    const dialog = document.getElementById(el.dataset.open);
    if (!(dialog instanceof HTMLDialogElement) || dialog.open) return;
    // A dialog gives the focus back to what had it when it opened. An item
    // of a menu is not there to take it once the menu has shut; the menu's
    // own button is.
    el.closest("[data-select]")?.querySelector("[popovertarget]")?.focus();
    dialog.showModal();
  });
  on("click", "[data-close]", (el) => el.closest("dialog")?.close());
  // A click on the backdrop lands on the <dialog> element itself.
  document.addEventListener("mousedown", (e) => {
    if (e.target instanceof HTMLDialogElement && e.target.open) e.target.close();
  });
  // A dialog marked data-autoopen is one the server sent back open: its form
  // was refused, and the person is put at the first field that is wrong.
  document.querySelectorAll("dialog[data-autoopen]").forEach((dialog) => {
    dialog.showModal();
    dialog.querySelector("[aria-invalid=true]")?.focus();
  });

  // A page that answers a POST and shows what it made (a token, a key) says
  // which address it has for a GET. Putting that in the address bar makes
  // Refresh fetch the page, where it would have sent the form again.
  if (document.body.dataset.address) history.replaceState(history.state, "", document.body.dataset.address);

  // data-alone="<id>" on a checkbox: while it is checked, the other
  // checkboxes inside that element cannot be, because it stands for them all.
  const alone = (el) =>
    document.getElementById(el.dataset.alone)?.querySelectorAll("input[type=checkbox]").forEach((box) => {
      if (box !== el) box.disabled = el.checked;
    });
  on("change", "[data-alone]", alone);
  document.querySelectorAll("[data-alone]").forEach(alone);

  // On a narrow screen the trail and a row of tabs are wider than the page
  // and scroll sideways. They start at where the person is: the end of the
  // trail, and the tab that is open.
  document.querySelectorAll(".crumbs").forEach((trail) => {
    trail.scrollLeft = trail.scrollWidth;
  });
  document.querySelectorAll(".tabs [aria-current]").forEach((tab) => {
    const row = tab.closest(".tabs");
    const from = tab.getBoundingClientRect().left - row.getBoundingClientRect().left;
    row.scrollLeft += from - (row.clientWidth - tab.offsetWidth) / 2;
  });

  // Critical actions: a submit button carrying data-confirm does not send
  // its form. It fills in the page's one confirm dialog and opens it, and
  // that dialog's button sends the form, as if this one had been pressed.
  let asking = null;
  on("click", "[data-confirm]", (el, e) => {
    const dialog = document.getElementById("confirm");
    if (!dialog || !el.form) return;
    e.preventDefault();
    // What the browser would have said about the form, it says first.
    if (!el.formNoValidate && !el.form.reportValidity()) return;
    asking = { el, action: el.form.getAttribute("action"), name: el.name, value: el.value };
    dialog.querySelector("#confirm-title").textContent = el.dataset.confirmTitle || "Are you sure?";
    dialog.querySelector("#confirm-text").textContent = el.dataset.confirm;
    const ok = dialog.querySelector("#confirm-ok");
    ok.textContent = el.dataset.confirmSubmit || "Confirm";
    ok.className = el.dataset.confirmTone === "danger" ? "btn btn-danger" : "btn btn-primary";
    // A dialog gives the focus back to what had it when it opened, and a
    // button in a menu is not there to take it once the menu has shut. Its
    // menu's own button is.
    el.closest("[data-select]")?.querySelector("[popovertarget]")?.focus();
    dialog.showModal();
  });
  on("click", "#confirm-ok", (ok) => {
    const asked = asking;
    asking = null;
    ok.closest("dialog").close();
    if (!asked) return;
    let el = asked.el;
    if (!el.isConnected) {
      // The page replaced the form while the dialog was open: a header that
      // refreshes itself does. The same button of the form that took its
      // place is the one that was meant.
      const form = [...document.forms].find((f) => f.getAttribute("action") === asked.action);
      el = [...(form?.querySelectorAll("[data-confirm]") || [])].find((b) => b.name === asked.name && b.value === asked.value);
    }
    el?.form.requestSubmit(el);
  });

  // data-filter="<id>" on a field narrows what is inside that element to the
  // items whose data-search holds the text. A data-filter-group with nothing
  // left in it goes too, and data-filter-empty="<id>" shows when nothing is.
  on("input", "[data-filter]", (el) => {
    const box = document.getElementById(el.dataset.filter);
    if (!box) return;
    const text = el.value.trim().toLowerCase();
    let shown = 0;
    box.querySelectorAll("[data-search]").forEach((item) => {
      item.hidden = !item.dataset.search.toLowerCase().includes(text);
      if (!item.hidden) shown++;
    });
    box.querySelectorAll("[data-filter-group]").forEach((g) => (g.hidden = !g.querySelector("[data-search]:not([hidden])")));
    const none = document.querySelector('[data-filter-empty="' + el.dataset.filter + '"]');
    if (none) none.hidden = shown > 0;
  });

  // Sidebar on small screens: a drawer over the page, which is out of reach
  // behind it as it is behind a dialog. The stylesheet draws the backdrop,
  // which keeps the pointer from the page; inert keeps the keyboard from it.
  // The focus goes in with the drawer and comes back to the bar's button.
  const sidebar = () => document.getElementById("sidebar");
  const setNav = (open) => {
    const s = sidebar();
    if (!s || (s.dataset.open === "true") === open) return;
    const main = document.querySelector(".main");
    const inside = s.contains(document.activeElement);
    s.dataset.open = String(open);
    document.querySelectorAll("[data-nav-toggle][aria-expanded]").forEach((b) => b.setAttribute("aria-expanded", String(open)));
    main.inert = open;
    if (open) s.querySelector("a, button").focus();
    // A press on the backdrop has taken the focus from the drawer already.
    else if (inside || document.activeElement === document.body) main.querySelector("[data-nav-toggle]").focus();
  };
  on("click", "[data-nav-toggle]", () => setNav(sidebar()?.dataset.open !== "true"));
  // A click beside the drawer lands on its backdrop.
  document.addEventListener("click", (e) => {
    const s = sidebar();
    if (s?.dataset.open === "true" && !s.contains(e.target) && !e.target.closest("[data-nav-toggle]")) setNav(false);
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") setNav(false);
  });
  // On a wide window the sidebar is part of the page, and the page would
  // stay inert behind a drawer that is one no more.
  matchMedia("(min-width: 64rem)").addEventListener("change", () => setNav(false));

  // Typed confirmation: the form's submit button unlocks only when the field
  // holds exactly the expected text.
  on("input", "[data-match]", (el) => {
    const submit = el.form?.querySelector("[type=submit]");
    if (submit) submit.disabled = el.value.trim() !== el.dataset.match;
  });

  // Copy to clipboard. navigator.clipboard needs HTTPS, and a fresh install
  // is reached over plain HTTP, so fall back to a hidden textarea.
  const copy = async (text) => {
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.className = "sr-only";
    document.body.append(ta);
    ta.select();
    document.execCommand("copy");
    ta.remove();
  };
  on("click", "[data-copy]", async (el) => {
    await copy(el.dataset.copy);
    const label = el.querySelector("[data-copy-label]");
    if (!label || label.dataset.busy) return;
    const before = label.textContent;
    label.dataset.busy = "1";
    label.textContent = "Copied";
    setTimeout(() => {
      label.textContent = before;
      delete label.dataset.busy;
    }, 1500);
  });

  // data-fill="<input id>" with data-value puts the value into that input,
  // for pick lists such as the repository browser.
  on("click", "[data-fill]", (el) => {
    const input = document.getElementById(el.dataset.fill);
    if (!input) return;
    input.value = el.dataset.value;
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
  });

  // Select, Combobox, Picker and the trail's switchers (ui): a button and a
  // popover listing the options, with a hidden input for the value, a field
  // to fill or, in a switcher, options that are links. The
  // browser opens and closes the popover itself (the button's popovertarget,
  // Escape, a click elsewhere); what is left is to put it by its button, to
  // move through the options and to record the choice.
  //
  // Without a filter field, focus moves from option to option. With one it
  // stays in the field, so typing keeps narrowing the list, and the arrow
  // keys move a marker (data-active) the field reports as its current option.
  let openList = null;
  let optionIDs = 0;
  // A Menu (data-menu-actions) lists things to do, and none of them is the
  // chosen one: it opens with nothing marked and the focus on its button.
  // Opened from the keyboard, the focus goes in, to the end the key says:
  // entering is that list and key, from the key press until the list opens.
  const isActions = (list) => list.dataset.menuActions !== undefined;
  let entering = null;
  const enter = (list, key) => {
    const all = optionsOf(list).filter((o) => !o.hidden);
    (key === "ArrowUp" ? all[all.length - 1] : all[0])?.focus();
  };
  const shut = (list) => list?.isConnected && list.matches(":popover-open") && list.hidePopover();
  const optionsOf = (list) => [...list.querySelectorAll("[role=option]")];
  const place = (list, button) => {
    const r = button.getBoundingClientRect();
    const gap = 4;
    const edge = 8;
    // At least as wide as the button; the stylesheet reads this.
    list.style.setProperty("--select-width", r.width + "px");
    list.style.removeProperty("--select-room");
    const below = innerHeight - r.bottom;
    // Above the button only when it does not fit below and there is more room.
    const up = below < list.offsetHeight + gap + edge && r.top > below;
    list.style.setProperty("--select-room", (up ? r.top : below) - gap - edge + "px");
    // Held by the edge next to the button, so a list that gets shorter as it
    // is filtered stays against it.
    list.style.top = up ? "auto" : r.bottom + gap + "px";
    list.style.bottom = up ? innerHeight - r.top + gap + "px" : "auto";
    // By the button's left edge, or by its right one for a menu marked
    // data-menu-end: one whose button is at the end of a row, where a menu
    // hung from the left edge would be pushed back in by the window.
    const left = list.dataset.menuEnd === undefined ? r.left : r.right - list.offsetWidth;
    list.style.left = Math.max(edge, Math.min(left, innerWidth - list.offsetWidth - edge)) + "px";
  };
  const setActive = (list, option, reveal = true) => {
    const filter = list.querySelector("[data-select-filter], [data-search-field]");
    list.querySelectorAll("[data-active]").forEach((o) => delete o.dataset.active);
    if (!option) return filter.removeAttribute("aria-activedescendant");
    option.dataset.active = "1";
    option.id ||= "option-" + ++optionIDs;
    filter.setAttribute("aria-activedescendant", option.id);
    if (!reveal) return;
    // Scrolled by hand: scrollIntoView may move the page, which closes the list.
    const box = option.parentElement;
    if (option.offsetTop < box.scrollTop) box.scrollTop = option.offsetTop - 8;
    else if (option.offsetTop + option.offsetHeight > box.scrollTop + box.clientHeight) box.scrollTop = option.offsetTop + option.offsetHeight - box.clientHeight + 8;
  };
  // narrow shows the options that contain the filter's text and marks the one
  // Enter would choose: the selected one in an unfiltered list, else the first.
  const narrow = (list) => {
    const text = list.querySelector("[data-select-filter]").value.trim().toLowerCase();
    const all = optionsOf(list);
    // An option marked data-keep is not one of the things listed but what
    // else the menu offers; it stays whatever is typed.
    const keep = (o) => o.dataset.keep !== undefined;
    all.forEach((o) => (o.hidden = !keep(o) && !(o.dataset.search ?? o.textContent).toLowerCase().includes(text)));
    const shown = all.filter((o) => !o.hidden);
    const none = list.querySelector("[data-select-empty]");
    if (none) none.hidden = shown.some((o) => !keep(o)) || all.length === 0;
    setActive(list, (!text && shown.find((o) => o.getAttribute("aria-selected") === "true")) || shown[0]);
  };
  // toggle does not bubble, hence the capture.
  document.addEventListener(
    "toggle",
    (e) => {
      const list = e.target;
      const box = list instanceof Element ? list.closest("[data-select]") : null;
      if (!box) return;
      const button = box.querySelector("[popovertarget]");
      const open = e.newState === "open";
      button.setAttribute("aria-expanded", String(open));
      if (!open) {
        delete list.dataset.placed;
        if (openList === list) openList = null;
        if (entering?.list === list) entering = null;
        return;
      }
      openList = list;
      // A Picker asks the server for its options (hx-trigger) until it has some.
      if (!list.querySelector("[role=option]")) list.dispatchEvent(new Event("select-load"));
      const filter = list.querySelector("[data-select-filter]");
      if (filter) {
        filter.value = "";
        narrow(list);
      }
      place(list, button);
      list.dataset.placed = "1";
      if (!isActions(list)) (filter || list.querySelector("[aria-selected=true]") || list.querySelector("[role=option]"))?.focus();
      else if (entering?.list === list) enter(list, entering.key);
      entering = null;
    },
    true,
  );
  // Options that arrived while the list was open: apply what has been typed
  // meanwhile, and place the list again now that it has its real height.
  document.addEventListener("htmx:afterSettle", (e) => {
    const list = e.target.closest?.("[data-select] [popover]");
    if (!list || list !== openList) return;
    // Without a filter field nothing had the focus yet: there were no options.
    if (list.querySelector("[data-select-filter]")) narrow(list);
    else (list.querySelector("[aria-selected=true]") || list.querySelector("[role=option]"))?.focus();
    place(list, list.closest("[data-select]").querySelector("[popovertarget]"));
  });
  // The list is fixed to the window, so it would be left behind when the page
  // under it moves. Scrolling the list itself is not that.
  const shutUnlessInside = (e) => {
    if (openList && !(e.target instanceof Node && openList.contains(e.target))) shut(openList);
  };
  document.addEventListener("scroll", shutUnlessInside, true);
  window.addEventListener("resize", shutUnlessInside);
  // A press outside an open list closes it and is spent on that, as a press
  // on a dialog's backdrop is: what was under the pointer is not pressed as
  // well. The browser closes the list and would hand the press on; spent is
  // that list, from such a press to the click it ends in, and both are kept
  // from the page. The list's own button is not outside: the browser closes
  // the list on it, and a spent click would leave it open. The page is asked
  // which list is open, not openList: toggle, which sets that, comes a
  // moment after the list is open.
  //
  // A press that ends in no click (a drag, the other button, a scroll by
  // touch) leaves spent set. The next press decides anew, and a key or a
  // cancelled touch forgets it: a click the keyboard makes is its own.
  let spent = null;
  const forget = () => (spent = null);
  document.addEventListener(
    "pointerdown",
    (e) => {
      const list = document.querySelector("[data-select] :popover-open");
      const box = list?.closest("[data-select]");
      spent = box && e.target instanceof Node && !box.contains(e.target) ? list : null;
      // The focus that was in the list would be nowhere once it is gone.
      if (spent && list.contains(document.activeElement)) box.querySelector("[popovertarget]").focus();
    },
    true,
  );
  document.addEventListener("pointercancel", forget, true);
  document.addEventListener("keydown", forget, true);
  // mousedown as well: it would move the focus to what was pressed, and a
  // dialog reads it as a press on its backdrop.
  const spend = (e) => {
    if (!spent) return;
    e.preventDefault();
    e.stopPropagation();
    if (e.type !== "click") return;
    // Still open after a press that ended somewhere else than it began.
    shut(spent);
    spent = null;
  };
  document.addEventListener("mousedown", spend, true);
  document.addEventListener("click", spend, true);
  on("input", "[data-select-filter]", (el) => narrow(el.closest("[popover]")));
  on("mouseover", "[data-select] [role=option]", (el) => {
    const list = el.closest("[popover]");
    if (list.querySelector("[data-select-filter]")) setActive(list, el, false);
  });
  on("click", "[data-select] [role=option]", (el) => {
    const box = el.closest("[data-select]");
    const list = el.closest("[popover]");
    shut(list);
    box.querySelector("[popovertarget]").focus();
    // A Picker's option has filled its field already (data-fill), a
    // switcher's is a link the browser now follows: no value here to keep.
    // Only the box's own input says so: a form inside a menu has hidden
    // fields too.
    const input = box.querySelector(":scope > input[type=hidden]");
    if (!input) return;
    optionsOf(list).forEach((o) => o.setAttribute("aria-selected", String(o === el)));
    box.querySelector("[data-select-label]").textContent = el.textContent.trim();
    if (input.value === el.dataset.value) return;
    input.value = el.dataset.value;
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  // Enter and Space press the button as they press any, and the browser
  // opens the list. A click that no pointer made says so (detail 0).
  on("click", "[data-select] > [popovertarget]", (button, e) => {
    if (e.detail === 0) entering = { list: button.parentElement.querySelector("[popover]"), key: "ArrowDown" };
  });
  on("keydown", "[data-select]", (box, e) => {
    const list = box.querySelector("[popover]");
    if (e.target.closest("[popovertarget]")) {
      const open = list.matches(":popover-open");
      // Open with the focus still on the button is a Menu the pointer
      // opened. Tab leaves it and the arrows go into it.
      if (e.key === "Tab") shut(list);
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      e.preventDefault();
      if (open) return enter(list, e.key);
      entering = { list, key: e.key };
      list.showPopover();
      return;
    }
    if (e.key === "Tab") {
      // Closing gives focus back to the button, and Tab then leaves from there.
      shut(list);
      return;
    }
    const filtered = e.target.matches("[data-select-filter]");
    const all = optionsOf(list).filter((o) => !o.hidden);
    const current = filtered ? list.querySelector("[data-active]") : document.activeElement;
    const i = all.indexOf(current);
    let next;
    // Space chooses a focused link, as Enter does: on its own it would
    // scroll the page, which closes the list. An option that is a button
    // is pressed by Space already.
    if (e.key === " " && !filtered && current?.matches("a[role=option]")) {
      e.preventDefault();
      current.click();
      return;
    }
    if (e.key === "ArrowDown") next = all[(i + 1) % all.length];
    else if (e.key === "ArrowUp") next = all[(i - 1 + all.length) % all.length];
    else if (filtered) {
      // Enter here would send the form the list is in.
      if (e.key !== "Enter") return;
      e.preventDefault();
      current?.click();
      return;
    } else if (e.key === "Home") next = all[0];
    else if (e.key === "End") next = all[all.length - 1];
    else if (e.key.length === 1 && e.key !== " " && !e.ctrlKey && !e.metaKey && !e.altKey) {
      // A letter goes to the next option that starts with it.
      const key = e.key.toLowerCase();
      next = [...all.slice(i + 1), ...all.slice(0, i + 1)].find((o) => o.textContent.trim().toLowerCase().startsWith(key));
    }
    if (!next) return;
    e.preventDefault();
    if (filtered) setActive(list, next);
    else next.focus();
  });

  // Search (ui.searchDialog): the one dialog everything the team has is
  // looked for in. Its button in the bar opens it, and so do "/" outside a
  // field and Cmd+K (Ctrl+K off a Mac), which also closes it. The field asks
  // the server as it is typed in (hx-get) and the answer becomes the list.
  // Focus stays in the field and the arrow keys move the marker, as in a
  // menu with a filter; Enter opens the marked row, which is a link.
  const searchBox = () => document.getElementById("search");
  // shown is the text the list is the answer to, read from each answer's
  // own request: the next request can leave before an answer has settled.
  // While the field holds another text, the list is stale: an answer is on
  // its way. Enter pressed meanwhile is owed to that answer (wanted), or
  // typing fast and pressing Enter would open a row of the search before.
  let shown = null;
  let wanted = false;
  const stale = (dialog) => dialog.querySelector("[data-search-field]").value !== shown;
  // The marked row is the listbox's selected one; nothing else here is.
  const markRow = (dialog, row, reveal) => {
    optionsOf(dialog).forEach((o) => o.setAttribute("aria-selected", String(o === row)));
    setActive(dialog, row, reveal);
  };
  const askSearch = (dialog) => {
    wanted = false;
    dialog.querySelector("[data-search-error]").hidden = true;
    dialog.querySelector("[data-search-field]").dispatchEvent(new Event("search-load"));
  };
  // It opens as new every time: an empty field over the pages a person can
  // go to. That is how it starts, and how it is left when it closes, so
  // there is nothing to do unless something was typed, or the list is
  // empty: the first time, and after a search that got no answer.
  const freshSearch = (dialog) => {
    const field = dialog.querySelector("[data-search-field]");
    if (field.value === "" && shown === "" && dialog.querySelector("[role=option]")) return;
    field.value = "";
    askSearch(dialog);
  };
  const openSearch = () => {
    const dialog = searchBox();
    // Not over another dialog: what that one asks would be left behind it.
    if (!dialog || document.querySelector("dialog[open]")) return;
    // Nor over the open sidebar, which leaves first.
    setNav(false);
    dialog.showModal();
    freshSearch(dialog);
  };
  on("click", "[data-search-open]", openSearch);
  const mac = /Mac|iPhone|iPad/.test(navigator.platform);
  document.addEventListener("keydown", (e) => {
    // A key another script took is not this one's: the terminal sends
    // Ctrl+K to the shell. A browser filling in a form sends key events
    // that name no key.
    if (e.defaultPrevented || e.altKey || !e.key || !searchBox()) return;
    // On a Mac Ctrl+K deletes to the end of the line in every text field,
    // so there it is Cmd+K alone.
    const chord = e.key.toLowerCase() === "k" && !e.shiftKey && (mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey);
    const slash = e.key === "/" && !e.ctrlKey && !e.metaKey && !openList && !e.target.closest?.("input, textarea, select, [contenteditable]");
    if (!chord && !slash) return;
    e.preventDefault();
    if (chord && searchBox().open) searchBox().close();
    else openSearch();
  });
  on("input", "[data-search-field]", (field) => {
    wanted = false;
    field.closest("dialog").querySelector("[data-search-error]").hidden = true;
  });
  on("keydown", "[data-search-field]", (field, e) => {
    const dialog = field.closest("dialog");
    const all = optionsOf(dialog);
    const i = all.indexOf(dialog.querySelector("[data-active]"));
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!all.length) return;
      const step = e.key === "ArrowDown" ? 1 : -1;
      markRow(dialog, all[i < 0 ? (step > 0 ? 0 : all.length - 1) : (i + step + all.length) % all.length]);
    } else if (e.key === "Enter" && !e.isComposing && e.keyCode !== 229) {
      // Enter that ends a composed character is not Enter. Safari sends it
      // after the composition has ended, and only the key's code says so.
      e.preventDefault();
      if (stale(dialog)) wanted = true;
      else all[i]?.click();
    }
  });
  // The pointer marks a row when it moves, not when a new list arrives
  // under it: that would take the marker from the first row.
  on("mousemove", "#search [role=option]", (el) => {
    if (!el.dataset.active) markRow(searchBox(), el, false);
  });
  // A row that leads to a place on the page it is opened from (a server, on
  // the list of servers) loads nothing: the dialog has to go by itself.
  on("click", "#search [role=option]", () => searchBox().close());
  // An answer arrived (the count for the status line settles apart, and is
  // not it). The first row is the best one: it is marked, and opened if
  // Enter was waiting for it.
  document.addEventListener("htmx:afterSettle", (e) => {
    const list = e.target.closest?.("#search-results");
    if (!list) return;
    const dialog = searchBox();
    const first = list.querySelector("[role=option]");
    shown = new URL(e.detail.pathInfo.finalRequestPath, location.href).searchParams.get("q");
    list.scrollTop = 0;
    markRow(dialog, first);
    if (!wanted || stale(dialog)) return;
    wanted = false;
    first?.click();
  });
  // No answer came. Rows of the search before, under the new text, would be
  // a lie: the list is emptied and the dialog says why.
  const searchFailed = (e) => {
    const dialog = e.target.closest?.("#search");
    if (!dialog) return;
    wanted = false;
    shown = null;
    dialog.querySelector("[role=listbox]").replaceChildren();
    const note = dialog.querySelector("[data-search-error]");
    note.hidden = false;
    // Said as well as shown: the status line still held the last count.
    dialog.querySelector("[role=status]").textContent = note.textContent;
  };
  document.addEventListener("htmx:sendError", searchFailed);
  document.addEventListener("htmx:responseError", searchFailed);
  // When it closes, the pages are put back behind it, so the next opening
  // shows them at once. close does not bubble, and the browser sends it with
  // its next frame, which can be after the dialog was opened again: then
  // opening has done this already, and the field holds what is being typed.
  searchBox()?.addEventListener("close", (e) => {
    if (!e.target.open) freshSearch(e.target);
  });
  // A page the browser kept whole and brings back (Back) has it closed.
  window.addEventListener("pageshow", (e) => {
    if (e.persisted) searchBox()?.close();
  });

  // Toasts that confirm an action leave on their own; errors stay until closed.
  const armToasts = (root) =>
    root.querySelectorAll("[data-autodismiss]").forEach((t) => {
      if (t.dataset.armed) return;
      t.dataset.armed = "1";
      setTimeout(() => t.remove(), 5000);
    });
  on("click", "[data-dismiss]", (el) => el.closest("[role=status],[role=alert]")?.remove());
  armToasts(document);
  document.addEventListener("htmx:afterSettle", (e) => armToasts(e.target));

  // Log views follow new output unless the reader has scrolled up.
  // Old lines are dropped so a log left open for days stays small.
  const maxLogNodes = 4000;
  document.addEventListener("htmx:sseMessage", (e) => {
    const log = e.target.closest?.("[data-follow]");
    if (!log) return;
    while (log.childNodes.length > maxLogNodes) log.firstChild.remove();
    if (log.dataset.follow !== "paused") log.scrollTop = log.scrollHeight;
  });
  on("scroll", "[data-follow]", (el) => {
    el.dataset.follow = el.scrollHeight - el.scrollTop - el.clientHeight < 40 ? "on" : "paused";
  });
})();
