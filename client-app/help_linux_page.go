package main

// help_linux_page.go — "Receive help" on Linux in the page (Go:
// receive_help_linux.go), injected on Linux only (ui_platform.go):
//   - Settings > Support gets a "Screen sharing on Linux" panel: "Remember
//     the screen sharing permission" (the desktop's own dialog, not
//     Oblireach's acceptance) and, outside the Flatpak, the video codec
//     (Cisco's OpenH264, downloaded on demand with Cisco's notice);
//   - Settings > About shows Cisco's notice once the codec is there (its
//     license asks for it wherever the downloaded codec is used): the main
//     script's renderAboutSection, wrapped by ui_platform.go's late script.
// The texts that differ on Linux (the limits of a helped session: no screen
// curtain, the desktop's dialog...) are the catalog's linux.* keys
// (i18nPlatformOverlay). Both render functions are wrapped, not redeclared.
// Injected verbatim; no backtick here.

const helpLinuxJS = `
// ── Receive help on Linux (help_linux_page.go) ──────────────────────────────
var HELP_LINUX = { v: null, timer: null };
function helpLinuxLoad() {
  return helpFetch('GET', 'linux').then(function (r) { if (r.ok) HELP_LINUX.v = r.data; return HELP_LINUX.v; }).catch(function () { return null; });
}
function helpLinuxRow(pb, titleKey, subText, id) {
  var row = document.createElement('div'); row.className = 'setting-row'; if (id) row.id = id;
  var lbl = document.createElement('div'); lbl.className = 'setting-label';
  var t = document.createElement('div'); t.className = 'title'; t.textContent = T(titleKey);
  var s = document.createElement('div'); s.className = 'sub'; s.textContent = subText;
  lbl.appendChild(t); lbl.appendChild(s); row.appendChild(lbl);
  var ctl = document.createElement('div'); ctl.className = 'setting-control'; row.appendChild(ctl);
  pb.appendChild(row);
  return { row: row, sub: s, ctl: ctl };
}
function helpLinuxRender(pb) {
  var v = HELP_LINUX.v;
  pb.innerHTML = '';
  if (!v) { var e = document.createElement('div'); e.className = 'muted-hint'; e.textContent = T('support.err.storage'); pb.appendChild(e); return; }
  var r1 = helpLinuxRow(pb, 'help.linux.remember', T('help.linux.rememberSub'), 'help-linux-remember-row');
  var cb = document.createElement('input'); cb.type = 'checkbox'; cb.id = 'help-linux-remember';
  cb.style.cssText = 'accent-color:var(--accent);width:16px;height:16px;cursor:pointer';
  cb.checked = !!v.rememberScreenShare; cb.disabled = !v.canRemember; cb.title = T('help.linux.remember');
  cb.addEventListener('change', function () {
    cb.disabled = true;
    helpFetch('POST', 'linux', { rememberScreenShare: cb.checked }).then(function (r) {
      if (r.ok) HELP_LINUX.v = r.data; else reachNotify(T('support.err.storage'), 'warn');
      helpLinuxRender(pb);
    });
  });
  r1.ctl.appendChild(cb);
  var state = v.inFlatpak ? T('help.linux.codecFlatpak') : v.openh264 ? T('help.linux.codecInstalled') : v.fetching ? T('help.linux.codecFetching')
    : v.fetchError ? T('help.linux.codecFailed') : T('help.linux.codecSub');
  var r2 = helpLinuxRow(pb, 'help.linux.codec', state, 'help-linux-codec-row');
  if (v.canFetchOpenh264) {
    var b = document.createElement('button'); b.className = 'btn-sm'; b.id = 'help-linux-codec-fetch';
    b.textContent = T('help.linux.codecFetch'); b.disabled = !!v.fetching;
    b.addEventListener('click', function () {
      b.disabled = true;
      helpFetch('POST', 'openh264', {}).then(function (r) { if (r.ok) HELP_LINUX.v = r.data; helpLinuxRender(pb); helpLinuxWatch(pb); });
    });
    r2.ctl.appendChild(b);
  }
  // (Cisco's attribution: shown where the download is offered and once it is there)
  if (!v.inFlatpak && v.notice) { var n = document.createElement('div'); n.className = 'muted-hint'; n.id = 'help-linux-notice'; n.textContent = String(v.notice); pb.appendChild(n); }
}
// The download's end, polled while it runs (the page on screen).
function helpLinuxWatch(pb) {
  if (HELP_LINUX.timer) clearTimeout(HELP_LINUX.timer);
  HELP_LINUX.timer = setTimeout(function () {
    HELP_LINUX.timer = null;
    if (!document.getElementById('help-linux-panel')) return;
    helpLinuxLoad().then(function (v) { helpLinuxRender(pb); if (v && v.fetching) helpLinuxWatch(pb); });
  }, 1500);
}
renderSupportSection = (function (orig) {
  return function (sb) {
    orig(sb);
    var panel = document.createElement('div'); panel.className = 'panel'; panel.id = 'help-linux-panel';
    var head = document.createElement('div'); head.className = 'panel-header'; head.textContent = T('help.linux.title');
    var pb = document.createElement('div'); pb.className = 'panel-body';
    pb.innerHTML = '<div class="muted-hint" style="padding:14px">' + TH('common.loading') + '</div>';
    panel.appendChild(head); panel.appendChild(pb); sb.appendChild(panel);
    helpLinuxLoad().then(function (v) { helpLinuxRender(pb); if (v && v.fetching) helpLinuxWatch(pb); });
  };
})(renderSupportSection);
// (Settings > About: the main script's, wrapped by ui_platform.go's late block)
`
