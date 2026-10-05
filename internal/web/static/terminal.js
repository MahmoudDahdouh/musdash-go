// musdash terminal: a small terminal emulator for the shell a page opens in
// a container. It is loaded on terminal pages only.
//
// It keeps a grid of cells, reads what the shell prints (text and the
// escape sequences programs use to move the cursor, erase, scroll and
// colour), and draws the grid as rows of text, so the browser's own
// selection and copy work. It claims to be xterm-256color and implements
// what shells and full-screen programs (vi, less, top, psql) use of that.
// What it leaves out: the mouse, images, and double-width characters,
// which are drawn but counted as one cell.
//
// The connection is a WebSocket: bytes typed one way, bytes printed the
// other, and a small JSON message for the first greeting and for a new
// size. Nothing here uses inline styles in markup or evaluates text, so
// the page's Content-Security-Policy stays as strict as everywhere else.
(() => {
  "use strict";

  const root = document.querySelector("[data-terminal]");
  if (!root) return;
  const panel = root.parentElement;
  const statusEl = panel.querySelector("[data-term-status]");
  const againEl = panel.querySelector("[data-term-reconnect]");

  // What is typed is caught by a text field nobody sees. A field is what
  // browsers deliver text to: plain keys, but also an on-screen keyboard,
  // a dead key, an input method that composes characters.
  const input = document.createElement("textarea");
  input.className = "sr-only";
  input.setAttribute("aria-label", "Terminal input. Keys you press are sent to the shell, Tab included.");
  input.setAttribute("autocapitalize", "off");
  input.setAttribute("autocomplete", "off");
  input.setAttribute("autocorrect", "off");
  input.spellcheck = false;
  root.before(input);
  const isMac = /Mac|iPhone|iPad/.test(navigator.platform || "");

  const SCROLLBACK = 2000;
  const BOLD = 1, DIM = 2, ITALIC = 4, UNDER = 8, INVERSE = 16, STRIKE = 32, HIDDEN = 64;
  const TRUE = 0x1000000; // a colour at or above this is 24-bit: TRUE + rgb

  // Attributes are shared objects, so two cells drawn alike compare equal.
  const attrs = new Map();
  const attr = (fg, bg, flags) => {
    const key = fg + "|" + bg + "|" + flags;
    let a = attrs.get(key);
    if (!a) attrs.set(key, (a = Object.freeze({ fg, bg, flags })));
    return a;
  };
  const PLAIN = attr(-1, -1, 0);

  // DEC special graphics: what programs draw boxes with.
  const GRAPHICS = {
    "`": "◆", a: "▒", f: "°", g: "±", j: "┘", k: "┐", l: "┌", m: "└", n: "┼", o: "⎺", p: "⎻",
    q: "─", r: "⎼", s: "⎽", t: "├", u: "┤", v: "┴", w: "┬", x: "│", y: "≤", z: "≥", "{": "π", "|": "≠", "}": "£", "~": "·",
  };

  // ---- State ----------------------------------------------------------

  let cols = 80, rows = 24;
  let lines = []; // the screen, top to bottom
  let scrollback = []; // lines that left the top of the main screen
  let saved = null; // the main screen while the alternate one is shown
  let x = 0, y = 0, wrapNext = false;
  let cur = PLAIN; // attributes new text gets
  let top = 0, bottom = 23; // the scroll region
  let savedCursor = null;
  let autowrap = true, originMode = false, insertMode = false, cursorOn = true;
  let appCursor = false, bracketedPaste = false;
  let charsets = ["B", "B"], shift = 0;
  let tabs = new Set();
  let focused = false;
  let follow = true; // keep the newest line in view

  const blank = (a) => attr(-1, a.bg, 0);
  const newLine = (a = PLAIN) => ({ ch: new Array(cols).fill(" "), at: new Array(cols).fill(a), el: null, dirty: true });
  const resetTabs = () => {
    tabs = new Set();
    for (let i = 8; i < cols; i += 8) tabs.add(i);
  };
  const drop = (line) => {
    if (line.el) line.el.remove();
    line.el = null;
  };

  function reset() {
    lines.forEach(drop);
    lines = [];
    for (let i = 0; i < rows; i++) lines.push(newLine());
    saved = null;
    x = y = 0;
    wrapNext = false;
    cur = PLAIN;
    top = 0;
    bottom = rows - 1;
    savedCursor = null;
    autowrap = true;
    originMode = insertMode = appCursor = bracketedPaste = false;
    cursorOn = true;
    charsets = ["B", "B"];
    shift = 0;
    resetTabs();
  }

  // ---- The grid -------------------------------------------------------

  const touch = (i) => (lines[i].dirty = true);

  function scrollUp(n) {
    for (; n > 0; n--) {
      const gone = lines.splice(top, 1)[0];
      if (top === 0 && !saved) {
        scrollback.push(gone);
        if (scrollback.length > SCROLLBACK) drop(scrollback.shift());
      } else {
        drop(gone);
      }
      lines.splice(bottom, 0, newLine(blank(cur)));
    }
  }
  function scrollDown(n) {
    for (; n > 0; n--) {
      drop(lines.splice(bottom, 1)[0]);
      lines.splice(top, 0, newLine(blank(cur)));
    }
  }
  function lineFeed() {
    if (y === bottom) scrollUp(1);
    else if (y < rows - 1) y++;
  }
  function reverseIndex() {
    if (y === top) scrollDown(1);
    else if (y > 0) y--;
  }
  function put(ch) {
    if (wrapNext && autowrap) {
      x = 0;
      lineFeed();
    }
    wrapNext = false;
    const line = lines[y];
    if (insertMode) {
      line.ch.splice(x, 0, " ");
      line.at.splice(x, 0, cur);
      line.ch.length = line.at.length = cols;
    }
    line.ch[x] = ch;
    line.at[x] = cur;
    line.dirty = true;
    if (x >= cols - 1) wrapNext = true;
    else x++;
  }
  function eraseCells(line, from, to) {
    const a = blank(cur);
    for (let i = Math.max(0, from); i < Math.min(cols, to); i++) {
      line.ch[i] = " ";
      line.at[i] = a;
    }
    line.dirty = true;
  }
  function moveTo(nx, ny) {
    const lo = originMode ? top : 0, hi = originMode ? bottom : rows - 1;
    x = Math.min(cols - 1, Math.max(0, nx));
    y = Math.min(hi, Math.max(lo, ny));
    wrapNext = false;
  }

  function saveCursor() {
    savedCursor = { x, y, cur, originMode, charsets: charsets.slice(), shift };
  }
  function restoreCursor() {
    if (!savedCursor) return moveTo(0, 0);
    ({ cur, originMode, shift } = savedCursor);
    charsets = savedCursor.charsets.slice();
    x = Math.min(cols - 1, savedCursor.x);
    y = Math.min(rows - 1, savedCursor.y);
    wrapNext = false;
  }

  // The alternate screen is what full-screen programs draw on, so that the
  // shell's own lines are back when they end.
  function alternate(on) {
    if (on === !!saved) return;
    lines.forEach(drop);
    if (on) {
      saved = { lines, x, y };
      lines = [];
      for (let i = 0; i < rows; i++) lines.push(newLine());
    } else {
      lines = saved.lines;
      x = Math.min(cols - 1, saved.x);
      y = Math.min(rows - 1, saved.y);
      saved = null;
      lines.forEach((l) => (l.dirty = true));
    }
    top = 0;
    bottom = rows - 1;
    wrapNext = false;
  }

  // ---- Escape sequences -----------------------------------------------

  let state = 0; // 0 text, 1 after ESC, 2 CSI, 3 OSC, 4 another string, 5 a charset, 6 skip one
  let params = "", inter = "", which = 0, strEsc = false;

  function control(code) {
    switch (code) {
      case 8: // backspace
        if (x > 0) x--;
        wrapNext = false;
        break;
      case 9: { // tab
        let nx = x + 1;
        while (nx < cols - 1 && !tabs.has(nx)) nx++;
        x = Math.min(cols - 1, nx);
        break;
      }
      case 10: case 11: case 12:
        lineFeed();
        break;
      case 13:
        x = 0;
        wrapNext = false;
        break;
      case 14:
        shift = 1;
        break;
      case 15:
        shift = 0;
        break;
    }
  }

  function setMode(n, privateMode, on) {
    if (!privateMode) {
      if (n === 4) insertMode = on;
      return;
    }
    switch (n) {
      case 1: appCursor = on; break;
      case 6: originMode = on; moveTo(0, on ? top : 0); break;
      case 7: autowrap = on; break;
      case 25: cursorOn = on; break;
      case 47: case 1047: alternate(on); break;
      case 1048: on ? saveCursor() : restoreCursor(); break;
      case 1049:
        if (on) {
          saveCursor();
          alternate(true);
        } else {
          alternate(false);
          restoreCursor();
        }
        break;
      case 2004: bracketedPaste = on; break;
    }
  }

  function sgr(list) {
    let { fg, bg, flags } = cur;
    for (let i = 0; i < list.length; i++) {
      const n = list[i] || 0;
      if (n === 0) (fg = -1), (bg = -1), (flags = 0);
      else if (n === 1) flags |= BOLD;
      else if (n === 2) flags |= DIM;
      else if (n === 3) flags |= ITALIC;
      else if (n === 4) flags |= UNDER;
      else if (n === 7) flags |= INVERSE;
      else if (n === 8) flags |= HIDDEN;
      else if (n === 9) flags |= STRIKE;
      else if (n === 21 || n === 22) flags &= ~(BOLD | DIM);
      else if (n === 23) flags &= ~ITALIC;
      else if (n === 24) flags &= ~UNDER;
      else if (n === 27) flags &= ~INVERSE;
      else if (n === 28) flags &= ~HIDDEN;
      else if (n === 29) flags &= ~STRIKE;
      else if (n >= 30 && n <= 37) fg = n - 30;
      else if (n >= 40 && n <= 47) bg = n - 40;
      else if (n >= 90 && n <= 97) fg = n - 90 + 8;
      else if (n >= 100 && n <= 107) bg = n - 100 + 8;
      else if (n === 39) fg = -1;
      else if (n === 49) bg = -1;
      else if (n === 38 || n === 48) {
        let colour = -1;
        if (list[i + 1] === 5) {
          colour = Math.min(255, Math.max(0, list[i + 2] || 0));
          i += 2;
        } else if (list[i + 1] === 2) {
          const [r, g, b] = [list[i + 2], list[i + 3], list[i + 4]].map((v) => Math.min(255, Math.max(0, v || 0)));
          colour = TRUE + (r << 16) + (g << 8) + b;
          i += 4;
        }
        if (n === 38) fg = colour;
        else bg = colour;
      }
    }
    cur = attr(fg, bg, flags);
  }

  function csi(final) {
    const priv = params[0] === "?" || params[0] === ">" || params[0] === "<" || params[0] === "=" ? params[0] : "";
    const list = (priv ? params.slice(1) : params).split(/[;:]/).map((p) => (p === "" ? 0 : parseInt(p, 10) || 0));
    const p0 = list[0] || 0, n = Math.max(1, p0);
    if (inter === "!" && final === "p") return reset();
    if (inter) return; // sequences with other intermediates set things this terminal does not have
    const line = lines[y];
    switch (final) {
      case "A": moveTo(x, Math.max(y - n, y >= top ? top : 0)); break;
      case "B": case "e": moveTo(x, Math.min(y + n, y <= bottom ? bottom : rows - 1)); break;
      case "C": case "a": moveTo(x + n, y); break;
      case "D": moveTo(x - n, y); break;
      case "E": moveTo(0, y + n); break;
      case "F": moveTo(0, y - n); break;
      case "G": case "`": moveTo(n - 1, y); break;
      case "d": moveTo(x, n - 1 + (originMode ? top : 0)); break;
      case "H": case "f": moveTo(Math.max(1, list[1] || 0) - 1, n - 1 + (originMode ? top : 0)); break;
      case "J":
        if (p0 === 0) {
          eraseCells(line, x, cols);
          for (let i = y + 1; i < rows; i++) eraseCells(lines[i], 0, cols);
        } else if (p0 === 1) {
          eraseCells(line, 0, x + 1);
          for (let i = 0; i < y; i++) eraseCells(lines[i], 0, cols);
        } else if (p0 === 2) {
          for (let i = 0; i < rows; i++) eraseCells(lines[i], 0, cols);
        } else if (p0 === 3) {
          scrollback.forEach(drop);
          scrollback = [];
        }
        break;
      case "K":
        if (p0 === 0) eraseCells(line, x, cols);
        else if (p0 === 1) eraseCells(line, 0, x + 1);
        else if (p0 === 2) eraseCells(line, 0, cols);
        break;
      case "L":
        if (y >= top && y <= bottom) {
          for (let i = Math.min(n, bottom - y + 1); i > 0; i--) {
            drop(lines.splice(bottom, 1)[0]);
            lines.splice(y, 0, newLine(blank(cur)));
          }
          x = 0;
        }
        break;
      case "M":
        if (y >= top && y <= bottom) {
          for (let i = Math.min(n, bottom - y + 1); i > 0; i--) {
            drop(lines.splice(y, 1)[0]);
            lines.splice(bottom, 0, newLine(blank(cur)));
          }
          x = 0;
        }
        break;
      case "P": {
        const k = Math.min(n, cols - x);
        line.ch.splice(x, k);
        line.at.splice(x, k);
        for (let i = 0; i < k; i++) {
          line.ch.push(" ");
          line.at.push(blank(cur));
        }
        line.dirty = true;
        break;
      }
      case "@": {
        const k = Math.min(n, cols - x);
        for (let i = 0; i < k; i++) {
          line.ch.splice(x, 0, " ");
          line.at.splice(x, 0, blank(cur));
        }
        line.ch.length = line.at.length = cols;
        line.dirty = true;
        break;
      }
      case "X": eraseCells(line, x, x + n); break;
      case "S": if (!priv) scrollUp(Math.min(n, rows)); break;
      case "T": if (!priv) scrollDown(Math.min(n, rows)); break;
      case "b": { // repeat the character before the cursor
        const before = x > 0 ? line.ch[wrapNext ? x : x - 1] : " ";
        for (let i = Math.min(n, cols * rows); i > 0; i--) put(before);
        break;
      }
      case "m": if (!priv) sgr(list); break;
      case "r": {
        const t = Math.max(1, p0) - 1, b = (list[1] || rows) - 1;
        if (!priv && t < b && b < rows) {
          top = t;
          bottom = b;
          moveTo(0, originMode ? top : 0);
        }
        break;
      }
      case "h": case "l": list.forEach((m) => setMode(m, priv === "?", final === "h")); break;
      case "s": if (!priv) saveCursor(); break;
      case "u": if (!priv) restoreCursor(); break;
      case "g":
        if (p0 === 0) tabs.delete(x);
        else if (p0 === 3) tabs.clear();
        break;
      case "n": // a program asking where the cursor is
        if (p0 === 6) send("\x1b[" + (y + 1 - (originMode ? top : 0)) + ";" + (x + 1) + "R");
        else if (p0 === 5) send("\x1b[0n");
        break;
      case "c": // and what kind of terminal this is
        if (priv === ">") send("\x1b[>0;10;1c");
        else if (!priv && p0 === 0) send("\x1b[?1;2c");
        break;
    }
  }

  function escape(c) {
    state = 0;
    switch (c) {
      case "[": state = 2; params = inter = ""; break;
      case "]": state = 3; strEsc = false; break;
      case "P": case "X": case "^": case "_": state = 4; strEsc = false; break;
      case "(": state = 5; which = 0; break;
      case ")": state = 5; which = 1; break;
      case "*": case "+": case "-": case ".": case "/": state = 5; which = 2; break;
      case "#": case " ": case "%": state = 6; break;
      case "7": saveCursor(); break;
      case "8": restoreCursor(); break;
      case "D": lineFeed(); break;
      case "E": x = 0; lineFeed(); break;
      case "M": reverseIndex(); break;
      case "H": tabs.add(x); break;
      case "c": reset(); scrollback.forEach(drop); scrollback = []; break;
    }
  }

  // feed takes what the shell printed.
  function feed(text) {
    for (const c of text) {
      const code = c.codePointAt(0);
      if (state === 3 || state === 4) {
        // A title or another string this terminal has no use for: read to
        // its end (BEL, or ESC \).
        if (code === 7 || (strEsc && c === "\\")) state = 0;
        else if (code === 0x18 || code === 0x1a) state = 0;
        strEsc = code === 0x1b;
        continue;
      }
      if (code === 0x1b) {
        state = 1;
        continue;
      }
      if (code < 0x20) {
        // Control characters act wherever they turn up, inside a sequence too.
        control(code);
        continue;
      }
      if (code === 0x7f) continue;
      switch (state) {
        case 1:
          escape(c);
          break;
        case 2:
          if (code >= 0x30 && code <= 0x3f) {
            if (params.length < 64) params += c;
          } else if (code >= 0x20 && code <= 0x2f) {
            inter += c;
          } else {
            state = 0;
            csi(c);
          }
          break;
        case 5:
          if (which < 2) charsets[which] = c;
          state = 0;
          break;
        case 6:
          state = 0;
          break;
        default:
          if (code >= 0x300 && /\p{M}/u.test(c)) {
            // A combining mark joins the character before it.
            const at = wrapNext ? x : x - 1;
            if (at >= 0) {
              lines[y].ch[at] += c;
              lines[y].dirty = true;
            }
          } else {
            put(charsets[shift] === "0" && GRAPHICS[c] ? GRAPHICS[c] : c);
          }
      }
    }
    schedule();
  }

  // ---- Drawing --------------------------------------------------------

  // The 256-colour palette beyond the sixteen the stylesheet names.
  function palette(n) {
    if (n >= 232) {
      const v = 8 + (n - 232) * 10;
      return "rgb(" + v + "," + v + "," + v + ")";
    }
    n -= 16;
    const level = (v) => (v === 0 ? 0 : 55 + v * 40);
    return "rgb(" + level(Math.floor(n / 36)) + "," + level(Math.floor(n / 6) % 6) + "," + level(n % 6) + ")";
  }
  // paint gives an element one colour: a class for the sixteen named ones,
  // a computed colour for the rest.
  function paint(el, colour, prop, cls) {
    if (colour < 0) return;
    if (colour < 16) el.classList.add(cls + colour);
    else if (colour >= TRUE) el.style[prop] = "#" + (colour - TRUE).toString(16).padStart(6, "0");
    else el.style[prop] = palette(colour);
  }
  function span(a, text, isCursor) {
    const el = document.createElement("span");
    let { fg, bg } = a;
    const flags = a.flags;
    if (flags & INVERSE) {
      // Swapped; a default colour swaps with the terminal's own.
      if (fg < 0 && bg < 0) el.classList.add("term-inv");
      else {
        if (bg < 0) el.classList.add("term-fg-bg");
        if (fg < 0) el.classList.add("term-bg-fg");
        [fg, bg] = [bg, fg];
      }
    }
    paint(el, fg, "color", "tf");
    paint(el, bg, "backgroundColor", "tb");
    if (flags & BOLD) el.classList.add("term-b");
    if (flags & DIM) el.classList.add("term-dim");
    if (flags & ITALIC) el.classList.add("term-i");
    if (flags & UNDER) el.classList.add("term-u");
    if (flags & STRIKE) el.classList.add("term-s");
    if (flags & HIDDEN) el.classList.add("term-hidden");
    if (isCursor) el.classList.add("term-cursor");
    el.textContent = text;
    return el;
  }

  function draw(line, cursorAt) {
    const el = line.el;
    el.textContent = "";
    // Cells at the end that are plain blanks need no node.
    let end = cols;
    while (end > 0 && line.ch[end - 1] === " " && line.at[end - 1] === PLAIN) end--;
    if (cursorAt >= 0) end = Math.max(end, cursorAt + 1);
    let i = 0;
    while (i < end) {
      if (i === cursorAt) {
        el.append(span(line.at[i], line.ch[i], true));
        i++;
        continue;
      }
      const a = line.at[i];
      let j = i, text = "";
      while (j < end && line.at[j] === a && j !== cursorAt) text += line.ch[j++];
      el.append(a === PLAIN ? document.createTextNode(text) : span(a, text, false));
      i = j;
    }
    line.dirty = false;
  }

  let drawn = { y: -1, x: -1, on: false, focused: false, line: null };
  let queued = false;
  function schedule() {
    if (queued) return;
    queued = true;
    requestAnimationFrame(render);
  }
  function render() {
    queued = false;
    const showCursor = cursorOn;
    if (drawn.line && (drawn.line !== lines[y] || drawn.x !== x || drawn.on !== showCursor || drawn.focused !== focused)) drawn.line.dirty = true;
    if (showCursor && (drawn.line !== lines[y] || drawn.x !== x || drawn.focused !== focused || !drawn.on)) lines[y].dirty = true;
    drawn = { y, x, on: showCursor, focused, line: showCursor ? lines[y] : null };

    // Rows are made from the bottom up, each put in front of the one after
    // it, so the page's order is the grid's whatever was scrolled or cut.
    let after = null;
    const place = (line, cursorAt) => {
      if (!line.el) {
        line.el = document.createElement("div");
        line.el.className = "term-row";
        root.insertBefore(line.el, after);
        line.dirty = true;
      }
      if (line.dirty) draw(line, cursorAt);
      after = line.el;
    };
    for (let i = rows - 1; i >= 0; i--) place(lines[i], showCursor && i === y ? x : -1);
    for (let i = scrollback.length - 1; i >= 0; i--) place(scrollback[i], -1);
    if (follow) root.scrollTop = root.scrollHeight;
  }

  // ---- Size -----------------------------------------------------------

  function measure() {
    const probe = document.createElement("span");
    probe.className = "term-probe";
    probe.textContent = "W".repeat(20);
    root.append(probe);
    const box = probe.getBoundingClientRect();
    probe.remove();
    const style = getComputedStyle(root);
    const w = root.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
    const h = root.clientHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom);
    if (!box.width || !box.height) return null;
    return { cols: Math.max(20, Math.min(500, Math.floor(w / (box.width / 20)))), rows: Math.max(5, Math.min(200, Math.floor(h / box.height))) };
  }

  function resize(nc, nr) {
    if (nc === cols && nr === rows) return false;
    const fit = (line) => {
      while (line.ch.length < nc) {
        line.ch.push(" ");
        line.at.push(PLAIN);
      }
      line.ch.length = line.at.length = nc;
      line.dirty = true;
    };
    const each = (list) => {
      list.forEach(fit);
      // Fewer rows: the ones above the cursor go first, into the past;
      // more rows: blank ones are added below.
      while (list.length > nr) {
        if (list === lines && y < list.length - 1) drop(list.pop());
        else {
          const gone = list.shift();
          if (list === lines && !saved) scrollback.push(gone);
          else drop(gone);
          if (list === lines) y = Math.max(0, y - 1);
        }
      }
      while (list.length < nr) {
        const line = { ch: new Array(nc).fill(" "), at: new Array(nc).fill(PLAIN), el: null, dirty: true };
        list.push(line);
      }
    };
    scrollback.forEach(fit);
    each(lines);
    if (saved) {
      each(saved.lines);
      saved.y = Math.min(nr - 1, saved.y);
      saved.x = Math.min(nc - 1, saved.x);
    }
    cols = nc;
    rows = nr;
    top = 0;
    bottom = rows - 1;
    x = Math.min(cols - 1, x);
    y = Math.min(rows - 1, y);
    wrapNext = false;
    resetTabs();
    schedule();
    return true;
  }

  // ---- The connection --------------------------------------------------

  let ws = null, ended = "";
  const encoder = new TextEncoder();
  let decoder = new TextDecoder("utf-8");

  const status = (text) => {
    statusEl.textContent = text;
  };
  function send(text) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(text));
  }
  function sendSize() {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ cols, rows }));
  }

  function connect() {
    againEl.hidden = true;
    ended = "";
    status("Connecting…");
    scrollback.forEach(drop);
    scrollback = [];
    const size = measure();
    if (size) {
      cols = size.cols;
      rows = size.rows;
    }
    reset();
    decoder = new TextDecoder("utf-8");
    schedule();
    const sock = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + root.dataset.terminal);
    sock.binaryType = "arraybuffer";
    ws = sock;
    sock.onopen = () => {
      sock.send(JSON.stringify({ csrf: root.dataset.csrf, cols, rows }));
      status("Connected.");
      grab();
    };
    sock.onmessage = (e) => {
      if (typeof e.data !== "string") return feed(decoder.decode(e.data, { stream: true }));
      try {
        const note = JSON.parse(e.data);
        ended = String(note.exit || note.error || "");
      } catch (_) {
        // Not a note this page knows.
      }
    };
    sock.onclose = () => {
      if (ws !== sock) return;
      ws = null;
      status(ended || "The connection was closed.");
      againEl.hidden = false;
      schedule();
    };
  }

  // ---- Keys -----------------------------------------------------------

  const NAMED = {
    Enter: "\r", Backspace: "\x7f", Tab: "\t", Escape: "\x1b",
    Insert: "\x1b[2~", Delete: "\x1b[3~", PageUp: "\x1b[5~", PageDown: "\x1b[6~",
    F1: "\x1bOP", F2: "\x1bOQ", F3: "\x1bOR", F4: "\x1bOS", F5: "\x1b[15~", F6: "\x1b[17~",
    F7: "\x1b[18~", F8: "\x1b[19~", F9: "\x1b[20~", F10: "\x1b[21~", F11: "\x1b[23~", F12: "\x1b[24~",
  };
  const ARROWS = { ArrowUp: "A", ArrowDown: "B", ArrowRight: "C", ArrowLeft: "D", Home: "H", End: "F" };

  // keyText is what a key press sends, or null to leave it to the browser.
  function keyText(e) {
    // The browser's own copy and paste: ⌘ on a Mac, Ctrl+Shift elsewhere.
    if (e.metaKey) return null;
    if (e.ctrlKey && e.shiftKey && (e.key === "C" || e.key === "V" || e.key === "c" || e.key === "v")) return null;
    if (e.shiftKey && (e.key === "PageUp" || e.key === "PageDown")) {
      follow = false;
      root.scrollTop += (e.key === "PageUp" ? -1 : 1) * root.clientHeight * 0.9;
      return "";
    }
    if (ARROWS[e.key]) return (appCursor ? "\x1bO" : "\x1b[") + ARROWS[e.key];
    if (e.key === "Tab" && e.shiftKey) return "\x1b[Z";
    if (NAMED[e.key]) return (e.altKey && e.key !== "Escape" ? "\x1b" : "") + NAMED[e.key];
    if (e.key.length !== 1) return null;
    if (e.ctrlKey) {
      const k = e.key.toLowerCase();
      if (k >= "a" && k <= "z") return String.fromCharCode(k.charCodeAt(0) - 96);
      const other = { " ": "\x00", "@": "\x00", "[": "\x1b", "\\": "\x1c", "]": "\x1d", "^": "\x1e", _: "\x1f", "?": "\x7f", "2": "\x00", "6": "\x1e", "-": "\x1f", "/": "\x1f" }[e.key];
      return other === undefined ? null : other;
    }
    // Alt with a letter is "meta" to a shell. On a Mac that key types
    // characters instead, and is left to do so.
    if (e.altKey && !isMac) return "\x1b" + e.key;
    // Plain text arrives through the field.
    return null;
  }

  input.addEventListener("keydown", (e) => {
    if (e.isComposing || e.keyCode === 229) return;
    const text = keyText(e);
    if (text === null) return;
    e.preventDefault();
    if (text === "") return;
    follow = true;
    send(text);
  });
  // Text: typed, composed, or put in by an on-screen keyboard.
  input.addEventListener("input", (e) => {
    if (e.isComposing) return;
    const text = input.value;
    input.value = "";
    if (!text) return;
    follow = true;
    send(text.replace(/\r?\n/g, "\r"));
  });
  input.addEventListener("compositionend", () => {
    const text = input.value;
    input.value = "";
    if (text) send(text);
  });
  input.addEventListener("paste", (e) => {
    e.preventDefault();
    let text = (e.clipboardData || window.clipboardData).getData("text");
    if (!text) return;
    // A terminal's lines end with a carriage return. In a paste the shell
    // is told about, the marker that ends it must not come from the text.
    text = text.replace(/\r?\n/g, "\r");
    if (bracketedPaste) text = "\x1b[200~" + text.replace(/\x1b\[20[01]~/g, "") + "\x1b[201~";
    follow = true;
    send(text);
  });
  input.addEventListener("focus", () => {
    focused = true;
    root.classList.add("term-focus");
    schedule();
  });
  input.addEventListener("blur", () => {
    focused = false;
    root.classList.remove("term-focus");
    schedule();
  });
  // A click in the terminal means "type here", unless it ended a selection:
  // moving the focus would drop what was just selected to be copied.
  const grab = () => input.focus({ preventScroll: true });
  root.addEventListener("mouseup", () => {
    if (String(getSelection()) === "") grab();
  });
  root.addEventListener("scroll", () => {
    follow = root.scrollHeight - root.scrollTop - root.clientHeight < 8;
  });
  againEl.addEventListener("click", connect);

  let resizing = 0;
  new ResizeObserver(() => {
    clearTimeout(resizing);
    resizing = setTimeout(() => {
      const size = measure();
      if (size && resize(size.cols, size.rows)) sendSize();
    }, 120);
  }).observe(root);

  connect();
})();
