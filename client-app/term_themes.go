package main

// term_themes.go — the THEMES of the remote and local terminals (SSH, CMD,
// PowerShell: terminal_viewer.go): a different theme for the shell, the CMD
// and the PowerShell, and in a mosaic a quick way to change the colour set
// of one terminal to tell it from another.
//
// A theme is a full xterm palette: background, text, cursor, selection and
// the 16 ANSI colours. Two layers:
//   - a default per TYPE (Settings > Sessions, this computer, every server):
//     by default three different ones (SSH: Obliance, CMD: Campbell, the
//     classic console, PowerShell: its blue);
//   - a theme of ONE tab (tab.themeKey), picked from its menu (right click on
//     the terminal, on its tab or its mosaic sub-tab, or its toolbar's Theme
//     button): it never changes its type's default, it follows the tab when it
//     moves to another window (the parked tunnel carries it: term_park.go) and
//     dies with it.
// "Obliance" is the app's own look (--term-bg / --term-fg, the accent for the
// cursor and the selection): it follows the app's theme when it changes.
//
// Both consts are injected verbatim after the terminals' (ui.go buildUI, not
// through Sprintf: '%' is safe); no backtick here.

const termThemesCSS = `
/* ── Terminal themes (term_themes.go) ── */
.term-theme-pop{position:fixed;z-index:70;min-width:240px;max-height:72vh;overflow:auto;padding:6px;border:none;border-radius:12px;background:var(--bg3);background:color-mix(in srgb,var(--bg3) 84%,transparent);-webkit-backdrop-filter:blur(16px) saturate(1.2);backdrop-filter:blur(16px) saturate(1.2);box-shadow:0 10px 32px var(--shadow)}
.term-theme-head{font-size:11px;color:var(--muted);padding:4px 8px 2px;font-weight:600}
.term-theme-sep{height:1px;border:none;background:color-mix(in srgb,var(--text) 9%,transparent);margin:4px 6px}
.term-theme-row{display:flex;align-items:center;gap:8px;width:100%;margin:0;padding:6px 8px;border:none;outline:none;border-radius:8px;background:transparent;color:var(--text);font:inherit;font-size:12px;cursor:pointer;text-align:start;-webkit-appearance:none;appearance:none}
.term-theme-row:hover,.term-theme-row:focus-visible{background:color-mix(in srgb,var(--text) 8%,transparent)}
.term-theme-row.active{background:color-mix(in srgb,var(--accent) 14%,transparent)}
.term-theme-chk{width:12px;flex-shrink:0;color:var(--accent-l);visibility:hidden}
.term-theme-row.active .term-theme-chk{visibility:visible}
.term-theme-name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.term-theme-pv{display:inline-flex;align-items:center;gap:3px;padding:3px 6px;border-radius:5px;font-family:"Cascadia Mono",Consolas,monospace;font-size:11px;font-weight:700;flex-shrink:0}
.term-theme-pv i{width:7px;height:7px;border-radius:50%;display:inline-block}
.term-theme-set{display:flex;flex-direction:column;align-items:flex-end;gap:6px}
.term-theme-sample{font-family:"Cascadia Mono",Consolas,monospace;font-size:11px;line-height:1.55;padding:8px 10px;border-radius:8px;min-width:230px;white-space:pre;text-align:start}
`

const termThemesJS = `
// ── Terminal themes (term_themes.go) ────────────────────────────────────────
var TERM_THEME_PREF = 'oblireach_term_themes';
var TERM_THEME_KINDS = ['ssh', 'cmd', 'powershell'];
var TERM_THEME_DEFAULTS = { ssh: 'obliance', cmd: 'campbell', powershell: 'powershell' };
var TERM_ANSI_KEYS = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white',
  'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite'];
// The palettes of the usual schemes (Windows Terminal's for Campbell and its
// PowerShell variant, the authors' own for the others). obliance: the app's
// colours for the background, the text, the cursor and the selection.
var TERM_THEMES = [
  { key: 'obliance', label: 'term.theme.obliance', dynamic: true, bg: '#05060D', fg: '#4ADE80', cursor: '#E03A3A', sel: 'rgba(224,58,58,0.3)',
    ansi: ['#1F2430', '#F87171', '#4ADE80', '#FACC15', '#60A5FA', '#C084FC', '#22D3EE', '#E5E7EB', '#6B7280', '#FCA5A5', '#86EFAC', '#FDE68A', '#93C5FD', '#D8B4FE', '#67E8F9', '#FFFFFF'] },
  { key: 'campbell', label: 'term.theme.campbell', bg: '#0C0C0C', fg: '#CCCCCC', cursor: '#FFFFFF', sel: 'rgba(255,255,255,0.25)',
    ansi: ['#0C0C0C', '#C50F1F', '#13A10E', '#C19C00', '#0037DA', '#881798', '#3A96DD', '#CCCCCC', '#767676', '#E74856', '#16C60C', '#F9F1A5', '#3B78FF', '#B4009E', '#61D6D6', '#F2F2F2'] },
  { key: 'powershell', label: 'term.theme.powershell', bg: '#012456', fg: '#CCCCCC', cursor: '#FFFFFF', sel: 'rgba(255,255,255,0.25)',
    ansi: ['#0C0C0C', '#C50F1F', '#13A10E', '#C19C00', '#0037DA', '#881798', '#3A96DD', '#CCCCCC', '#767676', '#E74856', '#16C60C', '#F9F1A5', '#3B78FF', '#B4009E', '#61D6D6', '#F2F2F2'] },
  { key: 'solarized-dark', label: 'term.theme.solarizedDark', bg: '#002B36', fg: '#839496', cursor: '#93A1A1', sel: 'rgba(147,161,161,0.25)',
    ansi: ['#073642', '#DC322F', '#859900', '#B58900', '#268BD2', '#D33682', '#2AA198', '#EEE8D5', '#586E75', '#CB4B16', '#859900', '#B58900', '#268BD2', '#6C71C4', '#2AA198', '#FDF6E3'] },
  { key: 'solarized-light', label: 'term.theme.solarizedLight', bg: '#FDF6E3', fg: '#586E75', cursor: '#073642', sel: 'rgba(7,54,66,0.18)',
    ansi: ['#073642', '#DC322F', '#859900', '#B58900', '#268BD2', '#D33682', '#2AA198', '#EEE8D5', '#586E75', '#CB4B16', '#859900', '#B58900', '#268BD2', '#6C71C4', '#2AA198', '#002B36'] },
  { key: 'dracula', label: 'term.theme.dracula', bg: '#282A36', fg: '#F8F8F2', cursor: '#F8F8F2', sel: 'rgba(68,71,90,0.85)',
    ansi: ['#21222C', '#FF5555', '#50FA7B', '#F1FA8C', '#BD93F9', '#FF79C6', '#8BE9FD', '#F8F8F2', '#6272A4', '#FF6E6E', '#69FF94', '#FFFFA5', '#D6ACFF', '#FF92DF', '#A4FFFF', '#FFFFFF'] },
  { key: 'nord', label: 'term.theme.nord', bg: '#2E3440', fg: '#D8DEE9', cursor: '#D8DEE9', sel: 'rgba(67,76,94,0.9)',
    ansi: ['#3B4252', '#BF616A', '#A3BE8C', '#EBCB8B', '#81A1C1', '#B48EAD', '#88C0D0', '#E5E9F0', '#4C566A', '#BF616A', '#A3BE8C', '#EBCB8B', '#81A1C1', '#B48EAD', '#8FBCBB', '#ECEFF4'] },
  { key: 'one-dark', label: 'term.theme.oneDark', bg: '#282C34', fg: '#ABB2BF', cursor: '#528BFF', sel: 'rgba(62,68,81,0.9)',
    ansi: ['#282C34', '#E06C75', '#98C379', '#E5C07B', '#61AFEF', '#C678DD', '#56B6C2', '#ABB2BF', '#5C6370', '#E06C75', '#98C379', '#E5C07B', '#61AFEF', '#C678DD', '#56B6C2', '#FFFFFF'] },
  { key: 'gruvbox', label: 'term.theme.gruvbox', bg: '#282828', fg: '#EBDBB2', cursor: '#EBDBB2', sel: 'rgba(80,73,69,0.9)',
    ansi: ['#282828', '#CC241D', '#98971A', '#D79921', '#458588', '#B16286', '#689D6A', '#A89984', '#928374', '#FB4934', '#B8BB26', '#FABD2F', '#83A598', '#D3869B', '#8EC07C', '#EBDBB2'] },
  { key: 'monokai', label: 'term.theme.monokai', bg: '#272822', fg: '#F8F8F2', cursor: '#F8F8F0', sel: 'rgba(73,72,62,0.9)',
    ansi: ['#272822', '#F92672', '#A6E22E', '#F4BF75', '#66D9EF', '#AE81FF', '#A1EFE4', '#F8F8F2', '#75715E', '#F92672', '#A6E22E', '#F4BF75', '#66D9EF', '#AE81FF', '#A1EFE4', '#F9F8F5'] },
  { key: 'tango', label: 'term.theme.tango', bg: '#000000', fg: '#D3D7CF', cursor: '#FFFFFF', sel: 'rgba(255,255,255,0.25)',
    ansi: ['#000000', '#CC0000', '#4E9A06', '#C4A000', '#3465A4', '#75507B', '#06989A', '#D3D7CF', '#555753', '#EF2929', '#8AE234', '#FCE94F', '#729FCF', '#AD7FA8', '#34E2E2', '#EEEEEC'] },
  { key: 'ubuntu', label: 'term.theme.ubuntu', bg: '#300A24', fg: '#EEEEEC', cursor: '#BBBBBB', sel: 'rgba(255,255,255,0.22)',
    ansi: ['#2E3436', '#CC0000', '#4E9A06', '#C4A000', '#3465A4', '#75507B', '#06989A', '#D3D7CF', '#555753', '#EF2929', '#8AE234', '#FCE94F', '#729FCF', '#AD7FA8', '#34E2E2', '#EEEEEC'] },
];
function termThemeDef(key) {
  for (var i = 0; i < TERM_THEMES.length; i++) if (TERM_THEMES[i].key === key) return TERM_THEMES[i];
  return null;
}
function termThemeKeyOk(key) { return typeof key === 'string' && !!termThemeDef(key); }
function termThemeKind(kind) { return TERM_THEME_KINDS.indexOf(kind) >= 0; }
// The default of each type (Settings > Sessions), validated.
function termThemeDefaults() {
  var v = null;
  try { v = JSON.parse(localStorage.getItem(TERM_THEME_PREF) || 'null'); } catch (e) { v = null; }
  var d = { ssh: TERM_THEME_DEFAULTS.ssh, cmd: TERM_THEME_DEFAULTS.cmd, powershell: TERM_THEME_DEFAULTS.powershell };
  if (v && typeof v === 'object') TERM_THEME_KINDS.forEach(function (k) { if (termThemeKeyOk(v[k])) d[k] = v[k]; });
  return d;
}
// A type's default: saved, and worn at once by its open terminals that have
// no theme of their own (here; another window's at its next theme change).
function termSetThemeDefault(kind, key) {
  if (!termThemeKind(kind)) return;
  var d = termThemeDefaults();
  d[kind] = termThemeKeyOk(key) ? key : TERM_THEME_DEFAULTS[kind];
  try { localStorage.setItem(TERM_THEME_PREF, JSON.stringify(d)); } catch (e) {}
  if (typeof toolTabs !== 'undefined') toolTabs.forEach(function (t) { if (t.kind === kind && !t.themeKey) termApplyTabTheme(t); });
}
// The theme a tab wears: its own, else its type's default.
function termThemeKeyOf(tab) {
  if (tab && termThemeKeyOk(tab.themeKey)) return tab.themeKey;
  var kind = tab && tab.kind;
  return termThemeKind(kind) ? termThemeDefaults()[kind] : 'obliance';
}
function termCssVar(name) {
  try {
    if (typeof getComputedStyle !== 'function' || !document.body) return '';
    var v = getComputedStyle(document.body).getPropertyValue(name);
    return v ? String(v).trim() : '';
  } catch (e) { return ''; }
}
// The xterm theme object of a theme (ITheme).
function termThemeObj(key) {
  var d = termThemeDef(key) || TERM_THEMES[0];
  var bg = d.bg, fg = d.fg, cur = d.cursor, sel = d.sel;
  if (d.dynamic) {
    bg = termCssVar('--term-bg') || bg; fg = termCssVar('--term-fg') || fg; cur = termCssVar('--accent') || cur;
    var m = /^(\d{1,3})[\s,]+(\d{1,3})[\s,]+(\d{1,3})$/.exec(termCssVar('--accent-rgb'));
    if (m) sel = 'rgba(' + m[1] + ',' + m[2] + ',' + m[3] + ',0.3)';
  }
  var t = { background: bg, foreground: fg, cursor: cur, cursorAccent: bg, selectionBackground: sel };
  for (var i = 0; i < TERM_ANSI_KEYS.length; i++) t[TERM_ANSI_KEYS[i]] = d.ansi[i];
  return t;
}
function termThemeOf(tab) { return termThemeObj(termThemeKeyOf(tab)); }
// The theme on the terminal (xterm), and its frame (the box and, in a
// mosaic, its cell) in the same background.
function termPaintTheme(tab, th) {
  if (!tab || !th) return;
  if (tab.term) { try { tab.term.options.theme = th; } catch (e) {} }
  if (tab.box && tab.box.style) tab.box.style.background = th.background;
  if (tab.tgCellEl && tab.tgCellEl.style) tab.tgCellEl.style.background = th.background;
}
function termApplyTabTheme(tab) { if (termIsTab(tab)) termPaintTheme(tab, termThemeOf(tab)); }
// This ONE tab's theme (key falsy or unknown: back to its type's default).
function termSetTabTheme(tab, key) {
  if (!termIsTab(tab)) return;
  tab.themeKey = termThemeKeyOk(key) ? key : null;
  termApplyTabTheme(tab);
}
// Every terminal of this window again (the app's theme changed: Obliance follows it).
function termApplyTheme() {
  if (typeof toolTabs !== 'undefined') toolTabs.forEach(function (t) { termApplyTabTheme(t); });
}
var termThemeObs = null;
function termWatchTheme() {
  if (termThemeObs || typeof MutationObserver !== 'function' || !document.body) return;
  try {
    termThemeObs = new MutationObserver(termApplyTheme);
    termThemeObs.observe(document.body, { attributes: true, attributeFilter: ['data-theme'] });
  } catch (e) { termThemeObs = null; }
}
// A small sample of a theme: "ab" in its text colour on its background, and
// six of its colours (the menu's rows).
function termThemeSwatch(key) {
  var th = termThemeObj(key);
  var pv = document.createElement('span'); pv.className = 'term-theme-pv';
  pv.style.background = th.background; pv.style.color = th.foreground;
  var tx = document.createElement('span'); tx.textContent = 'ab'; pv.appendChild(tx);
  ['red', 'green', 'yellow', 'blue', 'magenta', 'cyan'].forEach(function (c) {
    var i = document.createElement('i'); i.style.background = th[c]; pv.appendChild(i);
  });
  return pv;
}
// A few lines of a shell in a theme (Settings > Sessions): its prompt, a
// listing, an error. Technical text (a shell's), not translated.
function termThemeSample(el, key) {
  var th = termThemeObj(key);
  el.style.background = th.background; el.style.color = th.foreground;
  el.innerHTML = '';
  var line = function (parts) {
    var d = document.createElement('div');
    parts.forEach(function (p) { var s = document.createElement('span'); s.textContent = p[0]; if (p[1]) s.style.color = th[p[1]]; d.appendChild(s); });
    el.appendChild(d);
  };
  line([['admin@srv01', 'green'], [':'], ['~', 'blue'], ['$ ls -l']]);
  line([['docs', 'blue'], ['  '], ['build.sh', 'green'], ['  notes.txt  '], ['backup.tar', 'red']]);
  line([['warning:', 'yellow'], [' disk 82% '], ['error:', 'brightRed'], [' 1 failed']]);
}

// ── The theme menu ──
// A popover: "the default of the type" first, then every theme with its
// sample; hovering a row tries it on the terminal (put back on leaving);
// a click keeps it for this tab only. o.clip: Copy / Paste first (the
// terminal's own right click). at: an element (under it) or {x, y}.
var termThemePop = null;
function termThemeMenuClose() {
  var p = termThemePop;
  if (!p) return;
  termThemePop = null;
  if (p.el && typeof p.el.remove === 'function') p.el.remove(); else if (p.el && p.el.parentElement) p.el.parentElement.removeChild(p.el);
  document.removeEventListener('mousedown', termThemeMenuOutside, true);
  document.removeEventListener('keydown', termThemeMenuKey, true);
  if (p.tab) termApplyTabTheme(p.tab); // (a theme only tried: its own again)
}
function termThemeMenuOutside(e) { if (termThemePop && e && !(termThemePop.el.contains && termThemePop.el.contains(e.target))) termThemeMenuClose(); }
function termThemeMenuKey(e) { if (e && e.key === 'Escape') termThemeMenuClose(); }
function termThemeMenuOpen(at, tab, o) {
  termThemeMenuClose();
  if (!termIsTab(tab)) return null;
  o = o || {};
  var pop = document.createElement('div'); pop.className = 'term-theme-pop'; pop.id = 'term-theme-pop';
  pop.setAttribute('role', 'menu');
  var row = function (label, run, active, swatchKey, id) {
    var b = document.createElement('button'); b.type = 'button'; b.className = 'term-theme-row' + (active ? ' active' : '');
    b.setAttribute('role', 'menuitem');
    if (id) b.id = id;
    var chk = document.createElement('span'); chk.className = 'term-theme-chk'; chk.textContent = '✓'; b.appendChild(chk);
    if (swatchKey) b.appendChild(termThemeSwatch(swatchKey));
    var nm = document.createElement('span'); nm.className = 'term-theme-name'; nm.textContent = label; b.appendChild(nm);
    b.addEventListener('mousedown', function (e) { if (e && e.preventDefault) e.preventDefault(); }); // (the keyboard stays in the terminal)
    b.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); run(); });
    pop.appendChild(b);
    return b;
  };
  var sep = function () { var s = document.createElement('div'); s.className = 'term-theme-sep'; pop.appendChild(s); };
  if (o.clip) {
    row(T('term.copy'), function () { termThemeMenuClose(); termCopy(tab, false); }, false, null, 'term-theme-copy');
    row(T('term.paste'), function () { termThemeMenuClose(); termPaste(tab); }, false, null, 'term-theme-paste');
    if (!o.clipOnly) sep();
  }
  if (!o.clipOnly) {
  var head = document.createElement('div'); head.className = 'term-theme-head'; head.textContent = T('term.theme.menu'); pop.appendChild(head);
  var defKey = termThemeDefaults()[tab.kind] || 'obliance', defDef = termThemeDef(defKey);
  var dr = row(T('term.theme.typeDefault', { theme: T(defDef.label) }), function () { termThemeMenuClose(); termSetTabTheme(tab, null); termFocus(tab); },
    !tab.themeKey, defKey, 'term-theme-default');
  dr.addEventListener('mouseenter', function () { termPaintTheme(tab, termThemeObj(defKey)); });
  sep();
  TERM_THEMES.forEach(function (d) {
    var b = row(T(d.label), function () { termThemeMenuClose(); termSetTabTheme(tab, d.key); termFocus(tab); }, tab.themeKey === d.key, d.key);
    b.dataset.theme = d.key;
    b.addEventListener('mouseenter', function () { termPaintTheme(tab, termThemeObj(d.key)); });
  });
  pop.addEventListener('mouseleave', function () { termApplyTabTheme(tab); });
  } // (clipOnly: a right click inside the terminal = Copy / Paste only; themes from the tab or the palette button)
  document.body.appendChild(pop);
  termThemePop = { el: pop, tab: tab };
  var x = 0, y = 0;
  if (at && typeof at.getBoundingClientRect === 'function') { var r = at.getBoundingClientRect(); x = r.left; y = (typeof r.bottom === 'number' ? r.bottom : r.top + r.height) + 4; }
  else if (at && typeof at.x === 'number') { x = at.x; y = at.y; }
  pop.style.left = Math.round(x) + 'px'; pop.style.top = Math.round(y) + 'px';
  var pr = pop.getBoundingClientRect();
  var vw = window.innerWidth || 0, vh = window.innerHeight || 0;
  if (vw && pr.width && x + pr.width > vw - 6) pop.style.left = Math.max(6, vw - pr.width - 6) + 'px';
  if (vh && pr.height && y + pr.height > vh - 6) pop.style.top = Math.max(6, vh - pr.height - 6) + 'px';
  setTimeout(function () {
    if (!termThemePop || termThemePop.el !== pop) return;
    document.addEventListener('mousedown', termThemeMenuOutside, true);
    document.addEventListener('keydown', termThemeMenuKey, true);
  }, 0);
  return pop;
}
// A right click on a terminal tab (its tab, its mosaic sub-tab, its cell's
// title): the menu there.
function termThemeContext(el, tab) {
  if (!el || !termIsTab(tab)) return;
  el.addEventListener('contextmenu', function (e) {
    if (e && e.preventDefault) e.preventDefault();
    if (e && e.stopPropagation) e.stopPropagation();
    termThemeMenuOpen({ x: (e && e.clientX) || 0, y: (e && e.clientY) || 0 }, tab);
  });
}
// The palette button (a terminal's toolbar, its mosaic cell's title).
var TERM_THEME_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 22a10 10 0 110-20c5.5 0 10 4 10 9a5 5 0 01-5 5h-2a2 2 0 00-1.4 3.4A1.7 1.7 0 0112 22z"/><circle cx="7.5" cy="10.5" r="1.2" fill="currentColor"/><circle cx="12" cy="7" r="1.2" fill="currentColor"/><circle cx="16.5" cy="10.5" r="1.2" fill="currentColor"/></svg>';
function termThemeButton(tab, id) {
  var b = document.createElement('button'); b.className = 'btn-sm'; if (id) b.id = id;
  b.title = T('term.theme.button'); b.setAttribute('aria-label', T('term.theme.button'));
  b.innerHTML = TERM_THEME_ICON;
  b.addEventListener('mousedown', function (e) { if (e && e.preventDefault) e.preventDefault(); if (e && e.stopPropagation) e.stopPropagation(); });
  b.addEventListener('click', function (e) {
    if (e && e.stopPropagation) e.stopPropagation();
    if (termThemePop && termThemePop.tab === tab) { termThemeMenuClose(); return; }
    termThemeMenuOpen(b, tab);
  });
  return b;
}

// ── Settings > Sessions: the default theme of each type ──
function renderTermThemeSetting(kind, titleKey) {
  var row = document.createElement('div'); row.className = 'setting-row';
  var lbl = document.createElement('div'); lbl.className = 'setting-label';
  var title = document.createElement('div'); title.className = 'title'; title.textContent = T(titleKey);
  var sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('settings.sessions.themeSub');
  lbl.appendChild(title); lbl.appendChild(sub);
  var ctl = document.createElement('div'); ctl.className = 'setting-control term-theme-set';
  var sel = document.createElement('select'); sel.className = 'session-select'; sel.id = 'set-term-theme-' + kind;
  sel.title = T(titleKey);
  TERM_THEMES.forEach(function (d) { var op = document.createElement('option'); op.value = d.key; op.textContent = T(d.label); sel.appendChild(op); });
  sel.value = termThemeDefaults()[kind];
  var sample = document.createElement('div'); sample.className = 'term-theme-sample'; sample.id = 'set-term-sample-' + kind;
  termThemeSample(sample, sel.value);
  sel.addEventListener('change', function () { termSetThemeDefault(kind, sel.value); termThemeSample(sample, termThemeDefaults()[kind]); });
  ctl.appendChild(sel); ctl.appendChild(sample);
  row.appendChild(lbl); row.appendChild(ctl);
  return row;
}
`
