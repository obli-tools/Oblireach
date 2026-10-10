package main

// picker_multi.go — the "+" list of a session window (devPicker*, ui.go):
// several devices connected at once.
//
// A click on a row or on one of its buttons still opens that one device at
// once (devPickerLaunch, agent_tools.go). Next to it, each row has a box to
// tick, and each server / tenant / group header a three-state box that ticks
// (or unticks) every connectable device listed under it, its sub-groups
// included, as the current search and filters list them (folded or not).
//
// Under a Hyper-V host, its VMs (vms_page.go dpVmRows) have a box too: a
// VM's one service is its console ('vm'), opened here like a click on it
// (dpVmLaunch). A header's box ticks the devices under it, never their VMs
// (a server's box would otherwise open every console of its hosts).
//
// Connectable: at least one service opens right now, by the very predicates
// of the row's buttons: ObliReach when its agent is online (reachWinAddDevice's
// gate), CMD / PowerShell / SSH when toolState says the button is enabled
// (agent reachable, the right, not legacy, ConPTY, privacy with a code: its
// unlock asked by toolOpen as for a click). The explorer is not offered here.
// The others' boxes are greyed. Only the devices this list holds can be
// ticked: it lists what the server lets this user reach, nothing more.
//
// Selection rule: it keeps only the devices still listed AND still
// connectable; each redraw of the list (a filter, a search, a reload) drops
// the others, so the footer's count is always what "Connect" opens. It is
// cleared when the list opens and once "Connect" runs.
//
// "Connect": the devices grouped by identical service set; a set with one
// service opens it without asking, the sets with several are asked in one
// dialog (one line per set, a box per service: several boxes, several tabs
// per device; ObliReach ticked by default, else the first). Above
// DP_MANY_TABS tabs, a confirmation. Then each tab opens in turn, through
// the single click's own path (devPickerLaunch: the Windows session picker,
// the privacy code, a tab already open here or elsewhere brought to the
// front, multi-server routing), DP_GAP_MS apart; what could not open is said
// once at the end (the notes the single path would have shown in the list).
//
// The same boxes, dialog and openings serve the main window's Devices page
// (devices_select.go): its own selection and its own way to open the tabs
// (the sel arguments, dpConnect's options).
//
// Injected verbatim after the viewer library (buildUI, not through Sprintf):
// no backtick. pickMultiCSS / pickMultiJS carry the Devices page's part too.

const pickMultiCSS = pickMultiBaseCSS + devSelCSS
const pickMultiJS = pickMultiBaseJS + devSelJS

const pickMultiBaseCSS = `
/* ── "+" list: several devices at once (picker_multi.go) ── */
.dp-check{display:inline-flex;align-items:center;justify-content:center;width:16px;height:16px;flex-shrink:0;border-radius:5px;background:rgb(var(--ink) / .12);color:transparent;cursor:pointer;transition:background .1s}
.dp-check:hover{background:rgb(var(--accent-rgb) / .28)}
.dp-check:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.dp-check.on,.dp-check.mixed{background:var(--accent-fill);color:var(--on-accent)}
.dp-check.off{opacity:.35;cursor:default}
.dp-check.off:hover{background:rgb(var(--ink) / .12)}
.dp-check svg{width:11px;height:11px;display:block}
.dp-foot{display:flex;align-items:center;gap:10px;padding:10px 16px;background:var(--bg2);flex-shrink:0}
.dp-foot-count{font-size:12px;font-weight:600;color:var(--text)}
.dp-foot .spacer{flex:1}
.dp-foot .btn-primary{width:auto}
.modal-overlay.dpm-overlay{z-index:360}
.dpm-overlay .modal-box{width:520px}
.dpm-list{display:flex;flex-direction:column;gap:8px;max-height:50vh;overflow-y:auto}
.dpm-set{display:flex;flex-direction:column;gap:8px;padding:10px 13px;border-radius:10px;background:rgb(var(--ink) / .06)}
.dpm-set-name{font-size:12px;font-weight:600;color:var(--text);overflow:hidden;text-overflow:ellipsis}
.dpm-set.single{background:transparent;padding:4px 13px}
.dpm-set.single .dpm-set-name{font-weight:400;color:var(--muted)}
.dpm-svcs{display:flex;flex-wrap:wrap;gap:6px}
.dpm-svc{display:inline-flex;align-items:center;gap:7px;height:28px;padding:0 11px 0 8px;border-radius:8px;border:none;background:rgb(var(--ink) / .06);color:var(--muted);font-size:12px;font-weight:600;cursor:pointer;transition:background .1s,color .1s}
.dpm-svc:hover{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.dpm-svc:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.dpm-svc.on{background:rgb(var(--accent-rgb) / .14);color:var(--text)}
`

const pickMultiBaseJS = `
// ── "+" list: several devices at once (picker_multi.go) ─────────────────────
var DP_MANY_TABS = 20; // more tabs than this: asked first
var DP_GAP_MS = 250;   // between two openings (the proxy and the server breathe)
var DP_ICON = {
  check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="5 12.5 10 17.5 19 7"/></svg>',
  dash: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round"><line x1="6" y1="12" x2="18" y2="12"/></svg>',
};
var dpSel = new Map(); // devKey -> device, in the list's order
var dpBatch = null;    // { notes } while "Connect" opens its tabs

// What opens on a device right now, in this order: 'remote' (ObliReach),
// 'cmd', 'powershell', 'ssh'. The row buttons' own predicates. A local
// connection (localconn_tools.go): its terminal ('ssh') or its picture
// ('rdp'), unless its host was found unreachable (its ▶ greyed). A Hyper-V
// VM listed under its host (vms_page.go dpVmRows): 'vm', its console, when
// it opens now (vmsOpenable: running or paused, its host's gates).
function dpServices(dev) {
  if (!dev) return [];
  if (typeof devIsVm === 'function' && devIsVm(dev)) return dev._vmEntry && vmsOpenable(dev._vmEntry) ? ['vm'] : [];
  if (typeof devIsLocal === 'function' && devIsLocal(dev)) {
    var lk = cxnIsSsh(dev) ? 'ssh' : 'rdp';
    return devUsable(dev) && toolState(dev, lk).enabled ? [lk] : [];
  }
  var out = [];
  if (devReachOnline(dev)) out.push('remote');
  devShellKinds(dev).forEach(function (k) { if (toolState(dev, k).enabled) out.push(k); });
  return out;
}
function dpConnectable(dev) { return dpServices(dev).length > 0; }
function dpKindName(k) { return k === 'remote' ? T('picker.multi.reach') : k === 'vm' ? T('picker.multi.vmConsole') : T(toolNameKey(k)); }

// The box: state 'all' (ticked), 'some' (a header: part of it), 'none'.
// tip: its tooltip when another list reuses the box (the vault's machine
// picker, vault_devices_ui.go); by default this list's.
function dpCheckHtml(state, off, group, tip) {
  tip = tip || (off ? T('picker.multi.unavailable') : group ? T('picker.multi.groupTip') : T('picker.multi.selectTip'));
  return '<span class="dp-check' + (state === 'all' ? ' on' : state === 'some' ? ' mixed' : '') + (off ? ' off' : '') +
    '" role="checkbox" data-dp-check="1" tabindex="' + (off ? '-1' : '0') + '" aria-checked="' +
    (state === 'all' ? 'true' : state === 'some' ? 'mixed' : 'false') + '"' + (off ? ' aria-disabled="true"' : '') +
    ' title="' + i18nEsc(tip) + '">' + (state === 'some' ? DP_ICON.dash : DP_ICON.check) + '</span>';
}
// sel (optional): the selection it reads (the Devices page's, devices_select.go).
function dpGroupState(devs, sel) {
  sel = sel || dpSel;
  var c = (devs || []).filter(dpConnectable), n = 0;
  c.forEach(function (d) { if (sel.has(devKey(d))) n++; });
  return { total: c.length, state: !c.length || !n ? 'none' : n === c.length ? 'all' : 'some' };
}
function dpDevCheck(dev) { return dpCheckHtml(dpSel.has(devKey(dev)) ? 'all' : 'none', !dpConnectable(dev), false); }
function dpGroupCheck(devs) { var g = dpGroupState(devs); return dpCheckHtml(g.state, !g.total, true); }
function dpToggleDev(dev) {
  if (!dpConnectable(dev)) return;
  var k = devKey(dev);
  if (dpSel.has(k)) dpSel.delete(k); else dpSel.set(k, dev);
}
// A header: everything connectable under it ticked, or (all of it ticked
// already) unticked.
function dpToggleMany(devs, sel) {
  sel = sel || dpSel;
  var c = (devs || []).filter(dpConnectable);
  if (!c.length) return;
  var all = c.every(function (d) { return sel.has(devKey(d)); });
  c.forEach(function (d) { if (all) sel.delete(devKey(d)); else sel.set(devKey(d), d); });
}
// The list was drawn again (devs: every device it lists, folded or not):
// the selection keeps those still listed and connectable, in its order,
// with their fresh records.
function dpPrune(devs) {
  var next = new Map();
  (devs || []).forEach(function (d) {
    var k = devKey(d);
    if (dpSel.has(k) && !next.has(k) && dpConnectable(d)) next.set(k, d);
  });
  dpSel = next;
}
function dpSelClear() { dpSel = new Map(); dpFooter(); }
function dpFooter() {
  var f = document.getElementById('dp-foot');
  if (!f) return;
  var n = dpSel.size;
  f.style.display = n ? '' : 'none';
  var c = document.getElementById('dp-sel-count');
  if (c) c.textContent = T('picker.multi.selected', { count: n });
}
// The hooks of the "+" list's view (devViewPicker, ui.go).
var dpSelectHooks = { dev: dpDevCheck, group: dpGroupCheck, toggleDev: dpToggleDev, toggleMany: dpToggleMany };
// A box was ticked from the keyboard: the focus back on it once redrawn.
// tb (optional): the list's body (the Devices page's); by default this list's.
function dpRefocus(attr, val, tb) {
  tb = tb || document.getElementById('dp-tbody');
  if (!tb || typeof tb.querySelectorAll !== 'function') return;
  var rows = tb.querySelectorAll('tr') || [];
  for (var i = 0; i < rows.length; i++) {
    var r = rows[i];
    if (r && r.dataset && r.dataset[attr] === val) {
      var c = typeof r.querySelector === 'function' ? r.querySelector('.dp-check') : null;
      if (c && typeof c.focus === 'function') { try { c.focus(); } catch (e) {} }
      return;
    }
  }
}
// The target of an event is a box (its own click, not the row's).
function dpIsCheck(e) {
  var t = e && e.target, c = t && typeof t.closest === 'function' ? t.closest('.dp-check') : null;
  return !!(c && c.dataset && c.dataset.dpCheck !== undefined);
}

// The ticked devices grouped by identical service set, in the list's order:
// [{ id, kinds, devs }].
function dpPlan(devs) {
  var sets = [], byId = {};
  (devs || []).forEach(function (d) {
    var ks = dpServices(d);
    if (!ks.length) return;
    var id = ks.join(',');
    if (!byId[id]) { byId[id] = { id: id, kinds: ks, devs: [] }; sets.push(byId[id]); }
    byId[id].devs.push(d);
  });
  return sets;
}
function dpDefaultPick(kinds) { return kinds.length > 1 ? [kinds.indexOf('remote') >= 0 ? 'remote' : kinds[0]] : kinds.slice(); }
// What opens on each set: resolves to one list of kinds per set (in the
// order of sets), or null (cancelled). Asked only when a set has a choice.
function dpAsk(sets) {
  var picks = sets.map(function (s) { return dpDefaultPick(s.kinds); });
  if (!sets.some(function (s) { return s.kinds.length > 1; })) return Promise.resolve(picks);
  return new Promise(function (resolve) {
    var ov = document.createElement('div'); ov.className = 'modal-overlay visible dpm-overlay'; ov.id = 'dpm-overlay';
    var box = document.createElement('div'); box.className = 'modal-box';
    box.setAttribute('role', 'dialog'); box.setAttribute('aria-label', T('picker.multi.dialogTitle'));
    var head = document.createElement('div'); head.className = 'modal-head';
    var ico = document.createElement('div'); ico.className = 'icon'; ico.innerHTML = TOOLS_ICON.monitor.replace(/width="13" height="13"/, 'width="18" height="18"');
    var h = document.createElement('h3'); h.textContent = T('picker.multi.dialogTitle');
    var x = document.createElement('button'); x.className = 'close'; x.id = 'dpm-close'; x.title = T('common.cancel');
    x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    head.appendChild(ico); head.appendChild(h); head.appendChild(x);
    var desc = document.createElement('p'); desc.className = 'desc'; desc.id = 'dpm-desc'; desc.textContent = T('picker.multi.dialogDesc');
    var list = document.createElement('div'); list.className = 'dpm-list';
    var chips = []; // [set index, kind, button]
    var acts = document.createElement('div'); acts.className = 'modal-actions';
    var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'dpm-cancel'; cancel.textContent = T('common.cancel');
    var go = document.createElement('button'); go.className = 'btn-primary'; go.id = 'dpm-go'; go.style.width = 'auto';
    var paint = function () {
      chips.forEach(function (c) {
        var on = picks[c[0]].indexOf(c[1]) >= 0;
        c[2].className = 'dpm-svc' + (on ? ' on' : '');
        c[2].setAttribute('aria-checked', on ? 'true' : 'false');
        c[2].innerHTML = '<span class="dp-check' + (on ? ' on' : '') + '" aria-hidden="true">' + DP_ICON.check + '</span><span>' + i18nEsc(dpKindName(c[1])) + '</span>';
      });
      var n = 0;
      sets.forEach(function (s, i) { n += s.devs.length * picks[i].length; });
      go.textContent = T('picker.multi.open', { count: n });
      go.disabled = !n;
    };
    // The sets with a choice first, then those that open without one.
    var order = sets.map(function (s, i) { return i; });
    order.sort(function (a, b) { return (sets[a].kinds.length > 1 ? 0 : 1) - (sets[b].kinds.length > 1 ? 0 : 1) || a - b; });
    order.forEach(function (i) {
      var s = sets[i];
      var row = document.createElement('div'); row.className = 'dpm-set' + (s.kinds.length > 1 ? '' : ' single'); row.id = 'dpm-set-' + i;
      var name = document.createElement('div'); name.className = 'dpm-set-name';
      name.textContent = s.kinds.length > 1
        ? T('picker.multi.setLine', { count: s.devs.length, services: s.kinds.map(dpKindName).join(', ') })
        : T('picker.multi.single', { count: s.devs.length, service: dpKindName(s.kinds[0]) });
      name.title = s.devs.map(reachDevName).join(', ');
      row.appendChild(name);
      if (s.kinds.length > 1) {
        var wrap = document.createElement('div'); wrap.className = 'dpm-svcs';
        s.kinds.forEach(function (k) {
          var b = document.createElement('button'); b.id = 'dpm-' + i + '-' + k; b.dataset.kind = k;
          b.setAttribute('role', 'checkbox');
          b.addEventListener('click', function () {
            var p = picks[i], at = p.indexOf(k);
            if (at >= 0) p.splice(at, 1); else p.push(k);
            p.sort(function (a, c) { return s.kinds.indexOf(a) - s.kinds.indexOf(c); });
            paint();
          });
          chips.push([i, k, b]);
          wrap.appendChild(b);
        });
        row.appendChild(wrap);
      }
      list.appendChild(row);
    });
    acts.appendChild(cancel); acts.appendChild(go);
    box.appendChild(head); box.appendChild(desc); box.appendChild(list); box.appendChild(acts);
    ov.appendChild(box);
    var done = false;
    var finish = function (v) {
      if (done) return;
      done = true;
      ov.classList.remove('visible');
      if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
      resolve(v);
    };
    x.addEventListener('click', function () { finish(null); });
    cancel.addEventListener('click', function () { finish(null); });
    go.addEventListener('click', function () { if (!go.disabled) finish(picks.map(function (p) { return p.slice(); })); });
    ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) finish(null); }); // the backdrop
    ov.addEventListener('keydown', function (e) {
      if (e && e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); finish(null); }
    });
    paint();
    document.body.appendChild(ov);
    try { go.focus(); } catch (e) {}
  });
}

// "Connect": what each ticked device opens, then every tab in turn.
// o (optional: the Devices page's batch, devices_select.go): { sel (its
// selection), confirmKey (the confirmation's text), done() (instead of
// clearing and closing this list), plan(jobs) -> { launch(job, i), after(jobs) }
// (how its tabs open: a window of their own, a mosaic) }.
async function dpConnect(o) {
  o = o || {};
  if (dpBatch) return 'busy';
  var devs = Array.from((o.sel || dpSel).values());
  var sets = dpPlan(devs);
  if (!sets.length) return 'none';
  var picks = await dpAsk(sets);
  if (!picks) return 'cancelled';
  var bySet = {};
  sets.forEach(function (s, i) { bySet[s.id] = picks[i]; });
  var jobs = [];
  devs.forEach(function (d) {
    var ks = bySet[dpServices(d).join(',')] || [];
    ks.forEach(function (k) { jobs.push({ dev: d, kind: k }); });
  });
  if (!jobs.length) return 'none';
  if (jobs.length > DP_MANY_TABS && !confirm(T(o.confirmKey || 'picker.multi.confirmMany', { count: jobs.length }))) return 'cancelled';
  if (o.done) o.done();
  else {
    dpSelClear();
    if (typeof devPickerNote === 'function') devPickerNote('');
    if (typeof devPickerClose === 'function') devPickerClose(); // (the prompts and the tabs show over the window)
  }
  var how = o.plan ? o.plan(jobs) : null;
  var r = await dpRun(jobs, how && how.launch);
  if (how && how.after) await how.after(jobs, r);
  return r;
}
// Each job through the single click's path (devPickerLaunch: Reach for
// 'remote', the tool otherwise), one after the other. launch (optional):
// another path (the Devices page's, devices_select.go).
async function dpRun(jobs, launch) {
  launch = launch || function (j) { return devPickerLaunch(j.dev, j.kind); };
  dpBatch = { notes: [] };
  var notes;
  try {
    for (var i = 0; i < jobs.length; i++) {
      if (i && DP_GAP_MS > 0) await new Promise(function (r) { setTimeout(r, DP_GAP_MS); });
      try { await launch(jobs[i], i); }
      catch (e) { dpBatch.notes.push(reachDevName(jobs[i].dev) + ': ' + reachErrText(e)); }
    }
  } finally {
    notes = dpBatch.notes;
    dpBatch = null;
  }
  if (notes.length) reachToast(T('picker.multi.failed', { count: notes.length, errors: notes.join(' · ') }), 'warn');
  return notes.length ? 'partial' : 'done';
}
`
