package main

// dropdown.go — the app's own drop-down list, in place of the native one of
// a <select> (the list a <select> opens is drawn by Windows in
// WebView2, a light grey frame and a grey hover that no CSS reaches).
//
// A <select> is ENHANCED, not replaced: it stays in the page, hidden, the
// one source of truth (its value, its options, its 'change' event); a button
// (the trigger) is put right after it and opens a floating list drawn by the
// page (the look of the terminal theme menu, term_themes.go .term-theme-pop:
// a lighter, translucent panel with a blur, the hover and the choice in
// variants of colour, no border). So the code that builds a <select>, reads
// sel.value, sets it, fills its options or listens to 'change' keeps working
// without a change:
//   - a choice sets the select's value, then fires 'input' and 'change' on
//     it (bubbling), as the native list does;
//   - sel.value = x / sel.selectedIndex = n redraw the trigger (both are
//     wrapped on the element itself, the native accessors underneath);
//   - its options, its text, its disabled / hidden / style / class / title
//     are watched (a MutationObserver per select): the trigger follows (a
//     select hidden by its style hides its trigger);
//   - sel.focus() focuses the trigger.
// Every <select> of the page is enhanced as it enters the document (one
// MutationObserver on the document: main window, detached windows, dialogs,
// pages, panels; those built before the script runs by oddEnhanceAll), but:
// a list box (multiple, size > 1) and a select marked data-native.
//
// The list: role=listbox, its rows role=option + aria-selected; the trigger
// role=combobox (aria-expanded, aria-controls, aria-activedescendant), the
// focus stays on it while the list is open. Keyboard: Enter / Space /
// Alt+Down / arrows / F4 open; in the list, arrows, Home / End, Page Up /
// Down move, Enter / Space choose, Escape / Tab close (Escape never reaches
// the dialog under it), typing goes to the option that starts with it. A
// press outside, a scroll, a resize or the window losing the focus close
// it. Placed under the trigger, above when there is more room there, kept
// inside the window.
//
// An option may carry a theme's preview (data-swatch="page,surface,text,
// accent", four #rrggbb colours, Settings > Application > Appearance): a
// small page with a card on it, the text and the accent as two dots on the
// card, before its text in the list and on the trigger. An option's
// data-hint is a short muted note after its text, in the list only (the
// session toolbar's codec selector, reach_codec.go).
//
// Both consts are injected verbatim (ui.go buildUI, not through Sprintf:
// '%' is safe); no backtick here. The JS sits between the "odd:begin" and
// "odd:end" markers (the JS tests take it from there).

const dropdownCSS = `
/* ── Drop-down lists (dropdown.go) ── */
select.odd-native{display:none!important}
.odd-trigger{display:inline-flex;align-items:center;gap:8px;max-width:100%;min-width:0;margin:0;padding:6px 9px;border:none;outline:none;border-radius:8px;background-color:var(--input-bg);color:var(--text);font-size:12px;line-height:1.3;text-align:start;cursor:pointer;-webkit-appearance:none;appearance:none;transition:background-color .12s,box-shadow .12s}
.odd-trigger:hover,.odd-trigger.open{background-image:linear-gradient(rgb(var(--ink) / .06),rgb(var(--ink) / .06))}
.odd-trigger:focus-visible{box-shadow:0 0 0 3px rgb(var(--accent-rgb) / .3)}
.odd-trigger:disabled{opacity:.5;cursor:default;background-image:none}
.odd-face{display:grid;flex:1 1 auto;min-width:0}
.odd-face>*{grid-area:1/1;min-width:0}
.odd-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.odd-label.ph{color:var(--muted)}
.odd-sizer{display:grid;height:0;overflow:hidden;visibility:hidden}
.odd-sizer>span{grid-area:1/1;white-space:nowrap}
.odd-chev{display:flex;flex-shrink:0;color:var(--muted);transition:transform .15s}
.odd-trigger.open .odd-chev{transform:rotate(180deg)}
/* The places that styled their select by its tag: the trigger the same. */
.cxn-field .odd-trigger,.scr-field .odd-trigger,.vault-field .odd-trigger,.dep-field .odd-trigger{display:flex;width:100%;padding:8px 11px;border-radius:10px;font-size:12.5px}
.filter-bar .odd-trigger{padding:7px 11px;border-radius:10px;font-size:12px;min-width:140px}
.cimp-table .odd-trigger{padding:4px 6px;font-size:11.5px}
.dep-variant-row .odd-trigger{font-size:11px}
.vpage-toolbar .odd-trigger{padding:7px 10px;font-size:11.5px}
.vm-type-row .odd-trigger{flex:1;min-width:0}
/* The list: a lighter, translucent panel with a blur (the terminal theme
   menu's look), the hover and the choice in variants of colour. */
.odd-pop{position:fixed;z-index:1000;min-width:120px;max-height:320px;overflow:auto;margin:0;padding:5px;border:none;outline:none;border-radius:12px;background:var(--bg3);background:color-mix(in srgb,var(--bg3) 86%,transparent);-webkit-backdrop-filter:blur(16px) saturate(1.2);backdrop-filter:blur(16px) saturate(1.2);box-shadow:0 10px 32px var(--shadow);color:var(--text);font-size:12px;line-height:1.3;overscroll-behavior:contain}
.odd-opt{display:flex;align-items:center;gap:8px;padding:6px 10px 6px 8px;border-radius:8px;cursor:pointer;white-space:nowrap;user-select:none}
.odd-opt .odd-chk{width:12px;flex-shrink:0;color:var(--accent-l);visibility:hidden}
.odd-opt .odd-txt{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis}
.odd-opt.hl{background:color-mix(in srgb,var(--text) 9%,transparent)}
.odd-opt[aria-selected=true]{background:color-mix(in srgb,var(--accent) 14%,transparent)}
.odd-opt[aria-selected=true].hl{background:color-mix(in srgb,var(--accent) 22%,transparent)}
.odd-opt[aria-selected=true] .odd-chk{visibility:visible}
.odd-opt .odd-hint{flex-shrink:0;margin-inline-start:14px;color:var(--muted);font-size:.92em}
.odd-opt[aria-disabled=true]{opacity:.45;cursor:default}
.odd-opt[aria-disabled=true].hl{background:none}
.odd-opt.in-group{padding-inline-start:18px}
/* A theme's preview (data-swatch): the page, a card on it, the text and the
   accent as two dots; depth by a soft shadow, no border. */
.odd-swh{display:inline-flex;flex-shrink:0}
.odd-swh:empty{display:none}
.odd-sw{display:inline-flex;flex-shrink:0;box-sizing:border-box;width:34px;height:18px;padding:3px 3px 3px 10px;border-radius:6px;box-shadow:0 1px 3px var(--shadow)}
.odd-sw>span{display:flex;align-items:center;justify-content:flex-end;gap:2px;flex:1;min-width:0;padding:0 3px;border-radius:3px}
.odd-sw i{display:block;width:5px;height:5px;border-radius:50%}
.odd-group{padding:7px 10px 3px;font-size:10.5px;font-weight:700;letter-spacing:.5px;text-transform:uppercase;color:var(--muted);user-select:none}
`

const dropdownJS = `
// ── odd:begin ── Drop-down lists (dropdown.go) ─────────────────────────────
var oddStarted = false;
var oddOpenSt = null;   // the state of the list open now (one at a time)
var oddSeq = 0;
var ODD_CHEV = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg>';
var ODD_CHECK = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>';

// A select that gets the app's list: a drop-down one (not a list box), not
// marked data-native, not enhanced yet.
function oddEligible(sel) {
  if (!sel || sel.tagName !== 'SELECT' || sel._odd) return false;
  if (sel.multiple || sel.size > 1) return false;
  if (typeof sel.hasAttribute === 'function' && sel.hasAttribute('data-native')) return false;
  return true;
}
function oddNativeDesc(name) {
  try {
    if (typeof HTMLSelectElement === 'undefined') return null;
    return Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, name) || null;
  } catch (e) { return null; }
}
function oddFire(el, type) {
  var ev = null;
  try { ev = new Event(type, { bubbles: true }); } catch (e) {
    try { ev = document.createEvent('Event'); ev.initEvent(type, true, false); } catch (e2) { ev = null; }
  }
  if (ev) el.dispatchEvent(ev);
}
function oddClasses(sel) {
  return String(sel.className || '').split(/\s+/).filter(function (c) { return c && c !== 'odd-native'; }).join(' ');
}

// enhance: the trigger after the select, the select hidden (the source of
// truth). Returns the trigger (null: not enhanced).
function oddEnhance(sel) {
  if (!oddEligible(sel) || !sel.parentNode) return null;
  var doc = sel.ownerDocument || document;
  var btn = doc.createElement('button');
  btn.type = 'button';
  btn.setAttribute('role', 'combobox');
  btn.setAttribute('aria-haspopup', 'listbox');
  btn.setAttribute('aria-expanded', 'false');
  var face = doc.createElement('span'); face.className = 'odd-face';
  var lab = doc.createElement('span'); lab.className = 'odd-label';
  var sizer = doc.createElement('span'); sizer.className = 'odd-sizer'; sizer.setAttribute('aria-hidden', 'true');
  face.appendChild(lab); face.appendChild(sizer);
  var chev = doc.createElement('span'); chev.className = 'odd-chev'; chev.setAttribute('aria-hidden', 'true'); chev.innerHTML = ODD_CHEV;
  var swh = doc.createElement('span'); swh.className = 'odd-swh'; swh.setAttribute('aria-hidden', 'true');
  btn.appendChild(swh); btn.appendChild(face); btn.appendChild(chev);
  var st = { sel: sel, btn: btn, lab: lab, sizer: sizer, swh: swh, swKey: '', pop: null, rows: [], hl: -1, typed: '', typedAt: 0, sizerKey: null, id: 'odd-' + (++oddSeq) };
  sel._odd = st; btn._oddSel = sel;
  // The native value accessors, wrapped on the element: a value set by code
  // redraws the trigger.
  ['value', 'selectedIndex'].forEach(function (name) {
    var d = oddNativeDesc(name);
    if (!d || !d.get || !d.set) return;
    try {
      Object.defineProperty(sel, name, {
        configurable: true, enumerable: true,
        get: function () { return d.get.call(sel); },
        set: function (v) { d.set.call(sel, v); oddSync(sel); },
      });
    } catch (e) {}
  });
  try { sel.focus = function () { try { btn.focus(); } catch (e) {} }; } catch (e) {}
  sel.classList.add('odd-native');
  sel.setAttribute('tabindex', '-1');
  sel.setAttribute('aria-hidden', 'true');
  sel.addEventListener('change', function () { oddSync(sel); });
  sel.addEventListener('input', function () { oddSync(sel); });
  btn.addEventListener('click', function (e) {
    if (e && e.preventDefault) e.preventDefault();
    if (oddOpenSt === st) oddClose(); else oddOpen(st);
  });
  btn.addEventListener('keydown', function (e) { oddTriggerKey(st, e); });
  sel.parentNode.insertBefore(btn, sel.nextSibling);
  if (typeof MutationObserver === 'function') {
    st.mo = new MutationObserver(function () { oddSync(sel); });
    st.mo.observe(sel, { childList: true, subtree: true, characterData: true, attributes: true,
      attributeFilter: ['style', 'class', 'disabled', 'hidden', 'title', 'aria-label', 'selected', 'label', 'value', 'dir', 'data-swatch', 'data-hint'] });
  }
  oddSync(sel);
  return btn;
}

// Enhances every eligible select under root (the root itself included).
function oddEnhanceAll(root) {
  root = root || document;
  if (!root) return;
  if (root.tagName === 'SELECT') { oddEnhance(root); return; }
  if (typeof root.getElementsByTagName !== 'function') return;
  var list = root.getElementsByTagName('select');
  if (!list || !list.length) return;
  Array.prototype.slice.call(list).forEach(function (s) {
    if (s._odd) oddPlace(s._odd); else oddEnhance(s);
  });
}

// The trigger stays right after its select (a select moved by the code
// takes it along); a select gone takes it away.
function oddPlace(st) {
  var sel = st.sel, btn = st.btn;
  if (sel.parentNode) {
    if (sel.nextSibling !== btn) sel.parentNode.insertBefore(btn, sel.nextSibling);
  } else if (btn.parentNode) {
    if (oddOpenSt === st) oddClose();
    btn.parentNode.removeChild(btn);
  }
}

// An option's theme preview (data-swatch="page,surface,text,accent", four
// #rrggbb colours): its element, null without one (or a malformed one).
function oddSwatchColors(o) {
  var v = o && typeof o.getAttribute === 'function' ? o.getAttribute('data-swatch') : null;
  if (!v) return null;
  var c = String(v).split(',').map(function (x) { return x.trim(); });
  if (c.length !== 4) return null;
  for (var i = 0; i < 4; i++) if (!/^#[0-9a-fA-F]{6}$/.test(c[i])) return null;
  return c;
}
function oddSwatch(o, doc) {
  var c = oddSwatchColors(o);
  if (!c) return null;
  doc = doc || document;
  var sw = doc.createElement('span'); sw.className = 'odd-sw'; sw.setAttribute('aria-hidden', 'true');
  sw.style.background = c[0];
  var card = doc.createElement('span'); card.style.background = c[1];
  var tx = doc.createElement('i'); tx.style.background = c[2];
  var ac = doc.createElement('i'); ac.style.background = c[3];
  card.appendChild(tx); card.appendChild(ac); sw.appendChild(card);
  return sw;
}

function oddOptions(sel) { return sel.options ? Array.prototype.slice.call(sel.options) : []; }
function oddOptText(o) { return String((o && (o.label || o.textContent)) || '').replace(/\s+/g, ' ').trim(); }

// The trigger follows its select: its text, the widest option's width
// (as the native one), its classes, inline style, title, disabled, shown.
function oddSync(sel) {
  var st = sel && sel._odd;
  if (!st) return;
  var btn = st.btn;
  var cls = oddClasses(sel);
  btn.className = 'odd-trigger' + (cls ? ' ' + cls : '') + (oddOpenSt === st ? ' open' : '');
  var css = (sel.style && sel.style.cssText) || '';
  if (btn.style.cssText !== css) btn.style.cssText = css;
  if (sel.hidden) btn.style.display = 'none';
  btn.disabled = !!sel.disabled;
  var title = sel.getAttribute('title');
  if (title) btn.setAttribute('title', title); else btn.removeAttribute('title');
  var al = sel.getAttribute('aria-label') || title;
  if (al) btn.setAttribute('aria-label', al); else btn.removeAttribute('aria-label');
  if (sel.id) btn.setAttribute('data-odd-for', sel.id);
  var opts = oddOptions(sel);
  var cur = sel.selectedIndex >= 0 ? opts[sel.selectedIndex] : null;
  st.lab.textContent = cur ? oddOptText(cur) : '';
  var swc = cur ? oddSwatchColors(cur) : null, swKey = swc ? swc.join(',') : '';
  if (swKey !== st.swKey) {
    st.swKey = swKey;
    st.swh.innerHTML = '';
    var swEl = swc ? oddSwatch(cur, sel.ownerDocument) : null;
    if (swEl) st.swh.appendChild(swEl);
  }
  st.lab.classList.toggle('ph', !!(cur && cur.disabled && cur.value === ''));
  if (cur && cur.dir) st.lab.dir = cur.dir; else st.lab.removeAttribute('dir');
  var key = opts.map(oddOptText).join('\n');
  if (key !== st.sizerKey) {
    st.sizerKey = key;
    st.sizer.innerHTML = '';
    opts.forEach(function (o) { var s = document.createElement('span'); s.textContent = oddOptText(o) || ' '; st.sizer.appendChild(s); });
  }
  if (oddOpenSt === st) {
    if (btn.style.display === 'none' || btn.disabled || !btn.isConnected) oddClose(); else oddRender(st);
  }
}

// ── The list ──
function oddRender(st) {
  var sel = st.sel, pop = st.pop;
  if (!pop) return;
  pop.innerHTML = '';
  st.rows = [];
  var opts = oddOptions(sel);
  var cur = sel.selectedIndex;
  var addOpt = function (o, inGroup, groupDisabled) {
    var i = opts.indexOf(o);
    var row = document.createElement('div');
    row.className = 'odd-opt' + (inGroup ? ' in-group' : '');
    row.id = st.id + '-o' + i;
    row.setAttribute('role', 'option');
    row.setAttribute('aria-selected', i === cur ? 'true' : 'false');
    var dis = !!(o.disabled || groupDisabled);
    if (dis) row.setAttribute('aria-disabled', 'true');
    if (o.dir) row.dir = o.dir;
    var chk = document.createElement('span'); chk.className = 'odd-chk'; chk.innerHTML = ODD_CHECK;
    var txt = document.createElement('span'); txt.className = 'odd-txt'; txt.textContent = oddOptText(o) || ' ';
    if (o.title) row.title = o.title;
    row.appendChild(chk);
    var sw = oddSwatch(o, document);
    if (sw) row.appendChild(sw);
    row.appendChild(txt);
    // data-hint: a short muted note after the text, in the list only.
    var hint = typeof o.getAttribute === 'function' ? o.getAttribute('data-hint') : '';
    if (hint) { var hs = document.createElement('span'); hs.className = 'odd-hint'; hs.textContent = hint; row.appendChild(hs); }
    row._oddIndex = i;
    st.rows.push({ el: row, index: i, disabled: dis, text: oddOptText(o).toLowerCase() });
    pop.appendChild(row);
  };
  Array.prototype.forEach.call(sel.children, function (c) {
    if (c.tagName === 'OPTGROUP') {
      var g = document.createElement('div'); g.className = 'odd-group'; g.setAttribute('role', 'presentation');
      g.textContent = c.label || '';
      pop.appendChild(g);
      Array.prototype.forEach.call(c.children, function (o) { if (o.tagName === 'OPTION') addOpt(o, true, c.disabled); });
    } else if (c.tagName === 'OPTION') addOpt(c, false, false);
  });
  var want = st.hl >= 0 ? st.hl : cur;
  st.hl = -1;
  oddHighlight(st, oddRowOf(st, want), false);
}
function oddRowOf(st, index) {
  for (var k = 0; k < st.rows.length; k++) if (st.rows[k].index === index) return k;
  return -1;
}
// k: a position in st.rows (-1: none).
function oddHighlight(st, k, scroll) {
  st.rows.forEach(function (r, j) { r.el.classList.toggle('hl', j === k); });
  st.hl = k >= 0 && st.rows[k] ? st.rows[k].index : -1;
  if (k >= 0 && st.rows[k]) {
    st.btn.setAttribute('aria-activedescendant', st.rows[k].el.id);
    if (scroll !== false && typeof st.rows[k].el.scrollIntoView === 'function') { try { st.rows[k].el.scrollIntoView({ block: 'nearest' }); } catch (e) {} }
  } else st.btn.removeAttribute('aria-activedescendant');
}
// The next enabled row from position k in the direction dir (k itself
// included when it is enabled and dir is 0).
function oddStep(st, k, dir) {
  var n = st.rows.length;
  if (!n) return -1;
  if (dir === 0) { if (k >= 0 && k < n && !st.rows[k].disabled) return k; dir = 1; }
  for (var j = k + dir; j >= 0 && j < n; j += dir) if (!st.rows[j].disabled) return j;
  return k >= 0 && k < n && !st.rows[k].disabled ? k : -1;
}
function oddCurRow(st) { return oddRowOf(st, st.hl); }

function oddOpen(st) {
  if (!st || st.btn.disabled) return;
  if (oddOpenSt && oddOpenSt !== st) oddClose();
  if (oddOpenSt === st) return;
  var pop = document.createElement('div');
  pop.className = 'odd-pop';
  pop.id = st.id + '-list';
  pop.setAttribute('role', 'listbox');
  pop.tabIndex = -1;
  if (st.btn.getAttribute('aria-label')) pop.setAttribute('aria-label', st.btn.getAttribute('aria-label'));
  try { var fs = getComputedStyle(st.btn).fontSize; if (fs && parseFloat(fs) >= 11) pop.style.fontSize = fs; } catch (e) {}
  // (the focus stays on the trigger; a press in the list never takes it)
  pop.addEventListener('mousedown', function (e) { if (e && e.preventDefault) e.preventDefault(); });
  pop.addEventListener('click', function (e) {
    var t = e && e.target;
    while (t && t !== pop && t._oddIndex === undefined) t = t.parentNode;
    if (!t || t === pop) return;
    var k = oddRowOf(st, t._oddIndex);
    if (k < 0 || st.rows[k].disabled) return;
    oddChoose(st, t._oddIndex);
  });
  pop.addEventListener('mousemove', function (e) {
    var t = e && e.target;
    while (t && t !== pop && t._oddIndex === undefined) t = t.parentNode;
    if (!t || t === pop) return;
    var k = oddRowOf(st, t._oddIndex);
    if (k >= 0 && !st.rows[k].disabled && st.hl !== t._oddIndex) oddHighlight(st, k, false);
  });
  st.pop = pop;
  st.hl = -1;
  oddOpenSt = st;
  st.btn.classList.add('open');
  st.btn.setAttribute('aria-expanded', 'true');
  st.btn.setAttribute('aria-controls', pop.id);
  oddRender(st);
  pop.style.visibility = 'hidden';
  document.body.appendChild(pop);
  oddPosition(st);
  pop.style.visibility = '';
  var k = oddCurRow(st);
  if (k >= 0) oddHighlight(st, k, true);
  document.addEventListener('mousedown', oddOutside, true);
  window.addEventListener('keydown', oddKey, true);
  window.addEventListener('resize', oddClose);
  window.addEventListener('blur', oddClose);
  document.addEventListener('scroll', oddScroll, true);
}

// Under the trigger (above when there is more room there), inside the window.
function oddPosition(st) {
  var pop = st.pop, r = st.btn.getBoundingClientRect();
  var vw = window.innerWidth || document.documentElement.clientWidth || 0;
  var vh = window.innerHeight || document.documentElement.clientHeight || 0;
  var M = 6, GAP = 4;
  pop.style.minWidth = Math.max(120, Math.round(r.width)) + 'px';
  pop.style.maxWidth = Math.max(160, vw - 2 * M) + 'px';
  var below = vh - r.bottom - GAP - M, above = r.top - GAP - M;
  var natural = pop.scrollHeight || 0;
  var down = natural <= below || below >= above;
  var room = Math.max(80, down ? below : above);
  pop.style.maxHeight = Math.min(320, room) + 'px';
  var ph = Math.min(natural, Math.min(320, room));
  var pw = pop.offsetWidth || 0;
  var rtl = false;
  try { rtl = getComputedStyle(st.btn).direction === 'rtl'; } catch (e) {}
  var left = rtl ? r.right - pw : r.left;
  if (left + pw > vw - M) left = vw - M - pw;
  if (left < M) left = M;
  var top = down ? r.bottom + GAP : r.top - GAP - ph;
  if (top < M) top = M;
  pop.style.left = Math.round(left) + 'px';
  pop.style.top = Math.round(top) + 'px';
}

function oddClose() {
  var st = oddOpenSt;
  if (!st) return;
  oddOpenSt = null;
  document.removeEventListener('mousedown', oddOutside, true);
  window.removeEventListener('keydown', oddKey, true);
  window.removeEventListener('resize', oddClose);
  window.removeEventListener('blur', oddClose);
  document.removeEventListener('scroll', oddScroll, true);
  if (st.pop && st.pop.parentNode) st.pop.parentNode.removeChild(st.pop);
  st.pop = null; st.rows = []; st.hl = -1; st.typed = '';
  st.btn.classList.remove('open');
  st.btn.setAttribute('aria-expanded', 'false');
  st.btn.removeAttribute('aria-controls');
  st.btn.removeAttribute('aria-activedescendant');
}

// A choice: the select's value, then 'input' and 'change' on it (only when
// it changed, as the native list).
function oddChoose(st, index) {
  var sel = st.sel;
  var opts = oddOptions(sel);
  var o = opts[index];
  oddClose();
  try { st.btn.focus(); } catch (e) {}
  if (!o || o.disabled || (o.parentNode && o.parentNode.tagName === 'OPTGROUP' && o.parentNode.disabled)) return;
  if (sel.selectedIndex === index) return;
  sel.selectedIndex = index;
  oddFire(sel, 'input');
  oddFire(sel, 'change');
}

function oddOutside(e) {
  var st = oddOpenSt;
  if (!st) return;
  var t = e && e.target;
  if (t && st.pop && st.pop.contains(t)) return;
  if (t && st.btn.contains(t)) return; // (the trigger's click toggles it)
  oddClose();
}
function oddScroll(e) {
  var st = oddOpenSt;
  if (!st) return;
  if (e && e.target && st.pop && (e.target === st.pop || st.pop.contains(e.target))) return;
  oddClose();
}

// Typing goes to the next option that starts with what was typed (the same
// letter again: the next one with it).
function oddType(st, ch) {
  var now = Date.now();
  st.typed = (now - st.typedAt > 700 ? '' : st.typed) + ch.toLowerCase();
  st.typedAt = now;
  var q = st.typed;
  var same = q.length > 1 && q.split('').every(function (c) { return c === q[0]; });
  if (same) q = q[0];
  var n = st.rows.length, from = oddCurRow(st);
  var start = (q.length === 1 || same) ? from + 1 : Math.max(from, 0);
  for (var j = 0; j < n; j++) {
    var k = (start + j + n) % n;
    var r = st.rows[k];
    if (!r.disabled && r.text.indexOf(q) === 0) { oddHighlight(st, k, true); return true; }
  }
  return false;
}

function oddEat(e) {
  if (e.preventDefault) e.preventDefault();
  if (e.stopImmediatePropagation) e.stopImmediatePropagation(); else if (e.stopPropagation) e.stopPropagation();
}
function oddPrintable(e) { return e && typeof e.key === 'string' && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey; }

// The trigger, list closed: what opens it.
function oddTriggerKey(st, e) {
  if (!e || oddOpenSt === st) return;
  var k = e.key;
  if (k === 'Enter' || k === ' ' || k === 'ArrowDown' || k === 'ArrowUp' || k === 'F4' || (e.altKey && k === 'ArrowDown')) {
    oddEat(e);
    oddOpen(st);
    return;
  }
  if (oddPrintable(e)) {
    oddEat(e);
    oddOpen(st);
    if (oddOpenSt === st) oddType(st, e.key);
  }
}

// The list open (window, capture phase: before any other key handler, so
// Escape closes the list and not the dialog under it).
function oddKey(e) {
  var st = oddOpenSt;
  if (!st || !e) return;
  var k = e.key, cur = oddCurRow(st), n = st.rows.length;
  if (k === 'Escape') { oddEat(e); oddClose(); try { st.btn.focus(); } catch (x) {} return; }
  if (k === 'Tab') { oddClose(); return; }
  if (k === 'ArrowDown') { oddEat(e); if (e.altKey) return; oddHighlight(st, cur < 0 ? oddStep(st, -1, 1) : oddStep(st, cur, 1), true); return; }
  if (k === 'ArrowUp') {
    oddEat(e);
    if (e.altKey) { if (cur >= 0) oddChoose(st, st.rows[cur].index); else oddClose(); return; }
    oddHighlight(st, cur < 0 ? oddStep(st, n, -1) : oddStep(st, cur, -1), true); return;
  }
  if (k === 'Home') { oddEat(e); oddHighlight(st, oddStep(st, -1, 1), true); return; }
  if (k === 'End') { oddEat(e); oddHighlight(st, oddStep(st, n, -1), true); return; }
  if (k === 'PageDown' || k === 'PageUp') {
    oddEat(e);
    var dir = k === 'PageDown' ? 1 : -1, j = cur < 0 ? (dir > 0 ? -1 : n) : cur;
    for (var s = 0; s < 8; s++) { var nx = oddStep(st, j, dir); if (nx === j || nx < 0) break; j = nx; }
    oddHighlight(st, j, true); return;
  }
  if (k === 'Enter' || (k === ' ' && !(st.typed && Date.now() - st.typedAt < 700))) {
    oddEat(e);
    if (cur >= 0 && !st.rows[cur].disabled) oddChoose(st, st.rows[cur].index); else oddClose();
    return;
  }
  if (oddPrintable(e)) { oddEat(e); oddType(st, k); return; }
}

function oddOnMutations(records) {
  for (var i = 0; i < records.length; i++) {
    var r = records[i];
    var j, n;
    for (j = 0; j < r.addedNodes.length; j++) {
      n = r.addedNodes[j];
      if (!n || n.nodeType !== 1) continue;
      if (n.tagName === 'SELECT') { if (n._odd) oddPlace(n._odd); else oddEnhance(n); }
      else if (n.firstElementChild) oddEnhanceAll(n);
    }
    for (j = 0; j < r.removedNodes.length; j++) {
      n = r.removedNodes[j];
      if (!n || n.nodeType !== 1) continue;
      if (n.tagName === 'SELECT') { if (n._odd && !n.isConnected) oddPlace(n._odd); }
      else if (n._oddSel) {
        var s = n._oddSel;
        if (s.isConnected && s._odd) oddPlace(s._odd);
        else if (oddOpenSt && oddOpenSt.btn === n && !n.isConnected) oddClose();
      }
      else if (n.firstElementChild && typeof n.getElementsByTagName === 'function') {
        var l = n.getElementsByTagName('select');
        for (var q = 0; q < l.length; q++) if (l[q]._odd && oddOpenSt === l[q]._odd) oddClose();
      }
    }
  }
}

// Every select of the page, now and as they come.
function oddStart() {
  if (oddStarted) return true;
  if (typeof MutationObserver !== 'function' || typeof document === 'undefined' || !document || !document.documentElement || typeof document.documentElement.getElementsByTagName !== 'function') return false;
  oddStarted = true;
  new MutationObserver(oddOnMutations).observe(document.documentElement, { childList: true, subtree: true });
  oddEnhanceAll(document.documentElement);
  return true;
}
oddStart();
// ── odd:end ──
`
