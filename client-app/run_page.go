package main

// run_page.go — the Run page (nav rail "Run", under Devices with Chat and
// Files): one script on several devices at once, its results followed live.
//
// Setup: the Devices list (same rows, tree by server / tenant / group,
// search, state and OS filters: devViewFill) with a box on each row and each
// header (a click on a row selects that device alone, its box adds it); on
// the right the scripts the selected devices can run, the chosen one's
// preview (the whole script in a large view), its parameters form (the
// Scripts panel's: scripts_panel.go) and Run.
//
// Several servers and tenants (multi-server mode, a master session): a script
// runs where it exists. The selection is split by (server, tenant): each part
// runs with its server session on that tenant, exactly as the Scripts panel
// does for one device (Obliance runs a script on the devices of the session's
// tenant only). The script list offers every script of those parts, one line
// per script: the same script shared by the master tenant into several
// tenants (fan-out) is the same row on its server (same uuid), and a script
// carrying the same uuid on two servers (exported from one, imported on the
// other) is offered once and runs as each server's own copy. A script is
// never matched by its name alone: the devices of a part where the chosen
// script does not exist are said before the run ("not available for ...")
// and listed as not run in the results.
//
// Results: a summary (devices, running / OK / failed / offline-queued / not
// run, progress) and one row per device (its status with the exit code,
// duration, STDOUT / STDERR in a large view with copy). A row opens the
// device's console beside the list (the Scripts panel's console text). The
// executions are read again (GET /api/executions/batches/:batchId, one per
// part; GET /api/executions/:id on a server without batches) every 2 s, then
// 5 s after 2 min, 15 s after 10 min, up to RUN_POLL_MAX_MS: a device offline
// stays "queued" until it comes back (the server keeps its command), rows
// updated in place (dom_patch.go). "Re-run failed" selects the failed ones
// again with the same script and values (a secret is asked again), Export
// writes the results (CSV / JSON). A refusal of the server (403: the
// 'execute' capability, the tenant) is shown as it said it.
//
// Runs are kept while the app runs (RUN_MAX_JOBS, a tab each), forgotten at
// a sign-out or a server switch (runForget).
//
// Injected verbatim after the Scripts panel (buildUI, not through Sprintf:
// '%' is safe): no backtick here.

const runPageCSS = `
/* ── Run page (run_page.go) ── */
.run-page{flex:1;display:flex;flex-direction:column;min-height:0;min-width:0;overflow:hidden}
.run-page .page-header .breadcrumb:empty{display:none}
.run-tabbar{display:flex;align-items:flex-start;gap:6px;padding:0 24px 10px;flex-shrink:0}
.run-page .chat-tabs{flex:0 1 auto;min-width:0;flex-wrap:wrap;gap:6px;padding:0;overflow:visible}
.run-page .chat-tab{font-size:12px;padding:7px 9px 7px 11px;max-width:260px}
.run-page .chat-tab>span:nth-child(2){overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.run-setup{flex:1;min-height:0;display:flex;gap:16px;padding:4px 24px 20px}
.run-left{flex:1;min-width:0;display:flex;flex-direction:column;min-height:0}
.run-left .panel{flex:1;min-height:0;margin-bottom:0;display:flex;flex-direction:column}
.run-left .panel-body{flex:1;min-height:0;overflow:auto}
.run-selbar{display:flex;align-items:center;gap:10px;margin-top:10px;padding:9px 14px;border-radius:12px;background:var(--bg2);flex-shrink:0;font-size:12px;color:var(--muted)}
.run-selbar.on{background-image:linear-gradient(rgb(var(--accent-rgb) / .09),rgb(var(--accent-rgb) / .09));color:var(--text);font-weight:600}
.run-selbar .spacer{flex:1}
.run-right{width:360px;flex-shrink:0;display:flex;flex-direction:column;gap:12px;min-height:0}
.run-sec{background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow);padding:12px 14px;display:flex;flex-direction:column;gap:8px;min-height:0}
.run-sec.scripts{flex:0 1 auto;max-height:42%}
.run-sec.chosen{flex:1 1 auto;overflow:hidden}
.run-sec-title{font-size:11px;font-weight:700;letter-spacing:.04em;text-transform:uppercase;color:var(--muted2)}
.run-sec input.run-q{background:var(--input-bg);border:none;border-radius:9px;padding:7px 10px;font-size:12px;color:var(--text);outline:none;font-family:inherit}
.run-sec input.run-q:focus-visible{box-shadow:0 0 0 2px var(--accent)}
.run-scripts{flex:1;min-height:40px;overflow-y:auto;display:flex;flex-direction:column;gap:2px}
.run-script{display:flex;align-items:center;gap:8px;padding:7px 10px;border-radius:9px;cursor:pointer;font-size:12px;color:var(--text)}
.run-script:hover{background:rgb(var(--ink) / .05)}
.run-script:focus-visible{outline:none;box-shadow:inset 0 0 0 2px var(--accent)}
.run-script.active{background:rgb(var(--accent-rgb) / .13);color:var(--accent-l)}
.run-script .nm{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:500}
.run-script .sub{font-size:10.5px;color:var(--muted2);flex-shrink:0}
.run-script .part{font-size:10px;font-weight:600;padding:2px 7px;border-radius:8px;background:rgb(var(--warn-rgb) / .12);color:var(--warn);flex-shrink:0}
.run-empty{font-size:12px;color:var(--muted2);padding:6px 2px;line-height:1.45}
.run-chosen-body{flex:1;min-height:0;display:flex;flex-direction:column;gap:8px;overflow-y:auto}
.run-chosen-body .sc-preview{max-height:calc(8 * 1.45em + 20px)}
.run-avail{font-size:11.5px;line-height:1.45;padding:7px 11px;border-radius:10px;background:rgb(var(--warn-rgb) / .12);color:var(--warn);flex-shrink:0}
.run-avail:empty{display:none}
.run-err{font-size:11.5px;color:var(--danger);flex-shrink:0}
.run-err:empty{display:none}
.run-sec.chosen>.btn-primary{flex-shrink:0;padding:10px}
.run-results{flex:1;min-height:0;display:flex;flex-direction:column;gap:12px;padding:4px 24px 20px}
.run-sum{background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow);padding:12px 16px;display:flex;flex-direction:column;gap:9px;flex-shrink:0}
.run-sum-top{display:flex;align-items:center;gap:10px;min-width:0;flex-wrap:wrap}
.run-sum-top h2{font-size:14px;font-weight:650;color:var(--text);min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.run-sum-top .when{font-size:11px;color:var(--muted)}
.run-sum-top .spacer{flex:1}
.run-sum-top .btn-sm:disabled{opacity:.45;cursor:default}
.run-counts{display:flex;gap:6px;flex-wrap:wrap}
.run-count{display:inline-flex;align-items:center;gap:6px;padding:4px 10px;border-radius:9px;background:rgb(var(--ink) / .05);font-size:11.5px;color:var(--muted)}
.run-count b{font-size:12.5px;color:var(--text)}
.run-count.ok b{color:var(--success)}
.run-count.ko b{color:var(--danger)}
.run-count.run b{color:var(--info)}
.run-count.off b{color:var(--warn)}
.run-progress{height:6px;border-radius:3px;background:rgb(var(--ink) / .08);overflow:hidden}
.run-progress>div{height:100%;width:0;background:var(--info);border-radius:3px;transition:width .3s ease}
.run-progress.done>div{background:var(--success)}
.run-progress-txt{font-size:11px;color:var(--muted)}
.run-notice{font-size:12px;padding:8px 12px;border-radius:10px;background:rgb(var(--danger-rgb) / .1);color:var(--danger)}
.run-notice.info{background:rgb(var(--warn-rgb) / .12);color:var(--warn)}
.run-notice:empty{display:none}
.run-body{flex:1;min-height:0;display:flex;gap:12px}
.run-body .panel{flex:1;min-width:0;margin-bottom:0;display:flex;flex-direction:column}
.run-body .panel-body{flex:1;min-height:0;overflow:auto}
.run-body tr.run-row{cursor:pointer}
.run-body tr.run-row.sel{background:rgb(var(--accent-rgb) / .1)}
.run-body tr.run-row:focus-visible{outline:none;box-shadow:inset 0 0 0 2px var(--accent)}
.run-dev{display:flex;align-items:center;gap:7px;min-width:0}
.run-dev .nm{font-weight:500;color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.run-dev .where{font-size:10.5px;color:var(--muted2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.run-dev .srv-dot{width:8px;height:8px;border-radius:50%;flex-shrink:0}
.tag-chip.bad{background:rgb(var(--danger-rgb) / .12);color:var(--danger)}
.tag-chip.run{background:rgb(var(--info-rgb) / .14);color:var(--info)}
.run-outs{display:flex;gap:5px;justify-content:flex-end}
.run-outs .btn-sm{padding:4px 9px;font-family:ui-monospace,Consolas,monospace;font-size:10.5px}
.run-outs .btn-sm:disabled{opacity:.35;cursor:default}
.run-console{width:400px;flex-shrink:0;display:flex;flex-direction:column;gap:8px;background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow);padding:12px 14px;min-height:0}
.run-console.hide{display:none}
.run-console-head{display:flex;align-items:center;gap:8px;min-width:0}
.run-console-head .ttl{flex:1;min-width:0;display:flex;flex-direction:column;gap:1px}
.run-console-head .ttl b{font-size:13px;color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.run-console-head .ttl span{font-size:11px;color:var(--muted)}
.run-console .exec-output{flex:1;min-height:80px;max-height:none;overflow:auto;margin:0}
@media (max-width:1100px){.run-setup{flex-direction:column;overflow-y:auto}.run-right{width:auto}.run-left .panel{min-height:280px}.run-console{width:320px}}
`

const runPageJS = `
// ═════════════════════════════════════════════════════════════════════════
// Run page (run_page.go): a script on several devices
// ═════════════════════════════════════════════════════════════════════════
var RUN_MAX_JOBS = 8;
var RUN_POLL_MAX_MS = 2 * 3600 * 1000;
var runView = null;           // built at its first visit, kept (the same elements at every visit)
var runFilter = { q: '', online: '', os: '' };
var runSearchFolded = new Set();
var runSel = new Map();       // devKey -> device: the devices ticked
var runAnchor = null;         // the key of the box ticked last (Shift: a range from it)
var runRows = [];             // the device rows as listed on screen, in order
var runPick = null;           // the chosen script's identity (runScriptIdent)
var runQ = '';                // the scripts' search
var runForm = null, runFormIdent = null, runFormSig = null; // its parameters form
var runJobs = [];             // the runs (most recent last)
var runShownJob = null;       // the run on screen (null: the setup)
var runJobSeq = 0;
var runConsole = null;        // { job, key }: the console beside the results
var runStarting = false;

// What a run reaches: Obliance devices (not a local connection, a VM
// console, a code session).
function runSelectable(d) {
  if (!d || typeof d !== 'object') return false;
  if (typeof devIsLocal === 'function' && devIsLocal(d)) return false;
  if (typeof devIsVm === 'function' && devIsVm(d)) return false;
  if (typeof devIsSupport === 'function' && devIsSupport(d)) return false;
  return true;
}
function runPageShown() { return !!runView && typeof currentRoute !== 'undefined' && currentRoute === 'run'; }
function runJobById(id) { return runJobs.find(function (j) { return j.id === id; }) || null; }
function runShown() { var j = runShownJob === null ? null : runJobById(runShownJob); return j; }

// ── The page ──
function runPageBuild() {
  var mk = function (tag, cls, text) { return scMk(tag, cls, text); };
  var root = mk('div', 'run-page'); root.id = 'run-page';
  var head = mk('div', 'page-header');
  var h1 = mk('h1', '', T('nav.run')); h1.setAttribute('data-i18n', 'nav.run');
  var crumb = mk('span', 'breadcrumb');
  var actions = mk('div', 'page-actions');
  var refresh = mk('button', 'btn-sm', T('common.refresh')); refresh.setAttribute('data-i18n', 'common.refresh');
  refresh.addEventListener('click', async function () {
    try { await loadOverview(); } catch (e) {}
    runGroups(Array.from(runSel.values())).forEach(function (g) { scriptsRevalidate(g.first, true); });
    runPickerFill();
  });
  actions.appendChild(refresh);
  head.appendChild(h1); head.appendChild(crumb); head.appendChild(actions);
  var tabbar = mk('div', 'run-tabbar');
  var tabs = mk('div', 'chat-tabs'); tabs.setAttribute('role', 'tablist');
  var add = mk('button', 'chat-tab-add');
  add.title = T('run.newRun'); add.setAttribute('data-i18n-title', 'run.newRun'); add.setAttribute('aria-label', T('run.newRun'));
  add.innerHTML = FILES_PLUS_SVG;
  add.addEventListener('click', function () { runShowSetup(); });
  tabbar.appendChild(tabs); tabbar.appendChild(add);

  // Setup: the devices (left), the script and its parameters (right).
  var setup = mk('div', 'run-setup');
  var left = mk('div', 'run-left');
  var bar = mk('div', 'filter-bar');
  var q = mk('input'); q.style.minWidth = '200px';
  q.placeholder = T('devices.searchPlaceholder'); q.setAttribute('data-i18n-ph', 'devices.searchPlaceholder'); q.value = runFilter.q;
  q.addEventListener('input', function (e) { runFilter.q = e.target.value; runSearchFolded = new Set(); runPickerFill(); });
  bar.appendChild(q);
  bar.appendChild(devPickerSelect('', [['', 'devices.filter.allStates'], ['online', 'devices.filter.online'], ['offline', 'devices.filter.offline']], runFilter.online, function (val) { runFilter.online = val; runPickerFill(); }));
  bar.appendChild(devPickerSelect('', [['', 'devices.filter.allOs'], ['windows', 'Windows'], ['linux', 'Linux'], ['macos', 'macOS']], runFilter.os, function (val) { runFilter.os = val; runPickerFill(); }));
  bar.appendChild(mk('span', 'spacer'));
  var hint = mk('span', 'muted-hint');
  var count = mk('span', '', '0'), countLabel = mk('span', '', T('devices.resultsWord', { count: 0 }));
  countLabel.style.marginInlineStart = '.25em';
  hint.appendChild(count); hint.appendChild(countLabel);
  bar.appendChild(hint);
  var list = mk('div', 'panel');
  var pb = mk('div', 'panel-body flush'); pb.style.overflowX = 'auto';
  var table = mk('table', 'dt');
  var thead = mk('thead'); thead.innerHTML = devTableHead();
  var tbody = mk('tbody');
  table.appendChild(thead); table.appendChild(tbody); pb.appendChild(table); list.appendChild(pb);
  if (typeof dsWireTbody === 'function') dsWireTbody(tbody); // (Shift + a box: no text selected)
  var selbar = mk('div', 'run-selbar');
  var selCount = mk('span', '', T('run.noneSelected'));
  selCount.setAttribute('aria-live', 'polite');
  var clear = mk('button', 'btn-sm', T('common.clear')); clear.setAttribute('data-i18n', 'common.clear');
  clear.addEventListener('click', function () { runSel = new Map(); runAnchor = null; runPickerFill(); runSetupRefresh(); });
  selbar.appendChild(selCount); selbar.appendChild(mk('span', 'spacer')); selbar.appendChild(clear);
  left.appendChild(bar); left.appendChild(list); left.appendChild(selbar);

  var right = mk('div', 'run-right');
  var sec1 = mk('div', 'run-sec scripts');
  var t1 = mk('div', 'run-sec-title', T('run.scriptLabel')); t1.setAttribute('data-i18n', 'run.scriptLabel');
  var sq = mk('input', 'run-q'); sq.type = 'text';
  sq.placeholder = T('scripts.mgmt.search'); sq.setAttribute('data-i18n-ph', 'scripts.mgmt.search'); sq.value = runQ;
  sq.addEventListener('input', function (e) { runQ = String(e.target.value || ''); runSetupRefresh(); });
  var slist = mk('div', 'run-scripts'); slist.setAttribute('role', 'listbox'); slist.setAttribute('aria-label', T('run.scriptLabel'));
  var sempty = mk('div', 'run-empty');
  sec1.appendChild(t1); sec1.appendChild(sq); sec1.appendChild(slist); sec1.appendChild(sempty);
  var sec2 = mk('div', 'run-sec chosen');
  var chosen = mk('div', 'run-chosen-body');
  var go = mk('button', 'btn-primary', '▶ ' + T('run.go'));
  go.addEventListener('click', function () { runStart(); });
  sec2.appendChild(chosen); sec2.appendChild(go);
  right.appendChild(sec1); right.appendChild(sec2);
  setup.appendChild(left); setup.appendChild(right);

  // Results: the summary, the rows, the console of the device clicked.
  var results = mk('div', 'run-results hide');
  var sum = mk('div', 'run-sum');
  var sumTop = mk('div', 'run-sum-top');
  var sumTitle = mk('h2'), sumWhen = mk('span', 'when');
  var rerun = mk('button', 'btn-sm', T('run.rerunFailed')); rerun.setAttribute('data-i18n', 'run.rerunFailed');
  rerun.addEventListener('click', function () { var j = runShown(); if (j) runRerunFailed(j); });
  var refreshJob = mk('button', 'btn-sm', T('common.refresh')); refreshJob.setAttribute('data-i18n', 'common.refresh');
  refreshJob.addEventListener('click', function () { var j = runShown(); if (j) { j.pollStopped = false; runPollTick(j); } });
  var csv = mk('button', 'btn-sm', T('run.exportCsv')); csv.setAttribute('data-i18n', 'run.exportCsv');
  csv.addEventListener('click', function () { var j = runShown(); if (j) runExport(j, 'csv'); });
  var json = mk('button', 'btn-sm', T('run.exportJson')); json.setAttribute('data-i18n', 'run.exportJson');
  json.addEventListener('click', function () { var j = runShown(); if (j) runExport(j, 'json'); });
  sumTop.appendChild(sumTitle); sumTop.appendChild(sumWhen); sumTop.appendChild(mk('span', 'spacer'));
  sumTop.appendChild(rerun); sumTop.appendChild(refreshJob); sumTop.appendChild(csv); sumTop.appendChild(json);
  var counts = mk('div', 'run-counts');
  var prog = mk('div', 'run-progress'); var progBar = mk('div'); prog.appendChild(progBar);
  prog.setAttribute('role', 'progressbar'); prog.setAttribute('aria-valuemin', '0'); prog.setAttribute('aria-valuemax', '100');
  var progTxt = mk('div', 'run-progress-txt');
  var notice = mk('div', 'run-notice');
  sum.appendChild(sumTop); sum.appendChild(counts); sum.appendChild(prog); sum.appendChild(progTxt); sum.appendChild(notice);
  var body = mk('div', 'run-body');
  var rpanel = mk('div', 'panel');
  var rpb = mk('div', 'panel-body flush'); rpb.style.overflowX = 'auto';
  var rtable = mk('table', 'dt');
  var rthead = mk('thead');
  var rtbody = mk('tbody');
  rtable.appendChild(rthead); rtable.appendChild(rtbody); rpb.appendChild(rtable); rpanel.appendChild(rpb);
  var cons = mk('div', 'run-console hide');
  cons.setAttribute('role', 'region'); cons.setAttribute('aria-label', T('run.console'));
  var chead = mk('div', 'run-console-head');
  var cttl = mk('div', 'ttl'); var cName = mk('b'); var cSub = mk('span');
  cttl.appendChild(cName); cttl.appendChild(cSub);
  var cCopy = scIconBtn('copy', 'scripts.text.copy');
  cCopy.addEventListener('click', async function () {
    var ok2 = await reachNativeClipboardWrite(String(runView.consOut.textContent || ''));
    reachNotify(T(ok2 ? 'term.copied' : 'term.clipboardFailed'), ok2 ? 'ok' : 'warn');
  });
  var cX = scIconBtn('close', 'common.close');
  cX.addEventListener('click', function () { runConsoleClose(); });
  chead.appendChild(cttl); chead.appendChild(cCopy); chead.appendChild(cX);
  var cOut = mk('div', 'exec-output'); cOut.setAttribute('role', 'log'); cOut.setAttribute('aria-live', 'polite'); cOut.tabIndex = 0;
  cons.appendChild(chead); cons.appendChild(cOut);
  cons.addEventListener('keydown', function (e) { if (e && e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); runConsoleClose(); } });
  body.appendChild(rpanel); body.appendChild(cons);
  results.appendChild(sum); results.appendChild(body);

  root.appendChild(head); root.appendChild(tabbar); root.appendChild(setup); root.appendChild(results);
  runView = { root: root, crumb: crumb, tabbar: tabbar, tabs: tabs, add: add, setup: setup, results: results,
    q: q, tbody: tbody, count: count, countLabel: countLabel, selbar: selbar, selCount: selCount, clear: clear,
    sq: sq, slist: slist, sempty: sempty, chosen: chosen, go: go,
    chosenHead: null, chosenPreview: null, avail: null, err: null, formSlot: null, chosenIdent: null, chosenSig: null,
    sumTitle: sumTitle, sumWhen: sumWhen, rerun: rerun, refreshJob: refreshJob, csv: csv, json: json, counts: counts, prog: prog, progBar: progBar, progTxt: progTxt, notice: notice,
    rthead: rthead, rtbody: rtbody, cons: cons, consName: cName, consSub: cSub, consOut: cOut };
  return runView;
}
// navigateTo('run') (ui.go).
function renderRunPage(pc) {
  var v = runView || runPageBuild();
  pc.appendChild(v.root); // the same elements at every visit: nothing bound twice
  runRender();
}
// The page drawn from its state: the tabs, then the setup or the run in front.
function runRender() {
  var v = runView;
  if (!v) return;
  var job = runShown();
  if (runShownJob !== null && !job) runShownJob = null;
  runTabsRender();
  v.setup.classList.toggle('hide', !!job);
  v.results.classList.toggle('hide', !job);
  v.add.classList.toggle('on', !job);
  v.add.setAttribute('aria-pressed', !job ? 'true' : 'false');
  v.tabbar.classList.toggle('hide', !runJobs.length);
  domText(v.crumb, job ? '' : T('run.hint'));
  if (job) runResultsRefresh(job);
  else { runPickerFill(); runSetupRefresh(); }
}
function runShowSetup() { runShownJob = null; runRender(); }
function runShowJob(job) { runShownJob = job.id; if (!runPageShown()) { navigateTo('run'); return; } runRender(); }
function runTabsRender() {
  var v = runView;
  domKeyed(v.tabs, runJobs.map(function (j) {
    var c = runCounts(j);
    var st = c.active ? 'warn' : c.ko ? 'bad' : 'on';
    var on = runShownJob === j.id;
    return { key: 'j' + j.id, sig: [j.name, st, on ? 1 : 0].join('\u0001'), data: j, build: function () {
      var t = scMk('div', 'chat-tab' + (on ? ' active' : ''));
      t.setAttribute('role', 'tab'); t.setAttribute('aria-selected', on ? 'true' : 'false'); t.tabIndex = 0;
      t.title = j.name;
      t.appendChild(scMk('span', 'ct-dot ' + st));
      t.appendChild(scMk('span', '', j.name));
      var x = scMk('button', 'ct-x', '×');
      x.title = T('run.closeRun'); x.setAttribute('aria-label', x.title);
      x.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); runCloseJob(t._domData || j); });
      t.appendChild(x);
      t.addEventListener('click', function () { runShowJob(t._domData || j); });
      t.addEventListener('keydown', function (e) { if (e && (e.key === 'Enter' || e.key === ' ') && e.target === t) { if (e.preventDefault) e.preventDefault(); runShowJob(t._domData || j); } });
      return t;
    } };
  }));
}
function runCloseJob(job) {
  runStopJob(job);
  runJobs = runJobs.filter(function (j) { return j !== job; });
  if (runConsole && runConsole.job === job) runConsole = null;
  if (runShownJob === job.id) runShownJob = runJobs.length ? runJobs[runJobs.length - 1].id : null;
  runRender();
}
function runStopJob(job) {
  job.closed = true;
  if (job.timer) { clearTimeout(job.timer); job.timer = null; }
}
// Sign-out, server switch: nothing of the previous server or user is kept.
function runForget() {
  runJobs.forEach(runStopJob);
  runJobs = []; runShownJob = null; runConsole = null;
  runSel = new Map(); runAnchor = null; runPick = null;
  if (runForm) runForm.clearSecrets();
  runForm = null; runFormIdent = null; runFormSig = null;
  if (runView) { runView.chosenIdent = null; runView.chosenSig = null; }
  if (runPageShown()) runRender();
}
// A language switch (ui.go i18nRedrawApp): its texts.
function runPageI18n() {
  if (!runView) return;
  i18nApply(runView.root);
  runView.go.textContent = '▶ ' + T('run.go');
  runView.chosenIdent = null; runView.chosenSig = null; // (its header drawn again)
  runView.rtbody.innerHTML = ''; // (its rows' words)
  if (runPageShown()) runRender();
}

// ── The device picker ──
function runPickerView() {
  var v = runView;
  return { tbodyEl: function () { return v && v.tbody; }, countEl: function () { return v && v.count; }, countLabelEl: function () { return v && v.countLabel; },
    filter: function () { return runFilter; }, searchFolded: function () { return runSearchFolded; },
    actions: [], select: runSelectHooks, onTree: runOnTree, refresh: runPickerFill,
    open: function (dev) { runSelectOnly(dev); } };
}
function runPickerFill() { if (runView && !runShown()) devViewFill(runPickerView()); }
// The overview read again (a refresh, a server's devices in): the picker
// on screen follows (ui.go overviewRedraw).
function runPickerRefreshIfShown() { if (runPageShown() && !runShown()) { runPickerFill(); runSetupRefresh(); } }
function runDevCheck(dev) {
  var on = runSelectable(dev);
  return dpCheckHtml(runSel.has(devKey(dev)) ? 'all' : 'none', !on, false, on ? T('run.selectTip') : T('run.notSelectable'));
}
function runGroupState(devs) {
  var c = (devs || []).filter(runSelectable), n = 0;
  c.forEach(function (d) { if (runSel.has(devKey(d))) n++; });
  return { total: c.length, state: !c.length || !n ? 'none' : n === c.length ? 'all' : 'some' };
}
function runGroupCheck(devs) { var g = runGroupState(devs); return dpCheckHtml(g.state, !g.total, true); }
function runToggleDev(dev, e) {
  if (!runSelectable(dev)) return;
  var k = devKey(dev), on = !runSel.has(k);
  var a = (e && e.shiftKey && runAnchor && runAnchor !== k) ? runRows.findIndex(function (d) { return devKey(d) === runAnchor; }) : -1;
  var b = a >= 0 ? runRows.findIndex(function (d) { return devKey(d) === k; }) : -1;
  if (a >= 0 && b >= 0) {
    for (var i = Math.min(a, b); i <= Math.max(a, b); i++) {
      var d = runRows[i];
      if (!runSelectable(d)) continue;
      if (on) runSel.set(devKey(d), d); else runSel.delete(devKey(d));
    }
  } else if (on) runSel.set(k, dev); else runSel.delete(k);
  runAnchor = k;
}
function runToggleMany(devs) {
  var c = (devs || []).filter(runSelectable);
  if (!c.length) return;
  var all = c.every(function (d) { return runSel.has(devKey(d)); });
  c.forEach(function (d) { if (all) runSel.delete(devKey(d)); else runSel.set(devKey(d), d); });
}
var runSelectHooks = { dev: runDevCheck, group: runGroupCheck, toggleDev: runToggleDev, toggleMany: runToggleMany };
// A click on a row: that device alone.
function runSelectOnly(dev) {
  if (!runSelectable(dev)) return;
  runSel = new Map([[devKey(dev), dev]]);
  runAnchor = devKey(dev);
  runPickerFill();
}
// The list was drawn: its rows; the selection keeps the devices the lists'
// data still hold (fresh records), ticked ones hidden by a filter included.
function runOnTree(tree) {
  runRows = (tree && tree.rows || []).filter(function (r) { return r.kind === 'device'; }).map(function (r) { return r.dev; });
  var u = dsUniverse(), next = new Map();
  runSel.forEach(function (d, k) {
    var f = u.devs.get(k);
    if (f) { if (runSelectable(f)) next.set(k, f); }
    else if (u.keep.has(devPid(d))) next.set(k, d);
  });
  runSel = next;
  runSetupRefresh();
}

// ── Where the selection runs: its parts (server, tenant) ──
function runGroups(devs) {
  var by = new Map(), out = [];
  (devs || []).forEach(function (d) {
    if (!runSelectable(d)) return;
    var pid = devPid(d), tid = devTenant(d);
    var k = pid + ':' + (tid || '');
    var g = by.get(k);
    if (!g) { g = { key: k, pid: pid, tid: tid, devs: [], first: d }; by.set(k, g); out.push(g); }
    g.devs.push(d);
  });
  return out;
}
// A part in words: its server (multi-server mode) and its tenant (when known).
function runGroupLabel(g) {
  var bits = [];
  if (typeof serverMode !== 'undefined' && serverMode === 'multi') bits.push(srvName(g.pid));
  var tn = g.tid ? pgTenantName(g.pid, g.tid) : '';
  if (tn) bits.push(tn);
  return bits.filter(Boolean).join(' · ') || srvName(g.pid) || T('common.thisDeviceCap');
}
function runScriptIdent(g, sc) { return sc && sc.uuid ? 'u:' + String(sc.uuid) : 'i:' + g.pid + ':' + String(sc && sc.id); }
// The scripts the parts offer, one entry per script (runScriptIdent): its
// copy in each part, how many selected devices it reaches.
function runCatalog(groups) {
  var items = new Map(), loading = false, total = 0;
  groups.forEach(function (g) {
    total += g.devs.length;
    var list = scriptsFor(g.first);
    if (scriptsLoading(g.first)) loading = true;
    list.forEach(function (sc) {
      if (!sc || sc.id === undefined || sc.id === null) return;
      var id = runScriptIdent(g, sc);
      var it = items.get(id);
      if (!it) { it = { ident: id, sc: sc, name: String(sc.name || ''), byGroup: new Map(), devCount: 0 }; items.set(id, it); }
      if (!it.byGroup.has(g.key)) { it.byGroup.set(g.key, sc); it.devCount += g.devs.length; }
    });
  });
  return { items: items, loading: loading, total: total };
}
// A script list changed (scripts_panel.go scriptsChanged): the setup follows.
function runScriptsChanged() { if (runPageShown() && !runShown()) runSetupRefresh(); }
// The lists of the selection read again when stale (scripts_panel.go).
function runRevalidateShown(maxAge) {
  if (!runPageShown() || runShown()) return;
  runGroups(Array.from(runSel.values())).forEach(function (g) { scriptsRevalidate(g.first, false, maxAge); });
}

// ── The setup's right column ──
function runSetupRefresh() {
  var v = runView;
  if (!v || runShown()) return;
  var devs = Array.from(runSel.values());
  var n = devs.length;
  domText(v.selCount, n ? T('picker.multi.selected', { count: n }) : T('run.noneSelected'));
  v.selbar.classList.toggle('on', n > 0);
  v.clear.disabled = !n;
  var groups = runGroups(devs);
  var cat = runCatalog(groups);
  var q = runQ.trim().toLowerCase();
  var items = Array.from(cat.items.values()).filter(function (it) { return !q || it.name.toLowerCase().indexOf(q) >= 0; });
  items.sort(function (a, b) { return a.name.localeCompare(b.name) || a.ident.localeCompare(b.ident); });
  domKeyed(v.slist, items.map(function (it) {
    var part = it.devCount < cat.total;
    var sub = [it.sc.platform, it.sc.runtime].filter(Boolean).join(' · ');
    return { key: it.ident, sig: [it.name, sub, part ? it.devCount + '/' + cat.total : ''].join('\u0001'), data: it, build: function () {
      var el = scMk('div', 'run-script');
      el.setAttribute('role', 'option'); el.tabIndex = 0;
      el.appendChild(scMk('span', 'nm', it.name));
      if (part) { var p = scMk('span', 'part', it.devCount + '/' + cat.total); p.title = T('run.partialTip', { n: it.devCount, total: cat.total }); el.appendChild(p); }
      if (sub) el.appendChild(scMk('span', 'sub', sub));
      var pick = function () { runPick = (el._domData || it).ident; runSetupRefresh(); };
      el.addEventListener('click', pick);
      el.addEventListener('keydown', function (e) { if (e && (e.key === 'Enter' || e.key === ' ')) { if (e.preventDefault) e.preventDefault(); pick(); } });
      return el;
    } };
  }));
  Array.prototype.forEach.call(v.slist.children || [], function (c) {
    var on = c._domKey === runPick;
    c.classList.toggle('active', on);
    if (typeof c.setAttribute === 'function') c.setAttribute('aria-selected', on ? 'true' : 'false');
  });
  var emptyText = !n ? T('run.pickDevicesFirst') : (!cat.items.size ? (cat.loading ? T('common.loading') : T('run.noScripts')) : (!items.length ? T('scripts.mgmt.empty') : ''));
  domText(v.sempty, emptyText);
  v.sempty.style.display = emptyText ? '' : 'none';
  runChosenRender(groups, cat);
}
// The chosen script: its header and preview, what it does not reach, its
// parameters (kept while it stays chosen), Run.
function runChosenRender(groups, cat) {
  var v = runView;
  var it = runPick ? cat.items.get(runPick) : null;
  var sc = it ? it.sc : null;
  var sig = sc ? scriptDefSig(sc) : '';
  if (v.chosenIdent !== (it ? it.ident : null) || v.chosenSig !== sig) {
    v.chosenIdent = it ? it.ident : null; v.chosenSig = sig;
    v.chosen.innerHTML = '';
    if (!sc) {
      v.chosen.appendChild(scMk('div', 'run-empty', runPick && runSel.size ? T('run.pickedUnavailable') : T('run.pickScript')));
      v.avail = null; v.err = null;
    } else {
      var pc = { windows: 'windows', linux: 'linux', macos: 'macos', all: 'all' };
      var head = scMk('div', 'sc-head');
      var h3 = scMk('h3', '', sc.name || ''); h3.title = String(sc.name || '');
      head.appendChild(h3);
      head.appendChild(scMk('span', 'badge ' + (pc[sc.platform] || 'all'), sc.platform || ''));
      head.appendChild(scMk('span', 'sc-rt', sc.runtime || ''));
      var ex = scIconBtn('expand', 'scripts.preview.expand', 'sc-expand');
      ex.addEventListener('click', function () { scriptsShowWhole(sc); });
      head.appendChild(ex);
      v.chosen.appendChild(head);
      if (sc.description) { var d = scMk('p', 'sc-desc', sc.description); d.title = String(sc.description); v.chosen.appendChild(d); }
      var pre = scMk('pre', 'sc-preview', sc.content || ''); pre.tabIndex = 0; pre.setAttribute('aria-label', T('scripts.preview.label'));
      v.chosen.appendChild(pre);
      v.avail = scMk('div', 'run-avail'); v.chosen.appendChild(v.avail);
      var params = scriptParams(sc);
      var keep = runForm && runFormIdent === it.ident ? runForm.raw() : null;
      if (runForm && runFormIdent !== it.ident) runForm.clearSecrets();
      runForm = params.length ? scriptParamsForm(params, keep) : null;
      runFormIdent = it.ident; runFormSig = sig;
      if (runForm) v.chosen.appendChild(runForm.el);
      v.err = scMk('div', 'run-err'); v.chosen.appendChild(v.err);
    }
  }
  if (!sc && runForm) { runForm.clearSecrets(); runForm = null; runFormIdent = null; }
  // What it does not reach (parts without it).
  if (v.avail) {
    var miss = groups.filter(function (g) { return !it.byGroup.has(g.key); });
    var txt = miss.map(function (g) {
      var names = g.devs.slice(0, 5).map(reachDevName).join(', ') + (g.devs.length > 5 ? ', …' : '');
      return T('run.unavailableFor', { where: runGroupLabel(g), devices: names });
    }).join('\n');
    domText(v.avail, txt);
    v.avail.style.whiteSpace = 'pre-line';
  }
  v.go.disabled = runStarting || !sc || !runSel.size || !(it && it.devCount > 0);
}

// ── Running ──
var RUN_ACTIVE = ['starting', 'queued', 'offline', 'running'];
var RUN_FAILED = ['ko', 'timeout', 'error', 'cancelled'];
function runStateOf(row, fresh) {
  var s = row.status;
  if (!s) return row.state;
  if (s === 'pending') return (fresh && typeof devAgentReachable === 'function' && !devAgentReachable(fresh)) ? 'offline' : 'queued';
  if (s === 'sent' || s === 'running') return 'running';
  if (s === 'success') return 'ok';
  if (s === 'failure') return 'ko';
  if (s === 'timeout') return 'timeout';
  if (s === 'cancelled') return 'cancelled';
  if (s === 'skipped') return 'skipped';
  return 'running';
}
function runCounts(job) {
  var c = { total: 0, active: 0, running: 0, ok: 0, ko: 0, offline: 0, other: 0, done: 0 };
  job.order.forEach(function (k) {
    var r = job.rows.get(k);
    c.total++;
    if (RUN_ACTIVE.indexOf(r.state) >= 0) { c.active++; if (r.state === 'offline') c.offline++; else c.running++; return; }
    c.done++;
    if (r.state === 'ok') c.ok++;
    else if (RUN_FAILED.indexOf(r.state) >= 0 || r.state === 'forbidden' || r.state === 'invalid') c.ko++;
    else c.other++;
  });
  return c;
}
// Run (opts.devs: these devices instead of the selection). Returns the run,
// or a reason it did not start ('invalid', 'none').
async function runStart(opts) {
  opts = opts || {};
  if (runStarting) return 'busy';
  var devs = opts.devs || Array.from(runSel.values());
  var groups = runGroups(devs);
  var cat = runCatalog(groups);
  var it = runPick ? cat.items.get(runPick) : null;
  if (!it || !it.devCount) return 'none';
  if (runView && runView.err) domText(runView.err, '');
  var values = {};
  if (runForm) {
    var c = runForm.collect();
    if (!c.ok) { runForm.focusError(); return 'invalid'; }
    values = c.values;
  }
  runStarting = true;
  var job = { id: ++runJobSeq, ident: it.ident, name: it.name, startedAt: Date.now(), rows: new Map(), order: [], groups: [], notice: '', noticeKind: '', timer: null, closed: false, pollStopped: false, ticks: 0 };
  groups.forEach(function (g) {
    var sc = it.byGroup.get(g.key) || null;
    var jg = { key: g.key, pid: g.pid, tid: g.tid, first: g.first, devs: g.devs, sc: sc, batchId: null, batchOk: true, byId: new Map(), label: runGroupLabel(g) };
    g.devs.forEach(function (d) {
      var k = devKey(d);
      if (job.rows.has(k)) return;
      var row = { key: k, dev: d, gkey: g.key, where: jg.label, state: sc ? 'starting' : 'unavailable', status: null, execId: null, exitCode: null, stdout: '', stderr: '', startedAt: null, finishedAt: null, error: sc ? '' : T('run.state.unavailable') };
      job.rows.set(k, row); job.order.push(k); jg.byId.set(String(d.id), row);
    });
    job.groups.push(jg);
  });
  runJobs.push(job);
  while (runJobs.length > RUN_MAX_JOBS) {
    var old = runJobs.find(function (j) { return !runCounts(j).active; }) || runJobs[0];
    runStopJob(old);
    runJobs = runJobs.filter(function (j) { return j !== old; });
  }
  runShownJob = job.id;
  runConsole = null;
  runRender();
  try {
    for (var i = 0; i < job.groups.length; i++) {
      if (job.groups[i].sc && !job.closed) await runPost(job, job.groups[i], values);
    }
  } finally {
    values = null;
    if (runForm) runForm.clearSecrets(); // (a secret is typed for one run)
    runStarting = false;
  }
  // Every part refused the parameters: back to the setup, on the fields.
  var posted = job.groups.filter(function (g) { return !!g.sc; });
  if (posted.length && posted.every(function (g) { return g.invalid; })) {
    runStopJob(job);
    runJobs = runJobs.filter(function (j) { return j !== job; });
    runShownJob = null;
    runRender();
    if (runForm) { runForm.setErrors(posted[0].invalid); runForm.focusError(); }
    if (runView && runView.err) domText(runView.err, T('scripts.params.invalid'));
    return 'invalid';
  }
  runResultsRefresh(job);
  runPollSchedule(job, 1500);
  return job;
}
function runMark(job, g, state, error) {
  g.devs.forEach(function (d) {
    var r = job.rows.get(devKey(d));
    if (r && r.gkey === g.key && r.state === 'starting') { r.state = state; r.error = error || ''; }
  });
}
async function runPost(job, g, values) {
  var sid = apiId(g.sc && g.sc.id);
  if (sid === null) { runMark(job, g, 'error', T('scripts.invalidId')); return; }
  var r, d = null;
  try {
    r = await devApi(g.first, 'POST', '/api/scripts/' + sid + '/execute', { deviceIds: g.devs.map(function (x) { return x.id; }), parameterValues: values }, true);
    try { d = await r.json(); } catch (e) { d = null; }
  } catch (err) { runMark(job, g, 'error', T('scripts.error', { error: reachErrText(err) })); return; }
  if (!r.ok) {
    if (r.status === 400 && d && Array.isArray(d.details)) {
      g.invalid = d.details;
      runMark(job, g, 'invalid', T('scripts.params.invalid'));
      job.notice = T('scripts.params.invalid'); job.noticeKind = '';
      return;
    }
    var msg = scriptsRunErrText(r.status, d);
    runMark(job, g, r.status === 403 ? 'forbidden' : 'error', msg);
    job.notice = (job.notice ? job.notice + '\n' : '') + g.label + ': ' + msg; job.noticeKind = '';
    return;
  }
  if (d && d.data && !Array.isArray(d.data) && d.data.status === 'pending_approval') {
    runMark(job, g, 'approval', T('scripts.run.pendingApproval'));
    job.notice = (job.notice ? job.notice + '\n' : '') + g.label + ': ' + T('scripts.run.pendingApproval'); if (!job.noticeKind) job.noticeKind = 'info';
    return;
  }
  var execs = d && Array.isArray(d.data) ? d.data : [];
  execs.forEach(function (ex) {
    if (!ex) return;
    var row = g.byId.get(String(ex.deviceId));
    if (!row) return;
    var eid = apiId(ex.id);
    if (eid === null) { row.state = 'error'; row.error = T('scripts.invalidId'); return; }
    row.execId = eid; row.status = ex.status || 'pending'; row.state = runStateOf(row, row.dev);
    if (ex.batchId && apiId(ex.batchId) !== null) g.batchId = String(ex.batchId);
  });
  runMark(job, g, 'notStarted', T('scripts.run.noTarget'));
  runApplyStates(job);
}
// The rows' states from what the server said and the devices as known now.
function runApplyStates(job) {
  var u = null;
  try { u = dsUniverse().devs; } catch (e) { u = null; }
  job.rows.forEach(function (r) {
    if (!r.status) return;
    var fresh = (u && u.get(r.key)) || r.dev;
    r.state = runStateOf(r, fresh);
  });
}
function runPollSchedule(job, ms) {
  if (job.timer) { clearTimeout(job.timer); job.timer = null; }
  if (job.closed) return;
  job.timer = setTimeout(function () { job.timer = null; runPollTick(job); }, ms);
}
function runRowApply(row, it) {
  if (!row || !it) return;
  if (it.status) row.status = it.status;
  row.exitCode = it.exitCode === undefined ? row.exitCode : it.exitCode;
  if (typeof it.stdout === 'string') row.stdout = it.stdout;
  if (typeof it.stderr === 'string') row.stderr = it.stderr;
  if (it.startedAt) row.startedAt = it.startedAt;
  if (it.finishedAt) row.finishedAt = it.finishedAt;
}
// One read of the executions still going on; the next one scheduled.
async function runPollTick(job) {
  if (job.closed) return;
  if (job.timer) { clearTimeout(job.timer); job.timer = null; }
  job.ticks++;
  for (var i = 0; i < job.groups.length; i++) {
    var g = job.groups[i];
    var act = [];
    g.byId.forEach(function (r) { if (r.execId && RUN_ACTIVE.indexOf(r.state) >= 0) act.push(r); });
    if (!act.length || job.closed) continue;
    var done = false;
    if (g.batchId && g.batchOk) {
      try {
        var r = await devApi(g.first, 'GET', '/api/executions/batches/' + g.batchId, undefined, true);
        if (r && r.ok) {
          var d = await r.json();
          var items = d && Array.isArray(d.data) ? d.data : [];
          var byExec = new Map();
          g.byId.forEach(function (row) { if (row.execId) byExec.set(String(row.execId), row); });
          items.forEach(function (x) { if (x) runRowApply(byExec.get(String(x.id)), x); });
          done = true;
        } else if (r && r.status === 404) g.batchOk = false;
      } catch (e) {}
    }
    if (done) continue;
    for (var k = 0; k < act.length; k++) {
      if (job.closed) return;
      try {
        var pr = await devApi(g.first, 'GET', '/api/executions/' + act[k].execId, undefined, true);
        if (pr && pr.ok) { var pd = await pr.json(); runRowApply(act[k], (pd && pd.data) || pd); }
      } catch (e2) {}
    }
  }
  if (job.closed) return;
  runApplyStates(job);
  var c = runCounts(job);
  var elapsed = Date.now() - job.startedAt;
  if (c.active && elapsed > RUN_POLL_MAX_MS) job.pollStopped = true;
  runResultsRefresh(job);
  if (runPageShown()) runTabsRender();
  if (!c.active || job.pollStopped) return;
  runPollSchedule(job, elapsed < 120000 ? 2000 : elapsed < 600000 ? 5000 : 15000);
}

// ── Results ──
function runRowDuration(r) {
  var a = r.startedAt ? Date.parse(r.startedAt) : NaN, b = r.finishedAt ? Date.parse(r.finishedAt) : NaN;
  if (!isFinite(a)) return '';
  if (!isFinite(b)) return RUN_ACTIVE.indexOf(r.state) >= 0 ? fmtSeconds(Math.max(0, Math.round((Date.now() - a) / 1000))) : '';
  var s = Math.max(0, (b - a) / 1000);
  var n = s < 10 ? Math.round(s * 10) / 10 : Math.round(s);
  return fmtSeconds(n, n % 1 ? 1 : 0);
}
var RUN_CHIP = { ok: 'on', ko: 'bad', timeout: 'bad', error: 'bad', forbidden: 'bad', invalid: 'bad', queued: 'run', running: 'run', starting: 'run', offline: 'warn' };
var RUN_STATE_KEY = {
  starting: 'run.state.starting', queued: 'run.state.queued', offline: 'run.state.offline', running: 'run.state.running',
  ok: 'run.state.ok', ko: 'run.state.ko', timeout: 'run.state.timeout', cancelled: 'run.state.cancelled', skipped: 'run.state.skipped',
  unavailable: 'run.state.unavailable', forbidden: 'run.state.forbidden', invalid: 'run.state.invalid', error: 'run.state.error',
  notStarted: 'run.state.notStarted', approval: 'run.state.approval',
};
function runStateText(r) { return RUN_STATE_KEY[r.state] ? T(RUN_STATE_KEY[r.state]) : String(r.state || '-'); }
// A row as the console shows it (the Scripts panel's text).
function runConsoleText(r) {
  if (r.status && EXEC_TERMINAL.indexOf(r.status) >= 0) return scriptsConsoleText({ status: r.status, exitCode: r.exitCode, stdout: r.stdout, stderr: r.stderr });
  if (r.status) return scriptsConsoleText({ status: r.status }) + (r.state === 'offline' ? T('scripts.run.queuedOffline') + '\n' : '') + (r.stdout ? '--- stdout ---\n' + r.stdout + '\n' : '');
  return runStateText(r) + (r.error && r.error !== runStateText(r) ? '\n' + r.error : '') + '\n';
}
function runResultsRefresh(job) {
  var v = runView;
  if (!v || runShownJob !== job.id) return;
  domText(v.sumTitle, job.name); v.sumTitle.title = job.name;
  domText(v.sumWhen, T('run.startedAt', { time: fmtDateTime(job.startedAt) }));
  var c = runCounts(job);
  var chips = [['total', 'run.count.devices', c.total, ''], ['running', 'run.state.running', c.running, 'run'], ['ok', 'run.state.ok', c.ok, 'ok'],
    ['ko', 'run.count.failed', c.ko, 'ko'], ['offline', 'run.state.offline', c.offline, 'off'], ['other', 'run.count.notRun', c.other, '']];
  domKeyed(v.counts, chips.filter(function (x) { return x[0] === 'total' || x[2] > 0; }).map(function (x) {
    return { key: x[0], sig: x[1] + '\u0001' + x[2] + '\u0001' + T(x[1]), data: x, build: function () {
      var e = scMk('span', 'run-count' + (x[3] ? ' ' + x[3] : ''));
      e.appendChild(scMk('span', '', T(x[1]))); e.appendChild(scMk('b', '', String(x[2])));
      return e;
    } };
  }));
  var pct = c.total ? Math.round(c.done * 100 / c.total) : 100;
  v.progBar.style.width = pct + '%';
  v.prog.classList.toggle('done', !c.active);
  v.prog.setAttribute('aria-valuenow', String(pct));
  domText(v.progTxt, T('run.progress', { done: c.done, total: c.total }) + (job.pollStopped && c.active ? ' · ' + T('run.pollStopped') : ''));
  domText(v.notice, job.notice || '');
  v.notice.className = 'run-notice' + (job.noticeKind ? ' ' + job.noticeKind : '');
  v.notice.style.whiteSpace = 'pre-line';
  var failed = job.order.some(function (k) { return RUN_FAILED.indexOf(job.rows.get(k).state) >= 0; });
  v.rerun.disabled = !failed || runStarting;
  v.refreshJob.classList.toggle('hide', !(job.pollStopped && c.active));
  if (!v.rthead.children || !v.rthead.children.length || v.rthead._lang !== i18nLang()) {
    v.rthead._lang = i18nLang();
    v.rthead.innerHTML = '<tr><th>' + TH('run.col.device') + '</th><th>' + TH('run.col.status') + '</th><th>' + TH('run.col.exit') + '</th><th>' + TH('run.col.duration') + '</th><th style="text-align:end">' + TH('run.col.output') + '</th></tr>';
  }
  var multi = typeof serverMode !== 'undefined' && serverMode === 'multi';
  domKeyed(v.rtbody, job.order.map(function (k) {
    var r = job.rows.get(k);
    var sel = !!(runConsole && runConsole.job === job && runConsole.key === k);
    var code = r.exitCode === null || r.exitCode === undefined ? '' : String(r.exitCode);
    var dur = runRowDuration(r);
    var sig = [reachDevName(r.dev), r.where, r.state, code, dur, r.stdout ? 1 : 0, r.stderr ? 1 : 0, sel ? 1 : 0, r.error, multi ? srvColor(devPid(r.dev)) : ''].join('\u0001');
    return { key: k, sig: sig, data: { job: job, row: r }, build: function () { return runRowEl(job, r, sel, code, dur, multi); } };
  }));
  if (runConsole && runConsole.job === job) runConsoleFill();
  else v.cons.classList.add('hide');
}
function runRowEl(job, r, sel, code, dur, multi) {
  var tr = scMk('tr', 'run-row' + (sel ? ' sel' : ''));
  tr.tabIndex = 0;
  if (r.error) tr.title = r.error;
  var td1 = scMk('td'); var dv = scMk('div', 'run-dev');
  if (multi) { var sd = scMk('span', 'srv-dot'); sd.style.background = srvColor(devPid(r.dev)); sd.title = srvName(devPid(r.dev)); dv.appendChild(sd); }
  dv.appendChild(scMk('span', 'nm', reachDevName(r.dev)));
  if (r.where) dv.appendChild(scMk('span', 'where', r.where));
  td1.appendChild(dv);
  var td2 = scMk('td'); td2.appendChild(scMk('span', 'tag-chip ' + (RUN_CHIP[r.state] || 'off'), runStateText(r)));
  var td3 = scMk('td', '', code || '-');
  var td4 = scMk('td', '', dur || '-');
  var td5 = scMk('td'); var outs = scMk('div', 'run-outs');
  var b1 = scMk('button', 'btn-sm', 'STDOUT'); b1.title = T('run.openStdout'); b1.disabled = !r.stdout; b1.dataset.out = 'stdout';
  var b2 = scMk('button', 'btn-sm', 'STDERR'); b2.title = T('run.openStderr'); b2.disabled = !r.stderr; b2.dataset.out = 'stderr';
  var cur = function () { return (tr._domData && tr._domData.row) || r; };
  b1.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); runShowOutput(cur(), 'stdout'); });
  b2.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); runShowOutput(cur(), 'stderr'); });
  outs.appendChild(b1); outs.appendChild(b2); td5.appendChild(outs);
  tr.appendChild(td1); tr.appendChild(td2); tr.appendChild(td3); tr.appendChild(td4); tr.appendChild(td5);
  tr.addEventListener('click', function () { var d = tr._domData || { job: job, row: r }; runConsoleOpen(d.job, d.row.key); });
  tr.addEventListener('keydown', function (e) { if (e && e.key === 'Enter' && e.target === tr) { if (e.preventDefault) e.preventDefault(); var d = tr._domData || { job: job, row: r }; runConsoleOpen(d.job, d.row.key); } });
  return tr;
}
function runShowOutput(r, which) {
  var text = which === 'stderr' ? r.stderr : r.stdout;
  return scriptsTextModal({ title: reachDevName(r.dev) + ' · ' + (which === 'stderr' ? 'STDERR' : 'STDOUT'), sub: r.where || '', text: text || '', term: true });
}
// A device's console beside the results (the Scripts panel's text), kept up
// to date with its row.
function runConsoleOpen(job, key) {
  if (!job || !job.rows.has(key)) return;
  runConsole = { job: job, key: key };
  runResultsRefresh(job);
  try { runView.consOut.focus(); } catch (e) {}
}
function runConsoleClose() {
  var j = runConsole && runConsole.job;
  runConsole = null;
  if (runView) runView.cons.classList.add('hide');
  if (j) runResultsRefresh(j);
}
function runConsoleFill() {
  var v = runView, c = runConsole;
  var r = c && c.job.rows.get(c.key);
  if (!v || !r) return;
  v.cons.classList.remove('hide');
  domText(v.consName, reachDevName(r.dev));
  domText(v.consSub, [c.job.name, r.where].filter(Boolean).join(' · '));
  domText(v.consOut, runConsoleText(r));
}
// "Re-run failed": the failed devices selected again, the same script; run
// at once when the parameters are complete (a secret is asked again).
async function runRerunFailed(job) {
  var u = dsUniverse().devs;
  var devs = job.order.map(function (k) { return job.rows.get(k); }).filter(function (r) { return RUN_FAILED.indexOf(r.state) >= 0; })
    .map(function (r) { return u.get(r.key) || r.dev; }).filter(runSelectable);
  if (!devs.length) return 'none';
  runSel = new Map(devs.map(function (d) { return [devKey(d), d]; }));
  runPick = job.ident;
  runShownJob = null;
  runRender();
  if (runForm && !runForm.collect().ok) { runForm.focusError(); return 'invalid'; }
  return runStart();
}
// The results written to a file (the app's download: the Save dialog).
function runExportRows(job) {
  return job.order.map(function (k) {
    var r = job.rows.get(k);
    var a = r.startedAt ? Date.parse(r.startedAt) : NaN, b = r.finishedAt ? Date.parse(r.finishedAt) : NaN;
    return { device: reachDevName(r.dev), deviceId: r.dev && r.dev.id, server: srvName(devPid(r.dev)), where: r.where || '', state: r.state, status: r.status || '',
      exitCode: r.exitCode === undefined ? null : r.exitCode, durationSeconds: isFinite(a) && isFinite(b) ? Math.max(0, (b - a) / 1000) : null,
      startedAt: r.startedAt || null, finishedAt: r.finishedAt || null, stdout: r.stdout || '', stderr: r.stderr || '', error: r.error || '' };
  });
}
function runCsvCell(v) {
  var s = v === null || v === undefined ? '' : String(v);
  if (/^[=+\-@\t\r]/.test(s)) s = "'" + s; // (never read as a formula by a spreadsheet)
  return /[",\r\n;]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}
function runExport(job, kind) {
  var rows = runExportRows(job);
  var stamp = new Date(job.startedAt);
  var p2 = function (n) { return (n < 10 ? '0' : '') + n; };
  var base = 'run-' + String(job.name || 'script').replace(/[^A-Za-z0-9_.-]+/g, '_').slice(0, 60) + '-' + stamp.getFullYear() + p2(stamp.getMonth() + 1) + p2(stamp.getDate()) + '-' + p2(stamp.getHours()) + p2(stamp.getMinutes());
  var text, type, name;
  if (kind === 'json') {
    text = JSON.stringify({ script: job.name, startedAt: new Date(job.startedAt).toISOString(), results: rows }, null, 2);
    type = 'application/json'; name = base + '.json';
  } else {
    var cols = ['device', 'deviceId', 'server', 'where', 'state', 'status', 'exitCode', 'durationSeconds', 'startedAt', 'finishedAt', 'stdout', 'stderr', 'error'];
    text = String.fromCharCode(0xFEFF) + cols.join(',') + '\r\n' + rows.map(function (r) { return cols.map(function (c) { return runCsvCell(r[c]); }).join(','); }).join('\r\n') + '\r\n';
    type = 'text/csv'; name = base + '.csv';
  }
  reachSaveBlob(new Blob([text], { type: type + ';charset=utf-8' }), reachSafeFileName(name));
  return { name: name, text: text };
}
`
