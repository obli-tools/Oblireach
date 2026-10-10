package main

// term_drop.go — files dropped on a terminal (CMD / PowerShell / SSH) go to
// the shell's folder on the device.
//
// The folder: the Obliance agent starts the terminal's shell so that it
// reports its working directory at each prompt (Obliance
// agent shell integration): OSC 7 file://host/path from bash on
// Linux and macOS, OSC 9;9;<path> from CMD and PowerShell on Windows. Read
// here through xterm's parser (never drawn), validated like a path typed in
// the explorer (explorerNormPath: absolute, no ".."; for Windows never a
// network path: \\host\share, that only the shell's output names, would send
// the operator's files to another machine), each for its own OS only. The
// folder is SURE only when such a report came after the last Enter the
// operator sent, one prompt per Enter of it (several lines pasted at once:
// as many prompts; a program that swallows lines never gives them, so the
// folder is asked): during a command, in an
// editor, after ssh to another machine (an OSC 7 of another host), after
// sudo -i... it is not, and a drop asks for the folder (the last known one,
// "may have changed", or a default per OS). An agent without the
// integration reports nothing: the same question.
//   xterm parses what it is given a little later (its write buffer): each
// piece keeps the Enter count and the session of its arrival, so a report is
// judged by when the shell sent it, never by when it was parsed (a report
// still waiting in the buffer never counts as newer than an Enter).
//
// The sending: the transfer queue of the Files page (transfers.go:
// xferCollect, xferUploadCollected), so the explorer's gated path only
// (FILE_EXPLORER_CMD on the device's server socket: the capability 'files',
// the privacy code, the restriction's 2FA, the server's audit), 100 MB per
// file, folders included (the batch limits), a name already there asked
// (overwrite / keep both / skip). Nothing is ever typed into the shell. Main
// window and session windows alike (File objects: no local binding needed).
//
// A local SSH connection (localconn_tools.go) has no agent to report its
// folder, and its shell rarely does it itself (OSC 7: some distributions'
// bash, zsh or fish set up for it: then exactly as above). The app asks the
// connection instead (reachSshCwd, ssh_relay.go localCwdScript: on a second
// channel of the same SSH connection, never seen in the terminal): the
// folder of its login shell, SURE when that shell is at its prompt (no
// command running in it: its process group holds the terminal). Asked when
// files come over the terminal (the drop zone says it as soon as it is
// known) and again at the drop. Without /proc (macOS, the BSDs) ps names
// that shell and lsof / pwdx / procstat its folder (ssh_relay.go
// localCwdPs). Unsure (a command running, none of those tools, several
// terminals on one connection, a Windows server): the folder is asked, the
// shell's last one (or the connection's home) proposed. The files go by the
// connection's own SFTP, in chunks (sftp_stream.go: no size limit), or by
// SCP when the server has no SFTP (scp.go), the same queue and chip.
//
// A chip at the bottom right of the terminal follows its files (xferOnChange:
// the items whose origin is the tab); a click lists them. Closing the
// terminal while they are on their way asks first (they are cancelled).
//
// Both consts are injected verbatim after the editing round trip (buildUI,
// not through Sprintf: '%' is safe): no backtick here.

const termDropCSS = `
/* ── Files dropped on a terminal (term_drop.go) ── */
.term-drop{position:absolute;top:10px;right:14px;bottom:10px;left:10px;z-index:8;display:none;flex-direction:column;align-items:center;justify-content:center;gap:12px;padding:24px;text-align:center;border-radius:14px;background:rgb(var(--accent-rgb) / .16);color:var(--accent-l);pointer-events:none}
.term-drop.visible{display:flex}
.term-drop.denied{background:rgb(var(--warn-rgb) / .14);color:var(--warn)}
.term-drop .td-ico{width:46px;height:46px;border-radius:14px;display:flex;align-items:center;justify-content:center;background:rgb(var(--accent-rgb) / .18)}
.term-drop.denied .td-ico{background:rgb(var(--warn-rgb) / .18)}
.term-drop .td-text{max-width:560px;font-size:13.5px;font-weight:600;line-height:1.45;overflow-wrap:anywhere}
.term-drop .td-path{display:inline-block;max-width:100%;margin:0 2px;padding:2px 8px;border-radius:7px;background:rgb(var(--accent-rgb) / .2);font-family:"Cascadia Mono","Cascadia Code",Consolas,monospace;font-size:12.5px;font-weight:600;vertical-align:baseline;overflow-wrap:anywhere}
.term-drop .td-note{max-width:520px;font-size:12px;font-weight:500;line-height:1.4;opacity:.85}
.term-xfer{position:absolute;right:20px;bottom:14px;z-index:7;display:none;flex-direction:column;align-items:flex-end;gap:8px;max-width:calc(100% - 40px);pointer-events:none}
.term-xfer.visible{display:flex}
.term-xfer > *{pointer-events:auto}
.term-xfer-chip{display:inline-flex;align-items:center;gap:8px;max-width:100%;min-width:0;padding:5px 5px 5px 8px;border-radius:12px;background:var(--bg2);box-shadow:0 8px 30px var(--shadow);color:var(--text);font-size:12px;cursor:pointer;user-select:none}
.term-xfer-chip:hover{background:var(--bg3)}
.term-xfer-chip:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.term-xfer-chip .tx-ico{flex-shrink:0;width:22px;height:22px;border-radius:7px;display:inline-flex;align-items:center;justify-content:center;background:rgb(var(--accent-rgb) / .14);color:var(--accent-l)}
.term-xfer-chip.warn .tx-ico{background:rgb(var(--warn-rgb) / .16);color:var(--warn)}
.term-xfer-chip .tx-txt{min-width:0;max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-variant-numeric:tabular-nums}
.term-xfer-chip .xp-bar{width:64px}
.term-xfer-list{width:580px;max-width:100%;border-radius:14px;background:var(--bg2);box-shadow:0 8px 30px var(--shadow);overflow:hidden}
.term-xfer-head{display:flex;align-items:center;gap:8px;padding:8px 8px 6px 12px;font-size:11.5px;font-weight:600;color:var(--muted)}
.term-xfer-head .sp{flex:1}
.term-xfer-head .btn-sm{padding:4px 10px}
.term-xfer-head .btn-sm:disabled{opacity:.4;cursor:default}
.term-xfer-body{max-height:220px;overflow:auto;padding:0 6px 6px}
.term-xfer-body .xp-row-st{width:120px}
`

const termDropJS = `
// ═════════════════════════════════════════════════════════════════════════
// Files dropped on a terminal (term_drop.go): to the shell's folder
// ═════════════════════════════════════════════════════════════════════════
var TERM_OSC_MAX = 4096;  // a reported path, in characters, at most
var TERM_DROP_ROWS = 50;  // a terminal's list of files, rows at most

// ── The shell's folder ──
// A new session (termStart): nothing known. (The pieces still waiting in
// xterm's buffer keep their session: never taken for this one's.)
function termCwdReset(tab) {
  tab.enterSeq = 0; tab.enterNeed = 0; tab.cwdPrompts = 0; tab.cwd = null; tab.cwdHome = ''; tab.cwdLast = ''; tab.cwdShown = '';
  if (!tab.rxq) tab.rxq = [];
}
// Each piece written to xterm (termWrite): its arrival, dropped once parsed.
// Returns the callback of term.write.
function termRxTrack(tab) {
  var q = tab.rxq || (tab.rxq = []);
  var ent = { seq: tab.enterSeq || 0, gen: tab.gen || 0 };
  q.push(ent);
  return function () { var i = q.indexOf(ent); if (i >= 0) q.splice(i, 1); };
}
// The arrival of what xterm parses now (the oldest piece not parsed yet).
function termRxStamp(tab) {
  var q = tab.rxq;
  return (q && q.length) ? q[0] : { seq: tab.enterSeq || 0, gen: tab.gen || 0 };
}
// Keys sent to the shell (termSendData): an Enter may run a command, the
// folder is no longer sure until the next prompt reports it. Several Enters
// at once (lines pasted: CMD, an older bash run each of them): as many
// prompts must report it (enterNeed), the last line's included. (Lines of a
// bracketed paste are counted too: never fewer prompts than commands, at
// worst the folder is asked.)
function termNoteInput(tab, d) {
  var n = String(d).split('\r').length - 1;
  if (!n) return;
  tab.enterSeq = (tab.enterSeq || 0) + 1;
  tab.enterNeed = n;
  tab.cwdPrompts = 0;
}
// xterm's parser hands the reports here (never drawn: handled).
function termCwdWire(tab, term) {
  var p = term && term.parser;
  if (!p || typeof p.registerOscHandler !== 'function') return;
  try {
    p.registerOscHandler(7, function (data) { termOnOsc(tab, 7, data); return true; });
    p.registerOscHandler(9, function (data) { termOnOsc(tab, 9, data); return true; });
  } catch (e) {}
}
// OSC 9;9;<path> (CMD, PowerShell): the path, or null (another OSC 9: the
// progress or notifications of other terminals; a relative, "..", network,
// device namespace or control character path).
function termParseOsc9(data) {
  var s = String(data === undefined || data === null ? '' : data);
  if (s.slice(0, 2) !== '9;' || s.length > TERM_OSC_MAX + 4) return null;
  s = s.slice(2).trim();
  if (s.length >= 2 && s.charAt(0) === '"' && s.charAt(s.length - 1) === '"') s = s.slice(1, -1);
  if (!s || /[\u0000-\u001f\u007f-\u009f]/.test(s)) return null;
  var p = explorerNormPath(s, 'windows');
  // (a drive path only: never \\?\ nor \\.\, never \\host\share: any output
  // can name one, and the files would go to that host; a PowerShell standing
  // in a network folder: the folder is asked, the operator may type it)
  if (!p || !/^[A-Za-z]:\\/.test(p)) return null;
  return p;
}
// OSC 7 file://<host><path> (bash; percent-encoded): { host, path }, or null.
function termParseOsc7(data) {
  var s = String(data === undefined || data === null ? '' : data);
  if (s.length > TERM_OSC_MAX * 3 + 800 || !/^file:\/\//i.test(s)) return null;
  var rest = s.slice(7), i = rest.indexOf('/');
  if (i < 0) return null;
  var host, path;
  try { host = decodeURIComponent(rest.slice(0, i)); path = decodeURIComponent(rest.slice(i)); } catch (e) { return null; }
  if (host.length > 255 || path.length > TERM_OSC_MAX || /[\u0000-\u001f\u007f-\u009f]/.test(host + path)) return null;
  var p = explorerNormPath(path, 'unix');
  return p ? { host: host.toLowerCase(), path: p } : null;
}
// The host of a report is this device's: none or localhost; the one of the
// shell's first prompt (before any Enter: the agent's own shell), or the
// device's name (short names compared). Another one: the operator went
// elsewhere (ssh).
function termOscHome(tab, host, stamp) {
  if (!host || host === 'localhost') return true;
  if (tab.cwdHome) return host === tab.cwdHome;
  var short = function (h) { h = String(h || '').toLowerCase(); var i = h.indexOf('.'); return i > 0 ? h.slice(0, i) : h; };
  var dn = String((tab.device && tab.device.hostname) || '');
  if ((dn && short(dn) === short(host)) || stamp.seq === 0) { tab.cwdHome = host; return true; }
  return false;
}
function termOnOsc(tab, id, data) {
  if (!termIsTab(tab) || !tab.device) return;
  var stamp = termRxStamp(tab);
  if (stamp.gen !== (tab.gen || 0)) return; // (the previous session's output)
  var win = explorerOs(tab.device) === 'windows';
  if (id === 9) {
    if (!win) return;
    var p9 = termParseOsc9(data);
    if (p9) { termPromptSeen(tab, stamp); termCwdSet(tab, { path: p9, host: '', seq: stamp.seq }); }
    return;
  }
  if (win) return;
  var r = termParseOsc7(data);
  if (!r) return;
  termPromptSeen(tab, stamp);
  if (termOscHome(tab, r.host, stamp)) termCwdSet(tab, { path: r.path, host: r.host, seq: stamp.seq });
  else termCwdSet(tab, { elsewhere: true, path: '', host: r.host, seq: stamp.seq });
}
// A prompt reported after the last Enters (its arrival): one of those they need.
function termPromptSeen(tab, stamp) {
  if (stamp.seq === (tab.enterSeq || 0)) tab.cwdPrompts = (tab.cwdPrompts || 0) + 1;
}
function termCwdSet(tab, c) {
  tab.cwd = c;
  if (!c.elsewhere) tab.cwdLast = c.path;
  termCwdStatus(tab);
}
// Sure: this device's folder, reported by a prompt drawn after the last Enter
// (as many prompts as that input held Enters).
function termCwdCertain(tab) {
  var c = tab && tab.cwd;
  return !!c && !c.elsewhere && !!c.path && !!tab.live && c.seq === (tab.enterSeq || 0) && (tab.cwdPrompts || 0) >= (tab.enterNeed || 0);
}
// The toolbar says the folder (on each change only): "Connected · /var/www".
function termCwdStatus(tab) {
  if (!tab.live || tab.status !== 'live') return;
  var c = tab.cwd, path = (c && !c.elsewhere) ? c.path : '';
  if (path === (tab.cwdShown || '')) return;
  tab.cwdShown = path;
  toolSetStatus(tab, 'live', path ? { k: 'term.connectedIn', p: { path: path } } : { k: 'term.connected' });
}

// ── The drop ──
// The device as the app knows it now (its rights, its privacy flags).
function termDevNow(tab) {
  var d = tab.device, f = null;
  try { if (d && typeof reachFindDevice === 'function') f = reachFindDevice(String(d.id), devPid(d)); } catch (e) { f = null; }
  return f || d;
}
function termDropHasFiles(e) { var t = e && e.dataTransfer && e.dataTransfer.types; return !!t && Array.prototype.indexOf.call(t, 'Files') >= 0; }
// Why files cannot be sent to this device: the explorer's reason (the agent
// offline, legacy, privacy...), the missing permission said for a drop.
function termDropRefusal(st) {
  return (!st.show || !st.reason || st.reasonKey === 'tools.reason.notPermitted') ? T('termdrop.noFiles') : st.reason;
}
// What a drop would do: { ok, certain, path, note } or { ok: false, reason }.
function termDropState(tab) {
  var dev = termDevNow(tab), st = toolState(dev, 'files');
  if (!st.show || !st.enabled) return { ok: false, reason: termDropRefusal(st) };
  var note = st.lock === 'locked' ? st.reason : '';
  if (termCwdCertain(tab)) return { ok: true, certain: true, path: tab.cwd.path, note: note };
  var lc = termLocalCwdFresh(tab);
  if (lc && lc.sure) return { ok: true, certain: true, path: lc.path, note: note };
  return { ok: true, certain: false, path: '', note: note };
}
// ── A local connection's folder (reachSshCwd) ──
// Its last answer, while nothing was typed since and for a few seconds.
var TERM_LCWD_MS = 4000;
function termLocalCwdFresh(tab) {
  var c = tab && tab.lcwd;
  if (!c || !devIsLocal(tab.device) || !tab.live || c.seq !== (tab.enterSeq || 0) || c.gen !== (tab.gen || 0) || Date.now() - c.at > TERM_LCWD_MS) return null;
  return c;
}
// Asks the connection (one question at a time): resolves to { path, sure }
// ({ path: '' } when it cannot be known), kept on the tab.
function termLocalCwd(tab) {
  if (!tab || !devIsLocal(tab.device) || !tab.live || !TERM_PARK_RE.test(String(tab.token || ''))) return Promise.resolve({ path: '', sure: false });
  if (tab.lcwdAsk) return tab.lcwdAsk;
  var seq = tab.enterSeq || 0, gen = tab.gen || 0;
  var p = vaultAsyncCallWithin(10000, 'reachSshCwd', [String(tab.token)]).then(function (r) {
    var path = (r && typeof r.path === 'string') ? explorerNormPath(r.path, 'unix') : '';
    var c = { path: path || '', sure: !!(path && r.sure === true), seq: seq, gen: gen, at: Date.now() };
    tab.lcwd = c;
    if (c.path) tab.cwdLast = c.path;
    return c;
  }, function () { return { path: '', sure: false }; });
  tab.lcwdAsk = p;
  p.then(function () { if (tab.lcwdAsk === p) tab.lcwdAsk = null; });
  return p;
}
// Files over a local connection's terminal: its folder asked, the drop zone
// said again once it is known (still over it).
function termLocalCwdHover(tab) {
  if (!devIsLocal(tab.device) || termCwdCertain(tab) || termLocalCwdFresh(tab)) return;
  termLocalCwd(tab).then(function () { if (tab.dropSig) termDropShow(tab, termDropState(tab)); });
}
// The connection's home (its SFTP's), the folder proposed when nothing is known.
async function termLocalHome(dev) {
  try {
    var res = await explorerCtxCmd(explorerCtxFor(dev), 'list_directory', { path: '' }, EXPLORER_SHORT_MS);
    var p = res && typeof res.path === 'string' ? explorerNormPath(res.path, 'unix') : '';
    return p || '/';
  } catch (e) { return '/'; }
}
// Its box (termImpl.create): the drop zone over the terminal (before xterm
// sees anything), the chip of its files.
function termDropWire(tab) {
  var box = tab.box;
  if (!box || tab.dropEl) return;
  tab.dropEl = document.createElement('div'); tab.dropEl.className = 'term-drop';
  tab.xferEl = document.createElement('div'); tab.xferEl.className = 'term-xfer';
  box.appendChild(tab.dropEl); box.appendChild(tab.xferEl);
  var stop = function (e) { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
  box.addEventListener('dragenter', function (e) { if (termDropHasFiles(e)) { stop(e); termLocalCwdHover(tab); } }, true);
  box.addEventListener('dragover', function (e) {
    if (!termDropHasFiles(e)) return;
    stop(e);
    var st = termDropState(tab);
    if (e.dataTransfer) e.dataTransfer.dropEffect = st.ok ? 'copy' : 'none';
    termDropShow(tab, st);
  }, true);
  box.addEventListener('dragleave', function (e) {
    if (!e || !e.relatedTarget || typeof box.contains !== 'function' || !box.contains(e.relatedTarget)) termDropShow(tab, null);
  }, true);
  box.addEventListener('drop', function (e) {
    if (!termDropHasFiles(e)) return;
    stop(e);
    termDropShow(tab, null);
    var st = termDropState(tab);
    if (!st.ok) { reachNotify(st.reason, 'warn'); return; }
    termDropFiles(tab, e.dataTransfer);
  }, true);
}
function termDropIcon(svg) { return String(svg || '').replace(/width="\d+" height="\d+"/, 'width="22" height="22"'); }
// The drop zone: where the files go, or why they cannot (null: hidden).
function termDropShow(tab, st) {
  var el = tab.dropEl;
  if (!el) return;
  var sig = st ? (st.ok ? (st.certain ? 'c:' + st.path : 'a') : 'x:' + st.reason) + '|' + (st.note || '') : '';
  if (sig === (tab.dropSig || '')) return;
  tab.dropSig = sig;
  el.innerHTML = '';
  el.className = 'term-drop' + (st ? ' visible' + (st.ok ? '' : ' denied') : '');
  if (!st) return;
  var ic = document.createElement('div'); ic.className = 'td-ico'; ic.innerHTML = termDropIcon(st.ok ? EXPLORER_ICON.upload : TOOLS_ICON.blocked);
  var t = document.createElement('div'); t.className = 'td-text';
  if (st.ok && st.certain) {
    // (the folder in its own pill, where the language puts it)
    var parts = T('termdrop.hintCertain', { path: '\u0001' }).split('\u0001');
    var pa = document.createElement('span'); pa.textContent = parts[0] || ''; t.appendChild(pa);
    var pe = document.createElement('span'); pe.className = 'td-path'; pe.textContent = st.path; t.appendChild(pe);
    var pb = document.createElement('span'); pb.textContent = parts.slice(1).join(''); t.appendChild(pb);
  } else t.textContent = !st.ok ? st.reason : T('termdrop.hintAsk');
  t.setAttribute('aria-label', !st.ok ? st.reason : st.certain ? T('termdrop.hintCertain', { path: st.path }) : T('termdrop.hintAsk'));
  el.appendChild(ic); el.appendChild(t);
  if (st.note) { var n = document.createElement('div'); n.className = 'td-note'; n.textContent = st.note; el.appendChild(n); }
}
// Files dropped: taken now (a DataTransfer is readable during its drop
// event only), with the folder known now; then the gates, the folder asked
// when it is not sure, the queue.
function termDropFiles(tab, dt) {
  var colP = xferCollect(dt, tab.device);
  var snap = { certain: termCwdCertain(tab), path: '', last: tab.cwdLast || '', local: devIsLocal(tab.device) };
  if (snap.certain) snap.path = tab.cwd.path;
  var p = termDropRun(tab, colP, snap);
  if (p && typeof p.catch === 'function') p.catch(function () {});
  return p;
}
async function termDropRun(tab, colP, snap) {
  var dev = termDevNow(tab), st = toolState(dev, 'files');
  if (!st.show || !st.enabled) { reachNotify(termDropRefusal(st), 'warn'); return null; }
  tab.dropPlanning = (tab.dropPlanning || 0) + 1;
  termXferRender(tab);
  try {
    if (!(await privacyEnsure(dev, 'files'))) return null;
    var col = await colP;
    if (!col || toolTabs.indexOf(tab) < 0) return null;
    if (col.limit) { reachNotify(T('xfer.limit'), 'warn'); return null; }
    if (!col.items.length) { xferSaySkipped(0, col.tooLarge, 0, 0); return null; }
    dev = termDevNow(tab);
    // A local connection: its shell's folder asked now (sure: there).
    if (!snap.certain && snap.local) {
      var lc = await termLocalCwd(tab);
      if (lc.sure) { snap.certain = true; snap.path = lc.path; }
      else if (lc.path) snap.last = lc.path;
      if (toolTabs.indexOf(tab) < 0) return null;
    }
    var dir = snap.certain ? snap.path : await termDropAsk(dev, col, snap);
    if (!dir || toolTabs.indexOf(tab) < 0) return null;
    return await xferUploadCollected(dev, dir, col, { origin: tab.id });
  } finally {
    tab.dropPlanning = Math.max(0, (tab.dropPlanning || 1) - 1);
    termXferRender(tab);
  }
}
// A folder the shell does not report for sure: an absolute path of the
// device, when nothing is known a default one (never System32, the SYSTEM
// shell's own folder).
function termDropDefault(dev) {
  var k = devOsKind(dev);
  return k === 'windows' ? 'C:\\Windows\\Temp' : k === 'macos' ? '/var/root' : '/root';
}
async function termDropAsk(dev, col, snap) {
  // (a local connection: the shell's last folder, else its home, asked of its SFTP)
  var os = explorerOs(dev), def = devIsLocal(dev) ? (snap.last || await termLocalHome(dev)) : termDropDefault(dev);
  var v = await explorerDialog({
    title: T('termdrop.askTitle', { device: reachDevName(dev) }),
    text: T(snap.last ? 'termdrop.askLast' : 'termdrop.askDefault', { count: col.items.length }),
    value: snap.last || def, ok: 'termdrop.send', icon: EXPLORER_ICON.upload, maxLength: 4096,
    validate: function (x) { return (String(x || '').trim() && explorerNormPath(x, os)) ? '' : T('termdrop.badPath', { example: def }); },
  });
  if (typeof v !== 'string') return '';
  return explorerNormPath(v, os) || '';
}

// ── The terminal's files: its chip, its list ──
function termXferItems(tab) {
  var q = (tab && tab.device) ? xferQueues.get(devKey(tab.device)) : null;
  return q ? q.items.filter(function (it) { return it.batch && it.batch.origin === tab.id; }) : [];
}
function termXferLiveItem(it) { return it.state === 'queued' || xferLive(it); }
function termDropBusy(tab) { return termXferItems(tab).some(termXferLiveItem); }
function termXferRender(tab) {
  var el = tab && tab.xferEl;
  if (!el) return;
  var q = xferQueues.get(devKey(tab.device)) || null;
  var items = termXferItems(tab);
  var live = items.filter(termXferLiveItem);
  var errs = items.filter(function (it) { return it.state === 'error'; });
  var planning = (tab.dropPlanning || 0) > 0;
  if (!items.length) tab.xferOpen = false;
  var show = planning || live.length > 0 || errs.length > 0 || !!tab.xferOpen;
  el.innerHTML = '';
  el.classList.toggle('visible', show);
  if (!show) return;
  if (tab.xferOpen && q) {
    var list = explorerEl('div', 'term-xfer-list', el);
    var head = explorerEl('div', 'term-xfer-head', list);
    var ht = explorerEl('span', '', head); ht.textContent = T('termdrop.listTitle');
    explorerEl('span', 'sp', head);
    var cl = explorerEl('button', 'btn-sm', head); cl.textContent = T('viewer.tr.clearFinished');
    cl.disabled = items.length === live.length;
    cl.addEventListener('click', function () { termXferClear(tab); });
    var body = explorerEl('div', 'term-xfer-body', list);
    items.slice(-TERM_DROP_ROWS).forEach(function (it) { body.appendChild(xferRowEl(q, it)); });
  }
  var chip = explorerEl('div', 'term-xfer-chip' + (errs.length && !live.length && !planning ? ' warn' : ''), el);
  chip.setAttribute('role', 'button'); chip.tabIndex = 0; chip.title = T('termdrop.chipTip');
  chip.setAttribute('aria-expanded', tab.xferOpen ? 'true' : 'false');
  var ic = explorerEl('span', 'tx-ico', chip); ic.innerHTML = XFER_ICON.up;
  var txt = explorerEl('span', 'tx-txt', chip);
  var cur = items.filter(function (it) { return !it.batch.notified; });
  var fin = cur.filter(function (it) { return !termXferLiveItem(it); }).length;
  var act = (q && q.active && q.active.batch && q.active.batch.origin === tab.id) ? q.active : (live[0] || null);
  if (live.length) txt.textContent = T('termdrop.chipLive', { done: fin, files: cur.length }) + (act ? ' · ' + act.name : '');
  else if (planning) txt.textContent = T('xfer.preparing');
  else if (errs.length) txt.textContent = T('termdrop.chipErrors', { count: errs.length });
  else txt.textContent = T('termdrop.listTitle');
  if (live.length && act) {
    var bar = explorerEl('span', 'xp-bar' + (act.pct >= 0 ? '' : ' ind'), chip);
    var fill = explorerEl('i', '', bar);
    if (act.pct >= 0) fill.style.width = Math.max(2, Math.min(100, act.pct)) + '%';
  }
  var x = explorerEl('button', 'xp-row-x', chip); x.innerHTML = XFER_ICON.x;
  x.title = live.length ? T('termdrop.cancelTip') : T('common.close');
  x.setAttribute('aria-label', x.title);
  x.disabled = !live.length && planning;
  x.addEventListener('click', function (e) {
    if (e && e.stopPropagation) e.stopPropagation();
    if (termDropBusy(tab)) termXferCancel(tab); else termXferClear(tab);
  });
  var toggle = function () { tab.xferOpen = !tab.xferOpen; termXferRender(tab); };
  chip.addEventListener('click', toggle);
  chip.addEventListener('keydown', function (e) {
    if (!e || e.target !== chip || (e.key !== 'Enter' && e.key !== ' ')) return;
    if (e.preventDefault) e.preventDefault();
    toggle();
  });
}
// Its files still on their way: cancelled (one being sent ends first).
function termXferCancel(tab) {
  var q = xferQueues.get(devKey(tab.device));
  if (!q) return;
  q.batches.slice().forEach(function (b) { if (b.origin === tab.id && !b.notified) xferCancelBatch(b); });
}
// Its finished files: out of the list (the explorer's dock drops them too).
function termXferClear(tab) {
  var q = xferQueues.get(devKey(tab.device));
  tab.xferOpen = false;
  if (!q) { termXferRender(tab); return; }
  q.items = q.items.filter(function (it) { return !(it.batch && it.batch.origin === tab.id && !termXferLiveItem(it)); });
  q.batches = q.batches.filter(function (b) { return !b.notified || q.items.some(function (it) { return it.batch === b; }); });
  xferChanged(q);
  termXferRender(tab);
}
xferOnChange(function (q) {
  toolTabs.forEach(function (t) { if (termIsTab(t) && t.xferEl && t.device && devKey(t.device) === q.key) termXferRender(t); });
});
// Closing a terminal whose dropped files are on their way (toolCloseTab):
// asked first, then they are cancelled. true: the close waits for the answer.
function termDropCloseGuard(tab) {
  if (!termIsTab(tab) || tab.closeForced || !termDropBusy(tab)) return false;
  if (tab.closeAsking) return true;
  tab.closeAsking = true;
  explorerDialog({ title: T('xfer.closeTitle'), text: T('termdrop.closeConfirm', { device: reachDevName(tab.device) }), ok: 'xfer.closeAnyway', danger: true, icon: EXPLORER_ICON.upload }).then(function (ok) {
    tab.closeAsking = false;
    if (ok !== true || toolTabs.indexOf(tab) < 0) return;
    termXferCancel(tab);
    tab.closeForced = true;
    toolCloseTab(tab);
  });
  return true;
}
`
