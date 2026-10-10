package main

// ui_platform.go — what the page gets on one platform only, injected after
// the common blocks (ui.go buildUI): nothing on Windows (its page stays
// exactly what it was), the Linux pieces on Linux:
//   - LOCAL_OS = 'unix': the operator's own files are POSIX paths (the local
//     pane: local_pane.go; the roots come from localfs_linux.go);
//   - the RDP tab's stream mode (rdp_stream_view.go);
//   - "Receive help" on Linux (receive_help_linux.go): its notes and
//     settings (helpLinuxJS below).
// uiTargetOS lets a test build the page of another platform.

import (
	"runtime"
	"strings"
)

// platformLateJS: a platform's script run after the main one (it wraps
// functions the main script declares): Linux's update banner (Flatpak) and
// Settings > About (Cisco's notice).
func platformLateJS(goos string) string {
	if goos == "linux" {
		return linuxLateJS
	}
	return ""
}

// injectLateJS: the late script, after the main one ("" leaves the page as
// it is).
func injectLateJS(html, js string) string {
	if js == "" {
		return html
	}
	return strings.Replace(html, "</script>\n</body>", "</script>\n<script>"+js+"</script>\n</body>", 1)
}

// linuxLateJS: the update banner on Linux (updater_linux.go): the version is
// the version route's "linux" field (Obliance oblireachDesktopLinux
// .controller.ts); "Update now" asks the Flatpak portal; outside the Flatpak
// the banner only says how to update; after the update, "restart
// Oblireach". Settings > About gets Cisco's notice once OpenH264 was
// downloaded (help_linux_page.go). No backtick here.
const linuxLateJS = `
// ── Linux (ui_platform.go) ──
var LINUX_UPDATE = { mode: '', notice: null };
function linuxUpdateMode() {
  return reachNative('reachUpdateState').then(function (s) { LINUX_UPDATE.mode = s && s.code === 'manual' ? 'manual' : ''; }, function () { LINUX_UPDATE.mode = 'manual'; })
    .then(function () { renderUpdateBanner(); });
}
checkForUpdate = async function () {
  try {
    const pid = updatePid();
    const r = await fetch((pid === (activeServerId || '') ? '/proxy' : '/sp/' + pid) + '/api/oblireach-desktop/version');
    if (!r.ok) return false;
    const d = await r.json();
    const info = d.data || d;
    const l = info && info.linux;
    if (!l || typeof l.version !== 'string' || !l.version) return false;
    updateInfo = { version: l.version, releaseNotes: '', downloadUrl: null, _pid: pid, linux: l };
    if (!LINUX_UPDATE.asked) { LINUX_UPDATE.asked = true; linuxUpdateMode(); }
    renderUpdateBanner();
    return cmpSemver(l.version, window.__reach_version || '0.0.0') > 0;
  } catch (e) { return false; }
};
window.__reachOnUpdate = (function (orig) {
  return function (s) {
    if (s && s.state === 'idle' && s.result && (s.code === 'restart' || s.code === 'uptodate')) {
      if (updateRun && (Number(s.seq) || 0) < (Number(updateRun.seq) || 0)) return;
      updateRun = null;
      updateResultShown = Object.assign({}, s.result, { version: s.result.version || (updateInfo && updateInfo.version) || '', linuxCode: s.code });
      renderUpdateBanner();
      return;
    }
    orig(s);
  };
})(window.__reachOnUpdate);
renderUpdateBanner = (function (orig) {
  return function () {
    orig();
    const title = document.getElementById('ub-title'), sub = document.getElementById('ub-sub'), go = document.getElementById('ub-update');
    if (!title || !sub || !go) return;
    const r = updateResultShown;
    if (r && r.linuxCode && !updateRun) {
      if (r.linuxCode === 'uptodate') title.textContent = T('update.linux.upToDate');
      sub.textContent = i18nMsgText(r.text) || r.message || '';
      return;
    }
    if (!updateRun && !r && updateAvailable() && LINUX_UPDATE.mode === 'manual') {
      go.classList.add('hide');
      sub.textContent = T('update.linux.manualSub', { command: 'flatpak update tools.obli.Oblireach' });
    }
  };
})(renderUpdateBanner);
renderAboutSection = (function (orig) {
  return function (sb) {
    orig(sb);
    helpFetch('GET', 'linux').then(function (res) {
      const v = res && res.ok ? res.data : null;
      if (!v || !v.openh264 || v.inFlatpak || !v.notice) return;
      const p = document.createElement('div'); p.className = 'panel'; p.id = 'about-openh264';
      const h = document.createElement('div'); h.className = 'panel-header'; h.textContent = T('help.linux.codec');
      const b = document.createElement('div'); b.className = 'panel-body muted-hint'; b.textContent = String(v.notice);
      p.appendChild(h); p.appendChild(b); sb.appendChild(p);
    }).catch(function () {});
  };
})(renderAboutSection);
`

var uiTargetOS = runtime.GOOS

func platformPageCSS(goos string) string {
	if goos == "linux" {
		return rdpStreamCSS
	}
	return ""
}

func platformPageJS(goos string) string {
	if goos == "linux" {
		return "\n// ── Linux (ui_platform.go) ──\nLOCAL_OS = 'unix';\n" + rdpStreamJS + helpLinuxJS
	}
	return ""
}
