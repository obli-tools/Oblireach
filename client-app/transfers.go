package main

// transfers.go — the transfer queue of the Files page's split view: this PC
// to the device and back, one queue per device (its key), one file at a time,
// every remote operation through the explorer's gated path only
// (explorerCmd: FILE_EXPLORER_CMD on the device's server socket, the tenant
// alignment, the 2FA step-up, the privacy code), 100 MB per file at most (the
// agent's answer and the socket.io frame carry it whole).
//
// A batch comes from the local pane (a selection, folders walked by Go:
// reachFsWalk), from a drop of the operator's files (their File objects,
// folders read with webkitGetAsEntry), or from the device's pane (files and
// folders, listed one call at a time). Limits: 10,000 entries, 32 levels,
// 10 GiB, and a confirmation above 500 files or 1 GiB. Links are never
// followed, on either side. A name the device's OS refuses is left out
// (explorerValidName); a remote name reaches the disk sanitised by Go.
//
// Conflicts: before a file is sent, the folder it goes to is listed once
// (the batch keeps that listing); before a file is received, Go refuses to
// overwrite (reachFsWriteBegin conflict 'fail'). Either way the operator
// chooses: overwrite, keep both ("name (2).ext"), skip, for this one or all
// the next ones, or cancel the transfer.
//
// Progress is what is known: the local reading and writing (their share
// done), the files and bytes of the batch; the network leg of one file has no
// percentage (the agent's answer is one message).
//
// The views: the explorer's dock (xpDockRender: the transfers and, from
// edit_page.go, the files being edited), and any listener (xferOnChange: a
// terminal's own chip shows its items, xferItemsFor(dev, origin)). The dock
// draws a window of a large queue (those on their way, the next queued, the
// last finished, a line for the others): its cost stays the same whatever
// the batch holds.
//
// Both consts are injected verbatim after the local pane (buildUI, not
// through Sprintf: '%' is safe): no backtick here.

const xferCSS = `
/* ── Transfers, the dock (transfers.go) ── */
.xp-dock{flex-shrink:0;display:flex;flex-direction:column;border-radius:14px;background:var(--bg2);box-shadow:var(--surface-shadow);overflow:hidden}
.xp-dock.empty{display:none}
.xp-dock-head{display:flex;align-items:center;gap:6px;padding:6px 8px 6px 8px}
.xp-dock-tab{display:inline-flex;align-items:center;gap:6px;padding:5px 10px;border:none;border-radius:8px;background:none;color:var(--muted);font-size:11.5px;font-weight:600;cursor:pointer}
.xp-dock-tab:hover{background:rgb(var(--ink) / .06);color:var(--text)}
.xp-dock-tab.on{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.xp-dock-tab:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.xp-dock-sum{flex:1;min-width:0;padding:0 6px;font-size:11px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-variant-numeric:tabular-nums}
.xp-dock-head .btn-sm{padding:5px 10px}
.xp-dock-head .btn-sm:disabled{opacity:.4;cursor:default}
.xp-dock-body{max-height:176px;overflow:auto;padding:0 6px 6px}
.xp-row{display:flex;align-items:center;gap:10px;padding:5px 6px;border-radius:9px;font-size:12px;min-width:0}
.xp-row:hover{background:rgb(var(--ink) / .04)}
.xp-row-ico{flex-shrink:0;width:22px;height:22px;border-radius:7px;display:inline-flex;align-items:center;justify-content:center;background:rgb(var(--ink) / .06);color:var(--muted)}
.xp-row.live .xp-row-ico{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.xp-row-name{flex:0 1 auto;min-width:60px;max-width:34%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--text)}
.xp-row-to{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted2);font-size:11px}
.xp-row-size{flex-shrink:0;color:var(--muted);font-size:11px;font-variant-numeric:tabular-nums}
.xp-bar{flex-shrink:0;position:relative;width:84px;height:5px;border-radius:3px;overflow:hidden;background:rgb(var(--ink) / .08)}
.xp-bar > i{position:absolute;top:0;bottom:0;left:0;border-radius:3px;background:var(--accent)}
.xp-bar.ind > i{width:40%;animation:exp-ind 1.1s ease-in-out infinite}
.xp-bar.none{visibility:hidden}
.xp-row-st{flex-shrink:0;width:150px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted);font-size:11px;text-align:right}
.xp-row.done .xp-row-st{color:var(--success)}
.xp-row.error .xp-row-st{color:var(--danger)}
.xp-row.skipped .xp-row-st,.xp-row.cancelled .xp-row-st{color:var(--muted2)}
.xp-row-x{flex-shrink:0;width:24px;height:24px;border:none;border-radius:7px;display:inline-flex;align-items:center;justify-content:center;background:none;color:var(--muted2);cursor:pointer}
.xp-row-x:hover{background:rgb(var(--ink) / .08);color:var(--text)}
.xp-row-x:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.xp-dock-empty{padding:10px 8px 12px;font-size:11.5px;color:var(--muted2)}
.modal-overlay.xfer-cfl{z-index:345}
.xfer-cfl-rows{display:flex;flex-direction:column;gap:6px;margin:2px 0 12px}
.xfer-cfl-row{display:flex;align-items:center;gap:10px;padding:8px 12px;border-radius:10px;background:var(--input-bg);font-size:12px}
.xfer-cfl-row .l{width:96px;flex-shrink:0;color:var(--muted)}
.xfer-cfl-row .v{flex:1;min-width:0;color:var(--text);font-variant-numeric:tabular-nums}
.xfer-cfl-row.newer .v{color:var(--accent-l);font-weight:600}
.xfer-cfl-all{display:flex;align-items:center;gap:8px;margin:0 0 12px;font-size:12px;color:var(--muted);cursor:pointer}
.xfer-cfl-all input{accent-color:var(--accent);width:15px;height:15px}
.xfer-cfl .modal-box{width:520px}
.xfer-cfl .modal-actions{flex-wrap:wrap}
.xfer-cfl .modal-actions .sp{flex:1}
`

const xferJS = `
// ═════════════════════════════════════════════════════════════════════════
// Transfers (transfers.go): this PC <-> the device, a queue per device
// ═════════════════════════════════════════════════════════════════════════
var XFER_CHUNK = 786432;                     // a local read (768 KiB: a multiple of 3, the base64 joins)
var XFER_SLICE = 1048576;                    // base64 characters per local write (786432 bytes)
var XFER_MAX_ENTRIES = 10000;
var XFER_MAX_DEPTH = 32;
var XFER_MAX_TOTAL = 10 * 1024 * 1024 * 1024;
var XFER_CONFIRM_FILES = 500;
var XFER_CONFIRM_BYTES = 1024 * 1024 * 1024;
var XFER_MAX_ROWS = 200;
var XFER_DOCK_QUEUED = 30;   // the dock draws the rows on their way, the next 30 queued,
var XFER_DOCK_FINISHED = 30; // the last 30 finished and errors (a batch of 10,000: never 10,000 rows)
var XFER_LIVE = { queued: true, reading: true, sending: true, receiving: true, writing: true };
var xferQueues = new Map(); // device key -> { key, dev, items, batches, running, paused, planning }
var xferSeq = 0;
var xferListeners = [];     // fn(queue): a view of the queues (a terminal's chip...)

// What one file / a batch may weigh: the agent's one-message limit; a local
// SSH connection streams in chunks (sftp_stream.go: xferStreamUp / Down), so
// none of its own.
function xferStreams(dev) { return typeof devIsLocal === 'function' && devIsLocal(dev); }
function xferMaxBytes(dev) { return xferStreams(dev) ? Infinity : EXPLORER_MAX_BYTES; }
function xferMaxTotal(dev) { return xferStreams(dev) ? Infinity : XFER_MAX_TOTAL; }
function xferQueueFor(dev) {
  var k = devKey(dev), q = xferQueues.get(k);
  if (!q) { q = { key: k, dev: dev, items: [], batches: [], running: false, paused: false, planning: 0 }; xferQueues.set(k, q); }
  else if (dev) q.dev = dev;
  return q;
}
function xferOnChange(fn) { if (typeof fn === 'function') xferListeners.push(fn); }
function xferLive(it) { return !!it && XFER_LIVE[it.state] === true; }
function xferActive(q) { return !!q && (q.planning > 0 || q.items.some(xferLive)); }
function xferDevActive(dev) { return !!dev && xferActive(xferQueues.get(devKey(dev))); }
// The items of a device that came from one place (a terminal: its tab id).
function xferItemsFor(dev, origin) {
  var q = dev ? xferQueues.get(devKey(dev)) : null;
  return q ? q.items.filter(function (it) { return !origin || it.batch.origin === origin; }) : [];
}
function xferChanged(q) {
  if (!q) return;
  toolTabs.forEach(function (t) { if (t && t.kind === 'files' && t.ex && t.ex.split && !t.ex.disposed && devKey(t.device) === q.key) xpDockRender(t); });
  for (var i = 0; i < xferListeners.length; i++) { try { xferListeners[i](q); } catch (e) {} }
}
function xferErr(key, params, code) { return explorerErr(params ? { k: key, p: params } : { k: key }, code || ''); }
// A refusal in words: the local disk's, the device's.
function xferErrText(e) {
  if (e && e.local) return localFsErrText(e);
  return explorerErrText(e);
}
function xferNameKey(n, os) { return os === 'windows' ? String(n).toLowerCase() : String(n); }
function xferRelParent(rel) { var i = String(rel).lastIndexOf('/'); return i > 0 ? rel.slice(0, i) : ''; }
function xferB64Size(b64) {
  var n = b64.length;
  if (n % 4) return -1;
  return n / 4 * 3 - (b64.slice(-2) === '==' ? 2 : b64.slice(-1) === '=' ? 1 : 0);
}

// ── Batches ──
function xferNewBatch(q, dir, meta) {
  return { id: ++xferSeq, q: q, dir: dir, origin: meta.origin || 'explorer', dstDir: meta.dstDir || '', os: meta.os || 'windows',
    files: 0, bytes: 0, done: 0, doneBytes: 0, failed: 0, skipped: meta.skipped || 0, conflict: null, cancelled: false,
    listings: new Map(), localDirs: {}, remoteDirs: new Set(), notified: false, stopErr: null };
}
function xferAddItem(b, o) {
  var it = { id: ++xferSeq, batch: b, dir: b.dir, state: 'queued', pct: -1, error: '', size: 0, isDir: false, name: '', rel: '', dstPath: '', src: null };
  for (var k in o) if (Object.prototype.hasOwnProperty.call(o, k)) it[k] = o[k];
  b.q.items.push(it);
  if (!it.isDir) { b.files++; b.bytes += it.size || 0; }
  return it;
}
// items: [{ isDir, name, rel, dstPath, size, modified, src }].
function xferStart(dev, dir, meta, items) {
  var q = xferQueueFor(dev);
  var b = xferNewBatch(q, dir, meta);
  q.batches.push(b);
  items.forEach(function (o) { xferAddItem(b, o); });
  xferPrune(q);
  toolTabs.forEach(function (t) { if (t && t.kind === 'files' && t.ex && t.ex.split && devKey(t.device) === q.key) t.ex.dockTab = 'tr'; });
  xferChanged(q);
  if (!items.length) xferBatchCheck(b);
  xferPump(q);
  return b;
}
// The ones of this batch still to run for a command (itself included): one
// 2FA confirmation covers them (the server's count, 100 at most).
function xferLeft(b, type) {
  var n = 0;
  b.q.items.forEach(function (it) {
    if (it.batch !== b || !(it.state === 'queued' || xferLive(it))) return;
    var t = it.dir === 'up' ? (it.isDir ? 'create_directory' : 'upload_file') : (it.isDir ? '' : 'download_file');
    if (t === type) n++;
  });
  return Math.max(1, n);
}
// More than 500 files or 1 GiB: asked first.
async function xferConfirmBig(files, bytes, dst) {
  if (files <= XFER_CONFIRM_FILES && bytes <= XFER_CONFIRM_BYTES) return true;
  var ok = await explorerDialog({ title: T('xfer.bigTitle'), text: T('xfer.bigConfirm', { count: files, size: reachFmtBytes(bytes), path: dst }), ok: 'xfer.start', icon: EXPLORER_ICON.upload });
  return ok === true;
}

// ── Running: one file at a time per device ──
async function xferPump(q) {
  if (q.running) return;
  q.running = true;
  try {
    for (;;) {
      if (q.paused) break;
      var it = null;
      for (var i = 0; i < q.items.length; i++) if (q.items[i].state === 'queued') { it = q.items[i]; break; }
      if (!it) break;
      q.active = it;
      try {
        if (it.batch.cancelled) xferSetState(q, it, 'cancelled');
        else if (it.dir === 'up') await xferRunUp(q, it);
        else await xferRunDown(q, it);
      } catch (e) { xferFailed(q, it, e); }
      q.active = null;
      xferBatchCheck(it.batch);
      xferChanged(q);
    }
  } finally {
    q.running = false;
    q.active = null;
    xferChanged(q);
  }
}
function xferSetState(q, it, st, err) {
  it.state = st;
  if (err !== undefined) it.error = err;
  if (st !== 'reading' && st !== 'writing') it.pct = -1;
  xferChanged(q);
}
function xferDone(q, it) {
  var b = it.batch;
  it.state = 'done'; it.pct = -1; it.error = '';
  if (!it.isDir) { b.done++; b.doneBytes += it.size || 0; }
  xferChanged(q);
}
function xferSkip(q, it, why) {
  it.state = 'skipped'; it.pct = -1; it.error = why || T('xfer.skipped');
  if (!it.isDir) it.batch.skipped++;
  xferChanged(q);
}
function xferFailed(q, it, e) {
  var b = it.batch;
  if ((e && e.code === 'cancelled') || it.cancelled) {
    if (it.state !== 'cancelled') { it.state = 'cancelled'; it.pct = -1; }
    return;
  }
  it.state = 'error'; it.pct = -1; it.error = xferErrText(e);
  if (!it.isDir) b.failed++;
  // A refusal that holds for the whole batch: said once, the rest cancelled.
  if (e && !e.local && EXPLORER_BATCH_STOP.indexOf(e.code) >= 0) {
    b.stopErr = it.error;
    xferCancelBatch(b, true);
  }
}
// A batch finished: said once (with what it did), the panes where it went
// listed again.
function xferBatchCheck(b) {
  if (b.notified || b.q.items.some(function (it) { return it.batch === b && (it.state === 'queued' || xferLive(it)); })) return;
  b.notified = true;
  var q = b.q, dev = q.dev;
  if (b.stopErr) reachNotify(b.stopErr, 'warn');
  else if (b.files || b.skipped) {
    var ok = !b.failed && !b.skipped && !b.cancelled;
    var first = null;
    q.items.forEach(function (it) { if (it.batch === b && !it.isDir && it.state === 'done' && !first) first = it; });
    var text;
    if (ok && b.done === 1 && first) text = b.dir === 'up' ? T('viewer.tr.sentTo', { name: first.name, path: b.dstDir }) : T('xfer.receivedOne', { name: first.name, path: b.dstDir });
    else if (ok) text = b.dir === 'up' ? T('xfer.sentMany', { count: b.done, path: b.dstDir }) : T('xfer.receivedMany', { count: b.done, path: b.dstDir });
    else text = T('xfer.finished', { done: b.done, failed: b.failed, skipped: b.skipped });
    reachNotify(text, ok ? 'ok' : 'warn');
  }
  // The panes showing where it went: listed again.
  toolTabs.forEach(function (t) {
    if (!t || t.kind !== 'files' || !t.ex || t.ex.disposed) return;
    // (each view of the tab, listed: explorer_tabs.go)
    if (b.dir === 'up' && devKey(t.device) === q.key) explorerViews(t).forEach(function (v) {
      if (v.ex && !v.ex.disposed && !v.ex.closed && v.ex.listed && (v.ex.path === b.dstDir || b.remoteDirs.has(v.ex.path))) explorerList(v, v.ex.path, { keepSel: true });
    });
    if (b.dir === 'down' && t.lp && t.lp.listed && t.lp.path && String(t.lp.path).toLowerCase() === String(b.dstDir).toLowerCase()) lpList(t, t.lp.path, { keepSel: true });
  });
  xferPrune(q); // (the 200 rows kept: a batch's end drops the oldest finished ones)
}

// ── Cancel, pause, retry, clear ──
function xferRemove(q, it) {
  var i = q.items.indexOf(it);
  if (i >= 0) q.items.splice(i, 1);
  if (!it.isDir) { it.batch.files--; it.batch.bytes -= it.size || 0; }
}
function xferCancelItem(q, it, quiet) {
  switch (it.state) {
    case 'queued': xferRemove(q, it); break;
    case 'reading': case 'writing': case 'receiving':
      it.cancelled = true; it.state = 'cancelled'; it.pct = -1; break;
    case 'sending': // (on its way to the device: it cannot be called back)
      it.stopAfter = true; it.batch.cancelled = true;
      q.items.slice().forEach(function (x) { if (x.batch === it.batch && x.state === 'queued') xferRemove(q, x); });
      break;
    default: return;
  }
  if (!quiet) xferBatchCheck(it.batch);
  xferChanged(q);
}
function xferCancelBatch(b, quiet) {
  var q = b.q;
  b.cancelled = true;
  q.items.slice().forEach(function (it) { if (it.batch === b && (it.state === 'queued' || xferLive(it))) xferCancelItem(q, it, true); });
  if (!quiet) xferBatchCheck(b);
  xferChanged(q);
}
function xferCancelAll(q) {
  if (!q) return;
  q.batches.forEach(function (b) { if (!b.notified) xferCancelBatch(b); });
  xferChanged(q);
}
function xferTogglePause(q) {
  q.paused = !q.paused;
  xferChanged(q);
  if (!q.paused) xferPump(q);
}
function xferRetry(q, it) {
  if (it.state !== 'error') return;
  var b = it.batch;
  if (!it.isDir) b.failed = Math.max(0, b.failed - 1);
  it.state = 'queued'; it.error = ''; it.cancelled = false; it.stopAfter = false;
  b.cancelled = false; b.notified = false; b.stopErr = null;
  xferChanged(q);
  xferPump(q);
}
function xferClearFinished(q) {
  q.items = q.items.filter(function (it) { return it.state === 'queued' || xferLive(it); });
  q.batches = q.batches.filter(function (b) { return !b.notified; });
  xferChanged(q);
}
// The oldest finished rows go beyond 200.
function xferPrune(q) {
  while (q.items.length > XFER_MAX_ROWS) {
    var i = -1;
    for (var j = 0; j < q.items.length; j++) if (!(q.items[j].state === 'queued' || xferLive(q.items[j]))) { i = j; break; }
    if (i < 0) break;
    q.items.splice(i, 1);
  }
  q.batches = q.batches.filter(function (b) { return !b.notified || q.items.some(function (it) { return it.batch === b; }); });
}
// Sign-out, a server or tenant left: the queue ends (said), the device's
// context with it (explorer_viewer.go explorerCtxDisposeWhere).
function xferAbortQueue(q) {
  var live = xferActive(q);
  q.batches.forEach(function (b) { b.cancelled = true; b.notified = true; });
  q.items.forEach(function (it) { if (it.state === 'queued' || xferLive(it)) { it.cancelled = true; it.state = 'cancelled'; it.pct = -1; } });
  q.paused = false;
  xferQueues.delete(q.key);
  xferChanged(q);
  if (live) reachNotify(T('xfer.aborted', { device: reachDevName(q.dev) }), 'warn');
}

// ── Conflicts: the operator's choice ──
var xferDialogChain = Promise.resolve();
// existing / incoming: { size, modified, isDir }. Resolves to 'overwrite',
// 'rename' (keep both), 'skip' or 'cancel' (the transfer).
function xferResolve(b, it, existing, incoming) {
  if (b.cancelled) return Promise.resolve('cancel');
  var ok = function (a) { return !(existing && existing.isDir && a === 'overwrite'); };
  if (b.conflict && ok(b.conflict)) return Promise.resolve(b.conflict);
  var run = function () {
    if (b.cancelled) return 'cancel';
    if (b.conflict && ok(b.conflict)) return b.conflict;
    return xferConflictDialog(it, existing || {}, incoming || {}).then(function (r) {
      if (!r) return 'cancel';
      if (r.all) b.conflict = r.action;
      return r.action;
    });
  };
  var p = xferDialogChain.then(run, run);
  xferDialogChain = p.then(function () {}, function () {});
  return p;
}
function xferWhen(x) {
  var s = [x && !x.isDir && isFinite(Number(x.size)) ? reachFmtBytes(Number(x.size)) : '', explorerFmtDate(x && x.modified)].filter(Boolean).join(' · ');
  return s || T('xfer.unknown');
}
var xferDlgDone = null;
function xferConflictDialog(it, existing, incoming) {
  if (xferDlgDone) xferDlgDone(null);
  explorerMenuClose();
  return new Promise(function (resolve) {
    var ov = explorerEl('div', 'modal-overlay visible exp-dlg xfer-cfl', null, 'xfer-cfl');
    var box = explorerEl('div', 'modal-box', ov);
    box.setAttribute('role', 'alertdialog');
    var title = T('xfer.conflictTitle', { name: it.name });
    box.setAttribute('aria-label', title);
    var head = explorerEl('div', 'modal-head', box);
    var ico = explorerEl('div', 'icon', head); ico.innerHTML = it.dir === 'up' ? EXPLORER_ICON.upload : EXPLORER_ICON.download;
    var h = explorerEl('h3', '', head); h.textContent = title;
    var x = explorerEl('button', 'close', head, 'xfer-cfl-close'); x.title = T('common.cancel');
    x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    var p = explorerEl('p', 'desc', box, 'xfer-cfl-text');
    p.textContent = existing.isDir ? T('xfer.conflictFolder', { path: it.dir === 'up' ? explorerParent(it.dstPath, it.batch.os) : it.batch.dstDir })
      : T('xfer.conflictText', { path: it.dir === 'up' ? explorerParent(it.dstPath, it.batch.os) : it.batch.dstDir });
    var rows = explorerEl('div', 'xfer-cfl-rows', box);
    var te = Date.parse(existing.modified || ''), ti = Date.parse(incoming.modified || '');
    [['xfer.existing', existing, isFinite(te) && isFinite(ti) && te > ti], ['xfer.incoming', incoming, isFinite(te) && isFinite(ti) && ti > te]].forEach(function (r, i) {
      var row = explorerEl('div', 'xfer-cfl-row' + (r[2] ? ' newer' : ''), rows, i ? 'xfer-cfl-in' : 'xfer-cfl-ex');
      var l = explorerEl('span', 'l', row); l.textContent = T(r[0]);
      var v = explorerEl('span', 'v', row); v.textContent = xferWhen(r[1]) + (r[2] ? ' · ' + T('xfer.newer') : '');
    });
    var lab = explorerEl('label', 'xfer-cfl-all', box);
    var all = explorerEl('input', '', lab, 'xfer-cfl-all'); all.type = 'checkbox';
    var lt = explorerEl('span', '', lab); lt.textContent = T('xfer.applyAll');
    var acts = explorerEl('div', 'modal-actions', box);
    var cancel = explorerEl('button', 'btn-ghost', acts, 'xfer-cfl-cancel'); cancel.textContent = T('xfer.cancelTransfer');
    explorerEl('span', 'sp', acts);
    var skip = explorerEl('button', 'btn-ghost', acts, 'xfer-cfl-skip'); skip.textContent = T('xfer.skip');
    var both = explorerEl('button', 'btn-ghost', acts, 'xfer-cfl-rename'); both.textContent = T('xfer.keepBoth');
    var over = explorerEl('button', 'exp-btn-danger', acts, 'xfer-cfl-over'); over.textContent = T('xfer.overwrite');
    if (existing.isDir) over.disabled = true;
    var done = false;
    var finish = function (v) {
      if (done) return;
      done = true;
      if (xferDlgDone === finish) xferDlgDone = null;
      ov.classList.remove('visible');
      if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
      resolve(v);
    };
    var pick = function (a) { return function () { finish({ action: a, all: !!all.checked }); }; };
    over.addEventListener('click', function () { if (!over.disabled) pick('overwrite')(); });
    both.addEventListener('click', pick('rename'));
    skip.addEventListener('click', pick('skip'));
    cancel.addEventListener('click', function () { finish(null); });
    x.addEventListener('click', function () { finish(null); });
    ov.addEventListener('keydown', function (e) {
      if (!e) return;
      if (e.key === 'F5' || ((e.ctrlKey || e.metaKey) && String(e.key || '').toLowerCase() === 'r')) { if (e.preventDefault) e.preventDefault(); return; }
      if (e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); finish(null); }
    });
    xferDlgDone = finish;
    document.body.appendChild(ov);
    try { skip.focus(); } catch (e) {}
  });
}
// A free "name (N).ext" in a listing (keep both), '' when none up to 99.
function xferFreeName(listing, name, os) {
  for (var i = 2; i <= 99; i++) {
    var dot = name.lastIndexOf('.');
    var c = dot > 0 ? name.slice(0, dot) + ' (' + i + ')' + name.slice(dot) : name + ' (' + i + ')';
    if (!listing || !listing.has(xferNameKey(c, os))) return c;
  }
  return '';
}

// ── Sending a file to the device ──
// The listing of a folder of the device, once per batch: name -> entry; null
// when it could not be read (the device then says "already exists").
async function xferListingOf(b, ctx, dir) {
  var k = xferNameKey(dir, b.os);
  if (b.listings.has(k)) return b.listings.get(k);
  var m = new Map();
  try {
    var res = await explorerCtxCmd(ctx, 'list_directory', { path: dir }, EXPLORER_SHORT_MS);
    explorerItems(res, dir, b.os).forEach(function (e) { m.set(xferNameKey(e.name, b.os), e); });
  } catch (e) {
    if (e && (e.code === 'cancelled' || EXPLORER_BATCH_STOP.indexOf(e.code) >= 0)) throw e;
    m = null; // (a folder that does not exist yet: the agent creates it)
  }
  b.listings.set(k, m);
  return m;
}
// The file's content, base64 (the local disk's chunks joined, or a dropped
// File's): its share read so far on screen.
async function xferRead(q, it) {
  xferSetState(q, it, 'reading');
  it.pct = 0;
  var parts = [];
  if (it.src && it.src.kind === 'local') {
    var o = await localFs('reachFsOpenRead', it.src.path);
    try {
      var size = Number(o && o.size) || 0;
      if (size !== it.size) { it.batch.bytes += size - (it.size || 0); it.size = size; }
      for (var off = 0; off < size; off += XFER_CHUNK) {
        if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
        var r = await localFs('reachFsRead', o.handle, off, Math.min(XFER_CHUNK, size - off));
        parts.push(String((r && r.data) || ''));
        it.pct = Math.min(100, Math.round((off + XFER_CHUNK) / size * 100));
        xferChanged(q);
      }
    } finally { reachNative('reachFsClose', o.handle).catch(function () {}); }
  } else if (it.src && it.src.file) {
    var f = it.src.file, fs = Number(f.size) || 0;
    for (var o2 = 0; o2 < fs; o2 += XFER_CHUNK) {
      if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
      if (typeof f.readB64 === 'function') { // (a pasted file: explorer_viewer.go explorerClipFile)
        try { parts.push(await f.readB64(o2, Math.min(XFER_CHUNK, fs - o2))); } catch (e) { throw xferErr('viewer.tr.readFailed'); }
      } else {
        var buf;
        try { buf = await f.slice(o2, Math.min(fs, o2 + XFER_CHUNK)).arrayBuffer(); }
        catch (e) { throw xferErr('viewer.tr.readFailed'); }
        parts.push(explorerBytesToB64(new Uint8Array(buf)));
      }
      it.pct = Math.min(100, Math.round((o2 + XFER_CHUNK) / fs * 100));
      xferChanged(q);
    }
  }
  if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
  return parts.join('');
}
// The source of a file sent (a local disk's file, a dropped File): its
// size, its chunks in base64, its close. large: no 100 MB per file (a
// streamed transfer).
async function xferSourceOpen(it, large) {
  if (it.src && it.src.kind === 'local') {
    var o = await localFs(large ? 'reachFsOpenReadLarge' : 'reachFsOpenRead', it.src.path);
    return { size: Number(o && o.size) || 0,
      read: async function (off, n) { var r = await localFs('reachFsRead', o.handle, off, n); return String((r && r.data) || ''); },
      close: function () { reachNative('reachFsClose', o.handle).catch(function () {}); } };
  }
  if (it.src && it.src.file) {
    var f = it.src.file;
    return { size: Number(f.size) || 0,
      read: async function (off, n) {
        if (typeof f.readB64 === 'function') { try { return await f.readB64(off, n); } catch (e) { throw xferErr('viewer.tr.readFailed'); } }
        var buf;
        try { buf = await f.slice(off, Math.min(Number(f.size) || 0, off + n)).arrayBuffer(); } catch (e) { throw xferErr('viewer.tr.readFailed'); }
        return explorerBytesToB64(new Uint8Array(buf));
      },
      close: function () {} };
  }
  throw xferErr('viewer.tr.readFailed');
}
// A file to a local SSH connection, in chunks (sftp_stream.go): its progress
// is the bytes sent; stopped (cancelled, failed), its partial file goes.
async function xferStreamUp(q, it, ctx, overwrite) {
  var b = it.batch;
  xferSetState(q, it, 'reading');
  it.pct = 0;
  var src = await xferSourceOpen(it, true), h = '', done = false;
  try {
    if (src.size !== it.size) { b.bytes += src.size - (it.size || 0); it.size = src.size; }
    if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
    // (its size first: a server without SFTP takes it by SCP, scp.go)
    var o = await explorerCtxCmd(ctx, 'upload_open', { path: it.dstPath, overwrite: !!overwrite, size: src.size }, EXPLORER_SHORT_MS, xferLeft(b, 'upload_file'));
    h = (o && typeof o.handle === 'string') ? o.handle : '';
    if (!h) throw xferErr('explorer.err.badData');
    xferSetState(q, it, 'sending');
    for (var off = 0; off < src.size; off += XFER_CHUNK) {
      if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
      var data = await src.read(off, Math.min(XFER_CHUNK, src.size - off));
      if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
      await explorerCtxCmd(ctx, 'upload_write', { handle: h, offset: off, data: data }, EXPLORER_LONG_MS);
      it.pct = Math.min(100, Math.round((off + XFER_CHUNK) / src.size * 100));
      xferChanged(q);
    }
    await explorerCtxCmd(ctx, 'upload_close', { handle: h }, EXPLORER_LONG_MS);
    done = true;
  } finally {
    src.close();
    if (h && !done) explorerCtxCmd(ctx, 'upload_abort', { handle: h }, EXPLORER_SHORT_MS).catch(function () {});
  }
}
async function xferRunUp(q, it) {
  var b = it.batch, os = b.os, ctx = explorerCtxFor(q.dev), streams = xferStreams(q.dev);
  if (it.isDir) {
    xferSetState(q, it, 'sending');
    await explorerCtxCmd(ctx, 'create_directory', { path: it.dstPath }, EXPLORER_SHORT_MS, xferLeft(b, 'create_directory'));
    b.remoteDirs.add(explorerParent(it.dstPath, os));
    xferDone(q, it);
    return;
  }
  // (a stream can create an empty file; the agent's message cannot)
  if (!(it.size > 0) && !streams) { xferSkip(q, it, T('xfer.emptyFile')); return; }
  if (it.size > xferMaxBytes(q.dev)) { xferSkip(q, it, T('xfer.tooLarge', { max: reachFmtBytes(EXPLORER_MAX_BYTES) })); return; }
  var dir = explorerParent(it.dstPath, os), overwrite = false;
  var listing = await xferListingOf(b, ctx, dir);
  var resolve = async function (existing) {
    var act = await xferResolve(b, it, existing, { size: it.size, modified: it.modified });
    if (act === 'cancel') { xferCancelBatch(b, true); throw xferErr('viewer.tr.cancelled', null, 'cancelled'); }
    if (act === 'skip') return false;
    if (act === 'rename') {
      var nn = xferFreeName(listing, it.name, os);
      if (!nn) throw xferErr('local.err.exists', { name: it.name });
      it.name = nn; it.dstPath = explorerJoin(dir, nn, os); overwrite = false;
    } else overwrite = true;
    return true;
  };
  var ex = listing ? listing.get(xferNameKey(it.name, os)) : null;
  if (ex && !(await resolve(ex))) { xferSkip(q, it, T('xfer.skipped')); return; }
  if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
  if (streams) {
    for (;;) {
      try { await xferStreamUp(q, it, ctx, overwrite); break; }
      catch (e) {
        // It appeared meanwhile (or the folder could not be listed): asked now.
        if (!overwrite && e && !e.code && /already exists/i.test(e.message)) {
          if (!(await resolve({ size: NaN, modified: '' }))) { xferSkip(q, it, T('xfer.skipped')); return; }
          continue;
        }
        throw e;
      }
    }
    if (listing) listing.set(xferNameKey(it.name, os), { name: it.name, size: it.size, modified: new Date().toISOString(), isDir: false });
    b.remoteDirs.add(dir);
    xferDone(q, it);
    return;
  }
  var b64 = await xferRead(q, it);
  if (it.size > EXPLORER_MAX_BYTES) { xferSkip(q, it, T('xfer.tooLarge', { max: reachFmtBytes(EXPLORER_MAX_BYTES) })); return; }
  if (!b64) { xferSkip(q, it, T('xfer.emptyFile')); return; }
  xferSetState(q, it, 'sending');
  for (;;) {
    try {
      await explorerCtxCmd(ctx, 'upload_file', { path: it.dstPath, data: b64, overwrite: overwrite }, EXPLORER_LONG_MS, xferLeft(b, 'upload_file'));
      break;
    } catch (e) {
      // It appeared meanwhile (or the folder could not be listed): asked now.
      if (!overwrite && e && !e.code && /already exists/i.test(e.message)) {
        if (!(await resolve({ size: NaN, modified: '' }))) { xferSkip(q, it, T('xfer.skipped')); return; }
        xferSetState(q, it, 'sending');
        continue;
      }
      throw e;
    }
  }
  if (listing) listing.set(xferNameKey(it.name, os), { name: it.name, size: it.size, modified: new Date().toISOString(), isDir: false });
  b.remoteDirs.add(dir);
  xferDone(q, it);
}

// ── Receiving a file from the device ──
// The local file to write, its name's conflict settled first (before the
// device sends anything). null: skipped.
async function xferBeginWrite(q, it, dir, size, mode) {
  var b = it.batch;
  for (;;) {
    try {
      var wo = { size: size, modified: it.modified || '', conflict: mode };
      if (xferStreams(q.dev)) wo.large = true; // (a streamed transfer: no 100 MB per file)
      var r = await localFs('reachFsWriteBegin', dir, it.name, wo);
      r.mode = mode;
      return r;
    } catch (e) {
      if (!e || e.code !== 'exists' || mode !== 'fail') throw e;
      var ex = (e.info && e.info.existing) || {};
      var act = await xferResolve(b, it, ex, { size: it.size, modified: it.modified });
      if (act === 'cancel') { xferCancelBatch(b, true); throw xferErr('viewer.tr.cancelled', null, 'cancelled'); }
      if (act === 'skip') return null;
      mode = act === 'rename' ? 'rename' : 'overwrite';
    }
  }
}
function xferAbortWrite(h) { if (h && h.handle) reachNative('reachFsWriteAbort', h.handle).catch(function () {}); }
// A file from a local SSH connection, in chunks (sftp_stream.go), written
// as it comes: its progress is the bytes received.
async function xferStreamDown(q, it, ctx, parent, h) {
  var b = it.batch, rh = '';
  xferSetState(q, it, 'receiving');
  it.pct = 0;
  try {
    var o = await explorerCtxCmd(ctx, 'download_open', { path: it.src.path }, EXPLORER_SHORT_MS, xferLeft(b, 'download_file'));
    rh = (o && typeof o.handle === 'string') ? o.handle : '';
    if (!rh) throw xferErr('explorer.err.badData');
    var size = Number(o.size);
    if (!(size >= 0)) throw xferErr('explorer.err.badData');
    if (size !== it.size) {
      // (it changed since it was listed: the file as it is now)
      xferAbortWrite(h);
      b.bytes += size - it.size; it.size = size;
      var mode = h.mode;
      h = await localFs('reachFsWriteBegin', parent, it.name, { size: size, modified: it.modified || '', conflict: mode, large: true });
      h.mode = mode;
    }
    for (var off = 0; off < size; off += XFER_CHUNK) {
      if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
      var n = Math.min(XFER_CHUNK, size - off);
      var r = await explorerCtxCmd(ctx, 'download_read', { handle: rh, offset: off, length: n }, EXPLORER_LONG_MS);
      var d = (r && typeof r.data === 'string') ? r.data : '';
      if (xferB64Size(d) !== n) throw xferErr('explorer.err.badData');
      if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
      await localFs('reachFsWrite', h.handle, off, d);
      it.pct = Math.min(100, Math.round((off + n) / size * 100));
      xferChanged(q);
    }
    if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
    xferSetState(q, it, 'writing');
    var end = await localFs('reachFsWriteEnd', h.handle);
    if (end && typeof end.name === 'string') it.name = end.name;
    it.localPath = end && end.path;
  } catch (e) { xferAbortWrite(h); throw e; }
  finally { if (rh) explorerCtxCmd(ctx, 'download_close', { handle: rh }, EXPLORER_SHORT_MS).catch(function () {}); }
  xferDone(q, it);
}
async function xferRunDown(q, it) {
  var b = it.batch, ctx = explorerCtxFor(q.dev);
  var pr = xferRelParent(it.rel), parent = pr ? b.localDirs[pr] : b.dstDir;
  if (!parent) throw xferErr('xfer.parentMissing');
  if (it.isDir) {
    xferSetState(q, it, 'writing');
    var r = await localFs('reachFsMkdir', parent, it.name);
    b.localDirs[it.rel] = String((r && r.path) || '');
    xferDone(q, it);
    return;
  }
  if (it.size > xferMaxBytes(q.dev)) { xferSkip(q, it, T('xfer.tooLarge', { max: reachFmtBytes(EXPLORER_MAX_BYTES) })); return; }
  var h = await xferBeginWrite(q, it, parent, it.size, 'fail');
  if (!h) { xferSkip(q, it, T('xfer.skipped')); return; }
  if (xferStreams(q.dev)) return xferStreamDown(q, it, ctx, parent, h);
  xferSetState(q, it, 'receiving');
  var res;
  try { res = await explorerCtxCmd(ctx, 'download_file', { path: it.src.path }, EXPLORER_LONG_MS, xferLeft(b, 'download_file')); }
  catch (e) { xferAbortWrite(h); throw e; }
  if (it.cancelled) { xferAbortWrite(h); throw xferErr('viewer.tr.cancelled', null, 'cancelled'); }
  var b64 = (res && typeof res.data === 'string') ? res.data : '';
  var n = xferB64Size(b64);
  if (n < 0) { xferAbortWrite(h); throw xferErr('explorer.err.badData'); }
  if (n > EXPLORER_MAX_BYTES) { xferAbortWrite(h); xferSkip(q, it, T('xfer.tooLarge', { max: reachFmtBytes(EXPLORER_MAX_BYTES) })); return; }
  if (n !== it.size) {
    // (it changed since it was listed: the file as it came)
    xferAbortWrite(h);
    b.bytes += n - it.size; it.size = n;
    var mode = h.mode;
    h = await localFs('reachFsWriteBegin', parent, it.name, { size: n, modified: it.modified || '', conflict: mode });
    h.mode = mode;
  }
  xferSetState(q, it, 'writing');
  it.pct = 0;
  try {
    for (var i = 0; i < b64.length; i += XFER_SLICE) {
      if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
      await localFs('reachFsWrite', h.handle, i / 4 * 3, b64.slice(i, i + XFER_SLICE));
      it.pct = Math.min(100, Math.round((i + XFER_SLICE) / b64.length * 100));
      xferChanged(q);
    }
    if (it.cancelled) throw xferErr('viewer.tr.cancelled', null, 'cancelled');
    var end = await localFs('reachFsWriteEnd', h.handle);
    if (end && typeof end.name === 'string') it.name = end.name;
    it.localPath = end && end.path;
  } catch (e) { xferAbortWrite(h); throw e; }
  b64 = '';
  xferDone(q, it);
}

// ── Batches from the panes ──
// The local pane's selection (names in localDir) to the device's folder.
async function xferUploadLocal(tab, localDir, names, remoteDir) {
  var dev = tab.device, os = tab.ex.os, q = xferQueueFor(dev);
  q.planning++; xferChanged(q);
  var walk;
  try { walk = await localFs(xferStreams(dev) ? 'reachFsWalkLarge' : 'reachFsWalk', localDir, names); }
  catch (e) { q.planning--; xferChanged(q); if (e && e.code === 'limit') reachNotify(T('xfer.limit'), 'warn'); else localFsSay(e); return null; }
  q.planning--; xferChanged(q);
  var plan = xferPlanUpload(walk && Array.isArray(walk.items) ? walk.items : [], remoteDir, os, function (w) {
    return { kind: 'local', path: explorerJoin(localDir, String(w.rel).split('/').join(localSep()), LOCAL_OS) };
  });
  var sk = (walk && walk.skipped) || {};
  xferSaySkipped(Number(sk.links) || 0, Number(sk.tooLarge) || 0, plan.invalid, Number(sk.denied) || 0);
  if (!plan.items.length) return null;
  if (!(await xferConfirmBig(plan.files, plan.bytes, remoteDir))) return null;
  return xferStart(dev, 'up', { origin: 'explorer', dstDir: remoteDir, os: os, skipped: (Number(sk.tooLarge) || 0) + plan.invalid }, plan.items);
}
// What is left out, said once.
function xferSaySkipped(links, tooLarge, invalid, denied) {
  var parts = [];
  if (tooLarge) parts.push(T('xfer.skippedTooLarge', { count: tooLarge, max: reachFmtBytes(EXPLORER_MAX_BYTES) }));
  if (links) parts.push(T('xfer.skippedLinks', { count: links }));
  if (invalid) parts.push(T('xfer.skippedInvalid', { count: invalid }));
  if (denied) parts.push(T('xfer.skippedDenied', { count: denied }));
  if (parts.length) reachNotify(T('xfer.leftOut', { list: parts.join(', ') }), 'warn');
}
// A walk's items ({rel 'a/b.txt', isDir, size}) as sends to remoteDir:
// folders only when empty (the agent creates a file's folders), a name the
// device's OS refuses left out (with what is under it).
function xferPlanUpload(walkItems, remoteDir, os, srcOf) {
  var out = [], invalid = 0, files = 0, bytes = 0, full = new Set();
  walkItems.forEach(function (w) {
    var parts = String(w.rel).split('/');
    for (var i = 1; i < parts.length; i++) full.add(parts.slice(0, i).join('/'));
  });
  walkItems.forEach(function (w) {
    var parts = String(w.rel || '').split('/');
    if (!w.rel || parts.some(function (p) { return !explorerValidName(p, os); })) { invalid++; return; }
    var dst = remoteDir;
    parts.forEach(function (p) { dst = explorerJoin(dst, p, os); });
    var name = parts[parts.length - 1];
    if (w.isDir) { if (!full.has(w.rel)) out.push({ isDir: true, name: name, rel: w.rel, dstPath: dst }); return; }
    var size = Number(w.size) || 0;
    files++; bytes += size;
    out.push({ isDir: false, name: name, rel: w.rel, dstPath: dst, size: size, modified: typeof w.modified === 'string' ? w.modified : '', src: srcOf(w) });
  });
  return { items: out, invalid: invalid, files: files, bytes: bytes };
}
// Files dropped (or picked): File objects, folders read through their
// FileSystemEntry (the batch's limits). Resolves to { items: [{rel, isDir,
// size, file}], links, tooLarge, limit }.
// dev (optional): the device they go to (its limits: xferMaxBytes).
async function xferCollect(dt, dev) {
  var out = { items: [], tooLarge: 0, limit: '', total: 0 };
  var maxFile = dev ? xferMaxBytes(dev) : EXPLORER_MAX_BYTES, maxTotal = dev ? xferMaxTotal(dev) : XFER_MAX_TOTAL;
  var add = function (rel, file, isDir) {
    if (out.items.length >= XFER_MAX_ENTRIES) { out.limit = 'entries'; return false; }
    if (!isDir) {
      var size = Number(file && file.size) || 0;
      if (size > maxFile) { out.tooLarge++; return true; }
      out.total += size;
      if (out.total > maxTotal) { out.limit = 'bytes'; return false; }
      out.items.push({ rel: rel, isDir: false, size: size, modified: file && file.lastModified ? new Date(file.lastModified).toISOString() : '', file: file });
    } else out.items.push({ rel: rel, isDir: true, size: 0 });
    return true;
  };
  var readAll = function (reader) {
    return new Promise(function (resolve) {
      var all = [];
      var next = function () {
        try {
          reader.readEntries(function (batch) {
            if (!batch || !batch.length) { resolve(all); return; }
            all = all.concat(Array.prototype.slice.call(batch));
            next();
          }, function () { resolve(all); });
        } catch (e) { resolve(all); }
      };
      next();
    });
  };
  var fileOf = function (entry) { return new Promise(function (resolve) { try { entry.file(resolve, function () { resolve(null); }); } catch (e) { resolve(null); } }); };
  var walk = async function (entry, rel, depth) {
    if (out.limit) return;
    if (depth > XFER_MAX_DEPTH) { out.limit = 'depth'; return; }
    if (entry.isDirectory) {
      if (!add(rel, null, true)) return;
      var kids = await readAll(entry.createReader());
      for (var i = 0; i < kids.length && !out.limit; i++) await walk(kids[i], rel + '/' + kids[i].name, depth + 1);
    } else if (entry.isFile) {
      var f = await fileOf(entry);
      if (f) add(rel, f, false);
    }
  };
  var items = dt && dt.items, roots = [];
  if (items && items.length) {
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      if (!it || it.kind !== 'file') continue;
      var entry = typeof it.webkitGetAsEntry === 'function' ? it.webkitGetAsEntry() : null;
      if (entry) roots.push({ entry: entry });
      else { var f = it.getAsFile(); if (f) roots.push({ file: f }); }
    }
  } else if (dt && dt.files) Array.prototype.slice.call(dt.files).forEach(function (f) { roots.push({ file: f }); });
  else if (Array.isArray(dt)) dt.forEach(function (f) { roots.push({ file: f }); });
  for (var j = 0; j < roots.length && !out.limit; j++) {
    var r = roots[j];
    if (r.entry) await walk(r.entry, r.entry.name, 1);
    else add(String(r.file.name || ''), r.file, false);
  }
  return out;
}
// Collected files sent to remoteDir of the device (origin: who asked; a
// terminal gives its tab's id). Resolves to the batch, or null.
async function xferUploadCollected(dev, remoteDir, col, opts) {
  opts = opts || {};
  var os = explorerOs(dev);
  if (col.limit) { reachNotify(T('xfer.limit'), 'warn'); return null; }
  var plan = xferPlanUpload(col.items, remoteDir, os, function (w) { return { kind: 'file', file: w.file }; });
  xferSaySkipped(0, col.tooLarge, plan.invalid, 0);
  if (!plan.items.length) return null;
  if (!(await xferConfirmBig(plan.files, plan.bytes, remoteDir))) return null;
  return xferStart(dev, 'up', { origin: opts.origin || 'explorer', dstDir: remoteDir, os: os, skipped: col.tooLarge + plan.invalid }, plan.items);
}
// A drop (or the file picker's files) on the device's pane: to its folder.
async function xferUploadDropped(tab, dt, remoteDir) {
  var q = xferQueueFor(tab.device);
  q.planning++; xferChanged(q);
  var col;
  try { col = await xferCollect(dt, tab.device); } finally { q.planning--; xferChanged(q); }
  return xferUploadCollected(tab.device, remoteDir, col, { origin: 'explorer' });
}
// The device's elements (their paths, from its pane) to a local folder.
function xferDownloadPaths(tab, paths, localDir) {
  var set = new Set(paths), items = tab.ex.items.filter(function (it) { return set.has(it.path); });
  return xferDownloadItems(tab, items, localDir);
}
async function xferDownloadItems(tab, items, localDir) {
  var dev = tab.device, os = tab.ex.os, ctx = explorerCtxFor(dev), q = xferQueueFor(dev);
  if (!items.length || !localDir) return null;
  var plan = { items: [], links: 0, tooLarge: 0, invalid: 0, denied: 0, files: 0, bytes: 0, entries: 0, limit: '' };
  var add = function (o) {
    if (++plan.entries > XFER_MAX_ENTRIES) { plan.limit = 'entries'; return false; }
    if (!o.isDir) {
      plan.files++; plan.bytes += o.size;
      if (plan.bytes > xferMaxTotal(dev)) { plan.limit = 'bytes'; return false; }
    }
    plan.items.push(o);
    return true;
  };
  var file = function (e, rel) {
    if (e.size > xferMaxBytes(dev)) { plan.tooLarge++; return true; }
    return add({ isDir: false, name: e.name, rel: rel, size: e.size, modified: e.modified, src: { path: e.path } });
  };
  var dir = async function (e, rel, depth) {
    if (depth > XFER_MAX_DEPTH) { plan.limit = 'depth'; return; }
    if (!add({ isDir: true, name: e.name, rel: rel, src: { path: e.path } })) return;
    var res;
    try { res = await explorerCtxCmd(ctx, 'list_directory', { path: e.path }, EXPLORER_SHORT_MS); }
    catch (x) {
      if (x && (x.code === 'cancelled' || EXPLORER_BATCH_STOP.indexOf(x.code) >= 0)) throw x;
      plan.denied++; // (a folder it cannot read: created empty, said)
      return;
    }
    var kids = explorerItems(res, e.path, os);
    for (var i = 0; i < kids.length && !plan.limit; i++) {
      var k = kids[i];
      if (explorerIsLink(k)) { plan.links++; continue; }
      if (k.isDir) await dir(k, rel + '/' + k.name, depth + 1);
      else if (!file(k, rel + '/' + k.name)) return;
    }
  };
  q.planning++; xferChanged(q);
  try {
    for (var i = 0; i < items.length && !plan.limit; i++) {
      var it = items[i];
      if (explorerIsLink(it)) { plan.links++; continue; }
      if (it.isDir) await dir(it, it.name, 1);
      else file(it, it.name);
    }
  } catch (e) {
    q.planning--; xferChanged(q);
    if (e && e.code !== 'cancelled') reachNotify(explorerErrText(e, 'list'), 'warn');
    return null;
  }
  q.planning--; xferChanged(q);
  if (plan.limit) { reachNotify(T('xfer.limit'), 'warn'); return null; }
  xferSaySkipped(plan.links, plan.tooLarge, 0, plan.denied);
  if (!plan.items.length) return null;
  if (!(await xferConfirmBig(plan.files, plan.bytes, localDir))) return null;
  return xferStart(dev, 'down', { origin: 'explorer', dstDir: localDir, os: os, skipped: plan.tooLarge }, plan.items);
}

// ── The dock (the explorer's, split view) ──
var XFER_ICON = {
  up: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="19" x2="12" y2="5"/><polyline points="5 12 12 5 19 12"/></svg>',
  down: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><polyline points="19 12 12 19 5 12"/></svg>',
  folder: '<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor" stroke="none"><path d="M10 4H4a2 2 0 00-2 2v12a2 2 0 002 2h16a2 2 0 002-2V8a2 2 0 00-2-2h-8l-2-2z"/></svg>',
  x: '<svg width="10" height="10" viewBox="0 0 10 10"><path d="M1 1l8 8M9 1l-8 8" fill="none" stroke="currentColor" stroke-width="1.6"/></svg>',
  retry: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 11-2.12-9.36L23 10"/></svg>',
};
// An item's state, in words (the dock, a terminal's chip).
function xferStateText(it) {
  switch (it.state) {
    case 'queued': return T('viewer.tr.queued');
    case 'reading': return T('xfer.state.reading', { pct: Math.max(0, it.pct) });
    case 'writing': return it.isDir ? T('xfer.state.mkdir') : T('xfer.state.writing', { pct: Math.max(0, it.pct) });
    case 'sending': return it.stopAfter ? T('xfer.stopping') : it.isDir ? T('xfer.state.mkdir') : T('explorer.tr.uploading');
    case 'receiving': return T('explorer.tr.downloading');
    case 'done': return T('viewer.tr.done');
    case 'skipped': return it.error || T('xfer.skipped');
    case 'cancelled': return T('viewer.tr.cancelled');
    case 'error': return it.error || T('viewer.tr.failed');
  }
  return '';
}
// The batches' progress: files and bytes done of all (the live ones).
function xferSummary(q) {
  var files = 0, done = 0, bytes = 0, doneBytes = 0;
  q.batches.forEach(function (b) { if (b.notified) return; files += b.files; done += b.done + b.failed + b.skipped; bytes += b.bytes; doneBytes += b.doneBytes; });
  if (q.planning > 0 && !files) return T('xfer.preparing');
  if (!files) return '';
  return T('xfer.summary', { done: Math.min(done, files), files: files, doneSize: reachFmtBytes(doneBytes), size: reachFmtBytes(bytes) });
}
function xpDockRender(tab) {
  var ex = tab.ex, dock = ex && ex.ui.dock;
  if (!dock) return;
  var q = xferQueues.get(devKey(tab.device)) || null;
  var items = q ? q.items : [], edits = typeof editRowsFor === 'function' ? editRowsFor(tab.device) : [];
  dock.innerHTML = '';
  var empty = !items.length && !edits.length && !(q && q.planning);
  dock.classList.toggle('empty', empty);
  if (empty) return;
  if (!ex.dockTab || (ex.dockTab === 'tr' && !items.length && !(q && q.planning) && edits.length) || (ex.dockTab === 'ed' && !edits.length)) ex.dockTab = (items.length || (q && q.planning)) ? 'tr' : 'ed';
  var head = explorerEl('div', 'xp-dock-head', dock);
  var tabBtn = function (id, key, n, on) {
    var b = explorerEl('button', 'xp-dock-tab' + (on ? ' on' : ''), head, id);
    b.textContent = T(key) + (n ? ' · ' + n : '');
    b.setAttribute('aria-pressed', on ? 'true' : 'false');
    b.addEventListener('click', function () { ex.dockTab = id === 'xp-dock-tr' ? 'tr' : 'ed'; xpDockRender(tab); });
  };
  var live = items.filter(function (it) { return it.state === 'queued' || xferLive(it); }).length;
  tabBtn('xp-dock-tr', 'xfer.tabTitle', live, ex.dockTab === 'tr');
  tabBtn('xp-dock-ed', 'edit.tabTitle', edits.length, ex.dockTab === 'ed');
  var sum = explorerEl('span', 'xp-dock-sum', head, 'xp-dock-sum');
  var body = explorerEl('div', 'xp-dock-body', dock, 'xp-dock-body');
  if (ex.dockTab === 'ed') {
    sum.textContent = '';
    if (typeof editRenderRows === 'function') editRenderRows(body, edits, tab);
    return;
  }
  sum.textContent = q ? xferSummary(q) : '';
  if (q) {
    var pause = explorerEl('button', 'btn-sm', head, 'xp-dock-pause');
    pause.textContent = T(q.paused ? 'xfer.resume' : 'xfer.pause');
    pause.disabled = !q.paused && !items.some(function (it) { return it.state === 'queued'; });
    pause.addEventListener('click', function () { xferTogglePause(q); });
    var ca = explorerEl('button', 'btn-sm', head, 'xp-dock-cancel');
    ca.textContent = T('xfer.cancelAll');
    ca.disabled = !live;
    ca.addEventListener('click', function () { xferCancelAll(q); });
    var cl = explorerEl('button', 'btn-sm', head, 'xp-dock-clear');
    cl.textContent = T('viewer.tr.clearFinished');
    cl.disabled = items.length === live;
    cl.addEventListener('click', function () { xferClearFinished(q); });
  }
  if (!items.length) { var e0 = explorerEl('div', 'xp-dock-empty', body); e0.textContent = T('xfer.preparing'); return; }
  var rows = xferDockRows(items);
  rows.shown.forEach(function (it) { body.appendChild(xferRowEl(q, it)); });
  if (rows.hidden > 0) { var more = explorerEl('div', 'xp-dock-empty', body, 'xp-dock-more'); more.textContent = T('xfer.moreRows', { count: rows.hidden }); }
}
// The rows the dock draws, in the queue's order: every one on its way, the
// next queued ones, the last finished ones and errors (each kind capped);
// hidden: how many are not drawn.
function xferDockRows(items) {
  var n = items.length;
  if (n <= XFER_DOCK_QUEUED + XFER_DOCK_FINISHED + 4) return { shown: items, hidden: 0 };
  var keep = new Set(), queued = 0, done = 0, errs = 0;
  for (var i = 0; i < n; i++) {
    var it = items[i];
    if (it.state === 'queued') { if (queued < XFER_DOCK_QUEUED) { keep.add(it); queued++; } }
    else if (xferLive(it)) keep.add(it);
  }
  for (var j = n - 1; j >= 0; j--) {
    var x = items[j];
    if (x.state === 'error') { if (errs < XFER_DOCK_FINISHED) { keep.add(x); errs++; } }
    else if (x.state !== 'queued' && !xferLive(x)) { if (done < XFER_DOCK_FINISHED) { keep.add(x); done++; } }
  }
  var shown = items.filter(function (it) { return keep.has(it); });
  return { shown: shown, hidden: n - shown.length };
}
function xferRowEl(q, it) {
  var liveNow = xferLive(it) && it.state !== 'queued';
  var row = explorerEl('div', 'xp-row ' + it.state + (liveNow ? ' live' : ''));
  row.dataset.id = String(it.id);
  var ic = explorerEl('span', 'xp-row-ico', row); ic.innerHTML = it.isDir ? XFER_ICON.folder : it.dir === 'up' ? XFER_ICON.up : XFER_ICON.down;
  var nm = explorerEl('span', 'xp-row-name', row); nm.textContent = it.name; nm.title = it.rel || it.name;
  var to = explorerEl('span', 'xp-row-to', row);
  to.textContent = '→ ' + (it.dir === 'up' ? explorerParent(it.dstPath, it.batch.os) : (xferRelParent(it.rel) ? it.batch.dstDir + localSep() + xferRelParent(it.rel).split('/').join(localSep()) : it.batch.dstDir));
  to.title = to.textContent;
  var sz = explorerEl('span', 'xp-row-size', row); sz.textContent = it.isDir ? '' : reachFmtBytes(it.size);
  // (its place kept on every row: the columns stay aligned)
  var bar = explorerEl('span', 'xp-bar' + (!liveNow ? ' none' : it.pct >= 0 ? '' : ' ind'), row);
  if (liveNow) {
    var fill = explorerEl('i', '', bar);
    if (it.pct >= 0) fill.style.width = Math.max(2, Math.min(100, it.pct)) + '%';
  }
  var st = explorerEl('span', 'xp-row-st', row); st.textContent = xferStateText(it); st.title = st.textContent;
  var act = null;
  if (it.state === 'error') {
    act = explorerEl('button', 'xp-row-x', row); act.innerHTML = XFER_ICON.retry; act.title = T('common.retry');
    act.addEventListener('click', function () { xferRetry(q, it); });
  } else if (it.state === 'queued' || xferLive(it)) {
    act = explorerEl('button', 'xp-row-x', row); act.innerHTML = XFER_ICON.x; act.title = T('common.cancel');
    act.disabled = !!it.stopAfter;
    act.addEventListener('click', function () { xferCancelItem(q, it); });
  }
  if (act) act.setAttribute('aria-label', act.title + ' · ' + it.name);
  return row;
}
`
