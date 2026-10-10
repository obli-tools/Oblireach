package main

// help_page.go — "Receive help" in the page (Go: receive_help.go): this
// computer is helped through an Oblireach Support server, like the thin
// client OblireachSupport.exe shows it. It runs in the
// background while "Allow incoming support" is on; the Dashboard's card
// (support_dashboard.go) shows the ID and the code, this page the details:
//   - the page (from the card's "Details"): the anti-scam notice, what the
//     helper can and cannot do, the server (the default one of Settings ›
//     Support: no key needed), then the ID and the one-time code with Copy,
//     "New code", the status line, the last session's end, "Stop receiving
//     help" (incoming support off);
//   - the acceptance window, over any page, when an operator proved the
//     code: the name the operator typed (marked not verified), the account
//     the server verified if any, the verification code (SAS) both screens
//     show, control / clipboard / files (on, on, off), Accept armed after a
//     moment, Refuse, the countdown;
//   - while helped: a banner under the title bar on every page (who, since
//     when, Chat, Stop), the session on the page (permissions, path, files
//     received, the chat with the operator), the vault held locked;
//   - the end: why and how long, as a notice over the ID and the new code
//     (the ID stays while the registration lives);
//   - "Report this helper" (receive_help_report.go): in the banner, the
//     session panel, the Dashboard card and the end notice, while the
//     server allows it; an in-app dialog (reason, text, "End the session
//     now"), then the advice.
// The state comes from Go (GET /local/support/help/state), polled while
// receiving help is on (faster while a request or a session is on screen);
// the main window only (not a session window). The one-time code is only
// ever drawn: never kept by the page (no storage, no title, no notification).
//
// Both consts are injected verbatim after the other tool blocks (ui.go
// buildUI); no backtick here.

const helpCSS = `
/* ── Receive help (help_page.go) ── */
.help-wrap{max-width:620px;display:flex;flex-direction:column;gap:14px}
.help-scam{display:flex;gap:12px;padding:14px 16px;border-radius:14px;background:rgb(var(--warn-rgb) / .13);color:var(--text);font-size:12.5px;line-height:1.55}
.help-scam .help-scam-icon{flex-shrink:0;color:var(--warn);margin-top:1px}
.help-scam b{display:block;font-size:13.5px;color:var(--warn);margin-bottom:3px}
.help-note{padding:11px 14px;border-radius:12px;background:rgb(var(--ink) / .05);font-size:12px;color:var(--muted);line-height:1.5}
.help-form{display:flex;flex-direction:column;gap:12px}
.help-actions{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.help-actions .btn-primary{width:auto;padding:10px 22px}
.help-creds{display:flex;gap:14px;flex-wrap:wrap}
.help-cred{flex:1;min-width:220px;display:flex;flex-direction:column;gap:6px;padding:14px 16px;border-radius:14px;background:rgb(var(--accent-rgb) / .08);container-type:inline-size}
.help-cred-label{font-size:11.5px;color:var(--muted);font-weight:600;text-transform:uppercase;letter-spacing:.5px}
.help-cred-row{display:flex;align-items:center;gap:10px}
.help-cred-value{flex:1;min-width:0;font-size:clamp(14px,10cqi,32px);font-weight:700;letter-spacing:.08em;color:var(--text);font-variant-numeric:tabular-nums;white-space:nowrap;overflow:hidden;text-overflow:clip}
.help-cred-copy{flex:none;width:30px;height:30px;display:inline-flex;align-items:center;justify-content:center;border:none;border-radius:8px;background:rgb(var(--ink) / .07);color:var(--muted);cursor:pointer;padding:0;transition:background .12s,color .12s}
.help-cred-copy:hover{background:rgb(var(--ink) / .13);color:var(--text)}
.help-cred-copy:focus-visible{outline:none;box-shadow:0 0 0 2px rgb(var(--accent-rgb) / .45)}
.help-cred-copy.copied{color:var(--success)}
.help-cred-copy svg{width:15px;height:15px}
.help-status{font-size:12.5px;color:var(--muted);display:flex;align-items:center;gap:8px;min-height:18px}
.help-status.warn{color:var(--warn)}
.help-status .help-dot{width:8px;height:8px;border-radius:4px;background:var(--success);flex-shrink:0;animation:helpPulse 1.6s infinite}
@keyframes helpPulse{0%,100%{opacity:1}50%{opacity:.35}}
.help-wait{display:flex;flex-direction:column;align-items:center;gap:12px;padding:26px 18px;text-align:center;color:var(--muted);font-size:13px}
.help-sess-title{font-size:16px;font-weight:700;color:var(--text)}
.help-chips{display:flex;gap:6px;flex-wrap:wrap;align-items:center}
.help-chip{display:inline-flex;align-items:center;padding:2px 9px;border-radius:10px;font-size:10.5px;font-weight:700;white-space:nowrap;background:rgb(var(--ink) / .07);color:var(--muted)}
.help-chip.on{background:rgb(var(--accent-rgb) / .14);color:var(--accent-l)}
.help-danger{background:rgb(var(--danger-rgb) / .14)!important;color:var(--danger)!important;font-weight:700}
.help-danger:hover{background:rgb(var(--danger-rgb) / .24)!important}
.help-chat{display:flex;flex-direction:column;height:340px;border-radius:14px;overflow:hidden;background:linear-gradient(180deg,var(--bg) 0%,var(--bg2) 100%);box-shadow:inset 0 0 0 1px rgb(var(--ink) / .06)}
.help-chat-head{padding:10px 14px 4px;font-size:12.5px;font-weight:600;color:var(--text)}
.help-chat .chat-msg .bubble{white-space:pre-wrap}
.help-chat-err{display:none;font-size:11px;color:var(--danger);padding:0 14px 4px}
.help-chat-err.visible{display:block}
/* The banner while helped: under the title bar, on every page. */
.help-banner{display:none;align-items:center;gap:12px;padding:8px 18px;flex-shrink:0;font-size:12.5px;color:var(--text);background:linear-gradient(90deg,rgb(var(--danger-rgb) / .20),rgb(var(--danger-rgb) / .08))}
.help-banner.visible{display:flex}
.help-banner .help-dot{width:9px;height:9px;border-radius:5px;background:var(--danger);flex-shrink:0;animation:helpPulse 1.2s infinite}
.help-banner .hb-text{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:600}
.help-banner .hb-time{color:var(--muted);font-variant-numeric:tabular-nums}
.help-banner .hb-chat{position:relative}
.help-banner .hb-badge{position:absolute;top:-6px;right:-6px;min-width:16px;height:16px;padding:0 4px;border-radius:8px;background:var(--accent-fill);color:var(--on-accent);font-size:9.5px;font-weight:700;line-height:16px;text-align:center;display:none}
.help-banner .hb-badge.visible{display:block}
/* The acceptance window. */
.help-ask-back{position:fixed;inset:0;z-index:4000;background:rgb(0 0 0 / .45);display:flex;align-items:center;justify-content:center;padding:20px}
.help-ask{width:440px;max-width:100%;max-height:100%;overflow:auto;border-radius:18px;background:var(--bg);box-shadow:0 20px 60px var(--shadow);padding:20px 22px;display:flex;flex-direction:column;gap:12px}
.help-ask h2{font-size:16px;margin:0;color:var(--text)}
.help-ask-op{display:flex;flex-direction:column;gap:2px;padding:10px 12px;border-radius:12px;background:rgb(var(--ink) / .05)}
.help-ask-op .help-ask-name{font-size:15px;font-weight:700;color:var(--text);word-break:break-word}
.help-ask-op .help-ask-tag{font-size:11px;color:var(--warn);font-weight:600}
.help-ask-op .help-ask-acct{font-size:11.5px;color:var(--success);font-weight:600}
.help-ask-op .help-ask-acct.none{color:var(--muted)}
.help-ask-sas-label{font-size:12px;color:var(--muted)}
.help-ask-sas{align-self:center;font-size:40px;font-weight:700;letter-spacing:6px;color:var(--text);font-variant-numeric:tabular-nums;padding:8px 24px;border-radius:16px;background:rgb(var(--accent-rgb) / .10)}
.help-ask-toggles{display:flex;flex-direction:column;gap:8px}
.help-ask-toggles label{display:flex;align-items:center;gap:9px;font-size:12.5px;color:var(--text);cursor:pointer}
.help-ask-remaining{font-size:11px;color:var(--muted2)}
.help-ask-actions{display:flex;gap:8px;justify-content:flex-end}
.help-ask-actions .btn-primary{width:auto;padding:9px 22px}
.help-ended{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
.help-ended span{flex:1;min-width:0}
/* Report this helper (the dialog: the acceptance window's frame). */
.help-report-done{flex-basis:100%;font-size:12px;font-weight:600;color:var(--success)}
.help-rep-form{display:flex;flex-direction:column;gap:12px}
.help-rep-intro{font-size:12px;color:var(--muted);line-height:1.5}
.help-rep-label{font-size:12.5px;font-weight:700;color:var(--text)}
.help-rep-reasons{display:flex;flex-direction:column;gap:6px}
.help-rep-reason{width:100%;text-align:start;padding:9px 12px;border:none;border-radius:10px;background:rgb(var(--ink) / .06);color:var(--text);font-size:12.5px;cursor:pointer;transition:background .12s}
.help-rep-reason:hover{background:rgb(var(--ink) / .11)}
.help-rep-reason.on{background:var(--accent-fill);color:var(--on-accent);font-weight:600}
.help-rep-reason:focus-visible{outline:none;box-shadow:0 0 0 2px rgb(var(--accent-rgb) / .45)}
.help-rep-reason:disabled{cursor:default;opacity:.75}
.help-rep-text{width:100%;min-height:96px;resize:vertical;box-sizing:border-box;padding:10px 12px;border:none;border-radius:10px;background:rgb(var(--ink) / .05);color:var(--text);font:inherit;font-size:12.5px;line-height:1.5}
.help-rep-text:focus{outline:none;box-shadow:0 0 0 2px rgb(var(--accent-rgb) / .45)}
.help-rep-end{display:flex;align-items:center;gap:9px;font-size:12.5px;color:var(--text);cursor:pointer}
.help-rep-status{font-size:12px;line-height:1.45;color:var(--muted)}
.help-rep-status:empty{display:none}
.help-rep-status.warn{color:var(--warn)}
.help-rep-status.err{color:var(--danger)}
.help-rep-ok{font-size:16px;font-weight:700;color:var(--success)}
.help-rep-advice{display:flex;flex-direction:column;gap:3px;padding:12px 14px;border-radius:12px;background:rgb(var(--warn-rgb) / .12);font-size:12.5px;line-height:1.55;color:var(--text)}
.help-rep-advice b{color:var(--warn)}
`

const helpJS = `
// ── Receive help (help_page.go; Go: receive_help.go) ────────────────────────
var HELP_POLL_MS = 700;
var HELP_POLL_IDLE_MS = 1500; // (waiting for an operator, nothing on screen)
var HELP_ARM_MS = 1500;
var HELP_POLL_HIDDEN_MS = 5000; // (the window hidden, no session running)
var HELP_ICON = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 015.83 1c0 2-3 3-3 3"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>';
var HELP_WARN_ICON = '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>';
// The requests' refusals (receive_help.go helpError).
var HELP_ERR = { busy: 'help.err.busy', no_server: 'support.err.noServer', storage: 'support.err.storage', too_long: 'support.chat.tooLong',
  empty: 'help.err.failed', no_session: 'support.chat.failed', chat_failed: 'support.chat.failed', failed: 'help.err.failed' };
// The waiting screen's notices, the ends, the registration failures
// (receive_help.go helpNotice*, helpEnd*, helpErr*).
var HELP_NOTICE = { locked: 'help.notice.locked', failed: 'help.notice.failed', refused: 'help.notice.refused',
  timedOut: 'help.notice.timedOut', withdrawn: 'help.notice.withdrawn', setupFailed: 'help.notice.setupFailed', ended: 'help.ended.title', newId: 'help.notice.newId' };
var HELP_ENDED = { local: 'help.ended.local', peer: 'help.ended.peer', lost: 'help.ended.lost', error: 'help.ended.error' };
var HELP_UNREACH = { unreachable: 'help.unreachable.unreachable', refused: 'help.unreachable.refused', upgrade: 'help.unreachable.upgrade' };

// st: the last state from Go; chat: this session's lines (gen: its session).
var HELP = { st: null, timer: null, polling: false, seq: 0, gen: -1, chat: [], chatSeq: 0, unread: 0, err: '', chatErr: '',
  askSid: '', askArmed: false, askTimer: null, renderKey: '', copied: '', copiedTimer: null, banner: null, ask: null, rep: null };

function helpOn() { return !(typeof reachDetachedMode !== 'undefined' && reachDetachedMode); }
function helpPhase() { return HELP.st && HELP.st.phase ? HELP.st.phase : 'idle'; }
function helpFetch(method, path, body) { return supFetch(method, 'help/' + path, body); }
function helpErrText(code) { return T(HELP_ERR[code] || 'help.err.failed'); }
// 75 -> "1:15", 3725 -> "1:02:05".
function helpFmtDur(s) {
  s = Math.max(0, s | 0);
  var h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60;
  var p2 = function (n) { return (n < 10 ? '0' : '') + n; };
  return h ? h + ':' + p2(m) + ':' + p2(x) : m + ':' + p2(x);
}
function helpOperatorName(st) {
  var n = st && st.session ? st.session.operator : (st && st.ended ? st.ended.operator : '');
  return n || T('help.someone');
}

// ── The state, from Go ──
function helpPollStart() {
  if (!helpOn() || HELP.polling) return;
  HELP.polling = true;
  helpPollTick();
}
// Polled while receiving help runs (or is about to: the toggle on).
function helpPollOn() { return helpPhase() !== 'idle' || !!(HELP.st && HELP.st.incoming); }
function helpPollMs() {
  var ph = helpPhase(), r = typeof currentRoute !== 'undefined' ? currentRoute : '';
  // (the window hidden, no session running: nothing on screen to follow; an
  // operator asking is flashed on the taskbar by Go, the state read at once
  // when the window shows again)
  if (helpHidden() && ph !== 'session' && ph !== 'starting' && ph !== 'approval') return HELP_POLL_HIDDEN_MS;
  return ph === 'waiting' && r !== 'dashboard' && r !== 'help' ? HELP_POLL_IDLE_MS : HELP_POLL_MS;
}
function helpHidden() {
  var d = typeof document !== 'undefined' ? document : null;
  return !!(d && (d.hidden === true || d.visibilityState === 'hidden'));
}
// Back on screen: the state read now, the pace of the page then.
(function () {
  try {
    if (typeof document !== 'undefined' && document && typeof document.addEventListener === 'function') {
      document.addEventListener('visibilitychange', function () {
        if (helpHidden() || !HELP.polling) return;
        clearTimeout(HELP.timer);
        helpRefresh().then(function () { if (helpPollOn()) helpPollTick(); else HELP.polling = false; });
      });
    }
  } catch (e) {}
})();
function helpPollTick() {
  clearTimeout(HELP.timer);
  HELP.timer = setTimeout(async function () {
    await helpRefresh();
    if (helpPollOn()) helpPollTick(); else HELP.polling = false;
  }, helpPollMs());
}
async function helpRefresh() {
  var seq = ++HELP.seq;
  var r = null;
  try { r = await helpFetch('GET', 'state?chat=' + HELP.chatSeq + '&gen=' + HELP.gen); } catch (e) { r = null; }
  if (seq !== HELP.seq || !r || !r.ok) return;
  helpApply(r.data);
}
// The state at the app's start (a page reloaded during a session).
function helpInit() {
  if (!helpOn()) return;
  helpRefresh().then(function () { if (helpPollOn()) helpPollStart(); });
}

function helpApply(st) {
  if (!st || typeof st.phase !== 'string') return;
  var prev = helpPhase();
  if (st.gen !== HELP.gen) { HELP.gen = st.gen; HELP.chat = []; HELP.chatSeq = 0; HELP.unread = 0; HELP.chatErr = ''; }
  var lines = Array.isArray(st.chat) ? st.chat : [];
  var onPage = typeof currentRoute !== 'undefined' && currentRoute === 'help';
  lines.forEach(function (l) {
    if (!l || (l.seq | 0) <= HELP.chatSeq) return;
    HELP.chatSeq = l.seq | 0;
    HELP.chat.push({ from: l.from === 'me' ? 'me' : 'op', text: supChatClean(l.text) });
    if (l.from !== 'me' && !onPage) HELP.unread++;
  });
  if (HELP.chat.length > SUP_CHAT_KEEP) HELP.chat.splice(0, HELP.chat.length - SUP_CHAT_KEEP);
  if (st.phase === 'idle' || st.phase === 'waiting' || st.phase === 'connecting') { HELP.chat = []; HELP.unread = 0; }
  HELP.st = st;
  helpReportFollow(st);
  if (st.phase === 'approval' && st.approval) helpAskOpen(st.approval);
  else helpAskClose();
  if (st.phase === 'session' && prev !== 'session') helpOnSessionStart();
  helpBanner();
  if (onPage) helpRender();
  if (typeof supDashRefresh === 'function') supDashRefresh(false);
  if (helpPollOn()) helpPollStart();
}

// A session starts: the vault is held locked by Go; the vault's views drop
// what they showed.
function helpOnSessionStart() {
  if (typeof localConnsLoad === 'function') { try { var p = localConnsLoad(); if (p && p.catch) p.catch(function () {}); } catch (e) {} }
  reachNotify(T('help.session.vault'), 'warn');
}

// ── The banner (every page) ──
function helpBannerEl() {
  if (HELP.banner) return HELP.banner;
  var b = document.createElement('div'); b.className = 'help-banner'; b.id = 'help-banner'; b.setAttribute('role', 'status');
  var dot = document.createElement('span'); dot.className = 'help-dot';
  var text = document.createElement('span'); text.className = 'hb-text'; text.id = 'help-banner-text';
  var time = document.createElement('span'); time.className = 'hb-time'; time.id = 'help-banner-time';
  var chat = document.createElement('button'); chat.className = 'btn-sm hb-chat'; chat.id = 'help-banner-chat';
  chat.innerHTML = SUP_CHAT_ICON + '<span>' + TH('support.chat.button') + '</span>';
  var badge = document.createElement('span'); badge.className = 'hb-badge'; badge.id = 'help-banner-badge';
  chat.appendChild(badge);
  chat.addEventListener('click', function () { navigateTo('help'); });
  var stop = document.createElement('button'); stop.className = 'btn-sm help-danger'; stop.id = 'help-banner-stop'; stop.textContent = T('help.end');
  stop.addEventListener('click', helpEndSession);
  var rep = helpReportButton('help-banner-report', true);
  b.appendChild(dot); b.appendChild(text); b.appendChild(time); b.appendChild(chat); b.appendChild(rep); b.appendChild(stop);
  var ub = document.getElementById('update-banner');
  if (ub && ub.parentElement) ub.parentElement.insertBefore(b, ub.nextSibling);
  else document.body.appendChild(b);
  HELP.banner = b;
  return b;
}
function helpBanner() {
  var ph = helpPhase();
  var show = ph === 'session' || ph === 'starting';
  var b = HELP.banner;
  if (!show) { if (b) b.classList.remove('visible'); return; }
  b = helpBannerEl();
  b.classList.add('visible');
  var st = HELP.st, s = st.session || {};
  var name = helpOperatorName(st);
  var t = document.getElementById('help-banner-text');
  if (t) t.textContent = ph === 'starting' ? T('help.starting', { name: name })
    : T(s.perms && s.perms.control ? 'help.banner.control' : 'help.banner.view', { name: name });
  var tm = document.getElementById('help-banner-time');
  if (tm) tm.textContent = ph === 'session' ? helpFmtDur(s.seconds) : '';
  var ch = document.getElementById('help-banner-chat');
  if (ch) ch.style.display = ph === 'session' && st.chatOn ? '' : 'none';
  var rb = document.getElementById('help-banner-report');
  if (rb) rb.style.display = ph === 'session' && helpCanReport(st) ? '' : 'none';
  var badge = document.getElementById('help-banner-badge');
  if (badge) { badge.textContent = HELP.unread > 99 ? '99+' : String(HELP.unread); badge.classList.toggle('visible', HELP.unread > 0); }
}

// ── The acceptance window ──
function helpAskOpen(a) {
  if (HELP.ask && HELP.askSid === a.sid) { helpAskTick(a); return; }
  helpAskClose();
  HELP.askSid = a.sid; HELP.askArmed = false;
  var back = document.createElement('div'); back.className = 'help-ask-back'; back.id = 'help-ask-back';
  var box = document.createElement('div'); box.className = 'help-ask'; box.setAttribute('role', 'dialog'); box.setAttribute('aria-modal', 'true');
  var h = document.createElement('h2'); h.textContent = T('help.ask.title');
  var op = document.createElement('div'); op.className = 'help-ask-op';
  var name = document.createElement('div'); name.className = 'help-ask-name'; name.id = 'help-ask-name'; name.textContent = a.operator || T('help.someone');
  var tag = document.createElement('div'); tag.className = 'help-ask-tag'; tag.textContent = T('help.ask.unverified');
  var acct = document.createElement('div'); acct.id = 'help-ask-acct';
  acct.className = 'help-ask-acct' + (a.account ? '' : ' none');
  acct.textContent = a.account ? T('help.ask.account', { account: a.account }) : T('help.ask.noAccount');
  op.appendChild(name); op.appendChild(tag); op.appendChild(acct);
  var sasL = document.createElement('div'); sasL.className = 'help-ask-sas-label'; sasL.textContent = T('help.ask.sas');
  var sas = document.createElement('div'); sas.className = 'help-ask-sas'; sas.id = 'help-ask-sas'; sas.textContent = a.sas || '';
  var tg = document.createElement('div'); tg.className = 'help-ask-toggles';
  var mk = function (id, label, on) {
    var l = document.createElement('label');
    var c = document.createElement('input'); c.type = 'checkbox'; c.id = id; c.checked = !!on;
    var s = document.createElement('span'); s.textContent = T(label);
    l.appendChild(c); l.appendChild(s); tg.appendChild(l);
    return c;
  };
  var cCtl = mk('help-ask-control', 'help.ask.control', true);
  var cClip = mk('help-ask-clipboard', 'help.ask.clipboard', true);
  var cFiles = mk('help-ask-files', 'help.ask.files', false);
  var lim = document.createElement('div'); lim.className = 'help-note'; lim.textContent = T('help.ask.note');
  var rem = document.createElement('div'); rem.className = 'help-ask-remaining'; rem.id = 'help-ask-remaining';
  var acts = document.createElement('div'); acts.className = 'help-ask-actions';
  var refuse = document.createElement('button'); refuse.className = 'btn-ghost'; refuse.id = 'help-refuse'; refuse.textContent = T('help.ask.refuse');
  var accept = document.createElement('button'); accept.className = 'btn-primary'; accept.id = 'help-accept'; accept.textContent = T('help.ask.accept');
  accept.disabled = true; // armed after a moment: a click meant for something else cannot accept
  var sid = a.sid;
  refuse.addEventListener('click', function () { helpDecide(sid, false, {}); });
  accept.addEventListener('click', function () {
    if (!HELP.askArmed) return;
    helpDecide(sid, true, { control: !!cCtl.checked, clipboard: !!cClip.checked, files: !!cFiles.checked });
  });
  box.addEventListener('keydown', function (e) { if (e && e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); helpDecide(sid, false, {}); } });
  acts.appendChild(refuse); acts.appendChild(accept);
  box.appendChild(h); box.appendChild(op); box.appendChild(sasL); box.appendChild(sas); box.appendChild(tg); box.appendChild(lim); box.appendChild(rem); box.appendChild(acts);
  back.appendChild(box);
  document.body.appendChild(back);
  HELP.ask = back;
  HELP.askTimer = setTimeout(function () {
    if (HELP.askSid !== sid) return;
    HELP.askArmed = true; accept.disabled = false;
    try { refuse.focus(); } catch (e) {}
  }, HELP_ARM_MS);
  helpAskTick(a);
}
function helpAskTick(a) {
  var rem = document.getElementById('help-ask-remaining');
  if (rem) rem.textContent = T('help.ask.remaining', { s: Math.max(0, a.remaining | 0) });
}
function helpAskClose() {
  clearTimeout(HELP.askTimer);
  HELP.askSid = ''; HELP.askArmed = false;
  var back = HELP.ask;
  HELP.ask = null;
  if (back) {
    if (back.parentElement && back.parentElement.removeChild) back.parentElement.removeChild(back);
    else if (back.remove) back.remove();
  }
}
async function helpDecide(sid, accept, p) {
  helpAskClose();
  try {
    await helpFetch('POST', 'decide', { sid: sid, accept: !!accept, control: !!p.control, clipboard: !!p.clipboard, files: !!p.files });
  } catch (e) {}
  helpRefresh();
}

// ── Requests ──
// "Allow incoming support" on or off (ack: the anti-scam notice was read).
async function helpSetIncoming(on, ack) {
  HELP.err = '';
  var r = null;
  try { r = await helpFetch('POST', 'incoming', { on: !!on, ack: !!ack }); } catch (e) { r = null; }
  if (!r || !r.ok) { HELP.err = (r && r.data && r.data.error) || 'failed'; helpRefresh(); return; }
  helpApply(r.data);
}
async function helpScamAck() {
  var r = null;
  try { r = await helpFetch('POST', 'scam-ack', {}); } catch (e) { r = null; }
  if (r && r.ok) helpApply(r.data); else helpRefresh();
}
async function helpStop() { await helpSetIncoming(false, false); }
async function helpRetry() {
  try { await helpFetch('POST', 'retry', {}); } catch (e) {}
  helpRefresh();
}
async function helpEndSession() {
  try { await helpFetch('POST', 'end', {}); } catch (e) {}
  helpRefresh();
}
// A new one-time code (the same ID when the server allows it).
async function helpNewCode() {
  try { await helpFetch('POST', 'newcode', {}); } catch (e) {}
  helpRefresh();
}
// The last session's end notice, dismissed.
async function helpAckEnd() {
  try { await helpFetch('POST', 'ack', {}); } catch (e) {}
  helpRefresh();
}
async function helpSendChat(input) {
  var raw = String(input.value || '');
  if (supUtf8Len(raw) > SUP_CHAT_MAX_BYTES) { HELP.chatErr = T('support.chat.tooLong'); helpRenderChat(); return; }
  if (!supChatClean(raw)) return;
  var r = null;
  try { r = await helpFetch('POST', 'chat', { text: raw }); } catch (e) { r = null; }
  if (!r || !r.ok) { HELP.chatErr = helpErrText(r && r.data && r.data.error); helpRenderChat(); return; }
  HELP.chatErr = '';
  input.value = '';
  helpRefresh();
}
async function helpCopy(which) {
  var st = HELP.st || {};
  var v = which === 'code' ? st.code : st.id;
  if (!v) return;
  var ok = false;
  try { if (navigator.clipboard && navigator.clipboard.writeText) { await navigator.clipboard.writeText(v); ok = true; } } catch (e) {}
  if (!ok) return;
  HELP.copied = which;
  clearTimeout(HELP.copiedTimer);
  HELP.copiedTimer = setTimeout(function () {
    HELP.copied = '';
    if (currentRoute === 'help') { HELP.renderKey = ''; helpRender(); }
    if (typeof supDashRefresh === 'function') supDashRefresh(false);
  }, 1500);
  HELP.renderKey = '';
  helpRender();
  if (typeof supDashRefresh === 'function') supDashRefresh(false);
}

// ── The page ──
function renderHelpPage(pc) {
  var h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + TH('help.title') + '</h1><div class="breadcrumb">' + TH('help.subtitle') + '</div>';
  pc.appendChild(h);
  var body = document.createElement('div'); body.className = 'page-body';
  var wrap = document.createElement('div'); wrap.className = 'help-wrap'; wrap.id = 'help-wrap';
  body.appendChild(wrap);
  pc.appendChild(body);
  HELP.renderKey = '';
  HELP.unread = 0;
  helpRender();
  if (SUP.servers === null) supLoadServers().then(function () { if (currentRoute === 'help') { HELP.renderKey = ''; helpRender(); } });
  helpRefresh();
}
function helpWrap() { return (typeof currentRoute !== 'undefined' && currentRoute === 'help') ? document.getElementById('help-wrap') : null; }

// Drawn again only when what it shows changes (a field being typed in, the
// chat input, keep their focus); the times and the chat follow in place.
function helpRender() {
  var wrap = helpWrap();
  if (!wrap) return;
  var st = HELP.st || { phase: 'idle' };
  HELP.unread = 0;
  helpBanner();
  var key = [st.phase, st.id, st.code, st.notice, st.errKind, st.serverId, st.server, st.gen, HELP.err, HELP.copied, !!st.incoming, st.link || '',
    st.session ? JSON.stringify(st.session.perms) + st.session.path + st.session.lastFile : '', st.ended ? st.ended.reason + st.ended.seconds : '', helpReportKey(st)].join('|');
  if (key !== HELP.renderKey) {
    HELP.renderKey = key;
    wrap.innerHTML = '';
    switch (st.phase) {
      case 'connecting': helpRenderWait(wrap, T('help.connecting'), true); break;
      case 'unreachable': helpRenderWait(wrap, '', true); break;
      case 'waiting': case 'approval': helpRenderCreds(wrap, st); break;
      case 'starting': helpRenderWait(wrap, T('help.starting', { name: helpOperatorName(st) }), false); break;
      case 'session': helpRenderSession(wrap, st); break;
      default: helpRenderIdle(wrap, st);
    }
  }
  helpRenderLive(st);
}
function helpPanel(wrap) {
  var panel = document.createElement('div'); panel.className = 'panel';
  var pb = document.createElement('div'); pb.className = 'panel-body help-form';
  panel.appendChild(pb); wrap.appendChild(panel);
  return pb;
}
function helpScam() {
  var d = document.createElement('div'); d.className = 'help-scam'; d.id = 'help-scam';
  var i = document.createElement('span'); i.className = 'help-scam-icon'; i.innerHTML = HELP_WARN_ICON;
  var t = document.createElement('div');
  var b = document.createElement('b'); b.textContent = T('help.scam.title');
  var p = document.createElement('span'); p.textContent = T('help.scam.text');
  t.appendChild(b); t.appendChild(p);
  d.appendChild(i); d.appendChild(t);
  return d;
}
function helpNote(key, id) {
  var n = document.createElement('div'); n.className = 'help-note'; if (id) n.id = id; n.textContent = T(key);
  return n;
}
function helpStopButton() {
  var s = document.createElement('button'); s.className = 'btn-ghost'; s.id = 'help-stop'; s.textContent = T('help.stop');
  s.addEventListener('click', helpStop);
  return s;
}

// Off: what it is, the server it would use, "Allow incoming support".
function helpRenderIdle(wrap, st) {
  wrap.appendChild(helpScam());
  var pb = helpPanel(wrap);
  var off = document.createElement('div'); off.className = 'help-status'; off.id = 'help-off'; off.textContent = T('support.dash.off');
  pb.appendChild(off);
  if (st && st.server) pb.appendChild(helpServerLine(st));
  pb.appendChild(helpNote('help.limits', 'help-limits'));
  pb.appendChild(helpNote('help.privacy', 'help-privacy'));
  if (HELP.err) { var e = document.createElement('div'); e.className = 'sup-err visible'; e.id = 'help-err'; e.textContent = helpErrText(HELP.err); pb.appendChild(e); }
  var acts = document.createElement('div'); acts.className = 'help-actions';
  var go2 = document.createElement('button'); go2.className = 'btn-primary'; go2.id = 'help-go'; go2.textContent = T('support.dash.allow');
  go2.addEventListener('click', function () { helpSetIncoming(true, true); }); // (the notice is on screen: read)
  acts.appendChild(go2);
  pb.appendChild(acts);
}
function helpServerLine(st) {
  var sv = document.createElement('div'); sv.className = 'help-status'; sv.id = 'help-server-name';
  sv.textContent = T('help.server', { name: supServerLabel({ id: st.serverId, label: st.server }) });
  return sv;
}
// The last session's end, over the new code.
function helpEndedLine(st, onPage) {
  var e = st.ended || {};
  var d = document.createElement('div'); d.className = 'help-note help-ended'; d.id = onPage ? 'help-ended' : 'sdash-ended';
  var t = document.createElement('span');
  t.textContent = T('help.ended.title') + ' \u00B7 ' + T(HELP_ENDED[e.reason] || HELP_ENDED.lost) + ' ' + T('help.ended.duration', { time: helpFmtDur(e.seconds) });
  var ok = document.createElement('button'); ok.className = 'btn-sm'; ok.id = (onPage ? 'help' : 'sdash') + '-ended-ok'; ok.textContent = T('common.ok');
  ok.addEventListener('click', helpAckEnd);
  d.appendChild(t);
  // The helper can still be reported for a while (once).
  if (helpCanReport(st)) d.appendChild(helpReportButton((onPage ? 'help' : 'sdash') + '-ended-report', false));
  d.appendChild(ok);
  if (helpReportSent(st)) d.appendChild(helpReportDoneLine((onPage ? 'help' : 'sdash') + '-ended-reported'));
  return d;
}

function helpRenderWait(wrap, text, stoppable) {
  var pb = helpPanel(wrap);
  var w = document.createElement('div'); w.className = 'help-wait';
  var sp = document.createElement('div'); sp.className = 'reach-spinner';
  var t = document.createElement('div'); t.id = 'help-wait-text'; t.textContent = text;
  w.appendChild(sp); w.appendChild(t);
  if (stoppable) w.appendChild(helpStopButton());
  pb.appendChild(w);
}

var HELP_ICON_COPY = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V6a2 2 0 0 1 2-2h9"/></svg>';
var HELP_ICON_CHECK = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>';
function helpCred(label, value, which) {
  var c = document.createElement('div'); c.className = 'help-cred';
  var l = document.createElement('div'); l.className = 'help-cred-label'; l.textContent = T(label);
  var row = document.createElement('div'); row.className = 'help-cred-row';
  var v = document.createElement('div'); v.className = 'help-cred-value'; v.id = (HELP.credPrefix || 'help') + '-' + which; v.textContent = supFmt(value);
  var b = document.createElement('button'); b.id = (HELP.credPrefix || 'help') + '-copy-' + which;
  var done = HELP.copied === which;
  b.className = 'help-cred-copy' + (done ? ' copied' : '');
  b.title = T(done ? 'help.copied' : 'help.copy'); b.setAttribute('aria-label', b.title);
  b.innerHTML = done ? HELP_ICON_CHECK : HELP_ICON_COPY;
  b.addEventListener('click', function () { helpCopy(which); });
  row.appendChild(v); row.appendChild(b);
  c.appendChild(l); c.appendChild(row);
  return c;
}
function helpRenderCreds(wrap, st) {
  wrap.appendChild(helpScam());
  var pb = helpPanel(wrap);
  var creds = document.createElement('div'); creds.className = 'help-creds';
  creds.appendChild(helpCred('help.yourId', st.id || '', 'id'));
  creds.appendChild(helpCred('help.yourCode', st.code || '', 'code'));
  pb.appendChild(creds);
  var hint = document.createElement('div'); hint.className = 'help-note'; hint.textContent = T('help.readBoth');
  pb.appendChild(hint);
  var status = document.createElement('div'); status.className = 'help-status' + (st.notice && st.notice !== 'ended' ? ' warn' : ''); status.id = 'help-status';
  if (st.notice && st.notice !== 'ended') status.textContent = T(HELP_NOTICE[st.notice] || 'help.notice.setupFailed');
  else if (st.link === 'connecting') status.textContent = T('support.dash.reconnecting', { server: supServerLabel({ id: st.serverId, label: st.server }) });
  else { var dot = document.createElement('span'); dot.className = 'help-dot'; var tx = document.createElement('span'); tx.textContent = T('help.waiting'); status.appendChild(dot); status.appendChild(tx); }
  pb.appendChild(status);
  if (st.notice === 'ended' && st.ended) pb.appendChild(helpEndedLine(st, true));
  if (st.server) pb.appendChild(helpServerLine(st));
  var acts = document.createElement('div'); acts.className = 'help-actions';
  var nc = document.createElement('button'); nc.className = 'btn-sm'; nc.id = 'help-newcode'; nc.textContent = T('support.dash.newCode');
  nc.disabled = st.phase !== 'waiting';
  nc.addEventListener('click', helpNewCode);
  acts.appendChild(nc);
  acts.appendChild(helpStopButton());
  pb.appendChild(acts);
  pb.appendChild(helpNote('help.limits'));
}

function helpRenderSession(wrap, st) {
  var s = st.session || {};
  var pb = helpPanel(wrap);
  var t = document.createElement('div'); t.className = 'help-sess-title'; t.id = 'help-sess-title';
  t.textContent = T(s.perms && s.perms.control ? 'help.banner.control' : 'help.banner.view', { name: helpOperatorName(st) });
  pb.appendChild(t);
  var who = document.createElement('div'); who.className = 'help-status';
  who.textContent = s.account ? T('help.ask.account', { account: s.account }) : T('help.ask.unverified');
  pb.appendChild(who);
  var chips = document.createElement('div'); chips.className = 'help-chips'; chips.id = 'help-chips';
  [['control', 'help.perm.control'], ['clipboard', 'help.perm.clipboard'], ['files', 'help.perm.files']].forEach(function (p) {
    var on = !!(s.perms && s.perms[p[0]]);
    var c = document.createElement('span'); c.className = 'help-chip' + (on ? ' on' : '');
    c.textContent = T(p[1]) + ' · ' + T(on ? 'help.perm.on' : 'help.perm.off');
    chips.appendChild(c);
  });
  var path = supPathChip(s.path);
  if (path) { path.id = 'help-path'; chips.appendChild(path); }
  pb.appendChild(chips);
  var time = document.createElement('div'); time.className = 'help-status'; time.id = 'help-sess-time';
  pb.appendChild(time);
  if (s.lastFile) {
    var f = document.createElement('div'); f.className = 'help-note'; f.id = 'help-file';
    f.textContent = T('help.session.file', { name: s.lastFile });
    pb.appendChild(f);
  }
  pb.appendChild(helpNote('help.session.vault', 'help-vault'));
  var acts = document.createElement('div'); acts.className = 'help-actions';
  var end = document.createElement('button'); end.className = 'btn-sm help-danger'; end.id = 'help-end'; end.textContent = T('help.end');
  end.style.padding = '9px 18px';
  end.addEventListener('click', helpEndSession);
  acts.appendChild(end);
  if (helpCanReport(st)) acts.appendChild(helpReportButton('help-report', false));
  pb.appendChild(acts);
  if (helpReportSent(st)) pb.appendChild(helpReportDoneLine('help-reported'));
  if (st.chatOn) wrap.appendChild(helpChatPanel());
}
function helpChatPanel() {
  var p = document.createElement('div'); p.className = 'help-chat'; p.id = 'help-chat';
  var head = document.createElement('div'); head.className = 'help-chat-head'; head.textContent = T('help.chat.title');
  var list = document.createElement('div'); list.className = 'chat-messages'; list.id = 'help-chat-list';
  var note = document.createElement('div'); note.className = 'sup-chat-note'; note.textContent = T('support.chat.note');
  var err = document.createElement('div'); err.className = 'help-chat-err'; err.id = 'help-chat-err';
  var area = document.createElement('div'); area.className = 'chat-input-area';
  var row = document.createElement('div'); row.className = 'chat-input-row';
  var input = document.createElement('input'); input.type = 'text'; input.id = 'help-chat-input';
  input.placeholder = T('chat.placeholder'); input.setAttribute('maxlength', '4096'); input.setAttribute('autocomplete', 'off');
  var send = document.createElement('button'); send.className = 'chat-send-btn'; send.id = 'help-chat-send'; send.title = T('chat.send');
  send.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>';
  send.addEventListener('click', function () { helpSendChat(input); });
  input.addEventListener('keydown', function (e) { if (e && e.key === 'Enter') { if (e.preventDefault) e.preventDefault(); helpSendChat(input); } });
  row.appendChild(input); row.appendChild(send); area.appendChild(row);
  p.appendChild(head); p.appendChild(list); p.appendChild(note); p.appendChild(err); p.appendChild(area);
  return p;
}
function helpRenderChat() {
  var list = document.getElementById('help-chat-list');
  var err = document.getElementById('help-chat-err');
  if (err) { err.textContent = HELP.chatErr || ''; err.classList.toggle('visible', !!HELP.chatErr); }
  if (!list) return;
  if ((list.dataset.n | 0) > HELP.chat.length || list.dataset.gen !== String(HELP.gen)) { list.innerHTML = ''; list.dataset.n = '0'; list.dataset.gen = String(HELP.gen); }
  for (var i = list.dataset.n | 0; i < HELP.chat.length; i++) {
    var l = HELP.chat[i];
    var m = document.createElement('div'); m.className = 'chat-msg ' + (l.from === 'me' ? 'user' : 'op');
    var b = document.createElement('div'); b.className = 'bubble'; b.textContent = l.text;
    m.appendChild(b); list.appendChild(m);
  }
  list.dataset.n = String(HELP.chat.length);
  try { list.scrollTop = list.scrollHeight; } catch (e) {}
}

// What changes every poll, in place.
function helpRenderLive(st) {
  if (st.phase === 'unreachable') {
    var w = document.getElementById('help-wait-text');
    if (w) w.textContent = T(HELP_UNREACH[st.errKind] || HELP_UNREACH.unreachable, { s: st.retryIn | 0 });
    var rb = document.getElementById('help-retry');
    if ((!rb || !rb.parentElement) && w && w.parentElement) {
      rb = document.createElement('button'); rb.className = 'btn-sm'; rb.id = 'help-retry'; rb.textContent = T('support.dash.retry');
      rb.addEventListener('click', helpRetry);
      w.parentElement.appendChild(rb);
    }
  }
  if (st.phase === 'session') {
    var tm = document.getElementById('help-sess-time');
    if (tm) tm.textContent = T('help.session.duration', { time: helpFmtDur(st.session ? st.session.seconds : 0) });
    helpRenderChat();
  }
}

// ── Report this helper (receive_help_report.go) ──
// The action is offered while the session runs and for a while after its
// end (Go keeps the report token, in memory only), once per session, and
// never when the server gave no token (st.report absent). The dialog is the
// thin client's: a reason (one per line), a few lines, "End the session
// now" (on, while the session runs); then sending, failed (Try again), or
// sent with advice. The text only ever goes to Go in the POST.
var HELP_REPORT_REASONS = [['scam', 'help.report.reason.scam'], ['unwanted_access', 'help.report.reason.unwanted'],
  ['harassment', 'help.report.reason.harassment'], ['other', 'help.report.reason.other']];
var HELP_REPORT_ERR = { unreachable: 'help.report.err.unreachable', rateLimited: 'help.report.err.rateLimited',
  refused: 'help.report.err.refused', unavailable: 'help.report.err.unavailable', invalid: 'help.err.failed' };
var HELP_REPORT_MAX = 1000; // characters (wire.ReportMaxText)

function helpReportInfo(st) { st = st || HELP.st; return st && st.report && typeof st.report === 'object' ? st.report : null; }
function helpCanReport(st) { var r = helpReportInfo(st); return !!(r && r.available && !r.sent); }
function helpReportSent(st) { var r = helpReportInfo(st); return !!(r && r.sent); }
// What the views' render keys follow.
function helpReportKey(st) { var r = helpReportInfo(st); return r ? (r.available ? 'a' : '') + (r.sent ? 's' : '') : ''; }
function helpReportButton(id, short) {
  var b = document.createElement('button'); b.className = 'btn-sm help-report-btn'; b.id = id;
  b.textContent = T(short ? 'help.report.short' : 'help.report.open');
  if (short) { b.title = T('help.report.open'); b.setAttribute('aria-label', b.title); }
  b.addEventListener('click', helpReportOpen);
  return b;
}
function helpReportDoneLine(id) {
  var d = document.createElement('div'); d.className = 'help-report-done'; d.id = id; d.textContent = T('help.report.done');
  return d;
}
function helpRepRetryable(d) { return d.step === 'failed' && d.errKind !== 'refused' && d.errKind !== 'unavailable'; }
function helpRepEditable(d) { return d.step === 'editing' || d.step === 'failed'; }
function helpRepCanSubmit(d) { return helpRepEditable(d) && !!d.reason && (d.step !== 'failed' || helpRepRetryable(d)); }

function helpReportOpen() {
  if (HELP.rep) { try { HELP.rep.box.focus(); } catch (e) {} return; }
  var st = HELP.st || {};
  if (!helpCanReport(st)) return;
  var info = helpReportInfo(st);
  var d = { step: 'editing', reason: '', errKind: '', running: st.phase === 'session', end: true, reasons: [] };
  var back = document.createElement('div'); back.className = 'help-ask-back'; back.id = 'help-rep-back';
  var box = document.createElement('div'); box.className = 'help-ask help-rep'; box.id = 'help-rep';
  box.setAttribute('role', 'dialog'); box.setAttribute('aria-modal', 'true'); box.setAttribute('tabindex', '-1');
  d.back = back; d.box = box;
  // The form.
  var form = document.createElement('div'); form.className = 'help-rep-form'; form.id = 'help-rep-form';
  var h = document.createElement('h2'); h.textContent = T('help.report.title');
  var intro = document.createElement('div'); intro.className = 'help-rep-intro'; intro.id = 'help-rep-intro';
  intro.textContent = T('help.report.intro', { name: supServerLabel({ id: st.serverId, label: st.server }) });
  var who = document.createElement('div'); who.className = 'help-ask-op';
  var whoName = document.createElement('div'); whoName.className = 'help-ask-name'; whoName.id = 'help-rep-who';
  var name = (info && info.operator) || helpOperatorName(st);
  if (info && info.account) name += ' (' + info.account + ')';
  whoName.textContent = T('help.report.who', { name: name });
  who.appendChild(whoName);
  var rl = document.createElement('div'); rl.className = 'help-rep-label'; rl.textContent = T('help.report.reason');
  var reasons = document.createElement('div'); reasons.className = 'help-rep-reasons'; reasons.setAttribute('role', 'radiogroup');
  HELP_REPORT_REASONS.forEach(function (r, i) {
    var b = document.createElement('button'); b.className = 'help-rep-reason'; b.id = 'help-rep-reason-' + i;
    b.setAttribute('role', 'radio'); b.textContent = T(r[1]);
    b.addEventListener('click', function () { if (!helpRepEditable(d)) return; d.reason = r[0]; helpReportRender(); });
    d.reasons.push(b); reasons.appendChild(b);
  });
  var tl = document.createElement('div'); tl.className = 'help-rep-intro'; tl.textContent = T('help.report.details');
  var ta = document.createElement('textarea'); ta.className = 'help-rep-text'; ta.id = 'help-rep-text';
  ta.setAttribute('maxlength', String(HELP_REPORT_MAX)); ta.setAttribute('rows', '4'); ta.setAttribute('spellcheck', 'true');
  ta.setAttribute('aria-label', T('help.report.details'));
  d.ta = ta;
  var endRow = document.createElement('label'); endRow.className = 'help-rep-end'; endRow.id = 'help-rep-end-row';
  var endCb = document.createElement('input'); endCb.type = 'checkbox'; endCb.id = 'help-rep-end'; endCb.checked = true;
  endCb.addEventListener('change', function () { d.end = !!endCb.checked; });
  var endTx = document.createElement('span'); endTx.textContent = T('help.report.endNow');
  endRow.appendChild(endCb); endRow.appendChild(endTx);
  d.endRow = endRow; d.endCb = endCb;
  var status = document.createElement('div'); status.className = 'help-rep-status'; status.id = 'help-rep-status'; status.setAttribute('role', 'status');
  d.status = status;
  var acts = document.createElement('div'); acts.className = 'help-ask-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'help-rep-cancel'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', helpReportClose);
  var send = document.createElement('button'); send.className = 'btn-primary'; send.id = 'help-rep-send';
  send.addEventListener('click', helpReportSend);
  var fin = document.createElement('button'); fin.className = 'btn-primary'; fin.id = 'help-rep-final'; fin.textContent = T('common.close');
  fin.addEventListener('click', helpReportClose);
  acts.appendChild(cancel); acts.appendChild(send); acts.appendChild(fin);
  d.cancel = cancel; d.send = send; d.fin = fin;
  [h, intro, who, rl, reasons, tl, ta, endRow, status, acts].forEach(function (x) { form.appendChild(x); });
  // Sent: the confirmation and the advice.
  var done = document.createElement('div'); done.className = 'help-rep-form'; done.id = 'help-rep-sent';
  var ok = document.createElement('div'); ok.className = 'help-rep-ok'; ok.textContent = T('help.report.sent.title');
  var body = document.createElement('div'); body.className = 'help-rep-intro'; body.textContent = T('help.report.sent.body');
  var adv = document.createElement('div'); adv.className = 'help-rep-advice'; adv.id = 'help-rep-advice';
  var advT = document.createElement('b'); advT.textContent = T('help.report.advice.title');
  var advX = document.createElement('span'); advX.textContent = T('help.report.advice');
  adv.appendChild(advT); adv.appendChild(advX);
  var dacts = document.createElement('div'); dacts.className = 'help-ask-actions';
  var close = document.createElement('button'); close.className = 'btn-primary'; close.id = 'help-rep-close'; close.textContent = T('common.close');
  close.addEventListener('click', helpReportClose);
  dacts.appendChild(close);
  [ok, body, adv, dacts].forEach(function (x) { done.appendChild(x); });
  d.form = form; d.done = done; d.close = close;
  box.appendChild(form); box.appendChild(done);
  box.addEventListener('keydown', function (e) { if (e && e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); helpReportClose(); } });
  back.appendChild(box);
  document.body.appendChild(back);
  HELP.rep = d;
  helpReportRender();
  try { d.reasons[0].focus(); } catch (e) {}
}
// The dialog follows its step, in place (the text being typed stays).
function helpReportRender() {
  var d = HELP.rep;
  if (!d) return;
  var sent = d.step === 'sent', edit = helpRepEditable(d);
  d.form.style.display = sent ? 'none' : '';
  d.done.style.display = sent ? '' : 'none';
  if (sent) { try { d.close.focus(); } catch (e) {} return; }
  HELP_REPORT_REASONS.forEach(function (r, i) {
    var b = d.reasons[i], on = d.reason === r[0];
    b.className = 'help-rep-reason' + (on ? ' on' : '');
    b.setAttribute('aria-checked', on ? 'true' : 'false');
    b.disabled = !edit;
  });
  d.ta.readOnly = !edit;
  d.endRow.style.display = d.running ? '' : 'none';
  d.endCb.disabled = !edit;
  var st = '', cls = 'help-rep-status';
  if (d.step === 'sending') { st = T('help.report.sending'); cls += ' warn'; }
  else if (d.step === 'failed') { st = T(HELP_REPORT_ERR[d.errKind] || HELP_REPORT_ERR.unreachable); cls += ' err'; }
  d.status.textContent = st; d.status.className = cls;
  var final = d.step === 'failed' && !helpRepRetryable(d);
  d.cancel.style.display = final ? 'none' : '';
  d.send.style.display = final ? 'none' : '';
  d.fin.style.display = final ? '' : 'none';
  d.cancel.disabled = d.step === 'sending';
  d.send.textContent = T(d.step === 'failed' ? 'help.report.retry' : 'help.report.send');
  d.send.disabled = !helpRepCanSubmit(d);
}
function helpReportClose() {
  var d = HELP.rep;
  HELP.rep = null;
  if (d && d.back) {
    if (d.back.parentElement && d.back.parentElement.removeChild) d.back.parentElement.removeChild(d.back);
    else if (d.back.remove) d.back.remove();
  }
}
// The state moved: the option to end goes with the session; a form not
// sent closes once the report is no longer offered.
function helpReportFollow(st) {
  var d = HELP.rep;
  if (!d) return;
  if (d.running && st.phase !== 'session') d.running = false;
  if (helpRepEditable(d) && !helpCanReport(st)) { helpReportClose(); return; }
  helpReportRender();
}
async function helpReportSend() {
  var d = HELP.rep;
  if (!d || !helpRepCanSubmit(d)) return;
  var text = Array.from(String(d.ta.value || '')).slice(0, HELP_REPORT_MAX).join('');
  var end = d.running && d.end;
  d.step = 'sending'; d.errKind = '';
  helpReportRender();
  var r = null;
  try { r = await helpFetch('POST', 'report', { reason: d.reason, text: text, end: !!end }); } catch (e) { r = null; }
  var kind = '';
  if (!r) kind = 'unreachable';
  else if (!r.ok) kind = r.status === 400 ? 'invalid' : 'unreachable';
  else if (r.data.ok !== true) kind = HELP_REPORT_ERR[r.data.kind] ? r.data.kind : 'unreachable';
  if (HELP.rep === d) {
    if (kind) { d.step = 'failed'; d.errKind = kind; } else d.step = 'sent';
    helpReportRender();
  }
  helpRefresh();
}

helpInit();
`
