package main

// devices_select.go — the main window's Devices page: several devices ticked
// at once, from the Devices menu, like the "+" list of a session window.
//
// The boxes, the service sets, the dialog and the openings one by one are
// the "+" list's (picker_multi.go: dpCheckHtml, dpGroupState, dpToggleMany,
// dpServices, dpConnect / dpAsk / dpRun); this file holds what differs:
//
//   - Its own selection (dsSel), kept while the page scrolls, while the
//     search and filters change, while the operator goes elsewhere and comes
//     back: a ticked device the filters no longer list stays ticked, the bar
//     says how many are hidden. Only the devices the lists' data still hold
//     stay (a server signed out, a tenant switched, a right withdrawn: gone),
//     and only while something opens on them (dpServices), with their fresh
//     records. The lists hold what the server lets this user reach, nothing
//     more.
//   - A box on each row (devices and local connections) and on each server /
//     tenant / group header (three-state: every connectable device under it,
//     its sub-groups included, as the search and filters list them, folded or
//     not). Shift + a row's box: every row between it and the box ticked last,
//     as listed on screen, takes its state. A click on the row itself is
//     unchanged (devicesOpen).
//   - A bar under the list once something is ticked: the count (and the
//     hidden part), "Deselect all", "Open as mosaic", "Connect".
//   - "Connect" / "Open as mosaic": the "+" list's flow (one question for the
//     sets with several services, none for a single one, a confirmation past
//     DP_MANY_TABS), then each tab in turn, "Detach by default" respected:
//       on:  every tab in ONE session window: the first one opens it, the
//            others join it (reachOpenWindow with into, detached_windows.go
//            openInto) once its first tab is there (the note of
//            tab_groups.go, flat for "Connect", a mosaic for "Open as
//            mosaic": the window builds the mosaic as its tabs arrive);
//       off: every tab here, in the main window (a mosaic of them after).
//     A session already open somewhere is brought to the front, never
//     opened twice (it stays out of the batch's window).
//
// Injected verbatim with picker_multi.go's (pickMultiCSS / pickMultiJS, ui.go
// buildUI, not through Sprintf): no backtick.

const devSelCSS = `
/* ── Devices page: several devices at once (devices_select.go) ── */
.ds-bar{position:sticky;bottom:0;z-index:4;display:flex;align-items:center;gap:10px;margin-top:12px;padding:10px 14px;border-radius:12px;background:var(--bg2);background-image:linear-gradient(rgb(var(--accent-rgb) / .09),rgb(var(--accent-rgb) / .09))}
.ds-bar .ds-count{font-size:12px;font-weight:600;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;min-width:0}
.ds-bar .ds-hidden{font-weight:400;color:var(--muted)}
.ds-bar .spacer{flex:1}
.ds-bar .btn-sm{display:inline-flex;align-items:center;gap:6px;white-space:nowrap}
.ds-bar .btn-primary{width:auto;padding:7px 16px;font-size:12px;border-radius:10px;white-space:nowrap}
.dt .dp-check{user-select:none}
`

const devSelJS = `
// ── Devices page: several devices at once (devices_select.go) ───────────────
var dsSel = new Map();  // devKey -> device: what is ticked, listed or not
var dsAnchor = null;    // the key of the row box ticked last (Shift: a range from it)
var dsRows = [];        // the device rows as listed on screen, in order
var dsHidden = 0;       // ticked, not listed by the search / filters
// The Devices page has it (never a session window: its "+" list).
function dsOn() { return !reachDetachedMode; }

// The lists' data as they are now: { devs: key -> device, keep: the server
// profiles not read yet (their ticked devices left as they are) }.
function dsUniverse() {
  var devs = new Map(), keep = new Set();
  var add = function (groups) {
    (Array.isArray(groups) ? groups : []).forEach(function (g) {
      (g && Array.isArray(g.devices) ? g.devices : []).forEach(function (d) { if (d) devs.set(devKey(d), d); });
    });
  };
  var local = function () {
    if (localConns.state === 'ok') add(localDevGroups());
    else if (localConns.loading || !localConns.state) keep.add(LOCAL_PID);
  };
  if (serverMode === 'multi') {
    serverProfiles.forEach(function (p) {
      if (p.local) { local(); return; }
      var st = srvState(p.id);
      if (st.signedIn === true && Array.isArray(st.groups) && (st.groups.length || !st.loading)) add(st.groups);
      else if (st.signedIn !== false && !st.error) keep.add(p.id);
    });
  } else if (activeServerIsLocal()) local();
  else add(overview && overview.groups);
  return { devs: devs, keep: keep };
}
// The selection keeps what the data still holds and what still opens.
function dsPrune() {
  var u = dsUniverse(), next = new Map();
  dsSel.forEach(function (d, k) {
    var f = u.devs.get(k);
    if (f) { if (dpConnectable(f)) next.set(k, f); }
    else if (u.keep.has(devPid(d))) next.set(k, d);
  });
  dsSel = next;
}

// The view's hooks (devViewMain, ui.go).
function dsDevCheck(dev) {
  var on = dpConnectable(dev);
  return dpCheckHtml(dsSel.has(devKey(dev)) ? 'all' : 'none', !on, false, on ? T('devices.select.selectTip') : '');
}
function dsGroupCheck(devs) { var g = dpGroupState(devs, dsSel); return dpCheckHtml(g.state, !g.total, true); }
// A row's box (e: its event; Shift: the range from the box ticked last).
function dsToggleDev(dev, e) {
  if (!dpConnectable(dev)) return;
  var k = devKey(dev), on = !dsSel.has(k);
  var a = (e && e.shiftKey && dsAnchor && dsAnchor !== k) ? dsRows.findIndex(function (d) { return devKey(d) === dsAnchor; }) : -1;
  var b = a >= 0 ? dsRows.findIndex(function (d) { return devKey(d) === k; }) : -1;
  if (a >= 0 && b >= 0) {
    for (var i = Math.min(a, b); i <= Math.max(a, b); i++) {
      var d = dsRows[i];
      if (!dpConnectable(d)) continue;
      if (on) dsSel.set(devKey(d), d); else dsSel.delete(devKey(d));
    }
  } else if (on) dsSel.set(k, dev); else dsSel.delete(k);
  dsAnchor = k;
}
function dsToggleMany(devs) { dpToggleMany(devs, dsSel); }
var dsSelectHooks = { dev: dsDevCheck, group: dsGroupCheck, toggleDev: dsToggleDev, toggleMany: dsToggleMany };
// The list was drawn: its rows, the selection against the data, the bar.
function dsOnTree(tree) {
  dsRows = (tree && tree.rows || []).filter(function (r) { return r.kind === 'device'; }).map(function (r) { return r.dev; });
  dsPrune();
  var listed = new Set((tree && tree.devs || []).map(devKey));
  dsHidden = 0;
  dsSel.forEach(function (d, k) { if (!listed.has(k)) dsHidden++; });
  dsBar();
}

// ── The bar ──
// Built with the page (renderDevicesPage, ui.go): returns its element.
function dsBarEl() {
  var bar = document.createElement('div'); bar.className = 'ds-bar'; bar.id = 'ds-bar'; bar.style.display = 'none';
  bar.setAttribute('role', 'toolbar'); bar.setAttribute('aria-label', T('devices.select.barLabel')); bar.setAttribute('data-i18n-aria', 'devices.select.barLabel');
  var cnt = document.createElement('span'); cnt.className = 'ds-count'; cnt.id = 'ds-count';
  cnt.setAttribute('aria-live', 'polite');
  var sp = document.createElement('span'); sp.className = 'spacer';
  var clr = document.createElement('button'); clr.className = 'btn-sm'; clr.id = 'ds-clear';
  clr.textContent = T('devices.select.clear'); clr.setAttribute('data-i18n', 'devices.select.clear');
  clr.addEventListener('click', function () { dsClear(); });
  var mos = document.createElement('button'); mos.className = 'btn-sm'; mos.id = 'ds-mosaic';
  mos.title = T('devices.select.mosaicTip'); mos.setAttribute('data-i18n-title', 'devices.select.mosaicTip');
  var mi = document.createElement('span'); mi.className = 'tg-ico'; mi.innerHTML = TG_ICON;
  var ml = document.createElement('span'); ml.textContent = T('devices.select.mosaic'); ml.setAttribute('data-i18n', 'devices.select.mosaic');
  mos.appendChild(mi); mos.appendChild(ml);
  mos.addEventListener('click', function () { dsConnect(true); });
  var go = document.createElement('button'); go.className = 'btn-primary'; go.id = 'ds-go';
  go.textContent = T('common.connect'); go.setAttribute('data-i18n', 'common.connect');
  go.addEventListener('click', function () { dsConnect(false); });
  bar.appendChild(cnt); bar.appendChild(sp); bar.appendChild(clr); bar.appendChild(mos); bar.appendChild(go);
  return bar;
}
// Shift + a box: no text selected across the rows (the page's tbody).
function dsWireTbody(tbody) {
  if (!tbody || typeof tbody.addEventListener !== 'function') return;
  tbody.addEventListener('mousedown', function (e) { if (e && e.shiftKey && dpIsCheck(e) && typeof e.preventDefault === 'function') e.preventDefault(); });
}
function dsBar() {
  var bar = document.getElementById('ds-bar');
  if (!bar || bar.className !== 'ds-bar') return; // (not on the Devices page)
  var n = dsSel.size;
  bar.style.display = n ? '' : 'none';
  var c = document.getElementById('ds-count');
  if (c) {
    c.innerHTML = i18nEsc(T('picker.multi.selected', { count: n })) +
      (dsHidden ? ' <span class="ds-hidden">' + i18nEsc(T('devices.select.hidden', { count: dsHidden })) + '</span>' : '');
    c.title = Array.from(dsSel.values()).map(reachDevName).join(', ');
  }
}
function dsClear() {
  dsSel = new Map(); dsAnchor = null; dsHidden = 0;
  if (typeof currentRoute !== 'undefined' && currentRoute === 'devices' && typeof refreshDevicesTable === 'function') refreshDevicesTable();
  dsBar();
}

// ── Opening ──
// "Connect" (mosaic false) or "Open as mosaic": the "+" list's flow on this
// selection, the tabs opened this page's way (dsPlan).
function dsConnect(mosaic) {
  if (!dsSel.size) return Promise.resolve('none');
  return dpConnect({ sel: dsSel, confirmKey: 'devices.select.confirmMany', done: dsClear,
    plan: function (jobs) { return dsPlan(jobs, !!mosaic); } });
}
// A job's key: its device's (ObliReach), its tool's ("~<kind>").
function dsJobKey(job) { return job.kind === 'remote' ? devKey(job.dev) : toolKeyOf(job.dev, job.kind); }
function dsTabOf(job) { return job.kind === 'remote' ? reachTabOfDevice(devKey(job.dev)) : toolTabByKey(dsJobKey(job)); }
// o (optional): another kind of tab (the VMs page's consoles, vms_page.go):
// { key(job), open(job, into) (a session window), here(job), tabOf(job) }.
function dsPlan(jobs, mosaic, o) {
  o = o || {};
  if (reachDetachOn()) return dsPlanWindow(jobs, mosaic, o);
  // Here: every tab in this window; the mosaic once they are all there.
  var here = o.here || dsLaunchHere, tabOf = o.tabOf || dsTabOf;
  var tabs = [];
  return {
    launch: async function (job) {
      var r = await here(job);
      var t = tabOf(job);
      if (t && tabs.indexOf(t) < 0) tabs.push(t);
      return r;
    },
    after: function () { if (mosaic && tabs.length > 1) tgCreate(tabs, tabs[0]); },
  };
}
// One tab here (the main window), through the single click's own gates.
async function dsLaunchHere(job) {
  var dev = job.dev, k = job.kind;
  if (k !== 'remote') return toolOpen(dev, k, 'here', { notify: devPickerNote });
  var ex = await reachLaunchExisting(devKey(dev));
  if (ex) return ex;
  if (!devReachOnline(dev)) {
    devPickerNote(T(dev.oblireach && dev.oblireach.installed ? 'viewer.launchOffline' : 'viewer.launchNotInstalled', { device: reachDevName(dev) }));
    return 'offline';
  }
  var c = await reachPickSession(dev);
  if (c === null) return 'cancelled';
  reachWinConnect(dev, c, false);
  reachLaunchRecent(dev);
  return 'here';
}
// "Detach by default": every tab in one session window (the first opens it,
// the others join it: into), a mosaic there for "Open as mosaic". The note
// (tab_groups.go tgMovesRead) tells that window which tabs belong together.
function dsPlanWindow(jobs, mosaic, o) {
  o = o || {};
  var keyOf = o.key || dsJobKey, open = o.open || dsOpenWin;
  // "A window per device" (Settings > Sessions): Connect opens each on its own.
  if (!mosaic && reachMultiWindowMode() === 'each') return { launch: function (job) { return open(job, ''); } };
  var token = '', first = '', waited = false, alone = false;
  if (jobs.length > 1) {
    token = tgMoveToken();
    var keys = jobs.map(keyOf);
    var mv = tgMovesRead();
    mv[token] = { keys: keys, focus: keys[0], broadcast: false, ts: Date.now(), ready: false, flat: !mosaic };
    tgMovesWrite(mv);
  }
  var note = function (fn) {
    if (!token) return;
    var m = tgMovesRead(), rec = m[token];
    if (!rec) return;
    fn(rec);
    rec.ts = Date.now(); // (kept alive while prompts wait for the operator)
    if (rec.keys.length) m[token] = rec; else delete m[token];
    tgMovesWrite(m);
  };
  return {
    launch: async function (job) {
      var key = keyOf(job);
      note(function () {});
      if (first && !waited) {
        waited = true;
        alone = !(await tgMoveWaitReady(token)); // (its first tab never came: the others open on their own)
      }
      var r = await open(job, first && !alone ? first : '');
      if (r === 'opening' && !first) first = key;
      else if (r !== 'opening' && r !== 'moved') {
        // Not coming to the batch's window: nobody waits for it.
        note(function (rec) {
          rec.keys = rec.keys.filter(function (x) { return x !== key; });
          if (rec.focus === key) rec.focus = rec.keys[0] || '';
        });
      }
      return r;
    },
  };
}
// One tab in a session window (into: the key whose window it joins; '': a
// window of its own). The gates of the single click: ObliReach online and
// its Windows session picked, a tool enabled and its privacy code given.
async function dsOpenWin(job, into) {
  var dev = job.dev, k = job.kind, key = dsJobKey(job);
  var ex = await reachLaunchExisting(key);
  if (ex) return ex;
  var session = -1;
  if (k === 'remote') {
    if (!devReachOnline(dev)) {
      devPickerNote(T(dev.oblireach && dev.oblireach.installed ? 'viewer.launchOffline' : 'viewer.launchNotInstalled', { device: reachDevName(dev) }));
      return 'offline';
    }
    var c = await reachPickSession(dev);
    if (c === null) return 'cancelled';
    if (typeof c === 'number' && c >= 0) session = c;
  } else {
    var st = toolState(dev, k);
    if (!st.show || !st.enabled) { devPickerNote(st.reason || T('tools.reason.agentOffline')); return 'blocked'; }
    if (!(await privacyEnsure(dev, toolFeature(k), { notify: devPickerNote }))) return 'cancelled';
    if (k === 'cmd' || k === 'powershell') toolsSetShellLast(k);
  }
  var title = String(dev.hostname || dev.id) + (k === 'remote' ? '' : ' · ' + T(toolLabelKey(k)));
  var opts = { session: session, viewOnly: false, profile: devPid(dev) };
  if (k !== 'remote') opts.kind = k;
  if (into) opts.into = into;
  var r = '', until = Date.now() + TG_MOVE_WAIT_MS;
  for (;;) {
    try { r = String(await reachNative('reachOpenWindow', String(dev.id), title, opts)); }
    catch (e) { devPickerNote(reachDevName(dev) + ': ' + T('viewer.windowOpenFailed', { error: reachErrText(e) })); return 'error'; }
    if (r !== 'pending' || Date.now() >= until) break;
    await tgSleep(TG_MOVE_POLL_MS); // (its window is still being created)
  }
  if (r === 'pending') { devPickerNote(reachDevName(dev) + ': ' + T('viewer.windowOpenFailedNoReason')); return 'error'; }
  if (k === 'remote' && (r === 'opening' || r === 'moved')) {
    reachDetachedIds.add(key);
    if (!reachDetachedInfo[key]) reachDetachedInfo[key] = { session: session, viewOnly: false };
  }
  reachLaunchRecent(dev);
  return r;
}
`
