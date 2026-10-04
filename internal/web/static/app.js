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
    if (dialog && !dialog.open) dialog.showModal();
  });
  on("click", "[data-close]", (el) => el.closest("dialog")?.close());
  // A click on the backdrop lands on the <dialog> element itself.
  document.addEventListener("mousedown", (e) => {
    if (e.target instanceof HTMLDialogElement && e.target.open) e.target.close();
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
