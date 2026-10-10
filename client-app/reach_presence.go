package main

// reachPresenceCSS / reachPresenceJS: two session-toolbar additions,
// injected into the page like reachViewerJS (strings.Replace: no
// backticks, no closing script tag).
//
// 1. Presence — other operators on the same machine. The Obliance relay of
//    the server that carries the machine (multi-server: each session's own
//    server) pushes {type:'presence', v:1, self, operators:[{id, name,
//    client:'web'|'desktop', since, control, blank, sameUser}], event?} to
//    every viewer of the device whenever the set changes (no polling). This
//    viewer answers once, then on each view-only change, {type:
//    'presence_state', client:'desktop', viewOnly} (the relay keeps it; the
//    agent never sees it). Shown: a badge in the toolbar (hidden while alone)
//    with the list on hover / click, and a toast when someone arrives. Only
//    name, client, since, control / view only and blank screen are known —
//    never an e-mail or an address. Per session tab; the toolbar follows the
//    tab on screen. An older server sends nothing: nothing is shown.
//
// 2. Blank screen — "hide the remote screen" (TeamViewer's black screen),
//    gated on init.features 'blank_screen' (REACH.FEATURE.BLANK_SCREEN);
//    init.blankScreen {available, reason} says why it is not offered (the
//    button is then disabled with the reason in its tooltip):
//      viewer -> agent {type:'set_blank_screen', on}
//      agent -> viewer {type:'blank_screen_status', active, reason?}
//    Turning it on also blocks the remote user's mouse and keyboard where the
//    agent can (input_block_local; the page is click-through), turning it off
//    releases that block. The state shown is the agent's. Not in view only.
//    Never asked again after a reconnection (the agent removes the page with
//    its stream): the operator is told.

const reachPresenceCSS = `
.reach-presence{position:relative;display:none}
.reach-presence.visible{display:inline-flex}
.reach-presence > button{display:inline-flex;align-items:center;gap:4px;background:rgb(var(--warn-rgb) / .16);color:var(--warn)}
.reach-presence > button:hover{background:rgb(var(--warn-rgb) / .24)}
.reach-presence.blank > button{background:rgb(var(--info-rgb) / .16);color:var(--info)}
.reach-presence-pop{display:none;position:absolute;top:calc(100% + 6px);right:0;z-index:60;width:270px;max-height:320px;overflow-y:auto;background:var(--bg2);border-radius:10px;box-shadow:0 8px 24px var(--shadow);padding:8px}
.reach-presence.open .reach-presence-pop{display:block}
html[dir=rtl] .reach-presence-pop{right:auto;left:0}
.reach-presence-pop .rp-title{font-size:10px;font-weight:700;letter-spacing:.6px;text-transform:uppercase;color:var(--muted2);padding:2px 4px 6px}
.reach-presence-pop .rp-item{padding:6px 8px;border-radius:7px;background:rgb(var(--ink) / .05);margin-top:4px}
.reach-presence-pop .rp-name{font-size:12px;font-weight:600;color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.reach-presence-pop .rp-you{font-size:10px;font-weight:400;color:var(--muted2);margin-left:4px}
.reach-presence-pop .rp-meta{display:flex;flex-wrap:wrap;gap:4px 10px;margin-top:2px;font-size:11px;color:var(--muted)}
.reach-presence-pop .rp-blank{color:var(--info)}
.btn-sm.reach-blank-btn.on{background:rgb(var(--info-rgb) / .18);color:var(--info)}
.btn-sm.reach-blank-btn:disabled{opacity:.4;cursor:default}
`

const reachPresenceJS = `
// ── Presence + blank screen (reach_presence.go) ────────────────────────────
var REACH_PRESENCE_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>';
var REACH_BLANK_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17 17H4a2 2 0 0 1-2-2V5c0-1.5 1-2 1-2"/><path d="M22 15V5a2 2 0 0 0-2-2H9"/><path d="M8 21h8"/><path d="M12 17v4"/><line x1="2" y1="2" x2="22" y2="22"/></svg>';
// Why the blank screen is not offered (init.blankScreen.reason) -> catalog key.
var REACH_BLANK_UNAVAIL_TEXT = { os_version: 'viewer.blank.osVersion', platform: 'viewer.blank.platform', no_user: 'viewer.blank.noUser' };
// Why it went off (blank_screen_status.reason) -> catalog key.
var REACH_BLANK_OFF_TEXT = {
  local_user: 'viewer.blank.offLocalUser', session_changed: 'viewer.blank.offSessionChanged', privacy: 'viewer.blank.privacy',
  os_version: 'viewer.blank.osVersion', no_user: 'viewer.blank.noUser', failed: 'viewer.blank.failed', watchdog: 'viewer.blank.watchdog',
  platform: 'viewer.blank.platform', unsupported: 'viewer.blank.oldAgent',
};

// Per connection (reachResetConnState).
function reachPresenceReset(tab) {
  tab.presence = null;
  tab.presenceAck = false;
  tab.blankActive = false;
  tab.blankUnavail = 'old_agent';
  tab.blankBlockedInput = false;
}

// ── Presence ──
function reachParsePresence(msg) {
  if (!msg || msg.type !== 'presence' || typeof msg.self !== 'string' || !Array.isArray(msg.operators)) return null;
  var ops = [];
  msg.operators.slice(0, 50).forEach(function (o) {
    if (!o || typeof o.id !== 'string' || typeof o.name !== 'string') return;
    ops.push({ id: o.id, name: o.name.slice(0, 80), client: o.client === 'desktop' ? 'desktop' : 'web', since: typeof o.since === 'string' ? o.since : '',
      control: typeof o.control === 'boolean' ? o.control : null, blank: o.blank === true, sameUser: o.sameUser === true });
  });
  var ev = (msg.event && typeof msg.event.id === 'string' && /^(join|leave|update)$/.test(msg.event.kind)) ? { kind: msg.event.kind, id: msg.event.id } : null;
  return { self: msg.self, operators: ops, event: ev };
}
function reachPresenceOthers(tab) {
  var p = tab && tab.presence;
  return p ? p.operators.filter(function (o) { return o.id !== p.self; }) : [];
}
function reachOnPresence(tab, msg) {
  var p = reachParsePresence(msg);
  if (!p) return;
  if (!tab.presenceAck) {
    // The relay speaks presence: say which client this is and its mode.
    tab.presenceAck = true;
    reachSend(tab, { type: 'presence_state', client: 'desktop', viewOnly: !!tab.viewOnly });
  }
  tab.presence = p;
  if (p.event && p.event.kind === 'join' && p.event.id !== p.self) {
    var who = null;
    p.operators.forEach(function (o) { if (o.id === p.event.id) who = o; });
    if (who && !who.sameUser && reachUI(tab)) {
      reachToast(T(who.client === 'desktop' ? 'viewer.presence.joinedDesktop' : 'viewer.presence.joinedWeb', { name: who.name }), 'warn');
    }
  }
  reachRefreshPresence(tab);
}
// The view-only choice, for the other operators (once the relay spoke presence).
function reachPresenceViewOnly(tab) {
  if (tab && tab.presenceAck) reachSend(tab, { type: 'presence_state', client: 'desktop', viewOnly: !!tab.viewOnly });
}
function reachPresenceSince(iso) {
  var at = Date.parse(iso);
  if (!isFinite(at)) return '';
  var min = Math.max(0, Math.floor((Date.now() - at) / 60000));
  if (min < 1) return T('viewer.presence.sinceNow');
  var d = min < 60 ? T('viewer.presence.sinceMin', { n: min }) : T('viewer.presence.sinceHour', { h: Math.floor(min / 60), m: min % 60 });
  return T('viewer.presence.since', { d: d });
}
function reachRenderPresenceList(tab) {
  var pop = document.getElementById('reach-presence-pop');
  if (!pop) return;
  var others = reachPresenceOthers(tab);
  var h = '<div class="rp-title">' + TH('viewer.presence.listTitle') + '</div>';
  others.forEach(function (o) {
    var meta = [TH(o.client === 'desktop' ? 'viewer.presence.desktop' : 'viewer.presence.web')];
    var since = reachPresenceSince(o.since);
    if (since) meta.push(i18nEsc(since));
    if (o.control === true) meta.push(TH('viewer.presence.control'));
    if (o.control === false) meta.push(TH('viewer.presence.viewOnly'));
    if (o.blank) meta.push('<span class="rp-blank">' + TH('viewer.presence.blank') + '</span>');
    h += '<div class="rp-item"><div class="rp-name" title="' + i18nEsc(o.name) + '">' + i18nEsc(o.name) +
      (o.sameUser ? '<span class="rp-you">' + TH('viewer.presence.you') + '</span>' : '') + '</div>' +
      '<div class="rp-meta">' + meta.map(function (m) { return '<span>' + m + '</span>'; }).join('') + '</div></div>';
  });
  pop.innerHTML = h;
}
// Badge of the session on screen (called by reachRefreshToolbar too).
function reachRefreshPresence(tab) {
  if (!reachUI(tab)) return;
  var wrap = document.getElementById('reach-presence');
  if (!wrap) return;
  var live = !!(tab.ws && tab.gotInit);
  var others = live ? reachPresenceOthers(tab) : [];
  wrap.classList.toggle('visible', others.length > 0);
  wrap.classList.toggle('blank', others.some(function (o) { return o.blank; }));
  if (!others.length) wrap.classList.remove('open');
  var b = document.getElementById('reach-presence-btn');
  if (b) {
    b.innerHTML = REACH_PRESENCE_ICON + '<span>' + others.length + '</span>';
    b.title = T('viewer.presence.badgeTitle', { names: others.map(function (o) { return o.name; }).join(', ') });
  }
  if (wrap.classList.contains('open')) reachRenderPresenceList(tab);
}
function reachBuildPresenceTool(toolbar) {
  var wrap = document.createElement('div'); wrap.className = 'reach-presence'; wrap.id = 'reach-presence';
  var b = document.createElement('button'); b.className = 'btn-sm'; b.id = 'reach-presence-btn'; b.type = 'button';
  b.innerHTML = REACH_PRESENCE_ICON + '<span>0</span>';
  var pop = document.createElement('div'); pop.className = 'reach-presence-pop'; pop.id = 'reach-presence-pop'; pop.setAttribute('role', 'dialog');
  var open = function (on) {
    var tab = reachDisplayedTab();
    if (on && tab) reachRenderPresenceList(tab);
    wrap.classList.toggle('open', !!on && !!tab);
  };
  b.addEventListener('click', function () { open(!wrap.classList.contains('open')); });
  wrap.addEventListener('mouseenter', function () { open(true); });
  wrap.addEventListener('mouseleave', function () { open(false); });
  wrap.appendChild(b); wrap.appendChild(pop);
  toolbar.appendChild(wrap);
}

// ── Blank screen ──
function reachBlankUnavailOf(info) {
  var f = Array.isArray(info && info.features) ? info.features : [];
  if (f.indexOf(REACH.FEATURE.BLANK_SCREEN) >= 0) return null;
  var r = info && info.blankScreen && info.blankScreen.reason;
  return (r === 'os_version' || r === 'platform' || r === 'no_user') ? r : 'old_agent';
}
function reachBlankOnInit(tab, info) {
  tab.blankUnavail = reachBlankUnavailOf(info);
  reachRefreshBlank(tab);
}
function reachBlankUnavailText(u) { return T(REACH_BLANK_UNAVAIL_TEXT[u] || 'viewer.blank.oldAgent'); }
function reachToggleBlank(tab) {
  if (!tab || !tab.ws || !tab.gotInit || tab.blankUnavail || tab.viewOnly) return false;
  var on = !tab.blankActive;
  if (!reachSend(tab, { type: 'set_blank_screen', on: on })) return false;
  if (on && reachCanBlockInput(tab) && !tab.inputBlocked) {
    // The page is click-through: the local user would click blind.
    reachSend(tab, { type: 'set_input_block', block: true });
    tab.blankBlockedInput = true;
  } else if (!on && tab.blankBlockedInput) {
    reachSend(tab, { type: 'set_input_block', block: false });
    tab.blankBlockedInput = false;
    tab.wantInputBlock = false;
  }
  return true;
}
function reachOnBlankStatus(tab, msg) {
  var on = msg.active === true && !tab.blankUnavail;
  var was = !!tab.blankActive;
  var reason = typeof msg.reason === 'string' ? msg.reason : '';
  tab.blankActive = on;
  if (!on && tab.blankBlockedInput && reason !== 'operator') {
    // Refused or lifted on the remote: the block taken with it goes too.
    reachSend(tab, { type: 'set_input_block', block: false });
    tab.blankBlockedInput = false;
    tab.wantInputBlock = false;
  }
  reachRefreshBlank(tab);
  if (typeof renderSessionTabs === 'function') renderSessionTabs();
  if (!reachUI(tab)) return;
  if (on && !was) reachToast(T('viewer.blank.on'), 'ok');
  else if (!on && (was || (reason && reason !== 'operator'))) {
    reachToast(T(REACH_BLANK_OFF_TEXT[reason] || 'viewer.blank.off'), (!reason || reason === 'operator') ? 'ok' : 'warn');
  }
}
// The tunnel closed: the agent removed the page with its stream.
function reachBlankConnClosed(tab) {
  if (tab.blankActive && !tab.userStopped && reachUI(tab)) reachToast(T('viewer.blank.offConnection'), 'warn');
  tab.blankActive = false;
  tab.blankBlockedInput = false;
}
function reachRefreshBlank(tab) {
  if (!reachUI(tab)) return;
  var b = document.getElementById('reach-blank-btn');
  if (!b) return;
  var live = !!(tab.ws && tab.gotInit);
  var on = live && !!tab.blankActive;
  b.disabled = !live || !!tab.blankUnavail || !!tab.viewOnly;
  b.classList.toggle('on', on);
  b.setAttribute('aria-pressed', on ? 'true' : 'false');
  b.innerHTML = REACH_BLANK_ICON + '<span>' + TH(on ? 'viewer.blank.shortActive' : 'viewer.blank.short') + '</span>';
  b.title = tab.blankUnavail ? reachBlankUnavailText(tab.blankUnavail)
    : tab.viewOnly ? T('viewer.blank.viewOnly')
    : on ? T('viewer.blank.titleActive') : T('viewer.blank.title');
}
function reachBuildBlankTool(toolbar) {
  var b = document.createElement('button');
  b.className = 'btn-sm reach-ico-btn reach-blank-btn'; b.id = 'reach-blank-btn'; b.disabled = true;
  b.title = T('viewer.blank.title'); b.innerHTML = REACH_BLANK_ICON + '<span>' + TH('viewer.blank.short') + '</span>';
  b.addEventListener('click', function () { reachToggleBlank(reachDisplayedTab()); });
  toolbar.appendChild(b);
}
`
