package main

// edit_page.go — the editing round trip of the Files page (main window), as
// WinSCP does it: a double click (or Enter, or "Edit") on a device's file
// downloads it into a working copy of this PC, opens it in the editor of
// Settings > Files, and sends it back over the original at every save.
//
// The local side is Go's (editsession*.go: the working copies under
// %LOCALAPPDATA%\OblireachClient\edit, the watcher, the editor started on a
// copy it owns); the device's side is the explorer's gated path only
// (explorerCtxCmd: download_file, list_directory, upload_file overwrite:
// true, with the tenant's 2FA step-up and the privacy code as for any
// explorer action). Each save the watcher reports ('changed', its seq) is
// frozen in a snapshot; before it is sent, the file's folder on the device
// is listed again: changed there since it was opened (its size, its date to
// the second), or gone, the operator chooses (overwrite, save as a copy,
// skip this save; create it again). A skipped save stays unsent: "Upload
// now", "Send them, then stop" still send it (asked again). The baseline (the
// device's file when last read) is kept by Go too (reachEditRemote): a page
// that reloaded takes it back; one it cannot tell is asked about before the
// first send, never overwritten in silence. A newer save while one is on its
// way is sent after it (the ones in between never). Settings > Files may ask
// before each send. Every question of a send (the confirmation, a change on
// the device, the 2FA, the privacy code) flashes the taskbar: the operator
// is in the editor. Above the size of Settings > Files the operator is
// warned first; above 100 MB (the explorer's limit both ways) it is refused.
// A working copy is tied to its server's address (Go): moved, it is never
// sent (server_changed), it stops.
//
// On screen: the explorer's dock "Editing" (edit rows: the file, where it
// is, its state, Open again / Upload now / Stop), the Files page header's
// pill "N being edited" and its popover (every device's), the toasts. A
// sign-out, a server or tenant left, a device no longer listed for this
// operator stop them (the working copies with unsent changes kept on disk,
// a day).
//
// Settings > Files (renderFilesSection): the editor (Notepad, the type's
// default application, another program picked in Windows' own dialog: the
// page never names a program), the size that warns, the upload confirmation,
// hidden files in the local pane.
//
// Both consts are injected verbatim after the transfers (buildUI, not
// through Sprintf: '%' is safe): no backtick here.

const editCSS = `
/* ── Editing round trip (edit_page.go) ── */
.xp-row .xp-row-path{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted2);font-size:11px}
.xp-st{flex-shrink:0;display:inline-flex;align-items:center;gap:5px;padding:2px 8px;border-radius:7px;background:rgb(var(--ink) / .06);color:var(--muted);font-size:10.5px;font-weight:600;white-space:nowrap}
.xp-st.ok{background:rgb(var(--success-rgb) / .12);color:var(--success)}
.xp-st.warn{background:rgb(var(--warn-rgb) / .12);color:var(--warn)}
.xp-st.bad{background:rgb(var(--danger-rgb) / .12);color:var(--danger)}
.xp-st.busy{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l);animation:xp-pulse 1.4s ease-in-out infinite}
@keyframes xp-pulse{0%,100%{opacity:1}50%{opacity:.55}}
.xp-row-acts{flex-shrink:0;display:flex;align-items:center;gap:4px}
.xp-row-acts .btn-sm{padding:4px 9px;font-size:11px}
.xp-row-acts .btn-sm:disabled{opacity:.4;cursor:default}
.xp-row-srv{width:7px;height:7px;flex-shrink:0;border-radius:50%}
.files-edit-pill{display:inline-flex;align-items:center;gap:6px;padding:6px 11px;border:none;border-radius:10px;background:rgb(var(--accent-rgb) / .12);color:var(--accent-l);font-size:11.5px;font-weight:600;cursor:pointer}
.files-edit-pill:hover{background:rgb(var(--accent-rgb) / .2)}
.files-edit-pill:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.files-edit-pop{position:fixed;z-index:390;width:520px;max-width:calc(100vw - 24px);max-height:60vh;overflow:auto;padding:8px;border-radius:14px;background:var(--bg2);box-shadow:0 10px 30px var(--shadow)}
.files-edit-pop-h{padding:4px 8px 8px;font-size:12px;font-weight:600;color:var(--muted)}
.files-edit-pop .xp-row-dev{flex-shrink:0;max-width:26%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted);font-size:11px}
.set-files-opts{display:flex;flex-direction:column;gap:8px;align-items:flex-start}
.set-files-opt{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--text);cursor:pointer}
.set-files-opt input{accent-color:var(--accent);width:15px;height:15px;cursor:pointer}
.set-files-opt input:disabled{cursor:default}
.set-files-opt input:disabled + span{opacity:.5}
.set-files-prog{display:flex;align-items:center;gap:8px}
.set-files-prog .nm{max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted);font-size:12px}
.set-files-err{min-height:16px;font-size:11.5px;color:var(--danger)}
`

const editJS = `
// ═════════════════════════════════════════════════════════════════════════
// Editing round trip (edit_page.go; Go: editsession*.go)
// ═════════════════════════════════════════════════════════════════════════
var EDIT_MAX = 50;
var editSessions = new Map(); // uid -> e (e.id: Go's, once known)
var editUid = 0;
var editSettingsCache = { editor: 'notepad', editorPath: '', editorName: '', warnMB: 10, confirmUpload: false, showHidden: false };
var editRecovered = false;
var EDIT_ICON = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 013 3L7 19l-4 1 1-4z"/></svg>';

// ── Settings > Files (cached; Go keeps them) ──
function editSettingsApply(s) {
  if (!s || typeof s !== 'object') return editSettingsCache;
  var ed = s.editor === 'system' || s.editor === 'custom' ? s.editor : 'notepad';
  var w = Number(s.warnMB);
  editSettingsCache = { editor: ed, editorPath: typeof s.editorPath === 'string' ? s.editorPath : '', editorName: typeof s.editorName === 'string' ? s.editorName.slice(0, 80) : '',
    warnMB: [1, 5, 10, 25, 50].indexOf(w) >= 0 ? w : 10, confirmUpload: s.confirmUpload === true, showHidden: s.showHidden === true };
  return editSettingsCache;
}
function editSettingsNow() { return editSettingsCache; }
async function editSettingsLoad() {
  if (!localFsAvailable() || typeof window.reachFilesSettingsGet !== 'function') return editSettingsCache;
  try { editSettingsApply(await reachNative('reachFilesSettingsGet')); } catch (e) {}
  return editSettingsCache;
}
// The editor's name in a sentence ("Notepad", "the default application", a program's).
function editEditorName(s) {
  s = s || editSettingsCache;
  if (s.editor === 'custom' && s.editorName) return s.editorName;
  if (s.editor === 'system') return T('edit.defaultApp');
  return T('edit.notepad');
}

// ── The working copies ──
function editList() { return Array.from(editSessions.values()); }
function editById(id) { var f = null; editSessions.forEach(function (e) { if (e.id && e.id === id) f = e; }); return f; }
function editFind(key, path) { var f = null; editSessions.forEach(function (e) { if (e.key === key && e.remotePath === path) f = e; }); return f; }
function editRowsFor(dev) { var k = devKey(dev); return editList().filter(function (e) { return e.key === k; }); }
function editNew(dev, it, os) {
  var e = { uid: ++editUid, id: '', key: devKey(dev), dev: dev, os: os, remotePath: it.path, name: it.name, size: it.size || 0,
    state: 'downloading', seq: 0, sentSeq: 0, skippedSeq: 0, baseline: null, lastSyncAt: 0, error: '', editorName: '', busy: false, unsent: false };
  editSessions.set(e.uid, e);
  return e;
}
function editForget(e) { editSessions.delete(e.uid); editChanged(); }
function editChanged() {
  toolTabs.forEach(function (t) { if (t && t.kind === 'files' && t.ex && t.ex.split && !t.ex.disposed) xpDockRender(t); });
  filesEditPillRender();
}
function editSetState(e, st, err) { e.state = st; if (err !== undefined) e.error = err || ''; editChanged(); }
function editCtx(e) { return explorerCtxFor(e.dev); }
function editParent(e) { return explorerParent(e.remotePath, e.os); }
// The file's entry on the device now: {size, modified}, null (not there),
// or throws (it could not be listed). opts: explorerCmd's (onPrompt: a send
// in the background, its 2FA flashes the taskbar).
async function editRemoteEntry(e, opts) {
  var dir = editParent(e);
  var res = await explorerCtxCmd(editCtx(e), 'list_directory', { path: dir }, EXPLORER_SHORT_MS, undefined, opts);
  var found = null, name = e.os === 'windows' ? e.name.toLowerCase() : e.name;
  explorerItems(res, dir, e.os).forEach(function (x) { if ((e.os === 'windows' ? x.name.toLowerCase() : x.name) === name && !x.isDir) found = { size: x.size, modified: x.modified }; });
  return found;
}
// The baseline again (after a download, an upload); unknown date when the
// folder could not be listed (only the size is compared then). Go keeps it
// too (reachEditRemote): a reload of the page finds it again.
async function editRebase(e, size) {
  try { var r = await editRemoteEntry(e); e.baseline = r || { size: size, modified: '' }; }
  catch (x) { e.baseline = { size: size, modified: '' }; }
  editKeepBaseline(e);
}
function editKeepBaseline(e) {
  if (!e.id || !e.baseline || typeof window.reachEditRemote !== 'function') return;
  reachNative('reachEditRemote', e.id, { size: Number(e.baseline.size) || 0, modified: String(e.baseline.modified || '').slice(0, 64) }).catch(function () {});
}
// Changed on the device since the baseline. No baseline (a copy taken back
// after a reload that Go could not tell): changed, so the operator is asked
// (never overwritten in silence).
function editRemoteChanged(base, cur) {
  if (!cur) return false;
  if (!base) return true;
  if (Number(base.size) !== Number(cur.size)) return true;
  return !!base.modified && !!cur.modified && base.modified !== cur.modified;
}
// The question's words: changed there, or not known (no baseline).
function editChangedText(e) {
  return e.baseline ? T('edit.remoteChanged', { name: e.name }) : T('edit.remoteUnverified', { name: e.name });
}
// A device's file downloaded into a working copy (a write handle), its size
// as it came.
async function editDownloadInto(e, begin) {
  var res = await explorerCtxCmd(editCtx(e), 'download_file', { path: e.remotePath }, EXPLORER_LONG_MS);
  var b64 = (res && typeof res.data === 'string') ? res.data : '';
  var n = xferB64Size(b64);
  if (n < 0) throw xferErr('explorer.err.badData');
  if (n > EXPLORER_MAX_BYTES) throw localFsErr({ code: 'too_large' });
  var h = await begin(n);
  try {
    for (var i = 0; i < b64.length; i += XFER_SLICE) await localFs('reachFsWrite', h.handle, i / 4 * 3, b64.slice(i, i + XFER_SLICE));
    var end = await localFs('reachFsWriteEnd', h.handle);
    e.size = n;
    return end || {};
  } catch (x) { reachNative('reachFsWriteAbort', h.handle).catch(function () {}); throw x; }
}

// ── Opening a file for editing (the device's pane) ──
async function editOpen(tab, it) {
  if (!it || it.isDir) return;
  if (!localEditAvailable()) { explorerDownload(tab, [it]); return; }
  var dev = tab.device, key = devKey(dev);
  var ex = editFind(key, it.path);
  if (ex) { editReopen(ex); return; }
  if (it.size > EXPLORER_MAX_BYTES) { reachNotify(T('edit.tooLarge', { name: it.name, max: reachFmtBytes(EXPLORER_MAX_BYTES) }), 'warn'); return; }
  if (editSessions.size >= EDIT_MAX) { reachNotify(T('edit.tooMany', { count: EDIT_MAX }), 'warn'); return; }
  var set = await editSettingsLoad();
  if (it.size > set.warnMB * 1024 * 1024) {
    var ok = await explorerDialog({ tab: tab, title: T('edit.largeTitle'), text: T('edit.largeWarn', { size: reachFmtBytes(it.size), editor: editEditorName(set) }), ok: 'edit.editAnyway', icon: EDIT_ICON });
    if (ok !== true || tab.ex.disposed) return;
  }
  if (editFind(key, it.path)) return; // (opened meanwhile)
  var e = editNew(dev, it, tab.ex.os);
  tab.ex.dockTab = 'ed';
  editChanged();
  var binary = false, planned = set.editor;
  try {
    var first = null;
    var end = await editDownloadInto(e, async function (n) {
      var b = await localFs('reachEditBegin', { profile: devPid(dev) || '', deviceId: String(dev.id), remotePath: it.path, name: it.name, size: n, modified: it.modified || '' });
      e.id = String((b && b.editId) || '');
      first = b;
      return b;
    });
    binary = !!end.binary;
    // (the editor Go will start for it: "the default application" of a
    // script or a program is Notepad)
    if (end.editor === 'notepad' || end.editor === 'system' || end.editor === 'custom') planned = end.editor;
    if (!first) throw localFsErr({ code: 'io' });
    await editRebase(e, e.size);
  } catch (err) {
    if (e.id) localFs('reachEditStop', e.id, { force: true, keep: false }).catch(function () {});
    editForget(e);
    if (err && err.code === 'exists' && err.info && err.info.editId) { reachNotify(T('edit.already', { name: it.name }), 'warn'); return; }
    if (err && err.code !== 'cancelled') reachNotify(T('edit.openFailed', { name: it.name, error: xferErrText(err) }), 'warn');
    return;
  }
  // A binary file warned of only when Notepad opens it (its own application
  // reads it as it is: a .docx in Word, a picture in its viewer).
  if (binary && planned === 'notepad') {
    var yes = await explorerDialog({ tab: tab, title: T('edit.binaryTitle'), text: T('edit.binary', { name: it.name, editor: T('edit.notepad') }), ok: 'edit.openAnyway', danger: true, icon: EDIT_ICON });
    if (yes !== true) { localFs('reachEditStop', e.id, { force: true, keep: false }).catch(function () {}); editForget(e); return; }
  }
  await editLaunch(e, true);
}
// The editor on the working copy (Go starts it: editsession_windows.go).
async function editLaunch(e, first) {
  editSetState(e, 'launching');
  var r;
  try { r = await localFs('reachEditLaunch', e.id); }
  catch (err) { editSetState(e, 'error', localFsErrText(err)); reachNotify(T('edit.launchFailed', { name: e.name, error: localFsErrText(err) }), 'warn'); return false; }
  e.editorName = r && r.editor === 'custom' && r.editorName ? String(r.editorName).slice(0, 80) : r && r.editor === 'system' ? T('edit.defaultApp') : T('edit.notepad');
  if (e.state === 'launching') editSetState(e, 'watching');
  if (first) reachNotify(T('edit.opened', { name: e.name, editor: e.editorName }) + (r && r.fallback ? ' ' + T('edit.fallbackNotepad') : ''), 'ok');
  return true;
}
// Already edited: its editor again; changed on the device since, asked.
async function editReopen(e) {
  if (!e.id || e.state === 'downloading' || e.state === 'launching') { reachNotify(T('edit.opening', { name: e.name }), 'warn'); return; }
  var cur = null, known = true;
  try { cur = await editRemoteEntry(e); } catch (x) { known = false; }
  if (known && cur && editRemoteChanged(e.baseline, cur) && !e.busy) {
    var c = await xpChoiceDialog({ title: T('edit.remoteChangedTitle'), text: editChangedText(e) + ' ' + T('edit.reloadWhich'),
      choices: [{ id: 'keep', label: T('edit.keepMine') }, { id: 'reload', label: T('edit.reload'), danger: true }] });
    if (!c) return;
    if (c === 'reload') { if (!(await editReload(e, cur))) return; }
  }
  await editLaunch(e, false);
}
// The device's copy over the working copy (a new baseline).
async function editReload(e) {
  editSetState(e, 'downloading');
  try {
    await editDownloadInto(e, function (n) { return localFs('reachEditReload', e.id, { size: n, modified: '' }); });
    e.sentSeq = e.seq; e.skippedSeq = 0;
    await editRebase(e, e.size);
    editSetState(e, 'watching', '');
    return true;
  } catch (err) {
    if (err && err.code === 'server_changed') { editServerMoved(e, err); return false; }
    editSetState(e, 'error', xferErrText(err));
    reachNotify(T('edit.reloadFailed', { name: e.name, error: xferErrText(err) }), 'warn');
    return false;
  }
}

// ── A save: sent back ──
// Go's events (native_windows.go evalLocalEdit).
window.__reachLocalEdit = function (evt) {
  if (!evt || typeof evt !== 'object' || typeof evt.editId !== 'string') return;
  var e = editById(evt.editId);
  if (!e) return;
  switch (evt.type) {
    case 'changed':
      var s = Number(evt.seq);
      if (!isFinite(s) || s <= e.seq) return;
      e.seq = s;
      if (e.state === 'tooLarge' || e.state === 'missing') e.state = 'watching';
      editSync(e);
      break;
    case 'missing': editSetState(e, 'missing'); break;
    case 'back': if (e.state === 'missing') editSetState(e, 'watching'); break;
    case 'tooLarge':
      editSetState(e, 'tooLarge');
      reachNotify(T('edit.tooLargeSaved', { name: e.name, max: reachFmtBytes(EXPLORER_MAX_BYTES) }), 'warn');
      break;
  }
};
function editNewName(e) {
  var d = new Date(), p = function (n) { return (n < 10 ? '0' : '') + n; };
  var stamp = d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + '-' + p(d.getMinutes());
  var tag = T('edit.copySuffix', { date: stamp }).replace(/[\/\\:*?"<>|\u0000-\u001f]/g, '-');
  var dot = e.name.lastIndexOf('.');
  var n = dot > 0 ? e.name.slice(0, dot) + ' (' + tag + ')' + e.name.slice(dot) : e.name + ' (' + tag + ')';
  return explorerValidName(n, e.os) ? n : null;
}
// Sends the latest save (seq), then any newer one. opts.force: the operator
// asked for it (Upload now, Retry, Send then stop): without the confirmation
// of Settings > Files, a save skipped earlier included (the device's change
// asked about again). A skipped save (e.skippedSeq) is never sent on its
// own; a newer save asks again.
async function editSync(e, opts) {
  opts = opts || {};
  if (e.busy) return;
  if (!e.id || e.seq <= e.sentSeq || e.state === 'downloading' || e.state === 'launching') return;
  if (!opts.force && e.seq <= (e.skippedSeq || 0)) return;
  e.busy = true;
  // Every question of this send reaches an operator who is in the editor:
  // the taskbar flashes (the 2FA, the privacy code, the choices below).
  var onPrompt = function () { editSetState(e, 'confirm'); editAttention(); };
  var confirmed = false; // (the operator said "Send": a newer save meanwhile is not asked again)
  var decided = null;    // (a choice about the device's change: kept if a newer save starts over)
  try {
    for (;;) {
      if (!editSessions.has(e.uid)) return;
      var seq = e.seq;
      if (seq <= e.sentSeq || (!opts.force && seq <= (e.skippedSeq || 0))) break;
      if (editSettingsCache.confirmUpload && !opts.force && !confirmed) {
        e.unsent = true;
        editSetState(e, 'confirm');
        editAttention();
        var go = await xpChoiceDialog({ title: T('edit.confirmTitle'), text: T('edit.confirmSend', { name: e.name, device: reachDevName(e.dev) }),
          choices: [{ id: 'later', label: T('edit.notNow') }, { id: 'send', label: T('edit.send'), primary: true }] });
        if (go !== 'send') { editSetState(e, 'unsent'); return; }
        confirmed = true;
      }
      editSetState(e, 'checking');
      var target = e.remotePath, rebase = true, copyName = '';
      var cur, known = true;
      try { cur = await editRemoteEntry(e, { onPrompt: onPrompt }); } catch (x) { if (x && (x.code === 'cancelled' || EXPLORER_BATCH_STOP.indexOf(x.code) >= 0)) throw x; known = false; }
      var sameAsDecided = !!decided && (decided.cur === null ? cur === null : (!!cur && !!decided.cur && Number(decided.cur.size) === Number(cur.size) && decided.cur.modified === cur.modified));
      if (known && cur === null) {
        var c1 = sameAsDecided ? decided.choice : null;
        if (!c1) {
          editSetState(e, 'changedRemote');
          editAttention();
          c1 = await xpChoiceDialog({ title: T('edit.remoteGoneTitle'), text: T('edit.remoteGone', { name: e.name }),
            choices: [{ id: 'skip', label: T('edit.skipSave') }, { id: 'create', label: T('edit.createAgain'), primary: true }] });
        }
        if (c1 !== 'create') { e.skippedSeq = seq; editSetState(e, 'changedRemote'); return; }
        decided = { cur: null, choice: c1 };
      } else if ((known && editRemoteChanged(e.baseline, cur)) || (!known && !e.baseline)) {
        // (changed there; or its baseline not known and the device not read: asked too)
        var c2 = sameAsDecided ? decided.choice : null;
        if (!c2) {
          editSetState(e, 'changedRemote');
          editAttention();
          c2 = await xpChoiceDialog({ title: T('edit.remoteChangedTitle'), text: editChangedText(e) + ' ' + T('edit.overwriteWhich'),
            choices: [{ id: 'skip', label: T('edit.skipSave') }, { id: 'copy', label: T('edit.saveCopy') }, { id: 'over', label: T('xfer.overwrite'), danger: true }] });
        }
        if (!c2 || c2 === 'skip') { e.skippedSeq = seq; editSetState(e, 'changedRemote'); return; }
        decided = { cur: known ? cur : undefined, choice: c2 };
        if (c2 === 'copy') {
          copyName = editNewName(e);
          if (!copyName) throw xferErr('explorer.invalidName');
          target = explorerJoin(editParent(e), copyName, e.os); rebase = false;
        }
      }
      // A newer save while it was asked: that one goes (what the editor
      // holds now), without asking again.
      if (e.seq > seq) seq = e.seq;
      editSetState(e, 'uploading');
      var sn;
      try { sn = await localFs('reachEditSnapshot', e.id, seq); }
      catch (x) { if (x && x.code === 'stale') { if (e.seq > seq) continue; break; } throw x; }
      var parts = [], size = Number(sn && sn.size) || 0;
      try {
        for (var off = 0; off < size; off += XFER_CHUNK) {
          var r = await localFs('reachFsRead', sn.handle, off, Math.min(XFER_CHUNK, size - off));
          parts.push(String((r && r.data) || ''));
        }
      } finally { reachNative('reachFsClose', sn.handle).catch(function () {}); }
      if (!size) throw xferErr('xfer.emptyFile');
      if (!editSessions.has(e.uid)) return;
      await explorerCtxCmd(editCtx(e), 'upload_file', { path: target, data: parts.join(''), overwrite: true }, EXPLORER_LONG_MS, 1, { onPrompt: onPrompt });
      parts = [];
      try { await reachNative('reachEditSynced', e.id, seq); } catch (x) {}
      e.sentSeq = seq; e.skippedSeq = 0; e.unsent = false; e.lastSyncAt = Date.now();
      confirmed = false; decided = null; // (the next save: asked again when Settings > Files says so)
      if (rebase) await editRebase(e, size);
      editSetState(e, 'synced', '');
      reachNotify(rebase ? T('edit.sent', { name: e.name, device: reachDevName(e.dev) }) : T('edit.sentCopy', { name: copyName, device: reachDevName(e.dev) }), 'ok');
      if (e.seq <= seq) break;
    }
  } catch (err) {
    if (editSessions.has(e.uid) && err && err.code === 'server_changed') { editServerMoved(e, err); return; }
    if (editSessions.has(e.uid) && !(err && err.code === 'cancelled')) {
      editSetState(e, 'error', xferErrText(err));
      reachNotify(T('edit.sendFailed', { name: e.name, error: xferErrText(err) }), 'warn');
    }
  } finally { e.busy = false; editChanged(); }
}
// Its server is no longer at the address the copy came from (Go refused:
// server_changed): never sent there; it stops, its unsent changes kept on
// this PC (a day).
function editServerMoved(e, err) {
  if (e.id) localFs('reachEditStop', e.id, { force: false, keep: true }).catch(function () {});
  editForget(e);
  reachNotify(T('edit.sendFailed', { name: e.name, error: localFsErrText(err) }), 'warn');
}
// A confirmation (2FA, the privacy code) is on screen while the operator is
// in the editor: the taskbar button flashes.
function editAttention() {
  if (typeof window.reachAttention === 'function' && reachNativeToken()) {
    try { var p = window.reachAttention(reachNativeToken()); if (p && p.catch) p.catch(function () {}); } catch (e) {}
  }
}
// Upload now (a row's action): the latest save, without asking.
function editUploadNow(e) {
  if (e.seq <= e.sentSeq) { reachNotify(T('edit.nothingToSend', { name: e.name }), 'ok'); return; }
  editSync(e, { force: true });
}
function editRetry(e) {
  if (e.state === 'error' && e.seq > e.sentSeq) { editSync(e, { force: true }); return; }
  if (e.state === 'error' && e.id) { editLaunch(e, false); return; }
}

// ── Stopping ──
// Stopped: its working copy deleted. With changes the device does not have:
// asked (send them first, drop them, or keep editing).
async function editStop(e) {
  if (!e.id) { editForget(e); return; }
  try {
    await localFs('reachEditStop', e.id, { force: false, keep: false });
    editForget(e);
    reachNotify(T('edit.stopped', { name: e.name }), 'ok');
    return;
  } catch (err) {
    if (!err || err.code !== 'unsynced') { reachNotify(localFsErrText(err), 'warn'); return; }
  }
  var c = await xpChoiceDialog({ title: T('edit.stopTitle'), text: T('edit.unsynced', { name: e.name }),
    choices: [{ id: 'discard', label: T('edit.discard'), danger: true }, { id: 'send', label: T('edit.sendThenStop'), primary: true }] });
  if (c === 'discard') {
    try { await localFs('reachEditStop', e.id, { force: true, keep: false }); } catch (x) {}
    editForget(e);
    reachNotify(T('edit.stopped', { name: e.name }), 'ok');
    return;
  }
  if (c !== 'send') return;
  try {
    var list = await reachNative('reachEditList');
    (Array.isArray(list) ? list : []).forEach(function (i) { if (i && i.editId === e.id && Number(i.seq) > e.seq) e.seq = Number(i.seq); });
  } catch (x) {}
  if (e.busy) { reachNotify(T('edit.stillSaving', { name: e.name }), 'warn'); return; }
  if (e.seq > e.sentSeq) await editSync(e, { force: true });
  if (!editSessions.has(e.uid)) return;
  if (e.seq > e.sentSeq) return; // (not sent: skipped again, refused; its row says why, the editing goes on)
  try {
    await localFs('reachEditStop', e.id, { force: false, keep: false });
    editForget(e);
    reachNotify(T('edit.stopped', { name: e.name }), 'ok');
  } catch (x) { reachNotify(T('edit.stillSaving', { name: e.name }), 'warn'); }
}
// Sign-out, a server or tenant left, a device no longer listed for this
// operator (explorer_viewer.go explorerCtxDisposeWhere): they stop, their
// unsent changes kept on disk (a day). key: the notice (edit.stoppedLeft by
// default).
function editDisposeWhere(pred, key) {
  var gone = editList().filter(function (e) { try { return !!pred({ device: e.dev }); } catch (x) { return false; } });
  gone.forEach(function (e) {
    if (e.id) localFs('reachEditStop', e.id, { force: false, keep: true }).catch(function () {});
    editSessions.delete(e.uid);
  });
  if (gone.length) { editChanged(); reachNotify(T(key || 'edit.stoppedLeft', { count: gone.length }), 'warn'); }
  return gone.length;
}
// The page reloaded: the working copies Go still watches come back (their
// device must still be listed for this user; else they stop, kept on disk).
async function editRecover() {
  if (editRecovered || !localEditAvailable() || typeof window.reachEditList !== 'function') return;
  // (the devices not read yet: later, never stopped for that)
  var groups = (typeof overview !== 'undefined' && overview && Array.isArray(overview.groups)) ? overview.groups : [];
  if (!groups.length) return;
  editRecovered = true;
  editSettingsLoad();
  var list;
  try { list = await reachNative('reachEditList'); } catch (x) { return; }
  (Array.isArray(list) ? list : []).forEach(function (i) {
    if (!i || typeof i.editId !== 'string' || editById(i.editId)) return;
    var dev = reachFindDevice(String(i.deviceId), String(i.profile || ''));
    if (!dev) { localFs('reachEditStop', i.editId, { force: false, keep: true }).catch(function () {}); return; }
    var e = editNew(dev, { path: String(i.remotePath || ''), name: String(i.name || ''), size: 0 }, explorerOs(dev));
    e.id = i.editId; e.seq = Number(i.seq) || 0; e.sentSeq = i.dirty ? 0 : e.seq;
    // Its baseline, as Go kept it: the device's file is still checked before
    // a send (none: asked before the first one, never overwritten in silence).
    var rb = (i.remote && typeof i.remote === 'object') ? i.remote : null;
    e.baseline = (rb && isFinite(Number(rb.size)) && Number(rb.size) >= 0) ? { size: Number(rb.size), modified: typeof rb.modified === 'string' ? rb.modified.slice(0, 64) : '' } : null;
    e.size = e.baseline ? e.baseline.size : 0;
    e.state = i.state === 'missing' ? 'missing' : i.state === 'tooLarge' ? 'tooLarge' : 'watching';
    if (i.lastSyncAt) e.lastSyncAt = Date.parse(i.lastSyncAt) || 0;
    if (i.dirty) { e.state = 'unsent'; e.unsent = true; }
  });
  editChanged();
}

// ── On screen: the rows (the dock, the popover), the pill ──
// A state as a chip: [text, class].
function editStateChip(e) {
  switch (e.state) {
    case 'downloading': return [T('edit.st.downloading'), 'busy'];
    case 'launching': return [T('edit.st.opening'), 'busy'];
    case 'watching': return e.lastSyncAt ? [T('edit.st.synced', { time: editTime(e.lastSyncAt) }), 'ok'] : [T('edit.st.opened'), ''];
    case 'synced': return [T('edit.st.synced', { time: editTime(e.lastSyncAt) }), 'ok'];
    case 'checking': return [T('edit.st.checking'), 'busy'];
    case 'uploading': return [T('edit.st.uploading'), 'busy'];
    case 'confirm': return [T('edit.st.confirm'), 'warn'];
    case 'unsent': return [T('edit.st.unsent'), 'warn'];
    case 'changedRemote': return [T('edit.st.changedRemote'), 'warn'];
    case 'missing': return [T('edit.st.missing'), 'warn'];
    case 'tooLarge': return [T('edit.st.tooLarge'), 'warn'];
    case 'error': return [T('edit.st.error'), 'bad'];
  }
  return ['', ''];
}
function editTime(t) {
  try { return new Date(t).toLocaleTimeString(typeof i18nLang === 'function' ? i18nLang() : undefined, { hour: '2-digit', minute: '2-digit' }); } catch (x) { return ''; }
}
function editRowEl(e, withDev) {
  var row = explorerEl('div', 'xp-row edit-row');
  row.dataset.edit = String(e.uid);
  if (withDev && typeof serverMode !== 'undefined' && serverMode === 'multi') {
    var sd = explorerEl('span', 'xp-row-srv', row); sd.style.background = srvColor(devPid(e.dev)); sd.title = srvName(devPid(e.dev));
  }
  var ic = explorerEl('span', 'xp-row-ico', row); ic.innerHTML = EDIT_ICON.replace(/width="14" height="14"/, 'width="12" height="12"');
  if (withDev) { var dv = explorerEl('span', 'xp-row-dev', row); dv.textContent = e.dev && e.dev.hostname ? e.dev.hostname : ''; dv.title = reachDevName(e.dev); }
  var nm = explorerEl('span', 'xp-row-name', row); nm.textContent = e.name; nm.title = e.name;
  var pa = explorerEl('span', 'xp-row-path', row); pa.textContent = e.remotePath; pa.title = e.remotePath;
  var chip = editStateChip(e);
  var st = explorerEl('span', 'xp-st' + (chip[1] ? ' ' + chip[1] : ''), row); st.textContent = chip[0];
  if (e.error) st.title = e.error;
  var acts = explorerEl('div', 'xp-row-acts', row);
  var busy = e.busy || e.state === 'downloading' || e.state === 'launching';
  var btn = function (key, fn, off) { var b = explorerEl('button', 'btn-sm', acts); b.textContent = T(key); b.dataset.act = key; b.disabled = !!off; b.addEventListener('click', fn); return b; };
  if (e.state === 'error') btn('common.retry', function () { editRetry(e); }, busy);
  btn('edit.openAgain', function () { editReopen(e); }, busy || !e.id);
  btn('edit.uploadNow', function () { editUploadNow(e); }, busy || !e.id);
  btn('edit.stop', function () { editStop(e); }, e.state === 'downloading' && !e.id);
  return row;
}
function editRenderRows(body, edits, tab) {
  if (!edits.length) { var n = explorerEl('div', 'xp-dock-empty', body); n.textContent = T('edit.none'); return; }
  edits.forEach(function (e) { body.appendChild(editRowEl(e, false)); });
}
// The Files page header's pill: how many are edited, every device's.
function filesEditPillRender() {
  var v = typeof filesPageView !== 'undefined' ? filesPageView : null;
  if (!v || !v.editPill) return;
  var n = editSessions.size;
  v.editPill.classList.toggle('hide', !n);
  v.editPill.innerHTML = EDIT_ICON + '<span>' + TH('edit.pill', { count: n }) + '</span>';
  v.editPill.setAttribute('aria-expanded', editPopEl ? 'true' : 'false');
  if (!n) editPopClose();
  else if (editPopEl) editPopFill();
}
var editPopEl = null, editPopWired = false;
function editPopClose() {
  var p = editPopEl;
  editPopEl = null;
  if (p && p.parentElement && typeof p.parentElement.removeChild === 'function') { try { p.parentElement.removeChild(p); } catch (e) {} }
  var v = typeof filesPageView !== 'undefined' ? filesPageView : null;
  if (v && v.editPill) v.editPill.setAttribute('aria-expanded', 'false');
}
function editPopFill() {
  var p = editPopEl;
  if (!p) return;
  p.innerHTML = '';
  var h = explorerEl('div', 'files-edit-pop-h', p); h.textContent = T('edit.popTitle');
  editList().forEach(function (e) {
    var row = editRowEl(e, true);
    var acts = row.children[row.children.length - 1];
    var show = explorerEl('button', 'btn-sm', acts); show.textContent = T('edit.show'); show.dataset.act = 'edit.show';
    show.addEventListener('click', function () { editPopClose(); if (typeof filesPickerChoose === 'function') filesPickerChoose(e.dev); });
    p.appendChild(row);
  });
}
function editPopToggle(anchor) {
  if (editPopEl) { editPopClose(); return; }
  var p = editPopEl = explorerEl('div', 'files-edit-pop', null, 'files-edit-pop');
  p.setAttribute('role', 'dialog'); p.setAttribute('aria-label', T('edit.popTitle'));
  editPopFill();
  var r = anchor && typeof anchor.getBoundingClientRect === 'function' ? anchor.getBoundingClientRect() : { left: 40, top: 40, height: 0, width: 0 };
  var vw = (typeof window !== 'undefined' && window.innerWidth) || 1200;
  p.style.top = Math.round((r.top || 0) + (r.height || 0) + 6) + 'px';
  p.style.left = Math.max(12, Math.min(Math.round((r.left || 0) + (r.width || 0) - 520), vw - 532)) + 'px';
  document.body.appendChild(p);
  if (anchor) anchor.setAttribute('aria-expanded', 'true');
  if (!editPopWired) {
    editPopWired = true;
    document.addEventListener('mousedown', function (ev) {
      var pp = editPopEl, v = typeof filesPageView !== 'undefined' ? filesPageView : null;
      if (!pp || !ev || !ev.target) return;
      if (typeof pp.contains === 'function' && pp.contains(ev.target)) return;
      if (v && v.editPill && typeof v.editPill.contains === 'function' && v.editPill.contains(ev.target)) return;
      editPopClose();
    }, true);
    document.addEventListener('keydown', function (ev) { if (ev && ev.key === 'Escape' && editPopEl) editPopClose(); });
  }
}

// ── A choice among a few (the app's dialog look) ──
// o: { title, text, choices: [{ id, label, primary, danger }] }; resolves to
// the id chosen, or null (cancelled). One at a time: queued.
var xpChoiceChain = Promise.resolve();
function xpChoiceDialog(o) {
  var run = function () { return xpChoiceShow(o); };
  var p = xpChoiceChain.then(run, run);
  xpChoiceChain = p.then(function () {}, function () {});
  return p;
}
function xpChoiceShow(o) {
  explorerMenuClose();
  return new Promise(function (resolve) {
    var ov = explorerEl('div', 'modal-overlay visible exp-dlg xfer-cfl', null, 'xp-choice');
    var box = explorerEl('div', 'modal-box', ov);
    box.setAttribute('role', 'alertdialog'); box.setAttribute('aria-label', o.title);
    var head = explorerEl('div', 'modal-head', box);
    var ico = explorerEl('div', 'icon', head); ico.innerHTML = EDIT_ICON;
    var h = explorerEl('h3', '', head); h.textContent = o.title;
    var x = explorerEl('button', 'close', head, 'xp-choice-close'); x.title = T('common.cancel');
    x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    var p = explorerEl('p', 'desc', box, 'xp-choice-text'); p.textContent = o.text;
    var acts = explorerEl('div', 'modal-actions', box);
    var cancel = explorerEl('button', 'btn-ghost', acts, 'xp-choice-cancel'); cancel.textContent = T('common.cancel');
    explorerEl('span', 'sp', acts);
    var done = false;
    var finish = function (v) {
      if (done) return;
      done = true;
      ov.classList.remove('visible');
      if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
      resolve(v);
    };
    var first = null;
    o.choices.forEach(function (c) {
      var b = explorerEl('button', c.danger ? 'exp-btn-danger' : c.primary ? 'btn-primary' : 'btn-ghost', acts, 'xp-choice-' + c.id);
      if (c.primary) b.style.cssText = 'padding:8px 18px;font-size:12px;border-radius:10px';
      b.textContent = c.label;
      b.addEventListener('click', function () { finish(c.id); });
      if (c.primary && !first) first = b;
    });
    cancel.addEventListener('click', function () { finish(null); });
    x.addEventListener('click', function () { finish(null); });
    ov.addEventListener('keydown', function (e) {
      if (!e) return;
      if (e.key === 'F5' || ((e.ctrlKey || e.metaKey) && String(e.key || '').toLowerCase() === 'r')) { if (e.preventDefault) e.preventDefault(); return; }
      if (e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); finish(null); }
    });
    document.body.appendChild(ov);
    try { (first || cancel).focus(); } catch (e) {}
  });
}

// ── Settings > Files ──
function renderFilesSection(sb) {
  var mk = function (tag, cls, parent, id) { var e = document.createElement(tag); if (cls) e.className = cls; if (id) e.id = id; if (parent) parent.appendChild(e); return e; };
  var panel = mk('div', 'panel', sb);
  var head = mk('div', 'panel-header', panel); head.textContent = T('settings.nav.files');
  var pb = mk('div', 'panel-body', panel);
  var avail = localFsAvailable() && typeof window.reachFilesSettingsGet === 'function';
  var row = function (titleKey, subKey) {
    var r = mk('div', 'setting-row', pb);
    var lbl = mk('div', 'setting-label', r);
    var t = mk('div', 'title', lbl); t.textContent = T(titleKey);
    var s = mk('div', 'sub', lbl); s.textContent = subKey ? T(subKey) : '';
    var c = mk('div', 'setting-control', r);
    return { sub: s, ctl: c };
  };
  // The editor.
  var rEd = row('settings.files.editor', 'settings.files.editorSub');
  var opts = mk('div', 'set-files-opts', rEd.ctl);
  var radio = function (val, key) {
    var l = mk('label', 'set-files-opt', opts);
    var i = mk('input', '', l, 'set-files-ed-' + val); i.type = 'radio'; i.name = 'set-files-editor'; i.value = val;
    var s = mk('span', '', l); s.textContent = T(key);
    return { input: i, label: l };
  };
  var rN = radio('notepad', 'settings.files.notepad');
  var rS = radio('system', 'settings.files.system');
  var rC = radio('custom', 'settings.files.custom');
  var prog = mk('div', 'set-files-prog', opts);
  var browse = mk('button', 'btn-ghost', prog, 'set-files-browse'); browse.textContent = T('settings.files.browse');
  var progName = mk('span', 'nm', prog, 'set-files-prog');
  var err = mk('div', 'set-files-err', opts, 'set-files-err'); err.setAttribute('role', 'alert');
  // The size that warns.
  var rW = row('settings.files.warn', 'settings.files.warnSub');
  var sel = mk('select', '', rW.ctl, 'set-files-warn');
  sel.title = T('settings.files.warn');
  sel.style.cssText = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
  [1, 5, 10, 25, 50].forEach(function (n) { var o = mk('option', '', sel); o.value = String(n); o.textContent = T('units.mb', { n: n }); });
  // The confirmation, hidden files.
  var check = function (id, titleKey, subKey) {
    var r = row(titleKey, subKey);
    var cb = mk('input', '', r.ctl, id); cb.type = 'checkbox'; cb.title = T(titleKey);
    cb.style.cssText = 'accent-color:var(--accent);width:16px;height:16px;cursor:pointer';
    return cb;
  };
  var cbConfirm = check('set-files-confirm', 'settings.files.confirm', 'settings.files.confirmSub');
  var cbHidden = check('set-files-hidden', 'settings.files.hidden', 'settings.files.hiddenSub');
  var note = mk('p', 'muted-hint', pb, 'set-files-note'); note.style.cssText = 'margin-top:10px';
  note.textContent = T('settings.files.note');
  var controls = [rN.input, rS.input, rC.input, browse, sel, cbConfirm, cbHidden];
  var apply = function (s) {
    s = editSettingsApply(s);
    rN.input.checked = s.editor === 'notepad'; rS.input.checked = s.editor === 'system'; rC.input.checked = s.editor === 'custom';
    rC.input.disabled = !s.editorPath;
    progName.textContent = s.editorPath ? (s.editorName || s.editorPath) : T('settings.files.noProgram');
    progName.title = s.editorPath || '';
    sel.value = String(s.warnMB);
    cbConfirm.checked = s.confirmUpload; cbHidden.checked = s.showHidden;
  };
  if (!avail) {
    controls.forEach(function (c) { c.disabled = true; });
    rEd.sub.textContent = T('settings.withUnavailable', { text: T('settings.files.editorSub') });
    apply(editSettingsCache);
    controls.forEach(function (c) { c.disabled = true; });
    return;
  }
  apply(editSettingsCache);
  editSettingsLoad().then(apply);
  var save = function (done) {
    err.textContent = '';
    var ed = rC.input.checked ? 'custom' : rS.input.checked ? 'system' : 'notepad';
    reachNative('reachFilesSettingsSet', { editor: ed, warnMB: Number(sel.value) || 10, confirmUpload: !!cbConfirm.checked, showHidden: !!cbHidden.checked })
      .then(function (s) { var was = editSettingsCache.showHidden; apply(s); setStatus(T('settings.files.saved')); if (was !== editSettingsCache.showHidden) lpRefreshAll(); if (done) done(); })
      .catch(function (e) { err.textContent = T('settings.files.notSaved', { error: reachErrText(e) }); editSettingsLoad().then(apply); });
  };
  [rN.input, rS.input, rC.input, sel, cbConfirm, cbHidden].forEach(function (c) { c.addEventListener('change', function () { save(); }); });
  browse.addEventListener('click', function () {
    err.textContent = '';
    browse.disabled = true;
    localFs('reachFilesPickEditor').then(function (r) {
      browse.disabled = false;
      if (r && r.picked) { editSettingsLoad().then(function (s) { apply(s); setStatus(T('settings.files.picked', { name: s.editorName || s.editorPath })); }); }
    }, function (e) {
      browse.disabled = false;
      err.textContent = localFsErrText(e);
    });
  });
}
// Hidden files shown or not: the local panes listed again.
function lpRefreshAll() {
  toolTabs.forEach(function (t) { if (t && t.kind === 'files' && t.lp && t.lp.listed && t.lp.path && !t.ex.disposed) lpRefresh(t); });
}
`
