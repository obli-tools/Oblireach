package main

// connimport_page.go — the Connections page's Import / Export (the local
// connection tree: connections_page.go). The native side reads the sources
// and keeps the secrets (connimport_windows.go); the page only ever sees a
// preview ("password: yes / no"), the operator's choices go back by item
// reference, and a report comes at the end.
//
// Both consts are injected after connectionsPageCSS / connectionsPageJS
// (ui.go buildUI).

const connImportCSS = `
/* ── Connections import / export (connimport_page.go) ── */
.cimp-box{width:min(920px,94vw);max-height:88vh;display:flex;flex-direction:column}
.cimp-sources{display:flex;flex-direction:column;gap:8px}
.cimp-sources button{text-align:start;padding:10px 14px;border-radius:10px;background:var(--input-bg);color:var(--text);font-size:12.5px;cursor:pointer}
.cimp-sources button:hover{background:var(--bg2)}
.cimp-sources .sub{display:block;font-size:11px;color:var(--muted);margin-top:2px}
.cimp-opts{display:flex;align-items:center;gap:14px;flex-wrap:wrap;font-size:12px;color:var(--muted);margin:6px 0}
.cimp-opts label{display:flex;align-items:center;gap:6px;cursor:pointer}
.cimp-list{flex:1;min-height:120px;overflow:auto;border-radius:10px;background:rgb(var(--ink) / .03)}
.cimp-table{width:100%;border-collapse:collapse;font-size:12px}
.cimp-table th{position:sticky;top:0;background:var(--bg2);text-align:start;font-weight:600;color:var(--muted);padding:7px 8px;white-space:nowrap}
.cimp-table td{padding:6px 8px;box-shadow:inset 0 -1px 0 rgb(var(--ink) / .06);vertical-align:middle}
.cimp-table tr.off td{color:var(--muted2)}
.cimp-table td.cimp-name{font-weight:600;max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.cimp-table select{background:var(--input-bg);border:none;border-radius:8px;padding:4px 6px;font-size:11.5px;color:var(--text)}
.cimp-note{font-size:11px;color:var(--muted)}
.cimp-note.warn{color:var(--warn)}
.cimp-warn{font-size:12px;color:var(--warn);margin:4px 0}
.cimp-summary{font-size:12.5px;color:var(--text);margin:6px 0}
.cimp-pass{display:flex;flex-direction:column;gap:8px;margin:8px 0}
.cimp-pass input{background:var(--input-bg);border:none;border-radius:10px;padding:8px 11px;font-size:12.5px;color:var(--text);outline:none}
`

const connImportJS = `
// ── Connections import / export ────────────────────────────────────────────
function cimpAvailable() { return typeof window.reachConnImportPick === 'function' && !!reachNativeToken(); }

// (Import / Export: the Devices page's actions, ui.go renderDevicesPage;
// an import goes into the base's root.)
function cimpErrText(code) {
  var k = {
    canceled: '', locked: 'vault.locked.title', unreadable: 'vault.locked.unreadable',
    winscp_not_found: 'connImport.err.winscpNotFound', nothing_found: 'connImport.err.nothingFound',
    read_failed: 'connImport.err.readFailed', bad_format: 'connImport.err.badFormat',
    export_newer: 'connImport.err.exportNewer', expired: 'connImport.err.expired',
    bad_passphrase: 'connImport.err.badPassphrase', passphrase_short: 'connImport.err.passphraseShort',
    write_failed: 'connImport.err.writeFailed'
  }[code];
  if (k === '') return '';
  return k ? T(k) : T('vault.err.internal');
}

function cimpReasonText(code) {
  var k = {
    unsupported_protocol: 'connImport.reason.unsupportedProtocol', no_host: 'connImport.reason.noHost',
    bad_port: 'connImport.reason.badPort', master_password: 'connImport.reason.masterPassword',
    encrypted_source: 'connImport.reason.encryptedSource', undecodable: 'connImport.reason.undecodable',
    key_unreadable: 'connImport.reason.keyUnreadable', key_format: 'connImport.reason.keyFormat',
    too_long: 'connImport.reason.tooLong', not_selected: 'connImport.reason.notSelected',
    duplicate_ignored: 'connImport.reason.duplicateIgnored', invalid: 'connImport.reason.invalid',
    vault_full: 'connImport.reason.vaultFull', duplicate_gone: 'connImport.reason.duplicateGone'
  }[code];
  return k ? T(k) : String(code || '');
}

function cimpWarnText(code) {
  var k = { master_password: 'connImport.warn.masterPassword', wal_present: 'connImport.warn.walPresent', truncated: 'connImport.warn.truncated' }[code];
  return k ? T(k) : '';
}

// Step 1: the source.
function cimpOpenSources(parentId, onDone) {
  var m = scriptsBaseModal('connImport.title');
  var intro = document.createElement('p'); intro.className = 'desc'; intro.textContent = T('connImport.intro');
  var list = document.createElement('div'); list.className = 'cimp-sources';
  var err = document.createElement('div'); err.className = 'modal-err';
  var busy = false;
  [['winscp-registry', 'connImport.src.winscpInstalled', 'connImport.src.winscpInstalledSub'],
   ['winscp-file', 'connImport.src.winscpFile', 'connImport.src.winscpFileSub'],
   ['1remote', 'connImport.src.oneRemote', 'connImport.src.oneRemoteSub'],
   ['oblireach', 'connImport.src.oblireach', 'connImport.src.oblireachSub']].forEach(function (s) {
    var b = document.createElement('button'); b.type = 'button';
    b.textContent = T(s[1]);
    var sub = document.createElement('span'); sub.className = 'sub'; sub.textContent = T(s[2]); b.appendChild(sub);
    b.addEventListener('click', function () {
      if (busy) return;
      busy = true; err.textContent = '';
      reachNative('reachConnImportPick', s[0]).then(function (p) {
        busy = false;
        if (!p || p.error) { err.textContent = cimpErrText(p && p.error); return; }
        m.close();
        cimpOpenPreview(p, parentId, onDone);
      }, function () { busy = false; err.textContent = T('vault.err.internal'); });
    });
    list.appendChild(b);
  });
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  actions.appendChild(cancel);
  m.box.appendChild(intro); m.box.appendChild(list); m.box.appendChild(err); m.box.appendChild(actions);
}

// Step 2: the preview — tick, choose for the duplicates, import.
function cimpOpenPreview(p, parentId, onDone) {
  var m = scriptsBaseModal('connImport.previewTitle', 'cimp-box');
  var committed = false;
  // (a preview not imported is forgotten by the app: here on Cancel, else
  // after its delay — connimport.go connImportPendingTTL)
  var drop = function () { if (!committed && p.importId) reachNative('reachConnImportDrop', p.importId).catch(function () {}); };
  var head = document.createElement('div');
  m.box.appendChild(head);
  var opts = document.createElement('div'); opts.className = 'cimp-opts';
  var secLbl = document.createElement('label');
  var secChk = document.createElement('input'); secChk.type = 'checkbox'; secChk.checked = true;
  secLbl.appendChild(secChk); secLbl.appendChild(document.createTextNode(T('connImport.importPasswords')));
  var allLbl = document.createElement('label');
  var allChk = document.createElement('input'); allChk.type = 'checkbox';
  allLbl.appendChild(allChk); allLbl.appendChild(document.createTextNode(T('connImport.selectAll')));
  var dupLbl = document.createElement('label'); dupLbl.appendChild(document.createTextNode(T('connImport.dupAll')));
  var dupAll = document.createElement('select');
  [['skip', 'connImport.dup.skip'], ['replace', 'connImport.dup.replace'], ['duplicate', 'connImport.dup.duplicate']].forEach(function (o) {
    var op = document.createElement('option'); op.value = o[0]; op.textContent = T(o[1]); dupAll.appendChild(op);
  });
  dupLbl.appendChild(dupAll);
  opts.appendChild(allLbl); opts.appendChild(secLbl); opts.appendChild(dupLbl);
  m.box.appendChild(opts);
  var listWrap = document.createElement('div'); listWrap.className = 'cimp-list';
  m.box.appendChild(listWrap);
  var err = document.createElement('div'); err.className = 'modal-err';
  m.box.appendChild(err);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', function () { drop(); m.close(); });
  var go = document.createElement('button'); go.className = 'btn-primary';
  actions.appendChild(cancel); actions.appendChild(go);
  m.box.appendChild(actions);

  var state = {}; // ref -> { on, dup }
  var render = function () {
    head.innerHTML = '';
    (p.warnings || []).forEach(function (w) {
      var t = cimpWarnText(w); if (!t) return;
      var d = document.createElement('div'); d.className = 'cimp-warn'; d.textContent = t; head.appendChild(d);
    });
    var items = p.items || [];
    var ok = items.filter(function (it) { return !it.skip; });
    var sum = document.createElement('div'); sum.className = 'cimp-summary';
    sum.textContent = T('connImport.found', { count: items.length, importable: ok.length });
    head.appendChild(sum);
    if (p.needPassphrase) {
      var pw = document.createElement('div'); pw.className = 'cimp-pass';
      var note = document.createElement('div'); note.className = 'cimp-note'; note.textContent = T('connImport.passphraseNeeded');
      var inp = document.createElement('input'); inp.type = 'password'; inp.autocomplete = 'off'; inp.placeholder = T('connImport.passphrase');
      var unlock = document.createElement('button'); unlock.className = 'btn-sm'; unlock.textContent = T('connImport.passphraseOpen');
      var perr = document.createElement('div'); perr.className = 'modal-err';
      var submit = function () {
        if (!inp.value) return;
        unlock.disabled = true; perr.textContent = '';
        vaultAsyncCall('reachConnImportPassphrase', p.importId, inp.value).then(function (np) {
          unlock.disabled = false;
          if (!np || (np.error && np.error !== 'bad_passphrase')) { perr.textContent = cimpErrText(np && np.error); return; }
          if (np.error === 'bad_passphrase') { perr.textContent = cimpErrText('bad_passphrase'); return; }
          p = np; state = {}; render();
        }, function (e) { unlock.disabled = false; perr.textContent = vaultErrText(e); });
      };
      unlock.addEventListener('click', submit);
      inp.addEventListener('keydown', function (e) { if (e.key === 'Enter') submit(); });
      pw.appendChild(note); pw.appendChild(inp); pw.appendChild(unlock); pw.appendChild(perr);
      head.appendChild(pw);
    }
    listWrap.innerHTML = '';
    var table = document.createElement('table'); table.className = 'cimp-table';
    var thr = document.createElement('tr');
    ['', 'connImport.col.folder', 'connImport.col.name', 'connImport.col.type', 'connImport.col.address', 'connImport.col.user', 'connImport.col.password', 'connImport.col.duplicate'].forEach(function (k) {
      var th = document.createElement('th'); th.textContent = k ? T(k) : ''; thr.appendChild(th);
    });
    var thead = document.createElement('thead'); thead.appendChild(thr); table.appendChild(thead);
    var tbody = document.createElement('tbody');
    items.forEach(function (it) {
      var st = state[it.ref];
      if (!st) st = state[it.ref] = { on: !it.skip && !it.dup, dup: dupAll.value };
      var tr = document.createElement('tr'); if (it.skip) tr.className = 'off';
      var c0 = document.createElement('td');
      var chk = document.createElement('input'); chk.type = 'checkbox'; chk.checked = !it.skip && st.on; chk.disabled = !!it.skip;
      chk.addEventListener('change', function () { st.on = chk.checked; count(); });
      c0.appendChild(chk); tr.appendChild(c0);
      var cell = function (text, cls, title) { var td = document.createElement('td'); if (cls) td.className = cls; td.textContent = text; if (title) td.title = title; tr.appendChild(td); return td; };
      cell(it.folder || '');
      cell(it.name, 'cimp-name', it.name);
      cell(it.proto || '');
      cell(it.host ? it.host + (it.port ? ':' + it.port : '') : '');
      cell(it.username || '');
      var pwTd = cell(it.skip ? '' : (it.hasPassword ? T('connImport.yes') : T('connImport.no')));
      if (it.skip) {
        pwTd.appendChild(cimpNote(cimpReasonText(it.skip), true));
      } else if (it.secretNote) {
        pwTd.appendChild(cimpNote(cimpReasonText(it.secretNote), true));
      } else if (it.keyAuth && it.hasPassword) {
        pwTd.appendChild(cimpNote(T('connImport.keyIncluded'), false));
      }
      var dupTd = document.createElement('td');
      if (it.dup && !it.skip) {
        var lbl = cimpNote(T('connImport.dupOf', { name: it.dupName || '' }), false);
        var sel = document.createElement('select');
        [['skip', 'connImport.dup.skip'], ['replace', 'connImport.dup.replace'], ['duplicate', 'connImport.dup.duplicate']].forEach(function (o) {
          var op = document.createElement('option'); op.value = o[0]; op.textContent = T(o[1]); sel.appendChild(op);
        });
        sel.value = st.dup;
        sel.addEventListener('change', function () { st.dup = sel.value; if (sel.value !== 'skip') { st.on = true; chk.checked = true; } count(); });
        dupTd.appendChild(lbl); dupTd.appendChild(sel);
      }
      tr.appendChild(dupTd);
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    listWrap.appendChild(table);
    count();
  };
  var picked = function () {
    var out = [];
    (p.items || []).forEach(function (it) {
      var st = state[it.ref];
      if (!st || it.skip || !st.on) return;
      if (it.dup) { if (st.dup !== 'skip') out.push({ ref: it.ref, action: st.dup }); }
      else out.push({ ref: it.ref, action: 'import' });
    });
    return out;
  };
  var count = function () {
    var n = picked().length;
    go.textContent = T('connImport.importN', { count: n });
    go.disabled = n === 0;
    var all = (p.items || []).filter(function (it) { return !it.skip; });
    allChk.checked = all.length > 0 && all.every(function (it) { return state[it.ref] && state[it.ref].on; });
  };
  allChk.addEventListener('change', function () {
    (p.items || []).forEach(function (it) { if (!it.skip && state[it.ref]) state[it.ref].on = allChk.checked; });
    render();
  });
  dupAll.addEventListener('change', function () {
    (p.items || []).forEach(function (it) { if (it.dup && state[it.ref]) { state[it.ref].dup = dupAll.value; if (dupAll.value !== 'skip') state[it.ref].on = true; } });
    render();
  });
  go.addEventListener('click', function () {
    var dec = picked();
    if (!dec.length) return;
    go.disabled = true; err.textContent = '';
    var o = { importSecrets: secChk.checked, parentId: parentId || '', decisions: dec };
    vaultAsyncCall('reachConnImportCommit', p.importId, JSON.stringify(o)).then(function (rep) {
      if (!rep || rep.error) { go.disabled = false; err.textContent = cimpErrText(rep && rep.error); return; }
      committed = true;
      m.close();
      cimpShowReport(rep, p.items || []);
      if (onDone) onDone();
    }, function (e) { go.disabled = false; err.textContent = vaultErrText(e); });
  });
  render();
}

function cimpNote(text, warn) {
  var d = document.createElement('div'); d.className = 'cimp-note' + (warn ? ' warn' : ''); d.textContent = text; return d;
}

// Step 3: the report.
function cimpShowReport(rep, items) {
  var m = scriptsBaseModal('connImport.reportTitle', 'cimp-box');
  var sum = document.createElement('div'); sum.className = 'cimp-summary';
  sum.textContent = T('connImport.reportSummary', { imported: rep.imported || 0, replaced: rep.replaced || 0, skipped: rep.skipped || 0, failed: rep.failed || 0, folders: rep.folders || 0 });
  m.box.appendChild(sum);
  var lines = (rep.lines || []).filter(function (l) { return l.result !== 'imported' && l.result !== 'replaced'; });
  var withSecret = (rep.lines || []).filter(function (l) { return (l.result === 'imported' || l.result === 'replaced') && !l.secret; }).length;
  if (withSecret) m.box.appendChild(cimpNote(T('connImport.reportNoSecret', { count: withSecret }), true));
  if (lines.length) {
    var wrap = document.createElement('div'); wrap.className = 'cimp-list';
    var table = document.createElement('table'); table.className = 'cimp-table';
    lines.forEach(function (l) {
      var tr = document.createElement('tr');
      var a = document.createElement('td'); a.className = 'cimp-name'; a.textContent = l.name; a.title = l.name;
      var b = document.createElement('td'); b.textContent = T(l.result === 'failed' ? 'connImport.result.failed' : 'connImport.result.skipped');
      var c = document.createElement('td'); c.textContent = cimpReasonText(l.reason);
      tr.appendChild(a); tr.appendChild(b); tr.appendChild(c); table.appendChild(tr);
    });
    wrap.appendChild(table); m.box.appendChild(wrap);
  }
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var ok = document.createElement('button'); ok.className = 'btn-primary'; ok.textContent = T('common.close');
  ok.addEventListener('click', m.close);
  actions.appendChild(ok); m.box.appendChild(actions);
}

// Export: the format, with or without the passwords (then a passphrase).
function cimpOpenExport() {
  var m = scriptsBaseModal('connImport.exportTitle');
  var form = document.createElement('div'); form.className = 'cxn-form';
  var fmtField = cxnField('connImport.exportFormat', 'select');
  [['oblireach', 'connImport.fmt.oblireach'], ['winscp', 'connImport.fmt.winscp']].forEach(function (o) {
    var op = document.createElement('option'); op.value = o[0]; op.textContent = T(o[1]); fmtField.input.appendChild(op);
  });
  var secLbl = document.createElement('label'); secLbl.className = 'cimp-opts';
  var secChk = document.createElement('input'); secChk.type = 'checkbox';
  secLbl.appendChild(secChk); secLbl.appendChild(document.createTextNode(T('connImport.includePasswords')));
  var p1 = cxnField('connImport.passphrase', 'password');
  var p2 = cxnField('connImport.passphraseConfirm', 'password');
  var vc = cxnField('connImport.vaultCode', 'password'); // (the vault's code: every password leaves it)
  var hint = cimpNote(T('connImport.passphraseHint'), false);
  var noPw = cimpNote(T('connImport.winscpNoPasswords'), false);
  var err = document.createElement('div'); err.className = 'modal-err';
  var sync = function () {
    var ours = fmtField.input.value === 'oblireach';
    secLbl.style.display = ours ? '' : 'none';
    noPw.style.display = ours ? 'none' : '';
    var withPw = ours && secChk.checked;
    p1.wrap.style.display = p2.wrap.style.display = vc.wrap.style.display = hint.style.display = withPw ? '' : 'none';
  };
  fmtField.input.addEventListener('change', sync);
  secChk.addEventListener('change', sync);
  form.appendChild(fmtField.wrap); form.appendChild(secLbl); form.appendChild(noPw); form.appendChild(p1.wrap); form.appendChild(p2.wrap); form.appendChild(hint); form.appendChild(vc.wrap); form.appendChild(err);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var go = document.createElement('button'); go.className = 'btn-primary'; go.textContent = T('connImport.exportGo');
  go.addEventListener('click', function () {
    var fmt = fmtField.input.value;
    var pass = '', code = '';
    if (fmt === 'oblireach' && secChk.checked) {
      if (p1.input.value.length < 8) { err.textContent = T('connImport.err.passphraseShort'); return; }
      if (p1.input.value !== p2.input.value) { err.textContent = T('connImport.err.passphraseMismatch'); return; }
      if (!vc.input.value) { err.textContent = T('connImport.err.vaultCode'); return; }
      pass = p1.input.value; code = vc.input.value;
    }
    go.disabled = true; err.textContent = '';
    vaultAsyncCall('reachConnExport', fmt, pass, code).then(function (r) {
      go.disabled = false;
      if (!r || r.canceled) return;
      if (r.error) { err.textContent = cimpErrText(r.error); return; }
      m.close();
      var msg = T('connImport.exported', { count: r.count || 0, file: r.file || '' });
      if (r.skipped) msg += ' ' + T('connImport.exportedSkipped', { count: r.skipped });
      setStatus(msg);
    }, function (e) { go.disabled = false; err.textContent = vaultErrText(e); });
  });
  actions.appendChild(cancel); actions.appendChild(go);
  form.appendChild(actions);
  m.box.appendChild(form);
  sync();
}
`
