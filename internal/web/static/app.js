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
    if (dialog instanceof HTMLDialogElement && !dialog.open) dialog.showModal();
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

  // Sidebar on small screens.
  const sidebar = () => document.getElementById("sidebar");
  const setNav = (open) => {
    const s = sidebar();
    if (!s) return;
    s.dataset.open = String(open);
    document.querySelectorAll("[data-nav-toggle]").forEach((b) => b.setAttribute("aria-expanded", String(open)));
  };
  on("click", "[data-nav-toggle]", () => setNav(sidebar()?.dataset.open !== "true"));
  document.addEventListener("click", (e) => {
    const s = sidebar();
    if (s?.dataset.open === "true" && !s.contains(e.target) && !e.target.closest("[data-nav-toggle]")) setNav(false);
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") setNav(false);
  });

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
    list.style.left = Math.max(edge, Math.min(r.left, innerWidth - list.offsetWidth - edge)) + "px";
  };
  const setActive = (list, option, reveal = true) => {
    const filter = list.querySelector("[data-select-filter]");
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
      (filter || list.querySelector("[aria-selected=true]") || list.querySelector("[role=option]"))?.focus();
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
    const input = box.querySelector("input[type=hidden]");
    if (!input) return;
    optionsOf(list).forEach((o) => o.setAttribute("aria-selected", String(o === el)));
    box.querySelector("[data-select-label]").textContent = el.textContent.trim();
    if (input.value === el.dataset.value) return;
    input.value = el.dataset.value;
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  on("keydown", "[data-select]", (box, e) => {
    const list = box.querySelector("[popover]");
    if (e.target.closest("[popovertarget]")) {
      // Enter and Space open it as they press any button; so do the arrows.
      if ((e.key === "ArrowDown" || e.key === "ArrowUp") && !list.matches(":popover-open")) {
        e.preventDefault();
        list.showPopover();
      }
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
