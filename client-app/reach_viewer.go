package main

// reachViewerCSS and reachViewerJS are the remote-viewer additions of the
// desktop client (mosaic view, multi-monitor mapping, launch/session states,
// audio, clipboard, file transfer). They are injected into the page by
// buildUI with strings.Replace, NOT through fmt.Sprintf, so '%' needs no
// escaping here. The JS must not contain backticks (Go raw string) nor the
// closing script tag.
//
// Contract: the ObliReach viewer protocol (agent/protocol.go). The
// protocol constants live in ui.go (reachProtocolJS).
//
// Layout of reachViewerJS:
//   1. Pure helpers (no DOM): pointer mapping, mosaic hit-testing, binary
//      frame layouts, audio decoding + scheduling, SHA-256, file transfer
//      state machine. Unit-tested with node (see ui_test.go for the dump).
//   2. Session engine: one state object per session tab, message handling,
//      reconnect budget, overlays, toolbar widgets.

const reachViewerCSS = `
#remote-pane{position:relative}
#remote-pane.tb-auto .remote-toolbar{position:absolute;top:0;left:0;right:0;z-index:40;opacity:0;pointer-events:none;transform:translateY(-6px);transition:opacity .15s,transform .15s;box-shadow:0 6px 18px var(--shadow)}
#remote-pane.tb-auto.tb-show .remote-toolbar{opacity:1;pointer-events:auto;transform:none}
.tb-pin.on,.tb-focus.on{color:var(--accent-l);background:rgb(var(--accent-rgb) / .14)}
body.reach-focus .topbar,body.reach-focus #session-tabs,body.reach-focus .device-header,body.reach-focus #nav-rail,body.reach-focus .status-bar,body.reach-focus .update-banner{display:none!important}
.remote-toolbar{flex-wrap:wrap;row-gap:6px}
.btn-sm.active{color:var(--accent-l);background:rgb(var(--accent-rgb) / .14)}
.btn-sm:disabled{opacity:.45;cursor:default}
.reach-ico-btn{display:inline-flex;align-items:center;gap:5px}
.reach-overlay{position:absolute;inset:0;z-index:19;display:none;align-items:center;justify-content:center;background:rgba(1,4,9,.74);padding:20px}
.reach-overlay-box{max-width:440px;padding:20px 22px;border-radius:14px;background:var(--bg2);box-shadow:0 12px 40px var(--shadow);display:flex;flex-direction:column;align-items:center;gap:10px;text-align:center}
.reach-overlay-title{font-size:14px;font-weight:600;color:var(--text)}
.reach-overlay.error .reach-overlay-title{color:var(--danger)}
.reach-overlay-text{font-size:12px;color:var(--muted);line-height:1.5;word-break:break-word}
.reach-overlay-actions{display:flex;gap:8px;flex-wrap:wrap;justify-content:center;margin-top:4px}
.reach-btn-primary{background:var(--accent-fill)!important;color:var(--on-accent)!important}
.reach-spinner{width:26px;height:26px;border:2.5px solid rgb(var(--ink) / .12);border-top-color:var(--accent);border-radius:50%;animation:reachSpin .7s linear infinite}
@keyframes reachSpin{to{transform:rotate(360deg)}}
.reach-tiles{position:absolute;left:0;top:0;pointer-events:none;z-index:12;display:none}
.reach-tile{position:absolute;box-sizing:border-box;outline:1px dashed rgba(255,255,255,.14);outline-offset:-1px}
.reach-tile-label{position:absolute;left:6px;top:6px;pointer-events:auto;min-width:24px;height:22px;padding:0 8px;border-radius:11px;background:rgba(13,17,23,.78);color:#fff;font-size:11px;font-weight:700;cursor:pointer;opacity:.62;transition:opacity .15s,background .15s}
.reach-tile-label:hover{opacity:1;background:var(--accent-fill)}
.reach-transfers{position:absolute;left:10px;bottom:10px;width:330px;max-width:calc(100% - 20px);max-height:45%;overflow-y:auto;z-index:21;background:var(--bg2);border-radius:12px;box-shadow:0 8px 24px var(--shadow);display:none;font-size:11px;color:var(--text)}
.reach-tr-head{display:flex;align-items:center;gap:8px;padding:8px 10px;background:rgb(var(--ink) / .04);font-weight:600}
.reach-tr-head .sp{flex:1}
.reach-tr-row{padding:7px 10px;display:flex;flex-direction:column;gap:4px}
.reach-tr-line{display:flex;align-items:center;gap:6px;min-width:0}
.reach-tr-name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.reach-tr-meta{color:var(--muted);font-size:10.5px;white-space:nowrap}
.reach-tr-err{color:var(--danger);font-size:10.5px;word-break:break-word}
.reach-tr-bar{height:3px;border-radius:2px;background:rgb(var(--ink) / .08);overflow:hidden}
.reach-tr-bar > div{height:100%;background:var(--accent);width:0}
.reach-tr-x{border:none;background:none;color:var(--muted);cursor:pointer;font-size:13px;line-height:1;padding:0 2px}
.reach-tr-x:hover{color:var(--text)}
.reach-drop{position:absolute;inset:8px;z-index:22;border:2px dashed var(--accent);border-radius:14px;background:linear-gradient(rgb(var(--accent-rgb) / .10),rgb(var(--accent-rgb) / .10)),rgb(var(--bg-rgb) / .6);display:none;align-items:center;justify-content:center;color:var(--text);font-size:13px;font-weight:500;pointer-events:none;text-align:center;padding:20px}
.reach-drop.visible{display:flex}
.reach-drop.denied{color:var(--danger)}
.reach-toast{position:absolute;left:50%;top:10px;transform:translateX(-50%) translateY(-8px);z-index:26;max-width:70%;padding:7px 14px;border-radius:10px;background:var(--bg2);color:var(--text);font-size:12px;opacity:0;pointer-events:none;transition:opacity .2s,transform .2s;box-shadow:0 6px 18px var(--shadow)}
.reach-toast.visible{opacity:1;transform:translateX(-50%) translateY(0)}
.reach-toast.warn{background:linear-gradient(rgb(var(--warn-rgb) / .12),rgb(var(--warn-rgb) / .12)),var(--bg2);color:var(--warn)}
.reach-toast.ok{background:linear-gradient(rgb(var(--success-rgb) / .12),rgb(var(--success-rgb) / .12)),var(--bg2)}
.reach-toast.reach-page-toast{position:fixed;top:54px;z-index:380}
.toolbar-dropdown-menu.reach-screens-menu{min-width:260px;padding:8px}
.reach-minimap{position:relative;margin:2px auto 8px;background:var(--viewport-bg);border-radius:8px}
.toolbar-dropdown-menu .reach-minimap-mon{position:absolute;box-sizing:border-box;border:1px solid transparent;background:rgba(255,255,255,.12);background-clip:padding-box;color:rgba(255,255,255,.72);font-size:11px;font-weight:700;border-radius:3px;cursor:pointer;display:flex;align-items:center;justify-content:center;padding:0;text-align:center}
.toolbar-dropdown-menu .reach-minimap-mon:hover{background-color:rgba(255,255,255,.22);color:#fff}
.toolbar-dropdown-menu .reach-minimap-mon.active{background-color:var(--accent-fill);color:#fff}
.toolbar-dropdown-menu .reach-menu-sep{height:0;margin:4px 2px}
.toolbar-dropdown-menu .reach-menu-note{padding:4px 10px 2px;font-size:10.5px;color:var(--muted2);white-space:normal;max-width:260px;line-height:1.4}
.toolbar-dropdown-menu button:disabled{opacity:.45;cursor:default}
.reach-vo-btn.on{color:var(--on-accent);background:var(--accent-fill)}
.reach-vo-chip{position:absolute;right:10px;bottom:10px;z-index:23;display:none;align-items:center;gap:6px;padding:4px 10px 4px 8px;border-radius:12px;background:rgba(13,17,23,.84);color:var(--accent-l);font-size:11px;font-weight:600;pointer-events:none;box-shadow:0 4px 14px rgba(0,0,0,.4)}
.reach-vo-chip.visible{display:inline-flex}
.reach-block-btn.on{color:var(--warn);background:rgb(var(--warn-rgb) / .16)}
.reach-lockend-btn.on{color:var(--danger);background:rgb(var(--danger-rgb) / .14)}
.reach-block-chip{position:absolute;right:10px;bottom:40px;z-index:23;display:none;align-items:center;gap:6px;padding:4px 10px 4px 8px;border-radius:12px;background:linear-gradient(rgb(var(--warn-rgb) / .12),rgb(var(--warn-rgb) / .12)),var(--bg2);color:var(--warn);font-size:11px;font-weight:600;pointer-events:none;box-shadow:0 4px 14px var(--shadow)}
.reach-block-chip.visible{display:inline-flex}
.session-tab .st-vo{display:inline-flex;color:var(--accent-l)}
.session-tab .st-ib{display:inline-flex;color:var(--warn)}
.reach-chat-btn{position:relative;display:inline-flex;align-items:center;gap:5px;color:var(--accent-l);background:rgb(var(--accent-rgb) / .12)}
.reach-chat-btn:hover,.reach-chat-btn.on{color:var(--on-accent);background:var(--accent-fill)}
.reach-chat-btn .reach-chat-dot{position:absolute;top:-3px;right:-3px;width:8px;height:8px;border-radius:50%;background:var(--accent);box-shadow:0 0 0 2px var(--bg2);display:none}
.tb-devname{display:none;align-items:center;gap:6px;max-width:240px;padding:0 6px 0 2px;font-size:12px;font-weight:600;color:var(--text);white-space:nowrap;overflow:hidden}
.tb-devname span:last-child{overflow:hidden;text-overflow:ellipsis}
body.reach-focus .tb-devname{display:inline-flex}
body.reach-detached .tb-focus,body.reach-detached .tb-detach{display:none!important}
.reach-side-btn.on{color:var(--on-accent);background:var(--accent-fill)}
.side-panel{width:0;overflow:hidden;transition:width .2s ease;display:flex;flex-direction:column;background:var(--bg2);flex-shrink:0}
.side-panel.open{width:380px;box-shadow:-8px 0 20px -12px var(--shadow)}
.side-panel-head{display:flex;align-items:center;gap:10px;padding:10px 12px 10px 14px;flex-shrink:0;min-width:380px}
.side-panel-head .sp-titles{flex:1;min-width:0;display:flex;flex-direction:column;gap:2px}
.side-panel-head .sp-title{font-size:13px;font-weight:600;color:var(--text)}
.side-panel-head .sp-sub{font-size:11px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.side-panel-body{flex:1;overflow-y:auto;display:flex;flex-direction:column;min-height:0;min-width:380px}
.device-header .tab-btn.side-tab.active{color:var(--accent-l);box-shadow:inset 0 -2px 0 var(--accent)}
.reach-detached-pane{gap:12px;text-align:center;padding:20px}
.reach-detached-pane .rd-title{font-size:14px;font-weight:600;color:var(--text)}
.reach-detached-pane .rd-text{max-width:440px;line-height:1.5}
.reach-detached-pane .rd-actions{display:flex;gap:8px;flex-wrap:wrap;justify-content:center}
/* ── Window frame (frame_windows.go): no Windows title bar, the page's own ── */
body{--win-h:44px}
body.reach-focus{--win-h:34px}
.win-ctl{display:none;position:fixed;top:0;right:0;height:calc(var(--win-h) - 1px);z-index:400}
body.win-frameless .win-ctl{display:flex}
.wc-btn{width:46px;height:100%;padding:0;border:none;border-radius:0;background:transparent;color:var(--text);display:inline-flex;align-items:center;justify-content:center;cursor:default;transition:background .1s,color .1s}
.wc-btn svg{display:block}
.wc-btn:hover{background:rgb(var(--ink) / .08)}
.wc-btn:active{background:rgb(var(--ink) / .05)}
.wc-btn.wc-close:hover{background:#c42b1c;color:#fff}
.wc-btn.wc-close:active{background:rgba(196,43,28,.9);color:#fff}
body.win-inactive .wc-btn:not(:hover){color:var(--muted)}
.wc-btn .wc-restore,body.win-max .wc-btn .wc-maximize{display:none}
body.win-max .wc-btn .wc-restore{display:block}
.win-resize-top{display:none;position:fixed;top:0;left:0;right:0;height:5px;z-index:401;cursor:n-resize}
body.win-frameless:not(.win-max) .win-resize-top{display:block}
body.win-frameless .topbar{padding-right:150px;min-width:0}
body.win-frameless .topbar .tenant-switcher{min-width:0}
.win-titlebar{display:none;height:var(--win-h);flex-shrink:0;align-items:center;gap:8px;min-width:0;padding:0 150px 0 12px;background:var(--chrome);font-size:12px;color:var(--text);user-select:none}
body.win-frameless.reach-focus .win-titlebar{display:flex}
.win-titlebar .wt-logo{display:flex;align-items:center;flex-shrink:0}
.win-titlebar .dot{flex-shrink:0}
.win-titlebar .wt-title{font-weight:600;min-width:0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
body.win-inactive .win-titlebar .wt-title{color:var(--muted)}
body.win-frameless.reach-focus .tb-devname{display:none!important}
/* ── Session window tabs (detached window title bar, reachRenderWinTabs) ── */
.wt-tabs,.wt-add,.wt-grip{display:none}
body.reach-detached .win-titlebar{gap:6px;padding-left:10px}
body.reach-detached .win-titlebar .wt-title,body.reach-detached #win-title-dot{display:none}
body.reach-detached .wt-tabs{display:flex;align-items:flex-end;align-self:stretch;flex:0 1 auto;min-width:0;gap:2px}
body.reach-detached .wt-add{display:inline-flex}
body.reach-detached .wt-grip{display:block;flex:1 0 48px;align-self:stretch}
.wt-strip{position:relative;display:flex;align-items:flex-end;gap:2px;flex:0 1 auto;min-width:0;height:100%;overflow-x:auto;overflow-y:hidden;scrollbar-width:none}
.wt-strip::-webkit-scrollbar{display:none}
.wt-arrow{display:none;align-self:center;flex-shrink:0;width:20px;height:22px;padding:0;border:none;border-radius:6px;background:transparent;color:var(--muted);align-items:center;justify-content:center;cursor:default}
.wt-tabs.overflow .wt-arrow{display:inline-flex}
.wt-arrow:hover:not(:disabled){background:rgb(var(--ink) / .08);color:var(--text)}
.wt-arrow:disabled{opacity:.3}
.wt-tab{position:relative;display:flex;align-items:center;gap:6px;flex:0 1 200px;width:200px;min-width:120px;max-width:220px;height:28px;padding:0 5px 0 11px;border:none;border-radius:8px 8px 0 0;background:transparent;color:var(--muted);font-size:12px;cursor:default;user-select:none;transition:background .1s,color .1s}
.wt-tab:hover{background:rgb(var(--ink) / .05);color:var(--text)}
.wt-tab.active{background:rgb(var(--ink) / .09);color:var(--text);box-shadow:inset 0 2px 0 var(--accent)}
body.win-inactive .wt-tab.active{color:var(--muted);box-shadow:none}
.wt-tab .dot{width:7px;height:7px}
.wt-tab-name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.wt-tab-vo{display:inline-flex;flex-shrink:0;color:var(--accent-l)}
.wt-tab-srv{width:6px;height:6px;border-radius:3px;flex-shrink:0;background:var(--muted2)} /* its server (multi-server mode) */
.wt-tab-x{flex-shrink:0;width:18px;height:18px;padding:0;border:none;border-radius:5px;background:transparent;color:inherit;display:inline-flex;align-items:center;justify-content:center;opacity:.5;cursor:default}
.wt-tab:hover .wt-tab-x,.wt-tab.active .wt-tab-x{opacity:.9}
.wt-tab-x:hover{opacity:1;background:rgb(var(--ink) / .12)}
.wt-tab.tearing{opacity:.55;outline:1px dashed var(--accent);outline-offset:-2px}
body.wt-tearing,body.wt-tearing *{cursor:grabbing!important}
.wt-add{flex-shrink:0;width:26px;height:24px;padding:0;border:none;border-radius:7px;background:transparent;color:var(--muted);align-items:center;justify-content:center;cursor:default}
.wt-add:hover,.wt-add.on{background:rgb(var(--ink) / .08);color:var(--text)}
/* "+": the device list over the window (devPicker*, ui.go) */
.dev-picker{position:fixed;left:0;right:0;bottom:0;top:var(--win-h);z-index:350;display:none;align-items:flex-start;justify-content:center;padding:14px 16px 16px;background:var(--scrim);backdrop-filter:blur(3px)}
.dev-picker.open{display:flex}
.dev-picker-box{width:100%;max-width:1040px;max-height:100%;display:flex;flex-direction:column;background:var(--bg);border-radius:16px;box-shadow:0 20px 60px var(--shadow);overflow:hidden}
.dev-picker-head{display:flex;align-items:center;gap:10px;padding:12px 16px;background:var(--bg2);flex-shrink:0}
.dev-picker-head h3{font-size:14px;font-weight:600;color:var(--text)}
.dev-picker-head .spacer{flex:1}
.dev-picker .filter-bar{margin:12px 16px 0;flex-shrink:0}
.dev-picker-note{margin:10px 16px 0;padding:8px 12px;border-radius:10px;background:rgb(var(--warn-rgb) / .12);color:var(--warn);font-size:12px;flex-shrink:0}
.dev-picker-list{flex:1;min-height:0;overflow:auto;margin:12px 16px 16px}
/* Windows session picker (reachPickSession) */
.modal-overlay.reach-sess-picker{z-index:360}
.reach-sess-picker .modal-box{width:460px}
.rsp-list{display:flex;flex-direction:column;gap:6px;max-height:50vh;overflow-y:auto}
.rsp-item{display:flex;align-items:center;gap:10px;width:100%;padding:10px 13px;border-radius:10px;border:none;background:rgb(var(--ink) / .06);color:var(--text);font-size:12px;text-align:left;cursor:pointer;transition:background .1s,box-shadow .1s}
.rsp-item.auto{background:rgb(var(--accent-rgb) / .06)}
.rsp-item:hover,.rsp-item:focus{background:rgb(var(--accent-rgb) / .12);outline:none}
.rsp-item:focus-visible{box-shadow:0 0 0 2px var(--accent)}
.rsp-item .rsp-text{display:flex;flex-direction:column;gap:2px;min-width:0}
.rsp-item .rsp-user{font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.rsp-item .rsp-meta{font-size:11px;color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.rsp-item.off .rsp-user{color:var(--muted)}
`

const reachViewerJS = `
// ═════════════════════════════════════════════════════════════════════════
// 1. Pure helpers (no DOM access) — unit-tested with node.
// ═════════════════════════════════════════════════════════════════════════

function reachClamp(v, lo, hi) { return v < lo ? lo : (v > hi ? hi : v); }

// Where the frame is drawn inside an element box. fit = CSS object-fit:contain
// (letterboxed, "Fit" zoom); otherwise the frame fills the box (zoom modes set
// the element size to the frame size times the zoom factor).
function reachContentRect(boxW, boxH, fw, fh, fit) {
  if (!fit || !(fw > 0) || !(fh > 0) || !(boxW > 0) || !(boxH > 0)) return { left: 0, top: 0, width: boxW, height: boxH };
  var s = Math.min(boxW / fw, boxH / fh);
  var w = fw * s, h = fh * s;
  return { left: (boxW - w) / 2, top: (boxH - h) / 2, width: w, height: h };
}

// Element-relative CSS point -> frame pixel (the space the agent expects
// mouse x,y in). Always clamped into the frame; inside tells whether the point
// was really over the picture (and not over the letterbox bars).
function reachMapPointer(px, py, boxW, boxH, fw, fh, fit) {
  if (!(fw > 0) || !(fh > 0)) return null;
  var r = reachContentRect(boxW, boxH, fw, fh, fit);
  if (!(r.width > 0) || !(r.height > 0)) return null;
  var rx = (px - r.left) * fw / r.width, ry = (py - r.top) * fh / r.height;
  var inside = rx >= 0 && ry >= 0 && rx < fw && ry < fh;
  return { x: reachClamp(Math.floor(rx), 0, fw - 1), y: reachClamp(Math.floor(ry), 0, fh - 1), inside: inside };
}

// Frame pixel -> element-relative CSS point (inverse of reachMapPointer).
function reachFrameToBox(fx, fy, boxW, boxH, fw, fh, fit) {
  var r = reachContentRect(boxW, boxH, fw, fh, fit);
  return { x: r.left + fx * r.width / fw, y: r.top + fy * r.height / fh };
}

function reachIsMosaic(view) { return !!(view && view.mode === 'mosaic'); }

// Tile of the view containing frame point (x,y), or null (black gap).
function reachTileAt(view, x, y) {
  if (!view || !Array.isArray(view.tiles)) return null;
  for (var i = 0; i < view.tiles.length; i++) {
    var t = view.tiles[i], f = t && t.frame;
    if (!f) continue;
    if (x >= f.x && x < f.x + f.w && y >= f.y && y < f.y + f.h) return t;
  }
  return null;
}

// Normalises init.view; legacy agents (no view) get one tile covering the frame.
function reachNormalizeView(info) {
  var v = info && info.view;
  var fw = info.width | 0, fh = info.height | 0;
  if (v && Array.isArray(v.tiles) && v.tiles.length) {
    var tiles = [];
    for (var i = 0; i < v.tiles.length; i++) {
      var t = v.tiles[i];
      if (!t || !t.frame || !(t.frame.w > 0) || !(t.frame.h > 0)) continue;
      tiles.push({
        index: (typeof t.index === 'number') ? t.index : i,
        frame: { x: +t.frame.x || 0, y: +t.frame.y || 0, w: +t.frame.w, h: +t.frame.h },
        desktop: t.desktop || null,
        rotation: t.rotation | 0,
        primary: !!t.primary,
      });
    }
    if (tiles.length) {
      return { mode: v.mode === 'mosaic' ? 'mosaic' : 'monitor', width: (v.width | 0) || fw, height: (v.height | 0) || fh, scale: +v.scale || 1, tiles: tiles };
    }
  }
  var idx = (typeof info.monitor === 'number' && info.monitor >= 0) ? info.monitor : 0;
  return { mode: 'monitor', width: fw, height: fh, scale: 1, tiles: [{ index: idx, frame: { x: 0, y: 0, w: fw, h: fh }, desktop: null, rotation: 0, primary: false }] };
}

// Monitors (desktop rectangles, possibly negative origins) laid out in a box
// of boxW x boxH with pad pixels margin, keeping their relative positions.
function reachMonitorsLayout(monitors, boxW, boxH, pad) {
  var list = (monitors || []).filter(function (m) { return m && m.width > 0 && m.height > 0; });
  if (!list.length) return [];
  var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  list.forEach(function (m) { x0 = Math.min(x0, m.x | 0); y0 = Math.min(y0, m.y | 0); x1 = Math.max(x1, (m.x | 0) + m.width); y1 = Math.max(y1, (m.y | 0) + m.height); });
  var uw = x1 - x0, uh = y1 - y0;
  var s = Math.min((boxW - 2 * pad) / uw, (boxH - 2 * pad) / uh);
  var ox = (boxW - uw * s) / 2, oy = (boxH - uh * s) / 2;
  return list.map(function (m, i) {
    var l = ox + ((m.x | 0) - x0) * s, t = oy + ((m.y | 0) - y0) * s;
    return { index: (typeof m.index === 'number') ? m.index : i, left: l + 1, top: t + 1, width: Math.max(4, m.width * s - 2), height: Math.max(4, m.height * s - 2), primary: !!m.primary };
  });
}

// File data frames: [type][tid u32 LE][seq u32 LE][data].
function reachBuildFileFrame(type, tid, seq, data) {
  var out = new Uint8Array(9 + data.length);
  out[0] = type;
  var dv = new DataView(out.buffer);
  dv.setUint32(1, tid >>> 0, true);
  dv.setUint32(5, seq >>> 0, true);
  out.set(data, 9);
  return out;
}
function reachParseFileFrame(u8) {
  if (!u8 || u8.length < 9) return null;
  var dv = new DataView(u8.buffer, u8.byteOffset, u8.byteLength);
  return { type: u8[0], tid: dv.getUint32(1, true), seq: dv.getUint32(5, true), data: u8.subarray(9) };
}

// Interleaved PCM s16le starting at offset -> one Float32Array per channel.
function reachPcm16ToFloat(u8, offset, channels) {
  channels = channels > 1 ? channels : 1;
  var n = Math.floor((u8.length - offset) / (2 * channels));
  if (n < 0) n = 0;
  var out = [];
  for (var c = 0; c < channels; c++) out.push(new Float32Array(n));
  var dv = new DataView(u8.buffer, u8.byteOffset + offset, n * 2 * channels);
  var p = 0;
  for (var i = 0; i < n; i++) {
    for (var ch = 0; ch < channels; ch++) { out[ch][i] = dv.getInt16(p, true) / 32768; p += 2; }
  }
  return out;
}

// Audio v2 frame: 20-byte header then samples.
function reachParseAudioV2(u8) {
  if (!u8 || u8.length < 20 || u8[0] !== 0x07) return null;
  var dv = new DataView(u8.buffer, u8.byteOffset, u8.byteLength);
  var a = {
    version: u8[1], codec: u8[2], channels: u8[3],
    rate: dv.getUint32(4, true), seq: dv.getUint32(8, true),
    pos: dv.getUint32(12, true) + dv.getUint32(16, true) * 4294967296,
    samples: null,
  };
  if (a.version !== 1 || a.codec !== 0 || (a.channels !== 1 && a.channels !== 2) || !a.rate) return a;
  a.samples = reachPcm16ToFloat(u8, 20, a.channels);
  return a;
}

// Playhead scheduling: chunks are queued back to back; on underrun or
// a backlog over 300 ms the playhead restarts 80 ms ahead of "now".
function reachAudioSchedule(st, now, dur) {
  var reset = false;
  if (st.playhead < now) {
    if (st.playhead > 0) st.stats.underruns++;
    st.playhead = now + 0.08;
  } else if (st.playhead - now > 0.3) {
    st.stats.resets++;
    st.playhead = now + 0.08;
    reset = true;
  }
  var t = Math.max(st.playhead, now + 0.005);
  st.playhead = t + dur;
  return { t: t, reset: reset };
}

class ReachAudioPlayer {
  constructor(factory) {
    this.factory = factory || function (rate) {
      var C = window.AudioContext || window.webkitAudioContext;
      return rate ? new C({ sampleRate: rate, latencyHint: 'interactive' }) : new C({ latencyHint: 'interactive' });
    };
    this.ctx = null; this.rate = 0; this.enabled = false;
    this.playhead = 0; this.sources = [];
    this.stats = { chunks: 0, underruns: 0, resets: 0, dropped: 0 };
  }
  setRate(rate) {
    rate = rate | 0;
    if (rate <= 0 || rate === this.rate) return;
    if (this.ctx) this._closeCtx();
    this.rate = rate;
    if (this.enabled) this._openCtx();
  }
  _openCtx() {
    if (this.ctx) return;
    try { this.ctx = this.factory(this.rate || 0); } catch (e) {
      try { this.ctx = this.factory(0); } catch (e2) { this.ctx = null; }
    }
    this.playhead = 0;
  }
  _stopSources() {
    for (var i = 0; i < this.sources.length; i++) { try { this.sources[i].stop(); } catch (e) {} }
    this.sources = [];
  }
  _closeCtx() {
    this._stopSources();
    if (this.ctx) { try { this.ctx.close(); } catch (e) {} }
    this.ctx = null; this.playhead = 0;
  }
  enable() {
    this.enabled = true;
    if (!this.rate) this.rate = 48000;
    this._openCtx();
    if (this.ctx && this.ctx.state === 'suspended' && this.ctx.resume) { try { this.ctx.resume(); } catch (e) {} }
    this.playhead = 0;
  }
  disable() {
    this.enabled = false;
    this._stopSources();
    this.playhead = 0;
    if (this.ctx && this.ctx.suspend) { try { this.ctx.suspend(); } catch (e) {} }
  }
  reset() { this._stopSources(); this.playhead = 0; }
  close() { this.enabled = false; this._closeCtx(); }
  // channels: Float32Array per channel; rate: their sample rate.
  push(channels, rate) {
    if (!this.enabled) return false;
    if (rate && (rate | 0) !== this.rate) this.setRate(rate);
    if (!this.ctx) this._openCtx();
    var ctx = this.ctx;
    if (!ctx || ctx.state !== 'running') { this.stats.dropped++; return false; }
    if (!channels || !channels.length || !channels[0] || !channels[0].length) return false;
    var n = channels[0].length;
    var buf = ctx.createBuffer(channels.length, n, this.rate);
    for (var c = 0; c < channels.length; c++) buf.getChannelData(c).set(channels[c]);
    var src = ctx.createBufferSource();
    src.buffer = buf;
    src.connect(ctx.destination);
    var r = reachAudioSchedule(this, ctx.currentTime, n / this.rate);
    if (r.reset) this._stopSources();
    src.start(r.t);
    var self = this;
    this.sources.push(src);
    src.onended = function () { var i = self.sources.indexOf(src); if (i >= 0) self.sources.splice(i, 1); };
    this.stats.chunks++;
    return true;
  }
}

// Keyframe detection per codec.
function reachAnnexBFirstBytes(u8, max) {
  var out = [], i = 0, n = u8.length;
  while (i + 3 < n && out.length < max) {
    if (u8[i] === 0 && u8[i + 1] === 0 && (u8[i + 2] === 1 || (u8[i + 2] === 0 && u8[i + 3] === 1))) {
      var ns = u8[i + 2] === 1 ? i + 3 : i + 4;
      if (ns < n) out.push(u8[ns]);
      i = ns;
    } else i++;
  }
  return out;
}
function reachH264IsKey(u8) {
  var b = reachAnnexBFirstBytes(u8, 16);
  for (var i = 0; i < b.length; i++) { var t = b[i] & 0x1f; if (t === 5 || t === 7 || t === 8) return true; }
  return false;
}
function reachH265IsKey(u8) {
  var b = reachAnnexBFirstBytes(u8, 16);
  for (var i = 0; i < b.length; i++) { var t = (b[i] >> 1) & 0x3f; if ((t >= 16 && t <= 21) || t === 32 || t === 33 || t === 34) return true; }
  return false;
}
function reachVp9IsKey(u8) {
  if (!u8 || !u8.length) return false;
  var b = u8[0];
  if ((b >> 6) !== 2) return false; // frame_marker
  var profile = ((b >> 5) & 1) | (((b >> 4) & 1) << 1);
  var bit = profile === 3 ? 2 : 3;   // profile 3 has one reserved bit
  if ((b >> bit) & 1) return false;  // show_existing_frame
  return ((b >> (bit - 1)) & 1) === 0; // frame_type 0 = KEY_FRAME
}
function reachAv1IsKey(u8) {
  var i = 0, guard = 0;
  while (u8 && i < u8.length && guard++ < 16) {
    var hdr = u8[i], type = (hdr >> 3) & 0x0f, ext = (hdr >> 2) & 1, hasSize = (hdr >> 1) & 1;
    if (type === 1) return true; // sequence header: start of a key temporal unit
    if (!hasSize) return false;
    var p = i + 1 + ext, size = 0, mul = 1, b;
    do {
      if (p >= u8.length) return false;
      b = u8[p++]; size += (b & 0x7f) * mul; mul *= 128;
    } while ((b & 0x80) && mul < 72057594037927936);
    i = p + size;
  }
  return false;
}
function reachCodecString(codec, extradata) {
  if (codec === 'vp9') return 'vp09.00.10.08';
  if (codec === 'av1') return 'av01.0.04M.08';
  if (codec === 'h265') return 'hev1.1.6.L93.B0';
  // H.264 High 5.2 (up to 4096x2304). A valid AVCC record only refines the
  // codec string: the agent sends Annex B, so no description is configured.
  if (extradata) {
    try {
      var raw = atob(extradata);
      if (raw.length >= 4 && raw.charCodeAt(0) === 1) {
        var hx = function (v) { return ('0' + v.toString(16)).slice(-2); };
        return 'avc1.' + hx(raw.charCodeAt(1)) + hx(raw.charCodeAt(2)) + hx(raw.charCodeAt(3));
      }
    } catch (e) {}
  }
  return 'avc1.640034';
}

function reachB64ToBytes(b64) {
  var s = atob(b64 || '');
  var out = new Uint8Array(s.length);
  for (var i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}
function reachUtf8Length(s) {
  var n = 0;
  for (var i = 0; i < s.length; i++) {
    var c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      var d = s.charCodeAt(i + 1);
      if (d >= 0xdc00 && d <= 0xdfff) { n += 4; i++; } else n += 3;
    } else n += 3;
  }
  return n;
}
// A size, as the language on screen writes it: its units (catalog: Ko / Mo
// in French...) and its decimal separator.
var REACH_BYTE_UNITS = ['units.kb', 'units.mb', 'units.gb', 'units.tb'];
function reachFmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return T('units.bytes', { n: i18nNum(n, 0) });
  var i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < REACH_BYTE_UNITS.length - 1);
  return T(REACH_BYTE_UNITS[i], { n: i18nNum(n, n >= 100 ? 0 : 1) });
}
function reachSafeFileName(name) {
  var s = String(name || '').replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_').replace(/^[. ]+|[. ]+$/g, '');
  return s.slice(0, 200) || 'download';
}

// Query of a detached session window (proxy.go entryTarget):
// ?detached=<device id>[&session=<n>][&vo=1][&p=<server profile id>][&m=<tool>]
// [&vn=<VM name>] (m=vm-<guid>: a Hyper-V VM console of that host, r.vm).
// null for the main window. profile: the server the device id belongs to (''
// when none, or refused: the boot then refuses a server it cannot match).
// kind (only when m is one of the tools, terminal_viewer.go): a tool window.
function reachParseDetached(search) {
  var s = String(search || '');
  if (s.charAt(0) === '?') s = s.slice(1);
  var id = null, session = -1, vo = false, profile = '', kind = '', park = '', path = '', vmId = '', vmName = '';
  s.split('&').forEach(function (kv) {
    var i = kv.indexOf('='), k = i < 0 ? kv : kv.slice(0, i), v = i < 0 ? '' : kv.slice(i + 1);
    try { v = decodeURIComponent(v.replace(/\+/g, ' ')); } catch (e) { v = ''; }
    if (k === 'detached') id = /^[A-Za-z0-9_-]{1,64}$/.test(v) ? v : null;
    else if (k === 'session') session = /^[0-9]{1,7}$/.test(v) ? parseInt(v, 10) : -1;
    else if (k === 'vo') vo = v === '1';
    else if (k === 'p') profile = /^[A-Za-z0-9_-]{1,64}$/.test(v) ? v : '';
    else if (k === 'm') {
      if (/^vm-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v)) { vmId = v.slice(3); kind = ''; } // (a Hyper-V VM console: vm_console.go)
      else kind = /^(cmd|powershell|ssh|files|rdp)$/.test(v) ? v : ''; // (rdp: a local RDP connection, rdp_view.go)
    }
    else if (k === 'vn') vmName = v.length <= 400 ? v : '';         // (its VM's name)
    else if (k === 'pk') park = /^[0-9a-f]{64}$/.test(v) ? v : ''; // (a terminal or a local RDP session moved here: term_park.go, rdp_park.go)
    else if (k === 'fp') path = v.length <= 4096 ? v : '';          // (an explorer moved here: its folder)
  });
  if (id === null) return null;
  var r = { deviceId: id, session: session, viewOnly: vo, profile: profile };
  if (kind) r.kind = kind;
  if (park && /^(cmd|powershell|ssh|rdp)$/.test(kind)) r.park = park;
  if (path && kind === 'files') r.path = path;
  if (vmId) r.vm = { id: vmId, name: vmCleanName(vmName) || vmId }; // (a VM console's window)
  return r;
}

// ── Window frame (frame_windows.go): the page draws the title bar ──
// A press on an empty part of a title bar (an element marked data-win-drag:
// the top bar, the slim title bar, the sign-in page background) moves the
// window; a press on anything interactive in it keeps its own meaning.
var REACH_WIN_NO_DRAG_TAGS = ['BUTTON', 'A', 'INPUT', 'SELECT', 'TEXTAREA', 'LABEL', 'OPTION', 'SUMMARY', 'VIDEO', 'AUDIO', 'CANVAS', 'IFRAME'];
var REACH_WIN_NO_DRAG_ROLES = ['button', 'link', 'menuitem', 'tab', 'checkbox', 'switch', 'textbox', 'option', 'combobox', 'slider'];
var REACH_WIN_CORNER = 12; // CSS px at either end of the top band that resize diagonally

// An element that handles presses itself: a control, anything marked
// data-no-drag or with a click handler, or showing a pointer / text cursor
// (cursorOf(el): its computed cursor; the menus and switchers are divs).
function reachWinInteractive(el, cursorOf) {
  if (!el) return false;
  if (REACH_WIN_NO_DRAG_TAGS.indexOf(String(el.tagName || '').toUpperCase()) >= 0) return true;
  if (el.isContentEditable === true || typeof el.onclick === 'function') return true;
  var attr = function (k) { return typeof el.getAttribute === 'function' ? el.getAttribute(k) : null; };
  if (attr('data-no-drag') != null) return true;
  var role = attr('role');
  if (role && REACH_WIN_NO_DRAG_ROLES.indexOf(String(role)) >= 0) return true;
  var cur = cursorOf ? cursorOf(el) : '';
  return cur === 'pointer' || cur === 'text';
}

// The title bar a press on target moves the window from, or null: outside
// any title bar, or on something interactive (up to the title bar).
function reachWinDragZone(target, cursorOf) {
  for (var el = target; el && el.nodeType !== 9; el = el.parentElement) {
    if (reachWinInteractive(el, cursorOf)) return null;
    if (typeof el.getAttribute === 'function' && el.getAttribute('data-win-drag') != null) return el;
  }
  return null;
}

// The part of the band along the top edge a press at x (CSS px; w: the
// window's width) resizes from.
function reachWinTopEdge(x, w, corner) {
  if (!(corner > 0)) corner = REACH_WIN_CORNER;
  if (x < corner) return 'topleft';
  if (w > 0 && x >= w - corner) return 'topright';
  return 'top';
}
function reachWinEdgeCursor(edge) { return edge === 'topleft' ? 'nw-resize' : (edge === 'topright' ? 'ne-resize' : 'n-resize'); }

// Streaming SHA-256 (uploads of up to 2 GiB cannot use SubtleCrypto, which
// only hashes a whole buffer at once).
var REACH_SHA256_K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);
class ReachSha256 {
  constructor() {
    this.h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    this.buf = new Uint8Array(64); this.bufLen = 0; this.total = 0; this.w = new Uint32Array(64);
  }
  update(data) {
    var i = 0, n = data.length;
    this.total += n;
    if (this.bufLen > 0) {
      var take = Math.min(64 - this.bufLen, n);
      this.buf.set(data.subarray(0, take), this.bufLen);
      this.bufLen += take; i = take;
      if (this.bufLen < 64) return this;
      this._block(this.buf, 0); this.bufLen = 0;
    }
    while (i + 64 <= n) { this._block(data, i); i += 64; }
    if (i < n) { this.buf.set(data.subarray(i), 0); this.bufLen = n - i; }
    return this;
  }
  _block(d, o) {
    var w = this.w, k = REACH_SHA256_K, h = this.h, t;
    for (t = 0; t < 16; t++) { var j = o + t * 4; w[t] = ((d[j] << 24) | (d[j + 1] << 16) | (d[j + 2] << 8) | d[j + 3]) >>> 0; }
    for (t = 16; t < 64; t++) {
      var x = w[t - 15], y = w[t - 2];
      var s0 = ((x >>> 7) | (x << 25)) ^ ((x >>> 18) | (x << 14)) ^ (x >>> 3);
      var s1 = ((y >>> 17) | (y << 15)) ^ ((y >>> 19) | (y << 13)) ^ (y >>> 10);
      w[t] = (w[t - 16] + s0 + w[t - 7] + s1) >>> 0;
    }
    var a = h[0], b = h[1], c = h[2], dd = h[3], e = h[4], f = h[5], g = h[6], hh = h[7];
    for (t = 0; t < 64; t++) {
      var S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
      var ch = (e & f) ^ (~e & g);
      var t1 = (hh + S1 + ch + k[t] + w[t]) | 0;
      var S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
      var mj = (a & b) ^ (a & c) ^ (b & c);
      var t2 = (S0 + mj) | 0;
      hh = g; g = f; f = e; e = (dd + t1) | 0; dd = c; c = b; b = a; a = (t1 + t2) | 0;
    }
    h[0] += a; h[1] += b; h[2] += c; h[3] += dd; h[4] += e; h[5] += f; h[6] += g; h[7] += hh;
  }
  // Hex digest of everything fed so far (does not consume the state).
  hex() {
    var saved = new Uint32Array(this.h);
    var padLen = this.bufLen < 56 ? 64 : 128;
    var pad = new Uint8Array(padLen);
    pad.set(this.buf.subarray(0, this.bufLen), 0);
    pad[this.bufLen] = 0x80;
    var hi = Math.floor(this.total / 0x20000000), lo = (this.total * 8) >>> 0;
    var dv = new DataView(pad.buffer);
    dv.setUint32(padLen - 8, hi, false);
    dv.setUint32(padLen - 4, lo, false);
    this._block(pad, 0);
    if (padLen === 128) this._block(pad, 64);
    var out = '';
    for (var i = 0; i < 8; i++) out += ('00000000' + this.h[i].toString(16)).slice(-8);
    this.h.set(saved);
    return out;
  }
}

// Agent file error codes -> catalog keys (i18n.go).
var REACH_FILE_ERRORS = {
  too_large: 'viewer.fileErr.tooLarge',
  no_user_session: 'viewer.fileErr.noUserSession',
  bad_name: 'viewer.fileErr.badName',
  denied: 'viewer.fileErr.denied',
  disk_full: 'viewer.fileErr.diskFull',
  hash_mismatch: 'viewer.fileErr.hashMismatch',
  busy: 'viewer.fileErr.busy',
  cancelled: 'viewer.fileErr.cancelled',
  not_found: 'viewer.fileErr.notFound',
  unsupported: 'viewer.fileErr.unsupported',
  forbidden: 'viewer.fileErr.forbidden',
};
function reachFileErrorText(code, message) {
  if (code && Object.prototype.hasOwnProperty.call(REACH_FILE_ERRORS, code)) return T(REACH_FILE_ERRORS[code]);
  return (typeof message === 'string' && message) ? message : (code ? String(code) : T('viewer.fileErr.failed'));
}

// File transfers refused in this session: the Obliance relay adds
// init.filesPolicy {allowed:false, reason} (no 'files' permission on the
// device, the organization's restrictions on file transfers, or privacy mode
// without the 'files' unlock) and removes the file features. Returns
// 'capability' | 'restriction' | 'privacy' | 'other', or null.
function reachFilesDeniedOf(info) {
  var p = info && info.filesPolicy;
  if (!p || typeof p !== 'object' || p.allowed !== false) return null;
  return (p.reason === 'capability' || p.reason === 'restriction' || p.reason === 'privacy') ? p.reason : 'other';
}
// Why (for the disabled file actions), '' when transfers are permitted.
function reachFilesDeniedText(tab) {
  var r = tab && tab.filesDenied;
  if (!r) return '';
  return r === 'capability' ? T('viewer.file.deniedCapability')
    : r === 'restriction' ? T('viewer.file.deniedRestriction')
    : r === 'privacy' ? T('viewer.file.deniedPrivacy')
    : T('viewer.file.deniedOther');
}

// File transfer state machine: one upload and one download at a time,
// uploads flow-controlled by the agent's cumulative file_progress acks.
class ReachTransfers {
  constructor(o) {
    o = o || {};
    this.sendJson = o.sendJson || function () { return false; };
    this.sendBinary = o.sendBinary || function () { return false; };
    this.onChange = o.onChange || function () {};
    this.save = o.save || function () {};
    this.notify = o.notify || function () {};
    this.limits = { chunk: 262144, window: 8, maxFile: 2147483648 };
    this.nextTid = 1;
    this.up = null; this.upQueue = [];
    this.down = null; this.downQueue = [];
    this.items = [];
  }
  setLimits(f) {
    if (!f || typeof f !== 'object') return;
    if (f.chunk > 0) this.limits.chunk = reachClamp(f.chunk | 0, 1024, 4194304);
    if (f.window > 0) this.limits.window = reachClamp(f.window | 0, 1, 64);
    if (f.maxFile > 0) this.limits.maxFile = +f.maxFile;
  }
  _finished(it) { return it.state === 'done' || it.state === 'error' || it.state === 'cancelled'; }
  _track(it) {
    this.items.push(it);
    var self = this;
    var done = this.items.filter(function (x) { return self._finished(x); });
    if (done.length > 8) {
      var drop = done.slice(0, done.length - 8);
      this.items = this.items.filter(function (x) { return drop.indexOf(x) < 0; });
    }
  }
  visibleItems() { return this.items.slice(); }
  clearFinished() { var self = this; this.items = this.items.filter(function (x) { return !self._finished(x); }); this.onChange(); }
  busy() { return !!(this.up || this.down || this.upQueue.length || this.downQueue.length); }

  enqueueUpload(src) {
    if (!src || typeof src.read !== 'function') return { ok: false, code: 'bad_source' };
    var size = Number(src.size);
    if (!(size >= 0)) return { ok: false, code: 'bad_source' };
    // A paste-as-RDP file has no per-file cap (by design: only the
    // 20 GiB-per-paste total matters, checked once for the whole batch
    // before any of it is queued — see reachUploadNativeFiles).
    if (!src.paste && size > this.limits.maxFile) return { ok: false, code: 'too_large' };
    var it = { dir: 'up', tid: 0, name: String(src.name || 'file'), size: size, done: 0, state: 'queued', src: src, error: '' };
    this.upQueue.push(it); this._track(it);
    this._nextUp();
    this.onChange();
    return { ok: true, item: it };
  }
  _nextUp() {
    if (this.up || !this.upQueue.length) return;
    var it = this.upQueue.shift();
    it.tid = this.nextTid++; it.state = 'begin'; it.seq = 0; it.acked = -1; it.offset = 0;
    it.reading = false; it.endSent = false; it.hasher = new ReachSha256();
    it.chunk = this.limits.chunk; it.window = this.limits.window;
    this.up = it;
    var beginMsg = { type: 'file_begin', tid: it.tid, name: it.name, size: it.size, mime: it.src.mime || '', dest: 'downloads' };
    if (it.src.paste) beginMsg.paste = true;
    if (!this.sendJson(beginMsg)) this._failUp(it, T('viewer.tr.notConnected'));
  }
  _failUp(it, why, state) {
    if (this.up !== it) return;
    it.state = state || 'error'; it.error = why || '';
    it.src = null; it.hasher = null; this.up = null;
    this.onChange();
    this._nextUp();
  }
  _cancelUp(it, why) {
    if (this.up !== it) return;
    this.sendJson({ type: 'file_cancel', tid: it.tid });
    this._failUp(it, why, 'error');
  }
  _pump() {
    var it = this.up;
    if (!it || it.state !== 'sending' || it.reading) return;
    if (it.offset >= it.size) {
      if (!it.endSent) {
        it.endSent = true; it.state = 'ending';
        this.sendJson({ type: 'file_end', tid: it.tid, sha256: it.hasher.hex() });
        this.onChange();
      }
      return;
    }
    if (it.seq - (it.acked + 1) >= it.window) return; // window full: wait for acks
    var len = Math.min(it.chunk, it.size - it.offset);
    var off = it.offset, self = this;
    it.reading = true;
    Promise.resolve().then(function () { return it.src.read(off, len); }).then(function (data) {
      it.reading = false;
      if (self.up !== it || it.state !== 'sending') return;
      if (!data || data.length !== len) { self._cancelUp(it, T('viewer.tr.readFailed')); return; }
      it.hasher.update(data);
      if (!self.sendBinary(reachBuildFileFrame(REACH.FRAME.FILE_UP, it.tid, it.seq, data))) { self._failUp(it, T('viewer.tr.notConnected')); return; }
      it.seq++; it.offset += len;
      self.onChange();
      self._pump();
    }, function () {
      it.reading = false;
      self._cancelUp(it, T('viewer.tr.readFailed'));
    });
  }
  requestDownload(entry) {
    if (!entry || entry.dir) return { ok: false, code: 'unsupported' };
    var it = { dir: 'down', tid: 0, idx: entry.idx | 0, name: String(entry.name || 'file'), size: Number(entry.size) || 0, done: 0, state: 'queued', error: '' };
    this.downQueue.push(it); this._track(it);
    this._nextDown();
    this.onChange();
    return { ok: true, item: it };
  }
  _nextDown() {
    if (this.down || !this.downQueue.length) return;
    var it = this.downQueue.shift();
    it.tid = this.nextTid++; it.state = 'requested'; it.expect = 0; it.parts = []; it.hasher = new ReachSha256(); it.done = 0;
    this.down = it;
    if (!this.sendJson({ type: 'file_get', tid: it.tid, source: 'clipboard', idx: it.idx })) this._failDown(it, T('viewer.tr.notConnected'));
  }
  _failDown(it, why, state) {
    if (this.down !== it) return;
    it.state = state || 'error'; it.error = why || '';
    it.parts = null; it.hasher = null; this.down = null;
    this.onChange();
    this._nextDown();
  }
  _finishDown(it, msg) {
    if (msg.ok === false) { this._failDown(it, reachFileErrorText(msg.code, msg.message)); return; }
    if (it.size && it.done !== it.size) { this._failDown(it, T('viewer.tr.incomplete')); return; }
    var digest = it.hasher.hex();
    if (typeof msg.sha256 === 'string' && msg.sha256 && msg.sha256.toLowerCase() !== digest) { this._failDown(it, T('viewer.tr.checksum')); return; }
    var blob = new Blob(it.parts, { type: 'application/octet-stream' });
    var name = reachSafeFileName(it.name);
    it.state = 'done'; it.parts = null; it.hasher = null; this.down = null;
    try { this.save(blob, name); } catch (e) {}
    this.notify(T('viewer.tr.downloaded', { name: name }), 'ok');
    this.onChange();
    this._nextDown();
  }
  // Binary 0x21 frame, already parsed by reachParseFileFrame.
  onData(f) {
    var it = this.down;
    if (!it || !f || f.tid !== it.tid) return;
    if (it.state === 'requested') it.state = 'receiving';
    if (it.state !== 'receiving') return;
    if (f.seq !== it.expect) { this.sendJson({ type: 'file_cancel', tid: it.tid }); this._failDown(it, T('viewer.tr.outOfOrder')); return; }
    it.expect++;
    it.parts.push(f.data);
    it.hasher.update(f.data);
    it.done += f.data.length;
    if (it.size && it.done > it.size) { this.sendJson({ type: 'file_cancel', tid: it.tid }); this._failDown(it, T('viewer.tr.tooMuch')); return; }
    this.sendJson({ type: 'file_progress', tid: it.tid, seq: f.seq });
    this.onChange();
  }
  cancel(it) {
    if (!it) return;
    if (it.state === 'queued') {
      var q = it.dir === 'up' ? this.upQueue : this.downQueue, i = q.indexOf(it);
      if (i >= 0) q.splice(i, 1);
      it.state = 'cancelled'; it.src = null;
      this.onChange();
      return;
    }
    if (it.dir === 'up' && this.up === it) { this.sendJson({ type: 'file_cancel', tid: it.tid }); this._failUp(it, '', 'cancelled'); return; }
    if (it.dir === 'down' && this.down === it) { this.sendJson({ type: 'file_cancel', tid: it.tid }); this._failDown(it, '', 'cancelled'); }
  }
  abortAll(why) {
    var all = this.upQueue.concat(this.downQueue);
    if (this.up) all.push(this.up);
    if (this.down) all.push(this.down);
    for (var i = 0; i < all.length; i++) { var it = all[i]; it.state = 'error'; it.error = why || T('viewer.fileErr.cancelled'); it.src = null; it.hasher = null; it.parts = null; }
    this.upQueue = []; this.downQueue = []; this.up = null; this.down = null;
    if (all.length) this.onChange();
  }
  // Agent -> viewer control messages about transfers. Returns true if handled.
  handle(msg) {
    var tid = msg.tid, it;
    switch (msg.type) {
      case 'file_accept':
        it = this.up;
        if (!it || it.tid !== tid || it.state !== 'begin') return true;
        it.chunk = reachClamp((msg.chunk | 0) || this.limits.chunk, 1024, 4194304);
        it.window = reachClamp((msg.window | 0) || this.limits.window, 1, 64);
        it.path = typeof msg.path === 'string' ? msg.path : '';
        it.state = 'sending';
        this.onChange();
        this._pump();
        return true;
      case 'file_progress':
        it = this.up;
        if (!it || it.tid !== tid) return true;
        var s = Number(msg.seq);
        if (s > it.acked) { it.acked = s; it.done = Math.min(it.size, (s + 1) * it.chunk); }
        this.onChange();
        this._pump();
        return true;
      case 'file_offer':
        it = this.down;
        if (!it || it.tid !== tid) return true;
        if (typeof msg.name === 'string' && msg.name) it.name = msg.name;
        if (Number(msg.size) >= 0) it.size = Number(msg.size);
        it.state = 'receiving';
        this.onChange();
        return true;
      case 'file_done':
        if (this.up && this.up.tid === tid) {
          it = this.up;
          if (msg.ok === false) { this.notify(T('viewer.tr.fileError', { name: it.name, error: reachFileErrorText(msg.code, msg.message) }), 'warn'); this._failUp(it, reachFileErrorText(msg.code, msg.message)); return true; }
          it.state = 'done'; it.done = it.size; it.path = (typeof msg.path === 'string' && msg.path) ? msg.path : (it.path || '');
          it.src = null; it.hasher = null; this.up = null;
          this.notify(it.path ? T('viewer.tr.sentTo', { name: it.name, path: it.path }) : T('viewer.tr.sent', { name: it.name }), 'ok');
          this.onChange();
          this._nextUp();
        } else if (this.down && this.down.tid === tid) {
          this._finishDown(this.down, msg);
        }
        return true;
      case 'file_error':
      case 'file_cancel':
        var cancelled = msg.type === 'file_cancel' || msg.code === 'cancelled';
        var why = msg.type === 'file_cancel' ? T('viewer.tr.cancelledByRemote') : reachFileErrorText(msg.code, msg.message);
        if (this.up && this.up.tid === tid) {
          if (!cancelled) this.notify(T('viewer.tr.fileError', { name: this.up.name, error: why }), 'warn');
          this._failUp(this.up, why, cancelled ? 'cancelled' : 'error');
        } else if (this.down && this.down.tid === tid) {
          if (!cancelled) this.notify(T('viewer.tr.fileError', { name: this.down.name, error: why }), 'warn');
          this._failDown(this.down, why, cancelled ? 'cancelled' : 'error');
        }
        return true;
    }
    return false;
  }
}

// ═════════════════════════════════════════════════════════════════════════
// 2. Session engine (DOM). Uses the globals of the main script at call time:
//    sessionTabs, activeSessionTabId, selectedDevice, remoteWs, remoteDecoder,
//    zoomLevel, perf*, rec*, api(), setZoomLevel(), renderSessionTabs(), ...
// ═════════════════════════════════════════════════════════════════════════

var REACH_ICON = {
  grid: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect width="7" height="7" x="3" y="3" rx="1"/><rect width="7" height="7" x="14" y="3" rx="1"/><rect width="7" height="7" x="14" y="14" rx="1"/><rect width="7" height="7" x="3" y="14" rx="1"/></svg>',
  monitor: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>',
  sound: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.54 8.46a5 5 0 0 1 0 7.07"/><path d="M19.07 4.93a10 10 0 0 1 0 14.14"/></svg>',
  mute: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="23" y1="9" x2="17" y2="15"/><line x1="17" y1="9" x2="23" y2="15"/></svg>',
  upload: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>',
  clipboard: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="8" y="2" width="8" height="4" rx="1"/><path d="M16 4h2a2 2 0 012 2v14a2 2 0 01-2 2H6a2 2 0 01-2-2V6a2 2 0 012-2h2"/></svg>',
  // mouse pointer with a slash: view only
  viewOnly: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M7 3v14l3.5-3.2 2.5 5.4 2-.9-2.5-5.3H18z"/><line x1="3" y1="3" x2="21" y2="21"/></svg>',
  // padlock: the remote user's mouse and keyboard (closed = blocked, open = free)
  lock: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0110 0v4"/></svg>',
  unlock: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 019.9-1"/></svg>',
  chat: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z"/></svg>',
  // a window with an arrow out: open the session in its own window
  popout: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6"/><path d="M10 14L21 3"/><path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6"/></svg>',
  scripts: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/></svg>',
  info: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>',
  // a key: the password vault (vault_ui.go)
  vaultKey: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="7.5" cy="15.5" r="5.5"/><path d="M21 2l-9.6 9.6"/><path d="M15.5 7.5L19 11"/><path d="M18 4l2.5 2.5"/></svg>',
  // three filled keys: Ctrl and Alt on the bottom row, Del higher and further
  // right (beyond Alt), like on the keyboard (the one-click Ctrl+Alt+Del, ui.go)
  cad: '<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><rect x="0.5" y="13" width="9.5" height="9.5" rx="2.4"/><rect x="11.5" y="13" width="9" height="9.5" rx="2.4"/><rect x="15" y="1.5" width="8.5" height="9.5" rx="2.4"/></svg>',
  // a person: the vault types the username too (vault_ui.go)
  vaultUser: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0116 0"/></svg>',
};
// Ctrl+Alt+Del: the system keys' chord (sendSystemKeys sends it as {type:'sas'}).
var REACH_CAD_KEYS = [{ key: 'Control', code: 'ControlLeft' }, { key: 'Alt', code: 'AltLeft' }, { key: 'Delete', code: 'Delete' }];

// Agent / relay codes -> catalog keys (i18n.go); reachCodeText says them.
var REACH_ERROR_TEXT = {
  helper_spawn_failed: 'viewer.err.helperSpawnFailed',
  helper_timeout: 'viewer.err.helperTimeout',
  capture_init_failed: 'viewer.err.captureInitFailed',
  encoder_init_failed: 'viewer.err.encoderInitFailed',
  session_not_found: 'viewer.err.sessionNotFound',
  session_no_display: 'viewer.err.sessionNoDisplay',
  session_logged_off: 'viewer.err.sessionLoggedOff',
  session_switch_timeout: 'viewer.err.sessionSwitchTimeout',
  capture_unavailable: 'viewer.err.captureUnavailable',
  privacy_mode: 'viewer.err.privacyMode',
  internal: 'viewer.err.internal',
  agent_timeout: 'viewer.err.agentTimeout',
  agent_offline: 'viewer.err.agentOffline',
  agent_error: 'viewer.err.agentError',
  session_closed: 'viewer.err.sessionClosed',
  connect_timeout: 'viewer.err.agentTimeout',
  inactivity_timeout: 'viewer.err.inactivityTimeout',
  start_failed: 'viewer.err.startFailed',
  // Hyper-V VM consoles (vm_console.go; the agent's vmconsole_common.go codes)
  vm_off: 'vm.err.off',
  vm_not_found: 'vm.err.notFound',
  vm_not_hyperv: 'vm.err.notHyperV',
  vmconsole_disabled: 'vm.err.disabled',
  vmconsole_install_failed: 'vm.err.installFailed',
  vm_console_start_failed: 'vm.err.startFailed',
  vm_console_connect_failed: 'vm.err.connectFailed',
  vm_console_taken: 'vm.err.taken',
  vm_console_auth: 'vm.err.auth',
  vm_console_closed: 'vm.err.closed',
  vm_console_lost: 'vm.err.lost',
  vm_console_encoder: 'vm.err.encoder',
  vm_console_ended: 'vm.err.ended',
  vm_console_busy: 'vm.err.busy',
  vm_timeout: 'vm.err.timeout',
  relay_refused: 'vm.err.relayRefused',
  // (Obliance server >= 5.1.144: refusals up front / causes of a session over)
  vm_console_agent_outdated: 'vm.err.agentOutdated',
  vm_console_unsupported: 'vm.err.unsupported',
  vm_console_os_unsupported: 'vm.err.osUnsupported',
  vm_console_agent_timeout: 'vm.err.agentTimeout',
  vm_closed_no_picture: 'vm.err.closedNoPicture',
  vm_refused: 'vm.err.refused',
};
var REACH_VIEW_ERROR_TEXT = {
  mosaic_unavailable: 'viewer.viewErr.mosaicUnavailable',
  monitor_not_found: 'viewer.viewErr.monitorNotFound',
  capture_failed: 'viewer.viewErr.captureFailed',
  too_large: 'viewer.viewErr.tooLarge',
  secure_desktop: 'viewer.viewErr.secureDesktop',
};
var REACH_REASON_TEXT = {
  logon: 'viewer.reason.logon',
  logoff: 'viewer.reason.logoff',
  console_change: 'viewer.reason.consoleChange',
  desktop_change: 'viewer.reason.desktopChange',
  disconnected: 'viewer.reason.disconnected',
  reconnected: 'viewer.reason.reconnected',
  helper_restart: 'viewer.reason.helperRestart',
};
// capture_status no_frames reasons (the agent's no-picture watchdog).
var REACH_CAPTURE_STALL_TEXT = {
  rdp_minimized: 'viewer.capture.rdpMinimized',
  rdp_disconnected: 'viewer.capture.rdpDisconnected',
  capture_stalled: 'viewer.capture.stalled',
};
// The text of a code in one of the maps above; '' for an unknown code.
function reachCodeText(map, code) {
  return (code && Object.prototype.hasOwnProperty.call(map, code)) ? T(map[code]) : '';
}
// Why a session stopped (tab.lastError), in the app's language: the text of
// a known code first. The relay and the agent send an English message with
// their code, used only for a code the app does not know (reachErrorOverlay
// still shows it as the detail line). start_failed is the app's own: its
// message is the reason itself (a text of the app, or the server's refusal).
function reachErrorWhy(e) {
  if (!e) return '';
  var t = e.code === 'start_failed' ? '' : reachCodeText(REACH_ERROR_TEXT, e.code);
  return t || (typeof e.message === 'string' ? e.message : '') || '';
}
var REACH_CODEC_LABEL = { h264: 'H.264', vp9: 'VP9', av1: 'AV1', h265: 'H.265', jpeg: 'JPEG' };

var reachTabSeq = 0;
var reachWatchState = null;
var reachWatchTabId = null;
var reachToastTimer = null;
var reachResizeObserver = null;
var reachDecodeBudgetPromise = null;

function reachPref(key, def) { try { var v = localStorage.getItem(key); return v === null ? def : v; } catch (e) { return def; } }
function reachSetPref(key, val) { try { localStorage.setItem(key, val); } catch (e) {} }
function reachClipSyncEnabled() { return reachPref('oblireach_clip_sync', '1') !== '0'; }

// ── Mac keyboard mapping (Settings > Remote sessions) ──────────────────────
// Driving a Mac from this PC: the Windows key acts as Cmd, the characters
// typed are this keyboard's (whatever the Mac's layout), AltGr types its
// character, and with "Ctrl -> Cmd" the Windows shortcuts (Ctrl+C / V / X /
// A / Z / Y / S / F / T / W / N / Q / R / P, Ctrl+arrows, Home / End) do the
// same on the Mac. The agent does the mapping (agent keymap_mac.go); every
// key message carries macMap: 1 mapping, 2 mapping + Ctrl -> Cmd, absent
// none. Automatic (default) = on for an agent that lists key_map_mac (only
// macOS agents do, so a Windows / Linux target never changes) unless this
// computer is a Mac itself; Off = never. Per operator, on this computer.
var REACH_MACKEYS_KEY = 'oblireach_mac_keys';           // 'auto' | 'off'
var REACH_MACKEYS_CTRL_KEY = 'oblireach_mac_keys_ctrl'; // '1' | '0'
function reachMacKeysPolicy() { return reachPref(REACH_MACKEYS_KEY, 'auto') === 'off' ? 'off' : 'auto'; }
function reachSetMacKeysPolicy(v) { reachSetPref(REACH_MACKEYS_KEY, v === 'off' ? 'off' : 'auto'); }
function reachMacKeysCtrlCmd() { return reachPref(REACH_MACKEYS_CTRL_KEY, '1') !== '0'; }
function reachSetMacKeysCtrlCmd(on) { reachSetPref(REACH_MACKEYS_CTRL_KEY, on ? '1' : '0'); }
function reachOperatorIsMac() {
  try { return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || ''); } catch (e) { return false; }
}
// Pure decision: the macMap a key message carries (0 = none).
function reachMacMapMode(policy, ctrlCmd, agentMaps, operatorIsMac) {
  if (policy === 'off' || !agentMaps || operatorIsMac) return 0;
  return ctrlCmd ? 2 : 1;
}
// Stamps a key message for this tab's agent (returns the same object).
function reachKeyMsg(tab, k) {
  var m = reachMacMapMode(reachMacKeysPolicy(), reachMacKeysCtrlCmd(),
    reachHas(tab, REACH.FEATURE.KEY_MAP_MAC), reachOperatorIsMac());
  if (m) k.macMap = m;
  return k;
}

function reachHas(tab, feature) { return !!(tab && tab.features && tab.features.has(feature)); }
function reachCanMosaic(tab) { return reachHas(tab, REACH.FEATURE.MOSAIC) && !!tab.monitors && tab.monitors.length > 1; }
function reachActiveTab() {
  if (typeof sessionTabs === 'undefined') return null;
  for (var i = 0; i < sessionTabs.length; i++) if (sessionTabs[i].id === activeSessionTabId) return sessionTabs[i];
  return null;
}
// DOM of the remote pane if this tab is the one on screen, else null.
function reachUI(tab) {
  if (!tab || tab.id !== activeSessionTabId) return null;
  if (!selectedDevice || !tab.device || !devSame(selectedDevice, tab.device)) return null;
  var canvas = document.getElementById('remote-canvas');
  if (!canvas) return null;
  return {
    canvas: canvas,
    vp: document.getElementById('remote-viewport'),
    status: document.getElementById('remote-status'),
    ph: document.getElementById('remote-placeholder'),
    stop: document.getElementById('stop-btn'),
  };
}
function reachDisplayedTab() { var t = reachActiveTab(); return (t && reachUI(t)) ? t : null; }
// The session on screen, connected and streaming: the only target of the
// toolbar actions (system keys, quick actions, recording mark). Never the
// global remoteWs, which may belong to another device's background tab.
function reachLiveTab() { var t = reachDisplayedTab(); return (t && t.ws && t.ws.readyState === 1 && t.gotInit) ? t : null; }
function reachSend(tab, obj) {
  if (!tab || !tab.ws || tab.ws.readyState !== 1) return false;
  try { tab.ws.send(JSON.stringify(obj)); return true; } catch (e) { return false; }
}

function reachToast(text, kind) {
  var el = document.getElementById('reach-toast');
  if (!el) { if (typeof setStatus === 'function') setStatus(text); return; }
  el.textContent = text;
  el.className = 'reach-toast visible' + (kind ? ' ' + kind : '');
  clearTimeout(reachToastTimer);
  reachToastTimer = setTimeout(function () { el.classList.remove('visible'); }, 4500);
}
// A message that must be seen: the toast of the picture when one is on
// screen, else a toast over the page (Devices list, Dashboard), and the
// status bar (reachToast alone falls back to the status bar only).
var reachPageToastTimer = null;
function reachNotify(text, kind) {
  if (document.getElementById('reach-toast')) { reachToast(text, kind); return; }
  if (typeof setStatus === 'function') { try { setStatus(text); } catch (e) {} }
  var el = document.getElementById('reach-page-toast');
  if (!el) {
    el = document.createElement('div'); el.id = 'reach-page-toast';
    el.setAttribute('role', 'status');
    if (!document.body) return;
    document.body.appendChild(el);
  }
  el.textContent = text;
  el.className = 'reach-toast reach-page-toast visible' + (kind ? ' ' + kind : '');
  clearTimeout(reachPageToastTimer);
  reachPageToastTimer = setTimeout(function () { el.classList.remove('visible'); }, 5000);
}

// ── Per-tab state ──────────────────────────────────────────────────────────
function reachNewTab(device) {
  var tab = { id: 'tab_' + (++reachTabSeq), device: device, ws: null, decoder: null, ts: 0, label: device.hostname,
    reconnectAttempts: 0, userStopped: false, noAutoReconnect: false, audioOn: false, audio: null, wantMosaic: undefined,
    viewOnly: false, pasteCancel: 0, // viewOnly: operator choice, kept across reconnects
    codecWant: '', // video codec chosen in this tab (reach_codec.go), applied again after a reconnect
    wantInputBlock: false }; // operator asked to block the remote user's input: asked again after a reconnect
  reachResetConnState(tab);
  return tab;
}
// Everything that belongs to one connection (reset before each attempt).
function reachResetConnState(tab) {
  tab.gen = null;
  tab.sessionToken = null; // password vault (vault_ui.go): stale until the next session answers
  tab.features = new Set();
  tab.filesDenied = null;
  tab.domain = ''; // (the next init says it again)
  tab.monitors = [];
  tab.monitor = null;
  tab.view = null;
  tab.agentW = 0; tab.agentH = 0; tab.fps = 15;
  tab.codec = 'h264';
  tab.gotInit = false; tab.initCount = 0;
  tab.captureStall = null; // capture_status no_frames reason, while the agent reports no picture
  tab.vmState = null; tab.vmTyping = null; tab.vmVaultTyping = false; // (a VM console: vm_console.go)
  tab.paired = false;
  tab.sessionState = null;
  tab.sessionInfo = null;
  tab.lastError = null;
  tab.viewPending = false;
  // Remote user's input blocked (as the agent confirmed it): the agent ends
  // the block with its stream, so a new connection always starts unblocked.
  tab.inputBlocked = false;
  // Lock the session at the end (session_lock_state): the agent says it
  // again after the next init.
  tab.lockState = null;
  // Presence and blank screen belong to one relay (reach_presence.go).
  reachPresenceReset(tab);
  // Resolution selector (reach_resolution.go): asked again after the next init.
  tab.res = null;
  if (tab.resPending && tab.resPending.timer) clearTimeout(tab.resPending.timer);
  tab.resPending = null;
  // Codec selector (reach_codec.go): the agent's list comes with the next init.
  reachCodecReset(tab);
  tab.keyQueue = null;
  // Keys / mouse buttons pressed in this session and not released yet (a new
  // connection starts with nothing pressed): released when view only turns on.
  tab.keysHeld = {};
  tab.buttonsHeld = {};
  tab.copyPending = false;
  tab.needKey = true;
  tab.ts = 0;
  tab.shown = false;
  tab.delivery = 'live';
  tab.kaLast = 0; // last keepalive (reachNow), set at init: a new stream is activity
  // lastSentText / lastRemoteText: last text each side got from the other
  // (echo and "last copy wins" guards); lastFilesSeq: local clipboard already
  // uploaded by a Ctrl+V. staged: local text found when the session came on
  // screen, sent only on a paste gesture; stage: bumped by every clipboard
  // event, so a slow read never stages an outdated text.
  tab.clip = { seq: 0, lastSentText: null, lastRemoteText: null, getPending: null, lastFilesSeq: -1, lastTooLargeSeq: -1, remoteNewer: false,
    staged: null, stage: 0 };
  tab.clipLimits = null;
  tab.remoteFiles = [];
  tab.overlay = null;
  tab.statusText = '';
  if (tab.decoder) { try { tab.decoder.close(); } catch (e) {} }
  tab.decoder = null;
  if (tab.transfers) tab.transfers.abortAll(T('viewer.tr.connectionClosed'));
  tab.transfers = new ReachTransfers({
    sendJson: function (m) { return reachSend(tab, m); },
    sendBinary: function (u8) { if (tab.ws && tab.ws.readyState === 1) { tab.ws.send(u8); return true; } return false; },
    onChange: function () { reachRenderTransfers(tab); },
    save: function (blob, name) { reachSaveBlob(blob, name); },
    notify: function (text, kind) { if (reachUI(tab)) reachToast(text, kind); },
  });
  if (tab.audio) tab.audio.reset();
}

// ── Status, overlays, idle/stream UI ───────────────────────────────────────
function reachSetStatus(tab, text) {
  tab.statusText = text;
  var ui = reachUI(tab);
  if (ui && ui.status) ui.status.textContent = text;
}
function reachStreamStatus(tab) {
  var s = tab.agentW + '\u00D7' + tab.agentH + ' @ ' + (tab.fps || 15) + 'fps \u00B7 ' + (REACH_CODEC_LABEL[tab.codec] || 'H.264');
  if (reachIsMosaic(tab.view)) s += ' \u00B7 ' + T('viewer.status.allScreens');
  else if (tab.monitors.length > 1 && typeof tab.monitor === 'number' && tab.monitor >= 0) s += ' \u00B7 ' + T('viewer.status.screenOf', { n: tab.monitor + 1, total: tab.monitors.length });
  return s;
}
function reachShowOverlay(tab, kind, title, text, actions) {
  tab.overlay = { kind: kind, title: title, text: text || '', actions: actions || [] };
  reachRenderOverlay(tab);
}
function reachHideOverlay(tab) { tab.overlay = null; reachRenderOverlay(tab); }
function reachRenderOverlay(tab) {
  var ui = reachUI(tab);
  var el = document.getElementById('reach-overlay');
  if (!ui || !el) return;
  var o = tab.overlay;
  el.innerHTML = '';
  if (!o) { el.style.display = 'none'; return; }
  el.className = 'reach-overlay ' + o.kind;
  el.style.display = 'flex';
  var box = document.createElement('div'); box.className = 'reach-overlay-box';
  if (o.kind === 'busy') { var sp = document.createElement('div'); sp.className = 'reach-spinner'; box.appendChild(sp); }
  var t = document.createElement('div'); t.className = 'reach-overlay-title'; t.textContent = o.title; box.appendChild(t);
  if (o.text) { var p = document.createElement('div'); p.className = 'reach-overlay-text'; p.textContent = o.text; box.appendChild(p); }
  if (o.actions.length) {
    var row = document.createElement('div'); row.className = 'reach-overlay-actions';
    o.actions.forEach(function (a) {
      var b = document.createElement('button');
      b.className = 'btn-sm' + (a.primary ? ' reach-btn-primary' : '');
      b.textContent = a.label;
      b.addEventListener('click', function (ev) { ev.stopPropagation(); a.run(); });
      row.appendChild(b);
    });
    box.appendChild(row);
  }
  el.appendChild(box);
}
function reachShowStopButton(tab, on) { var ui = reachUI(tab); if (ui && ui.stop) ui.stop.style.display = on ? '' : 'none'; }
// ── Auto-hiding toolbar + focus mode ───────────────────────────────────────
// While a picture is on screen the connection toolbar floats over its top
// edge and hides itself; it comes back when the pointer rests on the very top
// of the pane (or stays on the toolbar), like the RDP connection bar. The pin
// keeps it in place (per-viewer preference). Focus mode also hides the app
// chrome (top bar, session tabs, device header, menu, status bar).
var reachTbAuto = reachPref('oblireach_tb_auto', '1') !== '0';
var reachTbTimer = null;
function reachToolbarState(streaming) {
  var pane = document.getElementById('remote-pane');
  if (!pane) return;
  var on = !!streaming && reachTbAuto;
  pane.classList.toggle('tb-auto', on);
  if (!on) pane.classList.remove('tb-show');
  var pin = document.getElementById('tb-pin');
  if (pin) { pin.classList.toggle('on', !reachTbAuto); pin.title = reachTbAuto ? T('viewer.pinToolbar') : T('viewer.unpinToolbar'); }
  var fb = document.getElementById('tb-focus');
  if (fb) { var f = document.body.classList.contains('reach-focus'); fb.classList.toggle('on', f); fb.title = f ? T('viewer.leaveFocus') : T('viewer.focus'); }
}
function reachToolbarRefresh() { var t = reachDisplayedTab(); reachToolbarState(!!(t && t.shown)); }
function reachWireToolbarAuto(pane, toolbar) {
  pane.addEventListener('mousemove', function (e) {
    if (!pane.classList.contains('tb-auto') || pane.classList.contains('tb-show')) return;
    if (e.clientY - pane.getBoundingClientRect().top <= 4) {
      if (!reachTbTimer) reachTbTimer = setTimeout(function () { reachTbTimer = null; pane.classList.add('tb-show'); }, 250);
    } else if (reachTbTimer) { clearTimeout(reachTbTimer); reachTbTimer = null; }
  });
  function hideSoon() {
    setTimeout(function () {
      // Stays for an open menu or a focused select / input only: WebView2
      // focuses every clicked button, so "focus inside" would keep it forever.
      var ae = document.activeElement;
      var inBar = !!ae && toolbar.contains(ae);
      // (an open page-drawn list, dropdown.go, lives outside the toolbar: its trigger is inside)
      var oddIn = typeof oddOpenSt !== 'undefined' && !!oddOpenSt && toolbar.contains(oddOpenSt.btn);
      if (oddIn || toolbar.matches(':hover') || toolbar.querySelector('.toolbar-dropdown-menu.open') || (inBar && (ae.tagName === 'SELECT' || ae.tagName === 'INPUT'))) return;
      // A hidden button keeps no focus (Space / Enter would press it unseen).
      if (inBar && ae.blur) ae.blur();
      pane.classList.remove('tb-show');
    }, 600);
  }
  toolbar.addEventListener('mouseleave', hideSoon);
  // A click in the picture puts the toolbar away (it was left open by a menu or a select).
  pane.addEventListener('mousedown', function (e) { if (pane.classList.contains('tb-show') && !toolbar.contains(e.target)) hideSoon(); }, true);
}
function reachToggleFocus() {
  if (reachDetachedMode) return; // a detached window is always in focus mode
  document.body.classList.toggle('reach-focus');
  reachToolbarRefresh();
}
function reachToggleToolbarPin() { reachTbAuto = !reachTbAuto; reachSetPref('oblireach_tb_auto', reachTbAuto ? '1' : '0'); reachToolbarRefresh(); }

function reachShowStreamUI(tab) {
  var ui = reachUI(tab);
  if (!ui) return;
  tab.shown = true;
  ui.canvas._reachTabId = tab.id; // whose picture this canvas holds (reachStashPicture)
  reachToolbarState(true);
  ui.canvas.style.display = 'block';
  if (ui.ph) ui.ph.style.display = 'none';
  reachShowStopButton(tab, true);
  // Focus left on a toolbar button (Connect...) moves to the picture: keys go
  // to the session, not to a button the auto-hidden toolbar no longer shows.
  var ae = document.activeElement;
  if (ae && ae.closest && ae.closest('.remote-toolbar')) {
    try { ui.canvas.focus({ preventScroll: true }); } catch (e) { try { ae.blur(); } catch (x) {} }
  }
  reachKeepaliveTick(); // on screen again (tab switch, back to its page): due keepalive now
}
function reachShowIdleUI(tab) {
  tab.shown = false;
  var ui = reachUI(tab);
  if (!ui) return;
  reachToolbarState(false);
  ui.canvas.style.display = 'none';
  if (ui.ph) ui.ph.style.display = 'flex';
  reachShowStopButton(tab, !!(tab.ws || tab.reconnectTimer));
  reachRefreshToolbar(tab);
  reachRenderTiles(tab);
  reachRenderTransfers(tab);
}

// Session a reconnect / Retry asks for. A console target asks for "the
// console" again (undefined: the agent resolves the console at that moment):
// a sign-in on the console that reconnects a user's disconnected session
// gives the console ANOTHER session id, and the old one no longer exists.
function reachReconnectSessionId(tab) {
  var p = tab && tab.reconnectParams;
  if (!p || p.wtsSessionId === undefined || p.wtsSessionId === null) return undefined;
  var sessions = reachDevSessions(p.device);
  for (var i = 0; i < sessions.length; i++) {
    if (sessions[i].id === p.wtsSessionId) return sessions[i].isConsole ? undefined : p.wtsSessionId;
  }
  return p.wtsSessionId;
}

function reachActRetry(tab) {
  return { label: T('common.retry'), primary: true, run: function () {
    tab.reconnectAttempts = 0;
    tab.vmAutoReconnects = 0; // (a VM console: an explicit Retry gives a fresh, bounded budget)
    tab.vmTakeover = false; tab.vmPrivacyAsked = false; // (a VM console: asked again: vm_console.go)
    startRemote(reachReconnectSessionId(tab), tab);
  } };
}
function reachActClose(tab, label) {
  return { label: label || T('common.close'), run: function () {
    // A detached window has no Connect button: a session stopped from its
    // overlay closes its tab, like the tab's own × (the last one: the window).
    if (reachDetachedMode) { if (sessionTabs.indexOf(tab) >= 0) closeSessionTab(tab.id); return; }
    reachStopTab(tab);
    reachHideOverlay(tab);
    reachShowIdleUI(tab);
  } };
}
function reachActCancel(tab) { return reachActClose(tab, T('common.cancel')); }
function reachActDisconnect(tab) { return reachActClose(tab, T('common.disconnect')); }
function reachActPickSession(tab) {
  return { label: T('viewer.pickAnother'), run: function () {
    reachStopTab(tab);
    reachHideOverlay(tab);
    reachShowIdleUI(tab);
    // A detached window has no session selector: the picker (the device's
    // sessions as they are now), then that session in the same tab.
    if (reachDetachedMode) { reachDetachedReconnect(tab, true); return; }
    var sel = document.querySelector('#remote-pane .session-select');
    if (sel) { sel.focus(); try { if (sel.showPicker) sel.showPicker(); } catch (e) {} }
  } };
}
function reachErrorOverlay(tab) {
  if (devIsSupport(tab.device)) { supErrorOverlay(tab); return; } // (no Retry: support_page.go)
  var e = tab.lastError || {};
  var title = reachCodeText(REACH_ERROR_TEXT, e.code) || T('viewer.stopped');
  var detail = (e.message && e.message !== title) ? e.message : '';
  var acts = [];
  if (e.code === 'session_logged_off' || e.code === 'session_not_found' || e.code === 'session_no_display' || e.code === 'session_switch_timeout') acts.push(reachActPickSession(tab));
  acts.push(reachActRetry(tab));
  acts.push(reachActClose(tab));
  reachShowOverlay(tab, 'error', title, detail, acts);
}

// ── Connection lifecycle ───────────────────────────────────────────────────
function reachArmConnectTimer(tab, ms) {
  reachClearConnectTimer(tab);
  tab.connectTimer = setTimeout(function () {
    tab.connectTimer = null;
    if (!tab.ws || tab.gotInit || tab.sessionState) return;
    if (tab.paired) {
      // The agent side is connected but never started the stream: keep the
      // connection, let the operator decide.
      reachShowOverlay(tab, 'info', T('viewer.noPictureTitle'),
        T('viewer.noPictureText'),
        [reachActRetry(tab), reachActDisconnect(tab)]);
      return;
    }
    tab.noAutoReconnect = true;
    tab.lastError = { code: devIsVm(tab.device) ? 'vm_timeout' : 'connect_timeout', message: '', retry: false };
    try { tab.ws.close(); } catch (e) {}
  }, ms);
}
function reachClearConnectTimer(tab) {
  if (tab && tab.connectTimer) { clearTimeout(tab.connectTimer); tab.connectTimer = null; }
}

async function startRemote(wtsSessionId, tabArg) {
  var tab = tabArg || null;
  if (!tab) {
    if (!selectedDevice) return;
    tab = addSessionTab(selectedDevice);
    tab.reconnectAttempts = 0;
  }
  if (sessionTabs.indexOf(tab) < 0) return;
  if (devIsSupport(tab.device)) { supRestart(tab); return; } // (a code session never starts again: support_page.go)
  var device = tab.device;
  tab.reconnectParams = { wtsSessionId: wtsSessionId, device: device };
  reachDetachedSaveState(tab); // a detached window: its Windows session, for "Bring it back here"
  if (typeof tab.reconnectAttempts !== 'number') tab.reconnectAttempts = 0;
  tab.userStopped = false;
  tab.noAutoReconnect = false;
  if (tab.reconnectTimer) { clearTimeout(tab.reconnectTimer); tab.reconnectTimer = null; }
  if (tab.ws) { var old = tab.ws; tab.ws = null; try { old.close(); } catch (e) {} }
  reachClearConnectTimer(tab);
  reachResetConnState(tab);
  tab.vmPictured = false; tab.vmOpened = false; // (this connection: a VM console never reconnects without a picture)
  if (tab.id === activeSessionTabId) { remoteWs = null; remoteDecoder = null; }
  reachRefreshToolbar(tab); // not live any more: system keys / quick actions off
  var attempt = tab.reconnectAttempts, max = REACH.RECONNECT_DELAYS_MS.length;
  var title = attempt > 0 ? T('viewer.reconnecting', { n: attempt, max: max }) : T('viewer.starting');
  reachSetStatus(tab, attempt > 0 ? title : T('viewer.startingShort'));
  reachShowOverlay(tab, 'busy', title, '', [reachActCancel(tab)]);
  reachShowStopButton(tab, true);
  renderSessionTabs();
  var token = tab.startToken = (tab.startToken || 0) + 1;
  try {
    var vmc = devIsVm(device); // (a Hyper-V VM console of the host: vm_console.go)
    var body = vmc ? vmStartBody(device, tab) : { deviceId: device.id, protocol: 'oblireach' };
    if (!vmc && wtsSessionId !== undefined && wtsSessionId !== null) body.sessionId = wtsSessionId;
    // The device's server, its session aligned on the device's tenant in
    // multi-server mode (ui.go devApi, tenantpin.go).
    var r = await devApi(device, 'POST', '/api/remote/sessions', body, true);
    var d = null;
    try { d = await r.json(); } catch (e) {}
    if (tab.userStopped || tab.startToken !== token || sessionTabs.indexOf(tab) < 0) return;
    if (!r.ok) {
      if (r.status === 401 && d && d.twoFactorRequired) { tab.noAutoReconnect = true; throw new Error(T('viewer.tfaRequired')); }
      if (vmc) {
        // The server's refusal: taken over (an administrator, confirmed), the
        // privacy code asked once, else said; never retried on its own.
        tab.noAutoReconnect = true;
        var why = await vmStartRefused(tab, r.status, d);
        if (tab.userStopped || tab.startToken !== token || sessionTabs.indexOf(tab) < 0) return;
        if (why === 'retry') { startRemote(wtsSessionId, tab); return; }
        throw new Error(why);
      }
      // 4xx = the server refused (permission, restriction, agent registered
      // with another API key...): retrying the same request cannot help.
      if (r.status >= 400 && r.status < 500 && r.status !== 408 && r.status !== 429) tab.noAutoReconnect = true;
      throw new Error((d && d.error) || ('HTTP ' + r.status));
    }
    if (r.status === 202 || (d && d.data && d.data.status === 'pending_approval')) {
      tab.noAutoReconnect = true;
      throw new Error(T('viewer.pendingApproval'));
    }
    var session = d && d.data;
    // (a plain token only: it goes in the tunnel's path, never '..' or '/')
    if (!session || !session.sessionToken || !/^[A-Za-z0-9_-]{1,512}$/.test(String(session.sessionToken))) throw new Error(T('viewer.noToken'));
    // Kept for the password vault (vault_ui.go): the exact token the proxy
    // registered the scope of this session under (reach_tunnels.go).
    tab.sessionToken = session.sessionToken;
    tab.delivery = session.delivery || 'live';
    tab.vmTakeover = false; tab.vmPrivacyAsked = false; // (a VM console: once per opening)
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    // (the token belongs to the device's server: its tunnel goes there)
    var ws = new WebSocket(proto + '//' + location.host + devBase(device, false) + '/api/remote/tunnel/' + encodeURIComponent(session.sessionToken));
    ws.binaryType = 'arraybuffer';
    tab.ws = ws;
    // A background (auto-)reconnect must not point remoteWs at this tab while
    // another device's page is on screen.
    if (reachUI(tab)) remoteWs = ws;
    reachSetStatus(tab, T('viewer.connecting'));
    if (vmc) {
      reachShowOverlay(tab, 'busy', T('vm.wait.openingTitle'), T('vm.wait.openingText'), [reachActCancel(tab)]);
    } else if (tab.delivery === 'queued') {
      reachShowOverlay(tab, 'busy', T('viewer.waitingAgent'), T('viewer.queuedText'), [reachActCancel(tab)]);
    } else {
      reachShowOverlay(tab, 'busy', title, T('viewer.openingTunnel'), [reachActCancel(tab)]);
    }
    reachArmConnectTimer(tab, vmc ? VM_CONNECT_TIMEOUT_MS : tab.delivery === 'queued' ? REACH.QUEUED_TIMEOUT_MS : REACH.CONNECT_TIMEOUT_MS);
    ws.onopen = function () {
      if (tab.ws !== ws) return;
      tab.vmOpened = true;
      renderSessionTabs();
    };
    ws.onclose = function (ev) {
      if (tab.ws !== ws) return; // replaced or disposed: nothing to do
      console.log('[reach] ws close tab=' + tab.id + ' code=' + (ev && ev.code) + ' clean=' + (ev && ev.wasClean) +
        ' userStopped=' + tab.userStopped + ' attempts=' + tab.reconnectAttempts);
      reachConnClosed(tab);
    };
    ws.onerror = function () { if (tab.ws === ws && !tab.gotInit) reachSetStatus(tab, T('viewer.wsError')); };
    ws.onmessage = function (ev) { if (tab.ws === ws) handleRemoteMessage(ev, tab); };
  } catch (err) {
    if (tab.userStopped || tab.startToken !== token) return;
    var msg = (err && err.message) || String(err);
    reachSetStatus(tab, T('viewer.errorStatus', { error: msg }));
    tab.lastError = { code: 'start_failed', message: msg, retry: !tab.noAutoReconnect };
    if (tab.noAutoReconnect || !scheduleRemoteReconnect(tab)) {
      reachShowOverlay(tab, 'error', T('viewer.couldNotStart'), msg, [reachActRetry(tab), reachActClose(tab)]);
      reachShowStopButton(tab, false);
    }
  }
}

// Reconnect budget: 8 attempts, backoff 2,2,3,5,5,8,8,10 s.
function scheduleRemoteReconnect(tab) {
  if (!tab || tab.userStopped || tab.noAutoReconnect || sessionTabs.indexOf(tab) < 0) return false;
  var delays = REACH.RECONNECT_DELAYS_MS;
  var n = tab.reconnectAttempts | 0;
  if (n >= delays.length) { tab.reconnectAttempts = 0; return false; }
  var delay = delays[n];
  tab.reconnectAttempts = n + 1;
  var why = reachErrorWhy(tab.lastError) || T('viewer.interrupted');
  reachSetStatus(tab, T('viewer.reconnecting', { n: tab.reconnectAttempts, max: delays.length }));
  reachShowOverlay(tab, 'busy', T('viewer.reconnectingIn', { n: tab.reconnectAttempts, max: delays.length, s: Math.round(delay / 1000) }), why, [reachActCancel(tab)]);
  tab.reconnectTimer = setTimeout(function () {
    tab.reconnectTimer = null;
    if (tab.userStopped || sessionTabs.indexOf(tab) < 0) return;
    startRemote(reachReconnectSessionId(tab), tab);
  }, delay);
  reachShowStopButton(tab, true);
  return true;
}

// Common cleanup after the tunnel of a tab closed (or was abandoned).
function reachConnClosed(tab) {
  var ws = tab.ws;
  tab.ws = null;
  if (remoteWs === ws || tab.id === activeSessionTabId) { remoteWs = null; remoteDecoder = null; }
  reachClearConnectTimer(tab);
  if (tab.decoder) { try { tab.decoder.close(); } catch (e) {} tab.decoder = null; }
  if (tab.transfers) tab.transfers.abortAll(T('viewer.tr.connectionClosed'));
  if (tab.audio) tab.audio.reset();
  tab.remoteFiles = [];
  tab.gotInit = false;
  // The stream ended: the agent released the block. Said when it was not the
  // operator's doing (the next connection asks for it again: wantInputBlock).
  if (tab.inputBlocked && !tab.userStopped && reachUI(tab)) reachToast(T('viewer.inputBlock.off'), 'warn');
  tab.inputBlocked = false;
  reachBlankConnClosed(tab); // (the agent removed the page with its stream: reach_presence.go)
  tab.presence = null;
  if (reachUI(tab)) {
    stopPerfHudTimer();
    if (recMediaRecorder) toggleRecording();
  }
  renderSessionTabs();
  reachUpdateClipboardWatch();
  // A code session (support_page.go): its end, never a reconnection.
  if (devIsSupport(tab.device)) { supConnClosed(tab); return; }
  if (tab.userStopped) {
    tab.overlay = null;
    reachRenderOverlay(tab);
    reachShowIdleUI(tab);
    reachSetStatus(tab, T('viewer.disconnected'));
    return;
  }
  reachShowIdleUI(tab);
  if (tab.lastError && tab.lastError.retry === false) {
    reachErrorOverlay(tab);
    reachSetStatus(tab, T('viewer.disconnected'));
    return;
  }
  // Session following agents keep the tunnel open across session changes:
  // a close while a session change was pending is not retried blindly.
  var pending = reachHas(tab, REACH.FEATURE.SESSION_FOLLOW) && tab.sessionState &&
    (tab.sessionState.state === 'switching' || tab.sessionState.state === 'no_display');
  // A VM console never reconnects in a loop: every reconnection is a NEW
  // session on the host. A close without any picture (helper that cannot
  // start, an upgrade refused because the host already ended the session,
  // an old agent's bare close) is not retried; after pictures, at most
  // VM_AUTO_RECONNECT_MAX times for the whole tab (reset by Retry only).
  if (devIsVm(tab.device) && !tab.noAutoReconnect && !pending &&
      (!tab.vmPictured || (tab.vmAutoReconnects | 0) >= VM_AUTO_RECONNECT_MAX)) {
    if (!tab.lastError) {
      tab.lastError = { code: tab.vmPictured ? 'vm_console_lost' : tab.vmOpened ? 'vm_closed_no_picture' : 'vm_refused', message: '', retry: false };
    }
    reachShowOverlay(tab, 'error', T('viewer.disconnected'),
      reachErrorWhy(tab.lastError) || T('viewer.lost'),
      [reachActRetry(tab), reachActClose(tab)]);
    reachSetStatus(tab, T('viewer.disconnected'));
    return;
  }
  if (devIsVm(tab.device)) tab.vmAutoReconnects = (tab.vmAutoReconnects | 0) + 1;
  if (tab.noAutoReconnect || pending) {
    reachShowOverlay(tab, 'error', T('viewer.disconnected'),
      reachErrorWhy(tab.lastError) || T('viewer.lost'),
      [reachActRetry(tab), reachActClose(tab)]);
    reachSetStatus(tab, T('viewer.disconnected'));
    return;
  }
  if (!scheduleRemoteReconnect(tab)) {
    // The budget ran out (no picture since the first attempt: the budget is
    // reset by a picture, not by an init): stop, with the last reason known.
    reachShowOverlay(tab, 'error', T('viewer.disconnected'), reachErrorWhy(tab.lastError) || T('viewer.lostForGood'), [reachActRetry(tab), reachActClose(tab)]);
    reachSetStatus(tab, T('viewer.disconnected'));
  }
}

function reachStopTab(tab) {
  if (!tab) return;
  tab.userStopped = true;
  tab.reconnectAttempts = 0;
  tab.startToken = (tab.startToken || 0) + 1; // abandons an in-flight POST
  if (tab.reconnectTimer) { clearTimeout(tab.reconnectTimer); tab.reconnectTimer = null; }
  reachClearConnectTimer(tab);
  reachReleaseInputBlock(tab); // the remote user gets the mouse and keyboard back first
  var ws = tab.ws;
  if (ws) { try { ws.close(); } catch (e) {} } // onclose runs the cleanup
  else reachConnClosed(tab);
}

// Stop the session shown on screen (toolbar Disconnect, leaving the remote
// tab). A session of ANOTHER device kept in a background tab is left alone.
function stopRemote() {
  var tab = reachActiveTab();
  if (tab && selectedDevice && devSame(tab.device, selectedDevice)) { reachStopTab(tab); return; }
  stopPerfHudTimer();
  if (recMediaRecorder) toggleRecording();
}

// Releases everything a tab holds (tab closed, sign-out). No UI updates.
function reachDisposeTab(tab) {
  if (!tab) return;
  tab.userStopped = true;
  tab.startToken = (tab.startToken || 0) + 1;
  if (tab.reconnectTimer) { clearTimeout(tab.reconnectTimer); tab.reconnectTimer = null; }
  reachClearConnectTimer(tab);
  reachReleaseInputBlock(tab); // the remote user gets the mouse and keyboard back first
  var ws = tab.ws;
  tab.ws = null;
  if (ws) { try { ws.close(); } catch (e) {} }
  if (remoteWs === ws) { remoteWs = null; remoteDecoder = null; }
  if (tab.decoder) { try { tab.decoder.close(); } catch (e) {} tab.decoder = null; }
  if (tab.transfers) tab.transfers.abortAll(T('viewer.tr.sessionClosed'));
  if (tab.audio) { tab.audio.close(); tab.audio = null; }
  tab.lastCanvas = null;
}
function reachDisposeAllTabs() {
  sessionTabs.slice().forEach(reachDisposeTab);
  sessionTabs.length = 0;
  activeSessionTabId = null;
  remoteWs = null; remoteDecoder = null;
  stopPerfHudTimer();
  if (recMediaRecorder) toggleRecording();
  renderSessionTabs();
  reachUpdateClipboardWatch();
}

// ── Keepalive (agents advertising "keepalive") ─────────────────────────────
// The agent ends a stream after 10 minutes without input, and a session that
// is only watched (view only, or just looked at) sends none. The viewer then
// sends {type:'keepalive'} (resets that timer, injects nothing, no answer)
// every REACH.KEEPALIVE_MS, as the operator chose in Settings > Remote
// sessions (localStorage: shared by the main window and the detached ones):
//   'always'  while the session is open, even minimized or in a tab not shown;
//   'visible' (default) while its picture is on screen: the page visible and
//             the session tab displayed (a detached window shows its own one);
//   'never'   nothing (the behaviour before).
// Older agents (no "keepalive" in init.features) get nothing new. A session
// shown again more than a period after its last keepalive gets one at once:
// one hidden for 9 minutes must not be cut seconds after it is shown.
var REACH_KEEPALIVE_KEY = 'oblireach_keepalive';
var REACH_KEEPALIVE_TICK_MS = 10000;
var reachKeepaliveTimer = null;
var reachKeepaliveSeen = true; // page on screen at the last check (resize)
function reachKeepalivePolicy() {
  var v = reachPref(REACH_KEEPALIVE_KEY, 'visible');
  return (v === 'always' || v === 'never') ? v : 'visible';
}
function reachSetKeepalivePolicy(v) {
  reachSetPref(REACH_KEEPALIVE_KEY, (v === 'always' || v === 'never') ? v : 'visible');
  reachKeepaliveTick();
}
// Monotonic when available: a clock change must not stop the keepalives.
function reachNow() { return (typeof performance !== 'undefined' && performance && typeof performance.now === 'function') ? performance.now() : Date.now(); }
// The page is on screen: not hidden, and not minimized (WebView2 may keep the
// page of a minimized window 'visible'; its client area is then 0x0).
function reachPageVisible() {
  if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return false;
  if (typeof window !== 'undefined' && typeof window.innerWidth === 'number' && typeof window.innerHeight === 'number' &&
      (window.innerWidth <= 0 || window.innerHeight <= 0)) return false;
  return true;
}
// Streaming from an agent that understands keepalive.
function reachKeepaliveCapable(tab) {
  return !!(tab && tab.ws && tab.ws.readyState === 1 && tab.gotInit && reachHas(tab, REACH.FEATURE.KEEPALIVE));
}
function reachKeepaliveWanted(tab, policy) {
  if (!reachKeepaliveCapable(tab) || policy === 'never') return false;
  if (policy === 'always') return true;
  return reachPageVisible() && !!reachUI(tab);
}
// Every tick, and at once when a session comes on screen (tab shown, page
// visible again): each session that wants one and had none for a period.
function reachKeepaliveTick() {
  if (typeof sessionTabs === 'undefined') return;
  var policy = reachKeepalivePolicy(), now = reachNow(), any = false;
  for (var i = 0; i < sessionTabs.length; i++) {
    var t = sessionTabs[i];
    if (!reachKeepaliveCapable(t)) continue;
    any = true;
    if (!reachKeepaliveWanted(t, policy)) continue;
    // Half a tick of slack: a tick landing a hair early does not delay the send a whole tick.
    if (now - (t.kaLast || 0) < REACH.KEEPALIVE_MS - REACH_KEEPALIVE_TICK_MS / 2) continue;
    if (reachSend(t, { type: 'keepalive' })) t.kaLast = now;
  }
  // Nothing left that could want one: the timer stops (armed again by the next init).
  if (!any && reachKeepaliveTimer !== null) { clearInterval(reachKeepaliveTimer); reachKeepaliveTimer = null; }
}
function reachKeepaliveArm() {
  if (reachKeepaliveTimer === null) reachKeepaliveTimer = setInterval(reachKeepaliveTick, REACH_KEEPALIVE_TICK_MS);
}
(function () {
  if (typeof document !== 'undefined' && document && typeof document.addEventListener === 'function') {
    document.addEventListener('visibilitychange', function () { reachKeepaliveSeen = reachPageVisible(); reachKeepaliveTick(); });
  }
  // Restored from minimized: a resize to a real size (checked on the
  // transition only: a drag resizes many times).
  if (typeof window !== 'undefined' && window && typeof window.addEventListener === 'function') {
    window.addEventListener('resize', function () {
      var v = reachPageVisible(), was = reachKeepaliveSeen;
      reachKeepaliveSeen = v;
      if (v && !was) reachKeepaliveTick();
    });
  }
})();

// ── Incoming messages ──────────────────────────────────────────────────────
function handleRemoteMessage(event, tab) {
  tab = tab || reachActiveTab();
  if (!tab) return;
  if (typeof event.data === 'string') {
    var msg = null;
    try { msg = JSON.parse(event.data); } catch (e) { return; }
    if (!msg || typeof msg !== 'object') return;
    try { reachOnText(tab, msg); } catch (e) { console.warn('[reach] handler failed for', msg.type, e); }
    return;
  }
  if (event.data instanceof ArrayBuffer) reachOnBinary(tab, new Uint8Array(event.data));
}

function reachOnText(tab, msg) {
  switch (msg.type) {
    case 'paired':
      tab.paired = true;
      if (!tab.gotInit && devIsVm(tab.device)) { vmOnPaired(tab); return; } // (a VM console: vm_console.go)
      if (!tab.gotInit && !tab.sessionState) {
        reachSetStatus(tab, T('viewer.waitingStream'));
        reachShowOverlay(tab, 'busy', T('viewer.starting'), T('viewer.deviceAnswered'), [reachActCancel(tab)]);
      }
      return;
    case 'waiting':
      if (!tab.paired && !tab.gotInit) {
        reachSetStatus(tab, T('viewer.waitingAgentShort'));
        reachShowOverlay(tab, 'busy', T('viewer.waitingAgent'),
          tab.delivery === 'queued' ? T('viewer.queuedText') : T('viewer.agentOpening'),
          [reachActCancel(tab)]);
      }
      return;
    case 'init': reachOnInit(tab, msg); return;
    case 'vm_state': vmOnState(tab, msg); return; // (a Hyper-V VM console's progress: vm_console.go)
    case 'codec_switch': reachOnCodecSwitch(tab, msg.codec); return;
    case 'session_state': reachOnSessionState(tab, msg); return;
    case 'error': reachOnError(tab, msg); return;
    case 'view_error': reachOnViewError(tab, msg); return;
    case 'capture_status': reachOnCaptureStatus(tab, msg); return;
    case 'clipboard_content': reachOnClipboardContent(tab, msg); return;
    case 'type_text_result': vaultOnTypeResult(tab, msg); return; // (the vault's typing: vault_ui.go)
    case 'clipboard_ack': reachOnClipboardAck(tab, msg); return;
    case 'clipboard_files':
      if (tab.filesDenied) return; // transfers refused: the relay does not forward it; ignored if it comes anyway
      tab.remoteFiles = Array.isArray(msg.files) ? msg.files.filter(function (f) { return f && typeof f.name === 'string'; }).slice(0, 100) : [];
      if (tab.remoteFiles.length) tab.clip.remoteNewer = true; // a remote copy wins over older local files
      reachRenderTransfers(tab);
      if (tab.remoteFiles.length && reachHas(tab, REACH.FEATURE.FILE_DOWNLOAD) && reachUI(tab)) {
        reachToast(T('viewer.remoteCopiedFiles', { count: tab.remoteFiles.length }), 'ok');
      }
      return;
    case 'file_accept': case 'file_progress': case 'file_done': case 'file_error': case 'file_offer': case 'file_cancel':
      if (tab.transfers) tab.transfers.handle(msg);
      return;
    case 'inactivity_warning':
      // A session meant to be kept alive (a late timer: throttled page) gets
      // its keepalive now instead of a warning.
      if (reachKeepaliveWanted(tab, reachKeepalivePolicy()) && reachSend(tab, { type: 'keepalive' })) { tab.kaLast = reachNow(); return; }
      if (reachUI(tab)) reachToast(T('viewer.inactivityWarning', { s: msg.seconds || 30 }), 'warn');
      return;
    case 'inactivity_timeout':
      tab.noAutoReconnect = true;
      tab.lastError = { code: 'inactivity_timeout', message: '', retry: false };
      return;
    case 'input_block_status': reachOnInputBlockStatus(tab, msg); return;
    case 'session_lock_state': reachOnLockState(tab, msg); return; // (lock the session at the end)
    case 'presence': reachOnPresence(tab, msg); return; // (the relay: other operators, reach_presence.go)
    case 'blank_screen_status': reachOnBlankStatus(tab, msg); return;
    case 'display_modes': reachResOnModes(tab, msg); return; // (the resolution selector: reach_resolution.go)
    case 'display_mode_result': reachResOnResult(tab, msg); return;
    case 'chat': if (devIsSupport(tab.device)) supChatOnMessage(tab, msg); return; // (a code session's chat: support_chat.go)
    case 'bitrate':
      return;
    default:
      if (!msg.type && msg.width && msg.height) reachOnInit(tab, msg); // very old agents
  }
}

function reachOnInit(tab, info) {
  var w = info.width | 0, h = info.height | 0;
  if (w <= 0 || h <= 0) return;
  reachClearConnectTimer(tab);
  tab.initCount = (tab.initCount | 0) + 1;
  tab.gotInit = true;
  // The reconnect budget is NOT reset here but on the first picture
  // (reachPaint): an agent sends its init before the capture gives a frame,
  // and a session that never shows anything (an RDP session whose client
  // window is minimised, a capture helper that dies at its first frame) used
  // to reset the budget on every attempt — "Reconnecting (1/8)" forever.
  tab.lastError = null;
  tab.vmState = null; // (a VM console: its picture is there)
  tab.agentW = w; tab.agentH = h;
  tab.fps = (info.fps | 0) || 15;
  tab.gen = (typeof info.gen === 'number') ? info.gen : null;
  tab.features = new Set(Array.isArray(info.features) ? info.features.filter(function (f) { return typeof f === 'string'; }) : []);
  tab.filesDenied = reachFilesDeniedOf(info);
  if (tab.filesDenied) tab.remoteFiles = [];
  // The machine's domain membership (agent: 'joined' / 'workgroup'; absent:
  // unknown, an older agent). The vault types a username only where the
  // sign-in screen can have a user field (vault_ui.go vaultUserMode).
  tab.domain = (info.domain === 'joined' || info.domain === 'workgroup') ? info.domain : '';
  if (Array.isArray(info.monitors)) {
    tab.monitors = info.monitors.filter(function (m) { return m && m.width > 0 && m.height > 0; }).map(function (m, i) {
      return { index: (typeof m.index === 'number') ? m.index : i, name: String(m.name || ''), x: m.x | 0, y: m.y | 0, width: m.width | 0, height: m.height | 0,
        primary: !!m.primary, dpi: m.dpi | 0, scale: m.scale | 0, rotation: m.rotation | 0 };
    });
  }
  tab.monitor = (typeof info.monitor === 'number') ? info.monitor : (tab.monitors.length === 1 ? tab.monitors[0].index : tab.monitor);
  tab.view = reachNormalizeView(info);
  if (reachIsMosaic(tab.view)) tab.monitor = -1;
  if (typeof info.audioAvail === 'boolean') tab.audioAvail = info.audioAvail;
  if (info.audioRate > 0) tab.audioRate = info.audioRate | 0;
  tab.clipLimits = info.clipboard || null;
  if (info.files) tab.transfers.setLimits(info.files);
  tab.sessionInfo = info.session || tab.sessionInfo;
  tab.viewPending = false;
  if (tab.viewPendingTimer) { clearTimeout(tab.viewPendingTimer); tab.viewPendingTimer = null; }
  if (typeof tab.monitor === 'number' && tab.monitor >= 0) tab.lastSingle = tab.monitor;
  // Always H.264 first; a codec_switch follows when the agent encodes another codec.
  tab.codec = 'h264';
  if (tab.decoder) { try { tab.decoder.close(); } catch (e) {} }
  tab.decoder = reachMakeDecoder(tab, 'h264', info.extradata);
  tab.needKey = true;
  tab.ts = 0;
  if (reachUI(tab)) remoteDecoder = tab.decoder;
  if (!tab.sessionState || tab.sessionState.state === 'live' || !reachHas(tab, REACH.FEATURE.SESSION_FOLLOW)) reachHideOverlay(tab);

  // Negotiation, repeated after every init (old agents ignore it).
  reachSend(tab, { type: 'audio_caps', v: 2, codecs: ['pcm16'], channels: 2, enabled: !!tab.audioOn });
  if (reachHas(tab, REACH.FEATURE.CLIP_PUSH)) reachSend(tab, { type: 'clipboard_sync', push: reachClipSyncEnabled() });
  if (tab.audio) tab.audio.setRate(tab.audioRate || 48000);
  if (!tab.kaLast) tab.kaLast = reachNow();
  if (reachHas(tab, REACH.FEATURE.KEEPALIVE)) reachKeepaliveArm();
  reachResOnInit(tab); // the resolution list, marked again after each init (reach_resolution.go)
  reachCodecOnInit(tab, info); // the codec list, the remembered codec applied again (reach_codec.go)
  reachBlankOnInit(tab, info); // blank screen offered or why not (reach_presence.go)

  if (tab.initCount === 1) {
    // A block the operator asked for, lost with the previous connection.
    if (tab.wantInputBlock && !tab.inputBlocked && reachCanBlockInput(tab)) reachSend(tab, { type: 'set_input_block', block: true });
    // Remembered view mode (per tab, else the last choice on this computer).
    var want = (typeof tab.wantMosaic === 'boolean') ? tab.wantMosaic : (reachPref('oblireach_view_mode', 'monitor') === 'mosaic');
    if (want && reachCanMosaic(tab) && !reachIsMosaic(tab.view)) reachRequestMosaic(tab);
  }

  var ui = reachUI(tab);
  if (ui) {
    if (ui.canvas.width !== w || ui.canvas.height !== h) { ui.canvas.width = w; ui.canvas.height = h; }
    reachShowStreamUI(tab);
    if (zoomLevel !== 'Fit') setZoomLevel(zoomLevel);
    if (perfInterval === null) startPerfHudTimer();
  }
  perfCodec = 'H.264';
  reachSetStatus(tab, reachStreamStatus(tab));
  reachRefreshToolbar(tab);
  reachRenderTiles(tab);
  reachRenderTransfers(tab);
  reachUpdateClipboardWatch();
  renderSessionTabs();
}

function reachOnCodecSwitch(tab, codec) {
  var c = (codec === 'vp9' || codec === 'av1' || codec === 'h265' || codec === 'jpeg') ? codec : 'h264';
  tab.codec = c;
  if (tab.id === activeSessionTabId) perfCodec = REACH_CODEC_LABEL[c];
  if (c !== 'jpeg') {
    // Uses the dimensions of the last init / decoded frame (tab.agentW/H),
    // never stale values from an earlier view.
    if (tab.decoder) { try { tab.decoder.close(); } catch (e) {} }
    tab.decoder = reachMakeDecoder(tab, c);
    tab.needKey = true;
    if (reachUI(tab)) remoteDecoder = tab.decoder;
  }
  if (tab.gotInit) reachSetStatus(tab, reachStreamStatus(tab));
  reachCodecOnSwitch(tab, c); // (a pending choice of the codec selector: reach_codec.go)
}

function reachOnSessionState(tab, msg) {
  tab.sessionState = msg;
  reachClearConnectTimer(tab);
  var st = msg.state;
  var user = msg.user ? String(msg.user) : '';
  if (st === 'live') {
    reachHideOverlay(tab);
    if (tab.gotInit) reachSetStatus(tab, reachStreamStatus(tab));
    return;
  }
  if (st === 'starting') {
    reachSetStatus(tab, T('viewer.startingCapture'));
    var sid = msg.sessionId != null ? msg.sessionId : '';
    reachShowOverlay(tab, 'busy', T('viewer.starting'), user ? T('viewer.sessionUser', { id: sid, user: user }) : T('session.number', { id: sid }), [reachActCancel(tab)]);
    return;
  }
  if (st === 'switching') {
    reachSetStatus(tab, T('viewer.following'));
    reachShowOverlay(tab, 'busy', T('viewer.following'), reachCodeText(REACH_REASON_TEXT, msg.reason) || T('viewer.sessionChanged'), [reachActDisconnect(tab)]);
    return;
  }
  if (st === 'no_display') {
    var wait = msg.waitSec > 0 ? ' ' + T('viewer.waitUpTo', { min: Math.max(1, Math.round(msg.waitSec / 60)) }) : '';
    reachSetStatus(tab, T('viewer.noDisplayStatus'));
    reachShowOverlay(tab, 'info', T('viewer.noDisplayTitle'),
      (user ? T('viewer.noDisplayTextUser', { user: user }) : T('viewer.noDisplayText')) + wait,
      [reachActPickSession(tab), reachActDisconnect(tab)]);
  }
}

// capture_status (the agent's no-picture watchdog): the stream is up but the
// capture gives no picture — an RDP session draws nothing while its client
// window is minimised or disconnected. The connection is kept (no reconnect:
// it would only start the same capture again); the picture comes back by
// itself (state ok, or simply the next frame).
function reachOnCaptureStatus(tab, msg) {
  if (msg.state === 'no_frames') {
    var reason = typeof msg.reason === 'string' ? msg.reason : 'capture_stalled';
    tab.captureStall = reason;
    // A session change in progress keeps its own notice.
    if (tab.sessionState && tab.sessionState.state !== 'live') return;
    var title = reachCodeText(REACH_CAPTURE_STALL_TEXT, reason) || T('viewer.capture.stalled');
    reachSetStatus(tab, T('viewer.capture.noPictureStatus'));
    reachShowOverlay(tab, 'info', title, T(msg.remote ? 'viewer.capture.hintRemote' : 'viewer.capture.hint'),
      [reachActPickSession(tab), reachActDisconnect(tab)]);
    tab.overlay.stall = true;
    return;
  }
  if (msg.state === 'ok') reachClearCaptureStall(tab);
}
function reachClearCaptureStall(tab) {
  if (!tab.captureStall) return;
  tab.captureStall = null;
  if (tab.overlay && tab.overlay.stall) reachHideOverlay(tab);
  if (tab.gotInit) reachSetStatus(tab, reachStreamStatus(tab));
}

function reachOnError(tab, msg) {
  // A relay notice for an agent failure (tunnel_failed) is code 'agent_error'
  // with the agent's own code in agentCode: that one says what happened.
  var code = (typeof msg.agentCode === 'string' && msg.agentCode) ? msg.agentCode : (typeof msg.code === 'string' ? msg.code : '');
  var retry = msg.retry;
  if (typeof retry !== 'boolean') {
    retry = (code && Object.prototype.hasOwnProperty.call(REACH.ERROR_RETRY, code)) ? REACH.ERROR_RETRY[code] : true;
  }
  var text = typeof msg.message === 'string' ? msg.message : (typeof msg.error === 'string' ? msg.error : '');
  tab.lastError = { code: code, message: text, retry: retry };
  if (text) console.log('[reach] error tab=' + tab.id + ' code=' + code + ': ' + text); // (the words of the agent / relay)
  if (!retry) tab.noAutoReconnect = true;
  reachClearConnectTimer(tab);
  reachErrorOverlay(tab);
  reachSetStatus(tab, T('viewer.errorStatus', { error: reachErrorWhy(tab.lastError) || code || T('viewer.unknown') }));
}

function reachOnViewError(tab, msg) {
  tab.viewPending = false;
  if (tab.viewPendingTimer) { clearTimeout(tab.viewPendingTimer); tab.viewPendingTimer = null; }
  if (!reachIsMosaic(tab.view)) tab.wantMosaic = false;
  if (msg.code === 'mosaic_unavailable' || msg.code === 'too_large') reachSetPref('oblireach_view_mode', 'monitor');
  if (reachUI(tab)) reachToast(reachCodeText(REACH_VIEW_ERROR_TEXT, msg.code) || msg.message || T('viewer.viewErr.generic'), 'warn');
  reachRefreshToolbar(tab);
}

// ── Video ──────────────────────────────────────────────────────────────────
function reachMakeDecoder(tab, codec, extradata) {
  if (typeof VideoDecoder === 'undefined') return null;
  var dec = null;
  try {
    dec = new VideoDecoder({
      output: function (frame) { reachDrawFrame(tab, frame, dec); },
      error: function (e) {
        console.warn('[reach] decoder error', e);
        if (tab.decoder === dec) { tab.decoder = null; tab.needKey = true; }
      },
    });
    dec.configure({ codec: reachCodecString(codec, extradata), codedWidth: tab.agentW || 1920, codedHeight: tab.agentH || 1080, optimizeForLatency: true });
  } catch (e) {
    console.warn('[reach] decoder init failed', e);
    try { if (dec) dec.close(); } catch (x) {}
    return null;
  }
  return dec;
}

// Size of the decoded picture changed without an init (legacy agents after a
// monitor switch): the mapping follows the picture actually shown.
function reachOnFrameSize(tab) {
  if (tab.view && !reachIsMosaic(tab.view) && tab.view.tiles && tab.view.tiles.length === 1) {
    tab.view.tiles[0].frame = { x: 0, y: 0, w: tab.agentW, h: tab.agentH };
    tab.view.width = tab.agentW; tab.view.height = tab.agentH;
  }
  if (tab.gotInit) reachSetStatus(tab, reachStreamStatus(tab));
  reachRenderTiles(tab);
}

function reachPaint(tab, img, w, h) {
  // A picture: the connection works — the reconnect budget starts over (see
  // reachOnInit) and a "no picture" notice of the agent is over.
  tab.reconnectAttempts = 0;
  tab.vmPictured = true; // (a VM console: its global budget vmAutoReconnects is NOT reset)
  if (tab.captureStall) reachClearCaptureStall(tab);
  if (w > 0 && h > 0 && (w !== tab.agentW || h !== tab.agentH)) { tab.agentW = w; tab.agentH = h; reachOnFrameSize(tab); }
  tgPaintReach(tab, img, w, h); // (in a mosaic: its cell's picture, tab_groups.go)
  var ui = reachUI(tab);
  if (!ui) {
    // Pane not on screen: the stashed picture (reachStashPicture) follows the
    // stream, so the pane shows the current screen when it comes back.
    var lc = tab.lastCanvas;
    if (lc && w > 0 && h > 0) {
      if (lc.width !== w || lc.height !== h) { lc.width = w; lc.height = h; }
      try { var lctx = lc.getContext('2d'); if (lctx) lctx.drawImage(img, 0, 0); } catch (e) {}
    }
    return;
  }
  var c = ui.canvas;
  if (c.width !== w || c.height !== h) {
    c.width = w; c.height = h;
    if (zoomLevel !== 'Fit') setZoomLevel(zoomLevel);
    reachRenderTiles(tab);
  }
  var ctx = c.getContext('2d');
  if (ctx) ctx.drawImage(img, 0, 0);
  // (a pane drawn again while the session ran, its picture still hidden:
  // a frame is proof enough that it streams)
  if (!tab.shown || (c.style && c.style.display === 'none')) reachShowStreamUI(tab);
}

function reachDrawFrame(tab, frame, dec) {
  try {
    if (tab.decoder !== dec) return; // frame of a replaced decoder
    reachPaint(tab, frame, frame.displayWidth, frame.displayHeight);
  } finally { frame.close(); }
}

function reachDrawJpeg(tab, payload) {
  if (tab.codec !== 'jpeg') { tab.codec = 'jpeg'; if (tab.id === activeSessionTabId) perfCodec = 'JPEG'; reachRefreshCodec(tab); }
  if (tab.codecPending) reachCodecOnFrame(tab, 'jpeg');
  createImageBitmap(new Blob([payload], { type: 'image/jpeg' })).then(function (bmp) {
    try { reachPaint(tab, bmp, bmp.width, bmp.height); } finally { bmp.close(); }
  }).catch(function () {});
}

function reachDecodeVideo(tab, type, payload) {
  var codec = type === REACH.FRAME.H264 ? 'h264' : type === REACH.FRAME.VP9 ? 'vp9' : type === REACH.FRAME.H265 ? 'h265' : 'av1';
  var isKey = codec === 'h264' ? reachH264IsKey(payload) : codec === 'h265' ? reachH265IsKey(payload) : codec === 'vp9' ? reachVp9IsKey(payload) : reachAv1IsKey(payload);
  if (codec !== tab.codec) {
    // Frames of another codec than announced (lost codec_switch, legacy
    // agent): follow the stream from its next keyframe.
    if (!isKey) return;
    tab.codec = codec;
    if (tab.id === activeSessionTabId) perfCodec = REACH_CODEC_LABEL[codec];
    if (tab.decoder) { try { tab.decoder.close(); } catch (e) {} tab.decoder = null; }
    reachRefreshCodec(tab);
  }
  if (!tab.decoder || tab.decoder.state === 'closed') {
    if (!isKey) return;
    tab.decoder = reachMakeDecoder(tab, codec);
    if (!tab.decoder) return;
    if (reachUI(tab)) remoteDecoder = tab.decoder;
    tab.needKey = true;
  }
  if (tab.needKey) { if (!isKey) return; tab.needKey = false; }
  if (tab.codecPending) reachCodecOnFrame(tab, codec); // (the codec selector: reach_codec.go)
  try {
    tab.decoder.decode(new EncodedVideoChunk({ type: isKey ? 'key' : 'delta', timestamp: tab.ts, data: payload }));
  } catch (e) { tab.needKey = true; }
  tab.ts += Math.round(1000000 / (tab.fps || 15));
}

function reachOnBinary(tab, u8) {
  if (!u8.length) return;
  var type = u8[0];
  if (type === REACH.FRAME.AUDIO_PCM || type === REACH.FRAME.AUDIO_V2) { reachOnAudio(tab, u8); return; }
  if (type === REACH.FRAME.FILE_DOWN) {
    var f = reachParseFileFrame(u8);
    if (f && tab.transfers) tab.transfers.onData(f);
    return;
  }
  if (tab.id === activeSessionTabId) { perfFrameCount++; perfByteCount += u8.length; }
  var payload = u8.subarray(1);
  if (type === REACH.FRAME.JPEG) reachDrawJpeg(tab, payload);
  else if (type >= REACH.FRAME.H264 && type <= REACH.FRAME.AV1) reachDecodeVideo(tab, type, payload);
}

// ── Audio ──────────────────────────────────────────────────────────────────
function reachOnAudio(tab, u8) {
  if (!tab.audioOn || !tab.audio || tab.id !== activeSessionTabId) return;
  if (u8[0] === REACH.FRAME.AUDIO_V2) {
    var a = reachParseAudioV2(u8);
    if (a && a.samples) tab.audio.push(a.samples, a.rate);
  } else {
    tab.audio.push(reachPcm16ToFloat(u8, 1, 1), tab.audioRate || 48000);
  }
}
function reachToggleAudio(tab) {
  if (!tab) return;
  tab.audioOn = !tab.audioOn;
  if (!tab.audio) tab.audio = new ReachAudioPlayer();
  if (tab.audioOn) {
    tab.audio.setRate(tab.audioRate || 48000);
    tab.audio.enable(); // inside the click: the AudioContext may start
  } else {
    tab.audio.disable();
  }
  if (reachHas(tab, REACH.FEATURE.AUDIO_V2)) reachSend(tab, { type: 'set_audio', enabled: tab.audioOn });
  reachRefreshToolbar(tab);
}

// ── Mouse & keyboard ───────────────────────────────────────────────────────
function sendInput(evType, e, canvas) {
  var tab = reachDisplayedTab();
  if (!tab || tab.viewOnly || !tab.ws || tab.ws.readyState !== 1 || !tab.gotInit) return;
  var fw = tab.agentW || canvas.width, fh = tab.agentH || canvas.height;
  var rect = canvas.getBoundingClientRect();
  var m = reachMapPointer(e.clientX - rect.left, e.clientY - rect.top, rect.width, rect.height, fw, fh, zoomLevel === 'Fit');
  if (!m) return;
  var action = evType === 'mousemove' ? 'move' : evType === 'mousedown' ? 'down' : evType === 'mouseup' ? 'up' : 'scroll';
  if (action === 'down') {
    if (!m.inside) return;
    if (reachIsMosaic(tab.view)) {
      // A click on the black area between screens goes nowhere.
      var sx = tab.view.width ? tab.view.width / fw : 1, sy = tab.view.height ? tab.view.height / fh : 1;
      if (!reachTileAt(tab.view, m.x * sx, m.y * sy)) return;
    }
  }
  if (action === 'move' && !m.inside && !e.buttons) return;
  // Right / middle click may paste (context menu, console QuickEdit, X11):
  // the staged local clipboard goes first, on the same ordered socket.
  if (action === 'down' && (e.button === 1 || e.button === 2)) reachFlushStagedClipboard(tab);
  var msg = { type: 'mouse', action: action, x: m.x, y: m.y };
  if (action === 'scroll') msg.delta = Math.sign(-e.deltaY);
  if (action === 'down' || action === 'up') msg.button = e.button;
  if (reachHas(tab, REACH.FEATURE.VIEW_GEN) && tab.gen !== null) msg.gen = tab.gen;
  tab.ws.send(JSON.stringify(msg));
  if (action === 'down') tab.buttonsHeld[e.button] = { x: m.x, y: m.y };
  else if (action === 'up') delete tab.buttonsHeld[e.button];
}

function sendInputKey(evType, e) {
  var tab = reachDisplayedTab();
  if (!tab || tab.viewOnly || !tab.ws || tab.ws.readyState !== 1) return;
  reachLastMods = { shift: !!e.shiftKey, ctrl: !!e.ctrlKey, alt: !!e.altKey };
  // meta also while the Windows key is held through the native hook (the page never sees it)
  var k = reachKeyMsg(tab, { type: 'key', action: evType === 'keydown' ? 'down' : 'up', key: e.key, code: e.code, shift: e.shiftKey, ctrl: e.ctrlKey, alt: e.altKey, meta: e.metaKey || reachNativeMetaHeld() });
  // (the operator's Caps Lock: the agent aligns the remote one before a letter)
  if (k.action === 'down' && typeof e.getModifierState === 'function') k.caps = !!e.getModifierState('CapsLock');
  var held = e.code || e.key;
  if (held) { if (k.action === 'down') tab.keysHeld[held] = { key: e.key, code: e.code }; else delete tab.keysHeld[held]; }
  if (tab.keyQueue) { tab.keyQueue.push(k); return; } // a paste is syncing the clipboard first
  var pasteKeys = k.action === 'down' &&
    ((((e.ctrlKey || e.metaKey) && !e.altKey) && e.code === 'KeyV') || (e.shiftKey && !e.ctrlKey && e.code === 'Insert'));
  // A VM console has no shared clipboard: the gesture TYPES the local
  // clipboard's text into the VM (vm_console.go vmPasteClipboard); the key
  // itself never reaches the VM (nor its auto-repeat).
  if (pasteKeys && devIsVm(tab.device)) {
    if (held) { delete tab.keysHeld[held]; tab.vmPasteKeyUp = held; } // (its release is not sent either)
    if (!e.repeat) vmPasteClipboard(tab, true);
    return;
  }
  if (k.action === 'up' && held && tab.vmPasteKeyUp === held) { tab.vmPasteKeyUp = null; return; }
  var isPaste = pasteKeys && !e.repeat;
  if (isPaste) { reachPasteThenKey(tab, k); return; }
  tab.ws.send(JSON.stringify(k));
  if ((e.code === 'KeyC' || e.code === 'KeyX') && (e.ctrlKey || e.metaKey) && !e.altKey && k.action === 'down') tab.copyPending = true;
  if ((e.code === 'KeyC' || e.code === 'KeyX') && k.action === 'up' && tab.copyPending) { tab.copyPending = false; reachAfterRemoteCopy(tab); }
}

// Ctrl+V with sync on: a local copy the remote has not seen yet is put on the
// remote clipboard first, then the keystroke is forwarded (queued keys keep
// their order). Files copied locally (once per copy) are sent to the remote
// Downloads folder instead. "Last copy wins": a copy made inside the session
// after the local one is never overwritten, so copy/paste of files or images
// inside the remote keeps working.
async function reachPasteThenKey(tab, k) {
  var ws = tab.ws;
  if (!reachClipSyncEnabled()) { ws.send(JSON.stringify(k)); return; }
  tab.clip.staged = null; // the clipboard is read again right below
  tab.keyQueue = [];
  var done = false, forwardV = true;
  // View only turned on meanwhile (reachReleaseHeldInput): nothing more is sent.
  var cancel = tab.pasteCancel;
  var cancelled = function () { return tab.pasteCancel !== cancel || tab.viewOnly; };
  var flush = function () {
    if (done) return;
    done = true;
    if (cancelled()) return; // the queue was dropped; a newer paste may own tab.keyQueue
    var q = tab.keyQueue || [];
    tab.keyQueue = null;
    if (tab.ws !== ws || !ws || ws.readyState !== 1) return;
    if (forwardV) ws.send(JSON.stringify(k));
    for (var i = 0; i < q.length; i++) {
      if (!forwardV && q[i].code === k.code) continue;
      ws.send(JSON.stringify(q[i]));
    }
  };
  var guard = setTimeout(flush, 700);
  try {
    var info = await reachNativeClipboardRead();
    if (info.tooLarge) {
      if (tab.clip.lastTooLargeSeq !== info.seq) reachToast(T('viewer.clip.tooLargePaste'), 'warn');
      tab.clip.lastTooLargeSeq = info.seq;
    } else if (!info.text && info.hasFiles && info.seq !== tab.clip.lastFilesSeq && !tab.clip.remoteNewer &&
               reachHas(tab, REACH.FEATURE.FILE_UPLOAD) && typeof window.reachClipboardFiles === 'function') {
      var files = await window.reachClipboardFiles(reachNativeToken());
      if (Array.isArray(files) && files.length && !done && !cancelled()) {
        tab.clip.lastFilesSeq = info.seq;
        forwardV = false;
        clearTimeout(guard);
        flush();
        reachUploadNativeFiles(tab, files);
        reachToast(T('viewer.clip.sendingItems', { count: files.length }), 'ok');
        return;
      }
    } else if (!info.text && info.hasFiles && tab.filesDenied && info.seq !== tab.clip.lastDeniedFilesSeq) {
      // Files refused in this session: said once per copy; V still goes
      // through (the remote pastes its own clipboard, as without the viewer).
      tab.clip.lastDeniedFilesSeq = info.seq;
      reachToast(reachFilesDeniedText(tab), 'warn');
    } else if (info.text && !done && !cancelled() && info.text !== tab.clip.lastSentText && info.text !== tab.clip.lastRemoteText) {
      reachSendClipboardSet(tab, info.text, false, true);
    }
  } catch (e) {}
  clearTimeout(guard);
  flush();
}

// After Ctrl+C / Ctrl+X in the session: agents with clipboard_push send the
// change themselves; older agents are asked 300 ms later.
function reachAfterRemoteCopy(tab) {
  if (!reachClipSyncEnabled() || reachHas(tab, REACH.FEATURE.CLIP_PUSH)) return;
  setTimeout(function () { reachRequestRemoteClipboard(tab, false); }, 300);
}

function reachWireCanvas(canvas) {
  canvas.setAttribute('tabindex', '0');
  // Pointer capture: a drag that leaves the picture keeps reporting (clamped)
  // coordinates and its button release is never lost.
  canvas.addEventListener('pointerdown', function (e) {
    if (annotationActive || reachViewOnlyNow()) return;
    try { canvas.setPointerCapture(e.pointerId); } catch (x) {}
  });
  canvas.addEventListener('mousedown', function (e) {
    if (annotationActive) return;
    e.preventDefault();
    // View only: the picture does not take the focus either (the keyboard
    // stays where it was, e.g. in the chat box).
    if (reachViewOnlyNow()) return;
    try { canvas.focus({ preventScroll: true }); } catch (x) { canvas.focus(); }
    sendInput('mousedown', e, canvas);
  });
  canvas.addEventListener('mousemove', function (e) { if (!annotationActive) sendInput('mousemove', e, canvas); });
  canvas.addEventListener('mouseup', function (e) { if (!annotationActive) sendInput('mouseup', e, canvas); });
  // Ctrl+wheel over the picture never zooms the app (WebView page zoom): in
  // view only / annotation it does nothing, else it goes to the remote as
  // before. View only leaves the plain wheel to the page (scrolling a zoomed picture).
  canvas.addEventListener('wheel', function (e) {
    if (e.ctrlKey) e.preventDefault();
    if (annotationActive || reachViewOnlyNow()) return;
    e.preventDefault(); sendInput('wheel', e, canvas);
  }, { passive: false });
  canvas.addEventListener('contextmenu', function (e) { e.preventDefault(); });
  canvas.addEventListener('keydown', function (e) { if (annotationActive || reachViewOnlyNow()) return; e.preventDefault(); sendInputKey('keydown', e); });
  canvas.addEventListener('keyup', function (e) { if (annotationActive || reachViewOnlyNow()) return; e.preventDefault(); sendInputKey('keyup', e); });
  // The Windows key is captured (native hook) only while a session canvas has
  // the focus (and the session is not in view only: reachSetKeyCapture).
  canvas.addEventListener('focus', function () { reachSetKeyCapture(true); });
  canvas.addEventListener('blur', function () { reachSetKeyCapture(false); vmReleaseAll(reachDisplayedTab()); }); // (a VM console's helper lets go of what it holds)
}

// ── Windows key (windowed mode) ─────────────────────────────────────────────
// The shell eats the Windows key before the page sees it. While a session
// canvas has the focus, the native hook (keyhook_windows.go) swallows it and
// calls __reachNativeKey: the key goes to the session like any other, and the
// keys typed while it is held carry meta (Win+R, Win+E, Win+Shift+S...).
var reachNativeMeta = { MetaLeft: false, MetaRight: false };
var reachNativeMetaTab = null;
var reachKeyCaptureState = false;
var reachLastMods = { shift: false, ctrl: false, alt: false };
function reachNativeMetaHeld() { return reachNativeMeta.MetaLeft || reachNativeMeta.MetaRight; }
function reachSetKeyCapture(on) {
  on = !!on && !reachViewOnlyNow(); // view only: the Windows key stays with this computer
  if (!on) reachReleaseNativeMeta();
  if (on === reachKeyCaptureState) return;
  reachKeyCaptureState = on;
  if (typeof window.reachKeyCapture !== 'function' || !reachNativeToken()) return;
  try { var p = window.reachKeyCapture(reachNativeToken(), on); if (p && p.catch) p.catch(function () {}); } catch (e) {}
}
function reachNativeMetaSend(tab, down, code) {
  if (!tab || !tab.ws || tab.ws.readyState !== 1) return;
  var k = reachKeyMsg(tab, { type: 'key', action: down ? 'down' : 'up', key: 'Meta', code: code,
    shift: reachLastMods.shift, ctrl: reachLastMods.ctrl, alt: reachLastMods.alt, meta: down || reachNativeMetaHeld() });
  if (tab.keyQueue) { tab.keyQueue.push(k); return; } // a paste is syncing the clipboard first
  tab.ws.send(JSON.stringify(k));
}
function reachReleaseNativeMeta() {
  var tab = reachNativeMetaTab;
  reachNativeMetaTab = null;
  ['MetaLeft', 'MetaRight'].forEach(function (code) {
    if (!reachNativeMeta[code]) return;
    reachNativeMeta[code] = false;
    reachNativeMetaSend(tab, false, code);
  });
}
window.__reachNativeKey = function (down, code) {
  if (code !== 'MetaLeft' && code !== 'MetaRight') return;
  if (down) {
    if (reachNativeMeta[code] || annotationActive) return;
    var tab = reachDisplayedTab();
    if (!tab || tab.viewOnly || !tab.ws || tab.ws.readyState !== 1) return;
    reachNativeMeta[code] = true;
    reachNativeMetaTab = tab;
    reachNativeMetaSend(tab, true, code);
    return;
  }
  if (!reachNativeMeta[code]) return;
  reachNativeMeta[code] = false;
  reachNativeMetaSend(reachNativeMetaTab, false, code);
  if (!reachNativeMetaHeld()) reachNativeMetaTab = null;
};

// ── View only (local) ───────────────────────────────────────────────────────
// Per session tab, off by default, kept across reconnects. While on, the
// picture is only watched: no mouse move / click / wheel, no key, no Windows
// key capture and no clipboard push (local copies, paste sync) reaches the
// remote. The local pointer moves freely over the picture, the keyboard stays
// here (the picture no longer takes the focus) and the wheel is left to the
// page (but Ctrl+wheel never zooms the app). Toolbar actions (system keys, quick actions, clipboard menu, files)
// are explicit clicks and still work. The remote's clipboard pushes still
// arrive, and the view (screens, mosaic, sound) can still be changed.
var REACH_MOD_CODES = { ShiftLeft: 1, ShiftRight: 1, ControlLeft: 1, ControlRight: 1, AltLeft: 1, AltRight: 1, MetaLeft: 1, MetaRight: 1 };
function reachViewOnlyNow() { var t = reachDisplayedTab(); return !!(t && t.viewOnly); }
// Everything the operator still holds down in the session is released: keys
// (modifiers last, all flags up so the agent also lets go of a stuck
// modifier), the Windows key held through the native hook, mouse buttons. A
// paste still syncing the clipboard forwards nothing more.
function reachReleaseHeldInput(tab) {
  if (!tab) return;
  tab.pasteCancel = (tab.pasteCancel || 0) + 1;
  tab.keyQueue = null;
  tab.copyPending = false;
  var keys = tab.keysHeld || {}, btns = tab.buttonsHeld || {};
  tab.keysHeld = {}; tab.buttonsHeld = {};
  var ws = tab.ws, open = !!(ws && ws.readyState === 1);
  if (open) {
    var codes = Object.keys(keys).sort(function (a, b) { return (REACH_MOD_CODES[a] ? 1 : 0) - (REACH_MOD_CODES[b] ? 1 : 0); });
    codes.forEach(function (c) {
      ws.send(JSON.stringify(reachKeyMsg(tab, { type: 'key', action: 'up', key: keys[c].key, code: keys[c].code, shift: false, ctrl: false, alt: false, meta: false })));
    });
  }
  if (reachNativeMetaTab === tab) {
    reachLastMods = { shift: false, ctrl: false, alt: false };
    reachReleaseNativeMeta();
  }
  if (open) {
    Object.keys(btns).forEach(function (b) {
      var msg = { type: 'mouse', action: 'up', x: btns[b].x, y: btns[b].y, button: Number(b) };
      if (reachHas(tab, REACH.FEATURE.VIEW_GEN) && tab.gen !== null) msg.gen = tab.gen;
      ws.send(JSON.stringify(msg));
    });
  }
  vmReleaseAll(tab); // (a VM console: its helper releases everything too)
}
function reachSetViewOnly(tab, on) {
  if (!tab) return;
  on = !!on;
  if (on === !!tab.viewOnly) { reachRefreshViewOnly(tab); return; }
  if (on) reachReleaseHeldInput(tab); // the last input of the session: nothing stays pressed there
  tab.viewOnly = on;
  var ui = reachUI(tab);
  if (ui) {
    var focused = document.activeElement === ui.canvas;
    if (on && focused && ui.canvas.blur) { try { ui.canvas.blur(); } catch (e) {} }
    reachSetKeyCapture(!on && focused);
    reachToast(on ? T('viewer.viewOnlyOn') : T('viewer.viewOnlyOff'), 'ok');
  }
  reachUpdateClipboardWatch();
  reachRefreshViewOnly(tab);
  reachPresenceViewOnly(tab); // the other operators see it (reach_presence.go)
  reachRefreshBlank(tab);
  if (typeof renderSessionTabs === 'function') renderSessionTabs();
  reachDetachedSaveState(tab); // a detached window: for "Bring it back here"
}
function reachToggleViewOnly(tab) { if (tab) reachSetViewOnly(tab, !tab.viewOnly); }
// Button and picture chip follow the session ON SCREEN (called by reachRefreshToolbar).
function reachRefreshViewOnly(tab) {
  if (!reachUI(tab)) return;
  var on = !!tab.viewOnly;
  var b = document.getElementById('reach-vo-btn');
  if (b) {
    b.disabled = false;
    b.classList.toggle('on', on);
    b.setAttribute('aria-pressed', on ? 'true' : 'false');
    b.title = on ? T('viewer.viewOnlyActive') : T('viewer.viewOnlyTitle');
  }
  var chip = document.getElementById('reach-vo-chip');
  if (chip) chip.classList.toggle('visible', on);
}

// ── Block the remote user's mouse and keyboard ─────────────────────────────
// Same protocol as the Obliance web viewer: the toolbar button asks the agent
// ({type:'set_input_block', block}); the state shown is the one the agent
// confirms ({type:'input_block_status', blocked}), never the one asked. Per
// session tab, like view only. A new connection starts unblocked (the agent
// ends the block with its stream) and leaving a session on purpose asks for
// the unblock first. No keyboard shortcut (the web viewer has none). An
// explicit toolbar action: it also works while view only is on.
// Offered only to agents that advertise REACH.FEATURE.INPUT_BLOCK_LOCAL: older
// agents lock the operator out too (Windows) or block nothing (macOS/Linux).
function reachCanBlockInput(tab) { return reachHas(tab, REACH.FEATURE.INPUT_BLOCK_LOCAL); }
function reachToggleInputBlock(tab) {
  if (!tab || !tab.ws || !tab.gotInit || !reachCanBlockInput(tab)) return false;
  var block = !tab.inputBlocked;
  if (!reachSend(tab, { type: 'set_input_block', block: block })) return false;
  tab.wantInputBlock = block;
  return true;
}
function reachOnInputBlockStatus(tab, msg) {
  // A status from an agent without the capability is not trusted as "blocked".
  var on = msg.blocked === true && reachCanBlockInput(tab);
  var changed = on !== !!tab.inputBlocked;
  tab.inputBlocked = on;
  if (!on) tab.blankBlockedInput = false; // (no block left for the blank screen to release)
  reachRefreshInputBlock(tab);
  if (changed) renderSessionTabs(); // the tab of a background session shows it too
  if (changed && reachUI(tab)) reachToast(on ? T('viewer.inputBlock.on') : T('viewer.inputBlock.off'), on ? 'warn' : 'ok');
}
// Before a session is left on purpose (Disconnect, tab closed, sign-out).
function reachReleaseInputBlock(tab) {
  if (!tab) return;
  tab.wantInputBlock = false; // a deliberate leave: never asked again
  if (!tab.inputBlocked) return;
  reachSend(tab, { type: 'set_input_block', block: false });
  tab.inputBlocked = false;
}
// Button and picture chip follow the session ON SCREEN (called by reachRefreshToolbar).
function reachRefreshInputBlock(tab) {
  if (!reachUI(tab)) return;
  var live = !!(tab.ws && tab.gotInit);
  var can = reachCanBlockInput(tab);
  var on = live && can && !!tab.inputBlocked;
  var b = document.getElementById('reach-block-btn');
  if (b) {
    b.style.display = can ? '' : 'none';
    b.disabled = !live;
    b.classList.toggle('on', on);
    b.setAttribute('aria-pressed', on ? 'true' : 'false');
    // Icon + words (the padlock alone read like Quick Actions "Lock the session").
    b.innerHTML = (on ? REACH_ICON.lock : REACH_ICON.unlock) + '<span>' + TH(on ? 'viewer.inputBlock.shortActive' : 'viewer.inputBlock.short') + '</span>';
    b.title = on ? T('viewer.inputBlock.titleActive') : T('viewer.inputBlock.title');
  }
  var chip = document.getElementById('reach-block-chip');
  if (chip) chip.classList.toggle('visible', on);
}

// ── Lock the session when the connection ends ──────────────────────────────
// Same protocol as the Obliance web viewer (agent lock_on_end.go). An agent
// that lists REACH.FEATURE.LOCK_ON_END sends, once the session is ready and
// after each change, {type:'session_lock_state', lockedAtConnect, lockOnEnd}:
// whether the Windows session was locked when the connection opened, and
// whether the agent will lock it when the connection ends (by default: if it
// was locked). The padlock shows the agent's state (never the one asked):
// red = locked at the end, neutral = left unlocked; a click asks for the other
// one ({type:'set_lock_on_end', enabled}), the agent answers with its state.
// Settings > Sessions can turn the offer off (reachLockOfferEnabled): the
// padlock is hidden and each session is told not to lock at the end, as soon
// as its state arrives (only when it says it would: its answer then says
// false, so it is never asked twice). Older agents: no padlock.
var REACH_LOCK_OFFER_KEY = 'oblireach_lock_on_end';
function reachLockOfferEnabled() { return reachPref(REACH_LOCK_OFFER_KEY, '1') !== '0'; }
function reachSetLockOfferEnabled(on) {
  reachSetPref(REACH_LOCK_OFFER_KEY, on ? '1' : '0');
  reachLockOfferApply();
}
// The setting changed (here, or in another window: the storage event): the
// sessions of this window follow it at once.
function reachLockOfferApply() {
  var off = !reachLockOfferEnabled();
  sessionTabs.forEach(function (t) {
    if (off && t.lockState && t.lockState.lockOnEnd && reachCanLockOnEnd(t)) reachSend(t, { type: 'set_lock_on_end', enabled: false });
    reachRefreshLockOnEnd(t);
  });
}
function reachCanLockOnEnd(tab) { return reachHas(tab, REACH.FEATURE.LOCK_ON_END); }
function reachOnLockState(tab, msg) {
  // A state from an agent without the feature, or malformed: ignored.
  if (!reachCanLockOnEnd(tab) || typeof msg.lockOnEnd !== 'boolean') return;
  tab.lockState = { lockedAtConnect: msg.lockedAtConnect === true, lockOnEnd: msg.lockOnEnd };
  if (tab.lockState.lockOnEnd && !reachLockOfferEnabled()) reachSend(tab, { type: 'set_lock_on_end', enabled: false });
  reachRefreshLockOnEnd(tab);
}
function reachToggleLockOnEnd(tab) {
  if (!tab || !tab.ws || !tab.gotInit || !reachCanLockOnEnd(tab) || !tab.lockState || !reachLockOfferEnabled()) return false;
  return reachSend(tab, { type: 'set_lock_on_end', enabled: !tab.lockState.lockOnEnd });
}
// The padlock follows the session ON SCREEN (called by reachRefreshToolbar).
function reachRefreshLockOnEnd(tab) {
  if (!reachUI(tab)) return;
  var b = document.getElementById('reach-lockend-btn');
  if (!b) return;
  var show = !!(tab.ws && tab.gotInit && tab.lockState) && reachCanLockOnEnd(tab) && reachLockOfferEnabled();
  var on = show && !!tab.lockState.lockOnEnd;
  b.style.display = show ? '' : 'none';
  b.classList.toggle('on', on);
  b.setAttribute('aria-pressed', on ? 'true' : 'false');
  b.innerHTML = on ? REACH_ICON.lock : REACH_ICON.unlock;
  b.title = T(on ? 'viewer.lockOnEnd.titleOn' : 'viewer.lockOnEnd.titleOff');
  b.setAttribute('aria-label', b.title);
}
try {
  window.addEventListener('storage', function (e) { if (e && e.key === REACH_LOCK_OFFER_KEY) reachLockOfferApply(); });
} catch (e) {}

// ── Detached session windows (detached_windows.go) ─────────────────────────
// A session can run in its own window: focus mode, that session only, so
// several sessions run side by side, on several screens. The main window
// opens it (toolbar "Detach", or Connect with "Detach by default" in the
// Settings) and keeps no session of that device: its Remote pane says the
// session is in its own window (show it / bring it back here). Go tells the
// main page which devices have a window (__reachOnDetached). A detached
// window loads this same page with ?detached=<id> (reachDetachedMode; its
// boot is detachedBoot in ui.go); closing it ends its session.
var reachDetachedParams = reachParseDetached((typeof location !== 'undefined' && location) ? location.search : '');
var reachDetachedMode = !!reachDetachedParams;
var reachDetachedDevice = null;   // detached window: its device, once loaded
var reachDetachedIds = new Set(); // main window: keys (devKey) of the devices that have their window
var reachDetachedInfo = {};       // main window: key -> {session, viewOnly} given to its window
// "Detach by default" is ON unless the operator turned it off: no
// value saved means on, only the '0' the Settings checkbox writes when it is
// unchecked keeps it off (so an operator who turned it off keeps it off).
function reachDetachDefault() { return reachPref('oblireach_detach_default', '1') !== '0'; }
function reachSetDetachDefault(on) { reachSetPref('oblireach_detach_default', on ? '1' : '0'); }
// Connect from the Devices selection in detached mode: 'one' = every session
// as a tab of one session window (default), 'each' = a window per device.
// ("Open as mosaic" always uses one window: a mosaic lives in one window.)
function reachMultiWindowMode() { return reachPref('oblireach_multi_window', 'one') === 'each' ? 'each' : 'one'; }
function reachSetMultiWindowMode(m) { reachSetPref('oblireach_multi_window', m === 'each' ? 'each' : 'one'); }
function reachCanDetach() { return !reachDetachedMode && typeof window.reachOpenWindow === 'function' && !!reachNativeToken(); }
// Detached mode: the main window, with "Detach by default" and the windows
// available. A click on a device launches its session in a session window
// (reachLaunchDevice), Connect opens one (reachConnect), and the nav rail
// folds for the embedded viewer only (reachRailFolds).
function reachDetachOn() { return reachDetachDefault() && reachCanDetach(); }
function reachIsDetached(dev) { return !reachDetachedMode && !!dev && reachDetachedIds.has(devKey(dev)); }
// Calls a native binding with the token: a promise, rejected when absent.
function reachNative(name) {
  var f = window[name], tok = reachNativeToken(), args = Array.prototype.slice.call(arguments, 1);
  if (typeof f !== 'function' || !tok) return Promise.reject('unavailable');
  try { return Promise.resolve(f.apply(window, [tok].concat(args))); } catch (e) { return Promise.reject(e); }
}
function reachErrText(e) { return String((e && e.message) || e || T('common.error')); }

// The server profile on screen ('' without profiles): device ids of two
// servers overlap.
function reachServerId() { return (typeof activeServerId !== 'undefined' && activeServerId) || ''; }

// ── Device keys (multi-server mode) ──
// Device ids of two servers overlap (both have a device 7) and, in
// multi-server mode, devices of every server are on screen at once. A device
// is known by its KEY: "<server profile>:<device id>" (the bare id without
// profiles), the same key the app uses for the session windows
// (detached_windows.go devKey): reachDetachedIds, the tabs reported, the
// claims, __reachShowTab / __reachCloseTab, favourites, recents. dev._pid:
// its server (set when its server's device list is loaded, ui.go srvStamp);
// none: the server on screen.
function devKeyOf(pid, id) { return (pid ? pid + ':' : '') + String(id); }
function devPid(dev) { return (dev && typeof dev._pid === 'string' && dev._pid) || reachServerId(); }
function devKey(dev) { return devKeyOf(devPid(dev), dev && dev.id) + (dev && dev._vm && dev._vm.id ? '~vm-' + String(dev._vm.id).toLowerCase() : ''); } // (a VM console: its host's key and "~vm-<guid>", vm_console.go)
function devSame(a, b) { return !!a && !!b && devKey(a) === devKey(b); }
// A key as the app or an older caller gives it: a bare id is a device of the
// server on screen.
function keyNorm(k) { k = String(k); return k.indexOf(':') >= 0 ? k : devKeyOf(reachServerId(), k); }
function keyPid(k) { k = String(k); var i = k.indexOf(':'); return i >= 0 ? k.slice(0, i) : ''; }
// A device's name in a notice: with its server's in multi-server mode.
function reachDevName(dev) {
  var h = String((dev && dev.hostname) || T('common.thisDeviceCap'));
  var n = (typeof serverMode !== 'undefined' && serverMode === 'multi' && typeof srvName === 'function') ? srvName(devPid(dev)) : '';
  return n ? n + ' / ' + h : h;
}

// The session of a detached window as it is NOW (its Windows session, view
// only), saved by that window at every change: "Bring it back here" in the
// main window restarts it that way, not as it was detached. localStorage is
// shared by every window of the app (same origin, same WebView2 profile).
// k: the device's key (devKey).
function reachDetachedStateKey(k) { return 'oblireach_detached_state:' + keyNorm(k); }
function reachDetachedSaveState(tab) {
  if (!reachDetachedMode || !tab || !tab.device) return; // (every tab of the window)
  var sid = reachReconnectSessionId(tab);
  reachSetPref(reachDetachedStateKey(devKey(tab.device)),
    JSON.stringify({ session: (typeof sid === 'number' && sid >= 0) ? sid : -1, viewOnly: !!tab.viewOnly }));
}
function reachDetachedForgetState(id) { try { localStorage.removeItem(reachDetachedStateKey(id)); } catch (e) {} }
// Main window: what the window of this device saved (null: nothing valid), removed.
function reachDetachedTakeState(id) {
  var raw = reachPref(reachDetachedStateKey(id), null);
  reachDetachedForgetState(id);
  if (!raw) return null;
  try {
    var st = JSON.parse(raw);
    if (!st || typeof st !== 'object') return null;
    var s = st.session;
    return { session: (typeof s === 'number' && s === Math.floor(s) && s >= 0 && s <= 1048576) ? s : -1, viewOnly: st.viewOnly === true };
  } catch (e) { return null; }
}

// Opens (or brings forward) the window of a device. sessionId: the Windows
// session to show (undefined: automatic). The server on screen goes with it:
// the window refuses to start on another one (a switch meanwhile).
async function reachOpenDetached(dev, sessionId, viewOnly) {
  if (!dev || !reachCanDetach()) return false;
  var id = String(dev.id), key = devKey(dev);
  var sid = (typeof sessionId === 'number' && sessionId >= 0) ? sessionId : -1;
  var known = reachDetachedIds.has(key);
  if (!known) reachDetachedForgetState(key); // left by a window closed earlier
  try {
    // The bare id and its server: the window opens on that server's device.
    await reachNative('reachOpenWindow', id, String(dev.hostname || id), Object.assign({ session: sid, viewOnly: !!viewOnly, profile: devPid(dev) }, vmWindowOpts(dev))); // (a VM console: its kind and VM name)
  } catch (e) {
    // (reachNotify: also seen from the Devices list, which has no picture)
    reachNotify(T('viewer.windowOpenFailed', { error: reachErrText(e) }), 'warn');
    return false;
  }
  if (!known || !reachDetachedInfo[key]) reachDetachedInfo[key] = { session: sid, viewOnly: !!viewOnly };
  reachDetachedIds.add(key);
  return true;
}

// The session of this device in the main window ends (its window connects on
// its own): no Disconnect overlay, no switch to another session tab.
function reachEndTabHere(tab) {
  var idx = sessionTabs.indexOf(tab);
  if (idx < 0) return;
  if (recMediaRecorder && recTab === tab) toggleRecording();
  reachDisposeTab(tab);
  sessionTabs.splice(idx, 1);
  tgOnGone(tab.id); // (out of its mosaic, if any: tab_groups.go)
  if (activeSessionTabId === tab.id) { activeSessionTabId = null; remoteWs = null; remoteDecoder = null; }
  renderSessionTabs();
  reachUpdateClipboardWatch();
}

// Toolbar "Detach" (main window), or Connect with "Detach by default": the
// device on screen goes to its own window, with the Windows session and the
// view-only choice of its session here (an explicit sessionId, from Connect,
// wins). The session here ends once the window is on its way.
async function reachDetachCurrent(sessionId) {
  var dev = selectedDevice;
  if (!dev || !reachCanDetach() || devIsSupport(dev)) return false; // (a code session stays in this window: support_page.go)
  var tab = null;
  for (var i = 0; i < sessionTabs.length; i++) if (devSame(sessionTabs[i].device, dev)) { tab = sessionTabs[i]; break; }
  if (arguments.length === 0) {
    if (tab) sessionId = reachReconnectSessionId(tab);
    else { // not connected yet: the session picked in the toolbar
      var sel = document.querySelector('#remote-pane .session-select');
      if (sel && sel.value) sessionId = parseInt(sel.value, 10);
    }
  }
  // Its side panel goes along (files_dock.go), back here if no window opens.
  var dock = tab ? filesDockLeaving(tab) : null;
  if (!(await reachOpenDetached(dev, sessionId, !!(tab && tab.viewOnly)))) { filesDockStay(dev, dock); return false; }
  if (tab && sessionTabs.indexOf(tab) >= 0) reachEndTabHere(tab);
  if (typeof currentRoute !== 'undefined' && currentRoute === 'device-detail' && selectedDevice && devSame(selectedDevice, dev)) openDeviceDetail(dev, 'remote');
  return true;
}

// Connect button: here, or in the device's own window ("Detach by default").
function reachConnect(sessionId) {
  if (reachDetachOn()) return reachDetachCurrent(sessionId);
  return startRemote(sessionId);
}

// ── Detached mode: a click on a device launches its session ──
// Main window, with "Detach by default": a click on a device (Devices list,
// Dashboard recents and favourites) starts its session in a session window,
// and the main window stays where it is (its nav rail does not fold). Its ▶
// opens its page here, its i its information (devicesOpen, ui.go).
//   - A device whose session already runs is brought to the front on it,
//     never a second session: the session window holding its tab, or the
//     main window's own session of it (opened from its page).
//   - Agent offline or not installed: a message, nothing starts.
//   - Several Windows sessions: the operator picks one first (reachPickSession);
//     cancelled: nothing starts.
// The device list may be old (loaded at sign-in, or by Refresh): a device
// that looks offline, or whose session is to be picked, is read again first.
// Returns what happened: 'main' (its session in this window, shown),
// 'focused' (its session window brought forward, or being created),
// 'opening' (a window asked for), 'offline', 'cancelled', 'busy' (already
// being launched: a double click), 'error', 'none' (not in detached mode).
var reachLaunching = {};
async function reachLaunchDevice(dev) {
  if (!dev || !reachDetachOn()) return 'none';
  var id = String(dev.id), pid = devPid(dev), key = devKey(dev);
  if (reachLaunching[key]) return 'busy';
  reachLaunching[key] = true;
  try {
    var where = await reachLaunchExisting(key);
    if (where) { reachLaunchRecent(dev); return where; }
    if (!(dev.oblireach && dev.oblireach.online) || reachNeedsSessionPick(dev)) {
      // (its own server's list: the other servers are not read again)
      try { if (typeof loadOverview === 'function') await loadOverview(pid); } catch (e) {}
      dev = reachFindDevice(id, pid) || dev;
    }
    if (!(dev.oblireach && dev.oblireach.online)) {
      reachNotify(T(dev.oblireach && dev.oblireach.installed ? 'viewer.launchOffline' : 'viewer.launchNotInstalled',
        { device: reachDevName(dev) }), 'warn');
      return 'offline';
    }
    var choice = await reachPickSession(dev);
    if (choice === null) return 'cancelled';
    // (Opened meanwhile from a session window's "+": the app brings that
    // window forward instead of opening a second session.)
    if (!(await reachOpenDetached(dev, choice, false))) return 'error';
    reachLaunchRecent(dev);
    return 'opening';
  } catch (e) {
    reachNotify(T('viewer.launchFailed', { error: reachErrText(e) }), 'warn');
    return 'error';
  } finally {
    delete reachLaunching[key];
  }
}
// A session of this device that runs already, brought to the front: 'main'
// (a session tab of the main window: its page, on it), 'focused' (the session
// window holding its tab, on that tab; or its window being created), ''
// (none). key: the device's key (devKey).
async function reachLaunchExisting(key) {
  key = keyNorm(key);
  // A tool's key (terminal_viewer.go): its tab here, else its window.
  var tt = (keyIsTool(key) && typeof toolTabByKey === 'function') ? toolTabByKey(key) : null;
  if (tt) { toolShowTab(tt); return 'main'; }
  var mine = reachTabOfDevice(key);
  if (mine) { switchSessionTab(mine.id); return 'main'; }
  var shown = false;
  try { shown = await reachNative('reachFocusWindow', key); } catch (e) {}
  return shown === true ? 'focused' : '';
}
// "Recent connections" (Dashboard) lists the sessions launched from here too.
function reachLaunchRecent(dev) { if (typeof addRecent === 'function') { try { addRecent(dev); } catch (e) {} } }

// The nav rail folds while a Remote page is on screen (updateRailCompact,
// ui.go). In detached mode, for the embedded viewer only: not for a device
// whose session is in its own window, nor for one whose agent is offline.
function reachRailFolds(dev) {
  if (!reachDetachOn()) return true;
  return !!(dev && dev.oblireach && dev.oblireach.online) && !reachIsDetached(dev);
}

function reachFocusDetached(dev) {
  if (!dev) return;
  var key = devKey(dev);
  reachNative('reachFocusWindow', key).then(function (ok) {
    if (ok === false) window.__reachOnDetached(Array.from(reachDetachedIds).filter(function (i) { return i !== key; }));
  }).catch(function () {});
}

// "Bring it back here": its window closes (its session ends), the session
// starts again in the main window, on the Windows session and with the view
// only state that window has now (saved by it; else those given at detach).
function reachBringBack(dev) {
  if (!dev) return;
  var key = devKey(dev), info = reachDetachedTakeState(key) || reachDetachedInfo[key] || { session: -1, viewOnly: false };
  reachNative('reachCloseWindow', key).catch(function () {});
  reachDetachedIds.delete(key);
  delete reachDetachedInfo[key];
  if (!selectedDevice || !devSame(selectedDevice, dev)) return;
  openDeviceDetail(dev, 'remote');
  var tab = addSessionTab(dev);
  tab.reconnectAttempts = 0;
  if (info.viewOnly) tab.viewOnly = true;
  startRemote(info.session >= 0 ? info.session : undefined, tab);
}

// Sign-out, server switch: the windows go with the sessions of this server.
function reachCloseAllDetached() {
  if (reachDetachedMode) return;
  reachNative('reachCloseWindows').catch(function () {});
  reachDetachedIds = new Set();
  reachDetachedInfo = {};
}

// Main page (re)loaded: the windows still open (a reload keeps them).
function reachSyncDetached() {
  if (reachDetachedMode) return;
  reachNative('reachListWindows').then(function (ids) { window.__reachOnDetached(ids); }).catch(function () {});
}

// The device on screen got or lost its window: its Remote pane follows.
function reachDetachedChanged(before) {
  if (typeof currentRoute === 'undefined' || currentRoute !== 'device-detail' || !selectedDevice) return;
  var key = devKey(selectedDevice);
  if (before.has(key) !== reachDetachedIds.has(key)) openDeviceDetail(selectedDevice, 'remote');
}
// ids: the keys of the devices that have a window (a bare id: a device of the
// server on screen).
window.__reachOnDetached = function (ids) {
  if (reachDetachedMode) return;
  var before = reachDetachedIds;
  reachDetachedIds = new Set((Array.isArray(ids) ? ids : []).map(keyNorm));
  before.forEach(function (k) { if (!reachDetachedIds.has(k)) { delete reachDetachedInfo[k]; reachDetachedForgetState(k); } });
  reachDetachedChanged(before);
};
window.__reachOnDetachFailed = function (id, msg) {
  if (reachDetachedMode) return;
  var before = new Set(reachDetachedIds), key = keyNorm(id);
  reachDetachedIds.delete(key);
  delete reachDetachedInfo[key];
  reachDetachedChanged(before); // first: the pane is rebuilt, and the toast with it
  // (reachNotify: launched from the Devices list, no picture is on screen)
  reachNotify(msg ? T('viewer.windowOpenFailed', { error: msg }) : T('viewer.windowOpenFailedNoReason'), 'warn');
};

// Main window, device with its own window: what its Remote pane shows.
function reachRenderDetachedPane(tc, dev) {
  var box = document.createElement('div');
  box.className = 'empty reach-detached-pane'; box.id = 'reach-detached-pane';
  var ico = document.createElement('div'); ico.className = 'empty-icon'; ico.innerHTML = REACH_ICON.popout.replace(/width="12" height="12"/, 'width="44" height="44"');
  var t = document.createElement('div'); t.className = 'rd-title'; t.textContent = T('viewer.detachedTitle');
  var p = document.createElement('div'); p.className = 'rd-text';
  p.textContent = T('viewer.detachedText');
  var row = document.createElement('div'); row.className = 'rd-actions';
  var show = document.createElement('button'); show.className = 'btn-sm reach-btn-primary'; show.textContent = T('viewer.showWindow');
  show.addEventListener('click', function () { reachFocusDetached(dev); });
  var back = document.createElement('button'); back.className = 'btn-sm'; back.textContent = T('viewer.bringBack');
  back.addEventListener('click', function () { reachBringBack(dev); });
  row.appendChild(show); row.appendChild(back);
  box.appendChild(ico); box.appendChild(t); box.appendChild(p); box.appendChild(row);
  tc.appendChild(box);
  // No session of this device here: the globals must not point at another
  // device's background tab, and the clipboard watcher follows.
  remoteWs = null; remoteDecoder = null;
  reachUpdateClipboardWatch();
  // Focus mode hides the menus and this pane has no toolbar to leave it.
  document.body.classList.remove('reach-focus');
}

// Toolbar: "Detach" (main window only; hidden in a detached one by CSS too).
function reachBuildDetachTool(toolbar) {
  var b = document.createElement('button');
  b.className = 'btn-sm reach-ico-btn tb-detach'; b.id = 'tb-detach';
  b.title = T('viewer.detach');
  b.innerHTML = REACH_ICON.popout;
  if (!reachCanDetach()) b.style.display = 'none';
  b.addEventListener('click', function () { reachDetachCurrent(); });
  toolbar.appendChild(b);
}
// Toolbar: Scripts / Info side panels (sidePanelToggle, ui.go), reachable in
// focus mode and in detached windows.
function reachBuildSideTools(toolbar) {
  [['scripts', 'common.scripts', 'detail.scriptsTip'], ['info', 'common.info', 'detail.infoTip']].forEach(function (d) {
    var b = document.createElement('button');
    b.className = 'btn-sm reach-ico-btn reach-side-btn'; b.id = 'tb-' + d[0]; b.title = T(d[2]);
    b.innerHTML = REACH_ICON[d[0]] + '<span>' + TH(d[1]) + '</span>';
    b.addEventListener('click', function () { sidePanelToggle(d[0]); });
    toolbar.appendChild(b);
  });
}
// Detached window: its "Close this window" buttons.
function reachCloseOwnWindow() { reachNative('reachCloseSelf').catch(function () {}); }

// ── Clipboard ──────────────────────────────────────────────────────────────
// Per-run secret of the native bridge (native_windows.go), only defined on the
// local UI origin.
function reachNativeToken() { return (typeof window.__reachNativeToken === 'string') ? window.__reachNativeToken : ''; }
function reachNativeWatch(on) {
  if (typeof window.reachClipboardWatch !== 'function' || !reachNativeToken()) return;
  try { var p = window.reachClipboardWatch(reachNativeToken(), !!on); if (p && p.catch) p.catch(function () {}); } catch (e) {}
}
async function reachNativeClipboardRead() {
  if (typeof window.reachClipboardRead === 'function' && reachNativeToken()) {
    try {
      var r = await window.reachClipboardRead(reachNativeToken());
      return { text: (r && !r.tooLarge && typeof r.text === 'string') ? r.text : '', hasFiles: !!(r && r.hasFiles), tooLarge: !!(r && r.tooLarge), seq: (r && r.seq) || 0 };
    } catch (e) {}
  }
  try { var t = await navigator.clipboard.readText(); return { text: t || '', hasFiles: false, tooLarge: false, seq: 0 }; }
  catch (e) { return { text: '', hasFiles: false, tooLarge: false, seq: 0, failed: true }; }
}
async function reachNativeClipboardWrite(text) {
  if (typeof window.reachClipboardWrite === 'function' && reachNativeToken()) {
    try { await window.reachClipboardWrite(reachNativeToken(), text); return true; } catch (e) {}
  }
  try { await navigator.clipboard.writeText(text); return true; } catch (e) { return false; }
}
function reachClipMax(tab) {
  var m = tab && tab.clipLimits && tab.clipLimits.maxText;
  return (typeof m === 'number' && m > 0) ? m : REACH.CLIP_MAX_TEXT;
}
function reachSendClipboardSet(tab, text, paste, quiet) {
  if (!tab || !tab.ws || tab.ws.readyState !== 1 || typeof text !== 'string') return false;
  if (reachUtf8Length(JSON.stringify(text)) > reachClipMax(tab)) {
    if (!quiet || reachUI(tab)) reachToast(T('viewer.clip.textTooLarge', { max: reachFmtBytes(reachClipMax(tab)) }), 'warn');
    return false;
  }
  var msg = { type: 'clipboard_set', text: text, id: 'c' + (++tab.clip.seq) };
  if (paste) msg.paste = true;
  tab.ws.send(JSON.stringify(msg));
  tab.clip.lastSentText = text;
  tab.clip.staged = null; // what was just sent is the latest local text
  return true;
}
// A session coming on screen (tab switch, start, sync toggled on) does NOT get
// the local clipboard right away: it may hold something copied for another
// customer while this session was in the background. The text is staged and
// only sent on a paste gesture (Ctrl+V / Shift+Insert, right or middle click)
// or from the Clipboard menu.
async function reachStageLocalClipboard(tab) {
  tab.clip.staged = null;
  if (tab.clip.remoteNewer) return; // the session copied something after us
  var stamp = tab.clip.stage;
  var r = await reachNativeClipboardRead();
  if (stamp !== tab.clip.stage || reachWatchTabId !== tab.id || reachDisplayedTab() !== tab) return;
  if (!r.text || r.text === tab.clip.lastSentText || r.text === tab.clip.lastRemoteText) return;
  tab.clip.staged = r.text;
}
function reachFlushStagedClipboard(tab) {
  var text = tab.clip.staged;
  if (typeof text !== 'string') return;
  tab.clip.staged = null;
  if (tab.clip.remoteNewer || !reachClipSyncEnabled() || !reachHas(tab, REACH.FEATURE.CLIP_TEXT)) return;
  if (text === tab.clip.lastSentText || text === tab.clip.lastRemoteText) return;
  reachSendClipboardSet(tab, text, false, true);
}
function reachRequestRemoteClipboard(tab, notify) {
  if (!tab) return;
  var id = 'g' + (++tab.clip.seq);
  tab.clip.getPending = { id: id, notify: !!notify };
  reachSend(tab, { type: 'clipboard_get', id: id });
}
// Local clipboard changed. The native watcher only runs while a syncing
// session is ON SCREEN (see reachUpdateClipboardWatch); the displayed-tab check
// below also drops a late notification after the operator navigated away.
window.__reachOnLocalClipboard = function (text, meta) {
  var tab = reachDisplayedTab();
  if (!tab || tab.viewOnly || !tab.ws || tab.ws.readyState !== 1 || !tab.gotInit) return;
  tab.clip.stage++;
  tab.clip.staged = null; // this copy supersedes any staged text
  tab.clip.remoteNewer = false; // the local side copied last
  // Copied while the operator worked in ANOTHER window of the app (detached
  // session windows, window_registry.go): that window's session got it, not
  // this one. It is staged here when this window comes back to the front
  // (reachOnWindowFocus), for a paste gesture only, like at a session start.
  if (meta && meta.elsewhere) { tab.clip.restage = true; return; }
  if (!reachHas(tab, REACH.FEATURE.CLIP_TEXT) || !reachClipSyncEnabled()) return;
  if (meta && meta.tooLarge) { reachToast(T('viewer.clip.tooLargeSync'), 'warn'); return; }
  if (typeof text !== 'string' || !text) return;
  if (text === tab.clip.lastRemoteText || text === tab.clip.lastSentText) return;
  reachSendClipboardSet(tab, text, false, true);
};
// The native watcher runs only while the session on screen (not merely the
// last active tab: the Dashboard, the Devices list or another device's page
// stop it) syncs its clipboard. Copies made while it runs are sent at once;
// what was on the clipboard before the session came on screen is only staged
// (reachStageLocalClipboard). Called on every navigation (navigateTo,
// switchTab), connection change and view-only toggle (view only: local copies
// stay local; the remote's copies still come here).
function reachUpdateClipboardWatch() {
  var tab = reachDisplayedTab();
  var on = !!(tab && !tab.viewOnly && tab.ws && tab.ws.readyState === 1 && tab.gotInit && reachHas(tab, REACH.FEATURE.CLIP_TEXT) && reachClipSyncEnabled());
  if (on !== reachWatchState) { reachWatchState = on; reachNativeWatch(on); }
  var newId = on ? tab.id : null;
  if (reachWatchTabId === newId) return;
  reachDropStaged(reachWatchTabId); // the session that left the screen forgets its staged text
  reachWatchTabId = newId;
  if (on) reachStageLocalClipboard(tab);
}
// This window came back to the front: a copy made meanwhile elsewhere is staged.
function reachOnWindowFocus() {
  var tab = reachDisplayedTab();
  if (!tab || !tab.clip || !tab.clip.restage || reachWatchTabId !== tab.id) return;
  tab.clip.restage = false;
  reachStageLocalClipboard(tab);
}
if (typeof window.addEventListener === 'function') window.addEventListener('focus', function () { reachOnWindowFocus(); });
function reachDropStaged(tabId) {
  if (tabId === null || typeof sessionTabs === 'undefined') return;
  for (var i = 0; i < sessionTabs.length; i++) if (sessionTabs[i].id === tabId) sessionTabs[i].clip.staged = null;
}
function reachSetClipSync(on) {
  reachSetPref('oblireach_clip_sync', on ? '1' : '0');
  sessionTabs.forEach(function (t) { if (reachHas(t, REACH.FEATURE.CLIP_PUSH)) reachSend(t, { type: 'clipboard_sync', push: on }); });
  reachUpdateClipboardWatch(); // stages the local clipboard when sync turns on
  reachToast(on ? T('viewer.clip.syncOn') : T('viewer.clip.syncOff'), 'ok');
}
function reachOnClipboardContent(tab, msg) {
  if (typeof msg.text !== 'string') return;
  var pushed = msg.origin === 'remote_change';
  var pending = tab.clip.getPending;
  var notify = !!(pending && (!msg.id || msg.id === pending.id) && pending.notify);
  if (!pushed) tab.clip.getPending = null;
  if (pushed && !reachClipSyncEnabled()) return;
  if (pushed) { tab.clip.stage++; tab.clip.staged = null; } // the remote copied last
  if (pushed && reachDisplayedTab() !== tab) {
    // A session that is not on screen (background tab, or the operator is on
    // another page) never overwrites the local clipboard, but its copy is
    // newer than ours: it is not overwritten when the session comes back.
    tab.clip.lastRemoteText = msg.text;
    tab.clip.remoteNewer = true;
    return;
  }
  tab.clip.lastRemoteText = msg.text;
  if (pushed) tab.clip.remoteNewer = true;
  if (pushed && msg.text === tab.clip.lastSentText) return; // our own text coming back
  if (!msg.text && !notify) return;
  reachNativeClipboardWrite(msg.text).then(function (ok) {
    if (!ok) { if (reachUI(tab)) reachToast(T('viewer.clip.writeFailed'), 'warn'); return; }
    if (notify) reachToast(msg.text ? T('viewer.clip.copiedHere') : T('viewer.clip.remoteEmpty'), 'ok');
  });
  if (msg.truncated && reachUI(tab)) reachToast(T('viewer.clip.truncated'), 'warn');
}
function reachOnClipboardAck(tab, msg) {
  if (msg.ok !== false || !reachUI(tab)) return;
  var code = msg.code;
  reachToast(code === 'too_large' ? T('viewer.clip.ackTooLarge')
    : code === 'unavailable' ? T('viewer.clip.ackUnavailable')
    : code === 'busy' ? T('viewer.clip.ackBusy')
    : T('viewer.clip.ackFailed'), 'warn');
}
async function reachSendLocalClipboard(tab, paste) {
  if (!tab || !tab.ws) return;
  var r = await reachNativeClipboardRead();
  if (r.tooLarge) { reachToast(T('viewer.clip.tooLargeSend'), 'warn'); return; }
  if (!r.text) {
    if (r.hasFiles && tab.filesDenied) { reachToast(reachFilesDeniedText(tab), 'warn'); return; }
    if (r.hasFiles && reachHas(tab, REACH.FEATURE.FILE_UPLOAD)) { reachSendClipboardFiles(tab); return; }
    reachToast(T('viewer.clip.noText'), 'warn');
    return;
  }
  var agentPastes = paste && reachHas(tab, REACH.FEATURE.CLIP_TEXT);
  if (!reachSendClipboardSet(tab, r.text, agentPastes)) return;
  if (paste && !agentPastes) {
    // Older agents: paste with a key combination.
    setTimeout(function () { sendSystemKeys([{ key: 'Control', code: 'ControlLeft' }, { key: 'v', code: 'KeyV' }]); }, 150);
  }
  reachToast(paste ? T('viewer.clip.sentPasted') : T('viewer.clip.sent'), 'ok');
}
// Old toolbar entry points (kept for callers of the previous viewer).
function clipboardPasteToRemote() { reachSendLocalClipboard(reachDisplayedTab(), false); }
function clipboardCopyFromRemote() { reachRequestRemoteClipboard(reachDisplayedTab(), true); }

// ── Files ──────────────────────────────────────────────────────────────────
function reachQueueUpload(tab, src) {
  if (!tab || !tab.transfers) return;
  var r = tab.transfers.enqueueUpload(src);
  if (!r.ok) reachToast(r.code === 'too_large' ? T('viewer.file.tooLargeMax', { name: src.name, max: reachFmtBytes(tab.transfers.limits.maxFile) }) : T('viewer.file.cannotSend', { name: src.name }), 'warn');
}
function reachUploadFiles(tab, files) {
  if (tab && tab.filesDenied) { reachToast(reachFilesDeniedText(tab), 'warn'); return; }
  if (!reachHas(tab, REACH.FEATURE.FILE_UPLOAD)) { reachToast(T('viewer.file.agentCannotReceive'), 'warn'); return; }
  files.forEach(function (f) {
    reachQueueUpload(tab, { name: f.name, size: f.size, mime: f.type || '', read: function (off, len) {
      return f.slice(off, off + len).arrayBuffer().then(function (b) { return new Uint8Array(b); });
    } });
  });
}
// REACH_PASTE_MAX_TOTAL: the clipboard paste-as-RDP
// design — no per-file cap any more (streamed, never buffered in memory;
// enforced agent-side too, filexfer.go), but the whole paste is capped at
// 20 GiB, checked once here before any file of the batch is queued (an
// all-or-nothing refusal, not a partial paste).
var REACH_PASTE_MAX_TOTAL = 20 * 1024 * 1024 * 1024; // 20 GiB (float64: exact)
function reachUploadNativeFiles(tab, list) {
  var skipped = 0, total = 0, usable = [];
  list.forEach(function (f) {
    if (!f || f.dir || !f.handle) { skipped++; return; }
    total += Number(f.size) || 0;
    usable.push(f);
  });
  if (total > REACH_PASTE_MAX_TOTAL) {
    reachToast(T('viewer.file.pasteTotalTooLarge', { max: reachFmtBytes(REACH_PASTE_MAX_TOTAL) }), 'warn');
    return;
  }
  usable.forEach(function (f) {
    var handle = f.handle;
    reachQueueUpload(tab, { name: f.name, size: f.size, mime: '', paste: true, read: function (off, len) {
      return window.reachReadFileChunk(reachNativeToken(), handle, off, len).then(reachB64ToBytes);
    } });
  });
  if (skipped) reachToast(T('viewer.file.foldersSkipped'), 'warn');
}
async function reachSendClipboardFiles(tab) {
  if (!tab) return;
  if (tab.filesDenied) { reachToast(reachFilesDeniedText(tab), 'warn'); return; }
  if (!reachHas(tab, REACH.FEATURE.FILE_UPLOAD)) { reachToast(T('viewer.file.agentCannotReceive'), 'warn'); return; }
  if (typeof window.reachClipboardFiles !== 'function') return;
  var list = [];
  try { list = await window.reachClipboardFiles(reachNativeToken()); } catch (e) { reachToast(T('viewer.file.readCopiedFailed'), 'warn'); return; }
  if (!Array.isArray(list) || !list.length) { reachToast(T('viewer.file.noneCopied'), 'warn'); return; }
  reachUploadNativeFiles(tab, list);
}
function reachSaveBlob(blob, name) {
  var url = URL.createObjectURL(blob);
  var a = document.createElement('a');
  a.href = url; a.download = name || 'download'; a.style.display = 'none';
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(function () { URL.revokeObjectURL(url); }, 60000);
}
function reachRenderTransfers(tab) {
  var el = document.getElementById('reach-transfers');
  if (!el || !reachUI(tab)) return;
  var items = tab.transfers ? tab.transfers.visibleItems() : [];
  var canDown = reachHas(tab, REACH.FEATURE.FILE_DOWNLOAD);
  var rfiles = canDown ? (tab.remoteFiles || []) : [];
  el.innerHTML = '';
  if (!items.length && !rfiles.length) { el.style.display = 'none'; return; }
  el.style.display = 'block';
  var head = document.createElement('div'); head.className = 'reach-tr-head';
  var ht = document.createElement('span'); ht.textContent = T('viewer.tr.title'); head.appendChild(ht);
  var sp = document.createElement('span'); sp.className = 'sp'; head.appendChild(sp);
  var clr = document.createElement('button'); clr.className = 'reach-tr-x'; clr.title = T('viewer.tr.clearFinished'); clr.textContent = '\u2715';
  clr.addEventListener('click', function () { tab.remoteFiles = []; if (tab.transfers) tab.transfers.clearFinished(); reachRenderTransfers(tab); });
  head.appendChild(clr);
  el.appendChild(head);
  items.forEach(function (it) {
    var row = document.createElement('div'); row.className = 'reach-tr-row';
    var line = document.createElement('div'); line.className = 'reach-tr-line';
    var arrow = document.createElement('span'); arrow.textContent = it.dir === 'up' ? '\u2191' : '\u2193'; line.appendChild(arrow);
    var nm = document.createElement('span'); nm.className = 'reach-tr-name'; nm.textContent = it.name; nm.title = it.path || it.name; line.appendChild(nm);
    var meta = document.createElement('span'); meta.className = 'reach-tr-meta';
    var pct = it.size > 0 ? Math.floor(it.done * 100 / it.size) : (it.state === 'done' ? 100 : 0);
    meta.textContent = it.state === 'done' ? T('viewer.tr.done') : it.state === 'cancelled' ? T('viewer.tr.cancelled') : it.state === 'error' ? T('viewer.tr.failed')
      : it.state === 'queued' ? T('viewer.tr.queued') : T('viewer.tr.pctOf', { pct: pct, size: reachFmtBytes(it.size) });
    line.appendChild(meta);
    var active = !(it.state === 'done' || it.state === 'error' || it.state === 'cancelled');
    if (active) {
      var x = document.createElement('button'); x.className = 'reach-tr-x'; x.title = T('common.cancel'); x.textContent = '\u2715';
      x.addEventListener('click', function () { if (tab.transfers) tab.transfers.cancel(it); });
      line.appendChild(x);
    }
    row.appendChild(line);
    if (active) {
      var bar = document.createElement('div'); bar.className = 'reach-tr-bar';
      var fill = document.createElement('div'); fill.style.width = pct + '%'; bar.appendChild(fill);
      row.appendChild(bar);
    }
    if (it.state === 'error' && it.error) { var er = document.createElement('div'); er.className = 'reach-tr-err'; er.textContent = it.error; row.appendChild(er); }
    el.appendChild(row);
  });
  if (rfiles.length) {
    var h2 = document.createElement('div'); h2.className = 'reach-tr-head';
    h2.textContent = T('viewer.tr.copiedOnRemote');
    el.appendChild(h2);
    rfiles.forEach(function (f) {
      var row = document.createElement('div'); row.className = 'reach-tr-row';
      var line = document.createElement('div'); line.className = 'reach-tr-line';
      var nm = document.createElement('span'); nm.className = 'reach-tr-name'; nm.textContent = f.name; line.appendChild(nm);
      var meta = document.createElement('span'); meta.className = 'reach-tr-meta'; meta.textContent = f.dir ? T('viewer.tr.folder') : reachFmtBytes(f.size); line.appendChild(meta);
      var b = document.createElement('button'); b.className = 'btn-sm'; b.style.padding = '2px 8px'; b.textContent = T('viewer.tr.download');
      if (f.dir) { b.disabled = true; b.title = T('viewer.tr.foldersUnsupported'); }
      b.addEventListener('click', function () { if (tab.transfers && !f.dir) tab.transfers.requestDownload(f); });
      line.appendChild(b);
      row.appendChild(line);
      el.appendChild(row);
    });
  }
}

// ── Screens & mosaic ───────────────────────────────────────────────────────
function reachProbeDecodeBudget() {
  if (reachDecodeBudgetPromise) return reachDecodeBudgetPromise;
  reachDecodeBudgetPromise = (async function () {
    if (typeof VideoDecoder === 'undefined' || !VideoDecoder.isConfigSupported) return { w: 1920, h: 1080 };
    for (var i = 0; i < REACH.MOSAIC_BUDGETS.length; i++) {
      var b = REACH.MOSAIC_BUDGETS[i];
      try {
        var r = await VideoDecoder.isConfigSupported({ codec: 'avc1.640034', codedWidth: b[0], codedHeight: b[1] });
        if (r && r.supported) return { w: b[0], h: b[1] };
      } catch (e) {}
    }
    return { w: 1920, h: 1080 };
  })();
  return reachDecodeBudgetPromise;
}
function reachArmViewPending(tab) {
  tab.viewPending = true;
  if (tab.viewPendingTimer) clearTimeout(tab.viewPendingTimer);
  tab.viewPendingTimer = setTimeout(function () { tab.viewPendingTimer = null; tab.viewPending = false; reachRefreshToolbar(tab); }, 10000);
}
async function reachRequestMosaic(tab) {
  if (!reachCanMosaic(tab) || tab.viewPending) return;
  tab.wantMosaic = true;
  reachSetPref('oblireach_view_mode', 'mosaic');
  reachArmViewPending(tab);
  reachRefreshToolbar(tab);
  var b = await reachProbeDecodeBudget();
  if (!reachSend(tab, { type: 'set_view', mode: 'mosaic', maxWidth: b.w, maxHeight: b.h, maxPixels: Math.min(b.w * b.h, REACH.MOSAIC_MAX_PIXELS) })) {
    tab.viewPending = false;
    reachRefreshToolbar(tab);
  }
}
function reachSelectMonitor(tab, index) {
  if (!tab || !tab.ws || typeof index !== 'number') return;
  document.querySelectorAll('.toolbar-dropdown-menu.open').forEach(function (m) { m.classList.remove('open'); });
  tab.wantMosaic = false;
  reachSetPref('oblireach_view_mode', 'monitor');
  if (!reachIsMosaic(tab.view) && tab.monitor === index) return;
  if (reachHas(tab, REACH.FEATURE.VIEW_GEN) || reachHas(tab, REACH.FEATURE.MOSAIC)) {
    reachArmViewPending(tab);
    reachSend(tab, { type: 'set_view', mode: 'monitor', index: index });
  } else {
    // Legacy agents: no view generation, no init guaranteed after the switch.
    reachSend(tab, { type: 'set_monitor', index: index });
    tab.monitor = index;
    tab.lastSingle = index;
    if (tab.gotInit) reachSetStatus(tab, reachStreamStatus(tab));
  }
  reachRefreshToolbar(tab);
}
function reachDefaultSingle(tab) {
  var has = function (i) { return tab.monitors.some(function (m) { return m.index === i; }); };
  if (typeof tab.lastSingle === 'number' && tab.lastSingle >= 0 && has(tab.lastSingle)) return tab.lastSingle;
  var p = tab.monitors.filter(function (m) { return m.primary; })[0];
  return p ? p.index : (tab.monitors[0] ? tab.monitors[0].index : 0);
}
function reachToggleMosaic(tab) {
  if (!tab || !tab.gotInit || tab.viewPending) return;
  if (reachIsMosaic(tab.view)) reachSelectMonitor(tab, reachDefaultSingle(tab));
  else reachRequestMosaic(tab);
}
function reachRenderScreensMenu(tab) {
  var menu = document.getElementById('reach-screens-menu');
  if (!menu) return;
  menu.innerHTML = '';
  if (!tab || !tab.monitors.length) return;
  var mosaic = reachIsMosaic(tab.view);
  var W = 244, H = 120;
  var map = document.createElement('div'); map.className = 'reach-minimap';
  map.style.width = W + 'px'; map.style.height = H + 'px';
  reachMonitorsLayout(tab.monitors, W, H, 8).forEach(function (b) {
    var d = document.createElement('button');
    d.className = 'reach-minimap-mon' + ((mosaic || b.index === tab.monitor) ? ' active' : '');
    d.style.left = b.left + 'px'; d.style.top = b.top + 'px'; d.style.width = b.width + 'px'; d.style.height = b.height + 'px';
    d.textContent = String(b.index + 1);
    d.title = T('viewer.screens.show', { n: b.index + 1 });
    d.addEventListener('click', function (ev) { ev.stopPropagation(); reachSelectMonitor(tab, b.index); });
    map.appendChild(d);
  });
  menu.appendChild(map);
  tab.monitors.forEach(function (m) {
    var b = document.createElement('button');
    var cur = !mosaic && m.index === tab.monitor;
    b.textContent = (cur ? '\u2713 ' : '') + T('viewer.screens.item', { n: m.index + 1, w: m.width, h: m.height }) + (m.primary ? ' ' + T('viewer.screens.main') : '') + (m.rotation ? ' \u21BB' : '');
    b.addEventListener('click', function (ev) { ev.stopPropagation(); reachSelectMonitor(tab, m.index); });
    menu.appendChild(b);
  });
  if (reachCanMosaic(tab)) {
    var sep = document.createElement('div'); sep.className = 'reach-menu-sep'; menu.appendChild(sep);
    var all = document.createElement('button');
    all.textContent = (mosaic ? '\u2713 ' : '') + T('viewer.screens.allMosaic');
    all.addEventListener('click', function (ev) { ev.stopPropagation(); menu.classList.remove('open'); if (!mosaic) reachRequestMosaic(tab); });
    menu.appendChild(all);
  }
}
// Numbered label on each screen of the mosaic; click = show only that screen.
function reachRenderTiles(tab) {
  var layer = document.getElementById('reach-tiles');
  var ui = reachUI(tab);
  if (!layer || !ui) return;
  layer.innerHTML = '';
  if (!tab.ws || !tab.gotInit || !reachIsMosaic(tab.view) || tab.view.tiles.length < 2 || ui.canvas.style.display === 'none') { layer.style.display = 'none'; return; }
  var c = ui.canvas, bw = c.clientWidth, bh = c.clientHeight;
  if (!(bw > 0) || !(bh > 0)) { layer.style.display = 'none'; return; }
  layer.style.display = 'block';
  layer.style.left = c.offsetLeft + 'px'; layer.style.top = c.offsetTop + 'px';
  layer.style.width = bw + 'px'; layer.style.height = bh + 'px';
  var vw = tab.view.width || tab.agentW, vh = tab.view.height || tab.agentH;
  var fit = zoomLevel === 'Fit';
  tab.view.tiles.forEach(function (t) {
    var p0 = reachFrameToBox(t.frame.x, t.frame.y, bw, bh, vw, vh, fit);
    var p1 = reachFrameToBox(t.frame.x + t.frame.w, t.frame.y + t.frame.h, bw, bh, vw, vh, fit);
    var box = document.createElement('div'); box.className = 'reach-tile';
    box.style.left = p0.x + 'px'; box.style.top = p0.y + 'px';
    box.style.width = Math.max(0, p1.x - p0.x) + 'px'; box.style.height = Math.max(0, p1.y - p0.y) + 'px';
    var lbl = document.createElement('button'); lbl.className = 'reach-tile-label';
    lbl.textContent = String(t.index + 1) + (t.primary ? ' \u2605' : '');
    lbl.title = T('viewer.screens.showOnly', { n: t.index + 1 });
    lbl.addEventListener('mousedown', function (ev) { ev.stopPropagation(); });
    lbl.addEventListener('click', function (ev) { ev.stopPropagation(); reachSelectMonitor(tab, t.index); });
    box.appendChild(lbl);
    layer.appendChild(box);
  });
}
function reachRefreshToolbar(tab) {
  if (!reachUI(tab)) return;
  var live = !!(tab.ws && tab.gotInit);
  reachRefreshPresence(tab); // (reach_presence.go)
  reachRefreshBlank(tab);
  // System keys and quick actions type into the session: only while it streams.
  var sk = document.getElementById('reach-syskeys-btn');
  if (sk) sk.disabled = !live;
  var cad = document.getElementById('reach-cad-btn'); // (Ctrl+Alt+Del, one click: ui.go)
  if (cad) cad.disabled = !live;
  var qa = document.getElementById('qa-toggle');
  if (qa) qa.disabled = !live;
  if (!live) {
    var skm = document.getElementById('reach-syskeys-menu');
    if (skm) skm.classList.remove('open');
    var qad = document.getElementById('quick-actions');
    if (qad) qad.classList.remove('open');
  }
  var multi = live && tab.monitors.length > 1;
  var mosaic = reachIsMosaic(tab.view);
  var scr = document.getElementById('reach-screens');
  if (scr) scr.style.display = multi ? '' : 'none';
  var sb = document.getElementById('reach-screens-btn');
  if (sb) {
    sb.innerHTML = REACH_ICON.monitor;
    var lab = document.createElement('span');
    lab.textContent = mosaic ? T('viewer.screens.all') : T('viewer.screens.current', { n: (typeof tab.monitor === 'number' && tab.monitor >= 0) ? (tab.monitor + 1) : '?', total: tab.monitors.length });
    sb.appendChild(lab);
  }
  var mb = document.getElementById('reach-mosaic-btn');
  if (mb) {
    mb.style.display = (live && reachCanMosaic(tab)) ? '' : 'none';
    mb.classList.toggle('active', mosaic);
    mb.disabled = !!tab.viewPending;
    mb.title = mosaic ? T('viewer.screens.backToOne') : T('viewer.screens.mosaicTitle');
  }
  var ab = document.getElementById('reach-audio-btn');
  if (ab) {
    ab.style.display = (live && tab.audioAvail) ? '' : 'none';
    ab.innerHTML = tab.audioOn ? REACH_ICON.sound : REACH_ICON.mute;
    ab.title = tab.audioOn ? T('viewer.audio.mute') : T('viewer.audio.play');
    ab.classList.toggle('active', !!tab.audioOn);
  }
  var fb = document.getElementById('reach-file-btn');
  if (fb) {
    // Refused in this session: shown dimmed, its tooltip and a click say why.
    var fDenied = !!(live && tab.filesDenied);
    fb.style.display = (live && (reachHas(tab, REACH.FEATURE.FILE_UPLOAD) || fDenied)) ? '' : 'none';
    fb.style.opacity = fDenied ? '0.45' : '';
    fb.setAttribute('aria-disabled', fDenied ? 'true' : 'false');
    fb.title = fDenied ? reachFilesDeniedText(tab) : T('viewer.file.sendTitle');
  }
  reachRefreshViewOnly(tab);
  reachRefreshInputBlock(tab);
  reachRefreshLockOnEnd(tab);
  reachRefreshResolution(tab);
  reachRefreshCodec(tab); // (reach_codec.go)
  vmRefreshTools(tab); // (a VM console's own tools: vm_console.go)
  if (devIsSupport(tab.device)) supChatRefresh(tab); // (a code session's chat: support_chat.go)
}
function reachRenderClipMenu(tab) {
  var menu = document.getElementById('reach-clip-menu');
  if (!menu) return;
  menu.innerHTML = '';
  var live = !!(tab && tab.ws && tab.gotInit);
  var add = function (label, run, disabled) {
    var b = document.createElement('button');
    b.textContent = label;
    b.disabled = !!disabled;
    b.addEventListener('click', function (ev) { ev.stopPropagation(); menu.classList.remove('open'); if (!disabled) run(); });
    menu.appendChild(b);
  };
  // A VM console has no shared clipboard: "send" types the local text into
  // the VM, like Ctrl+V there (vm_console.go vmPasteClipboard); nothing to
  // read back, no files, no synchronisation.
  if (tab && devIsVm(tab.device)) {
    var canType = live && reachHas(tab, VM_FEATURE.TYPE_TEXT) && !tab.viewOnly;
    add(T('vm.clip.typeClipboard'), function () { vmPasteClipboard(tab, false); }, !canType);
    add(T('vm.type.button') + '…', function () { vmOpenTypeText(tab); }, !canType);
    var vnote = document.createElement('div'); vnote.className = 'reach-menu-note'; vnote.id = 'reach-clip-vm-note';
    vnote.textContent = (live && !reachHas(tab, VM_FEATURE.TYPE_TEXT)) ? T('vm.type.unavailable') : T('vm.clip.hint');
    menu.appendChild(vnote);
    return;
  }
  add(T('viewer.clip.sendMine'), function () { reachSendLocalClipboard(tab, false); }, !live);
  add(T('viewer.clip.sendPaste'), function () { reachSendLocalClipboard(tab, true); }, !live);
  add(T('viewer.clip.getRemote'), function () { reachRequestRemoteClipboard(tab, true); }, !live);
  if (live && reachHas(tab, REACH.FEATURE.FILE_UPLOAD)) add(T('viewer.clip.sendFiles'), function () { reachSendClipboardFiles(tab); }, false);
  else if (live && tab.filesDenied) {
    // Refused in this session: disabled, with the reason under it.
    add(T('viewer.clip.sendFiles'), function () {}, true);
    var fdn = document.createElement('div'); fdn.className = 'reach-menu-note'; fdn.id = 'reach-files-denied-note';
    fdn.textContent = reachFilesDeniedText(tab);
    menu.appendChild(fdn);
  }
  var sep = document.createElement('div'); sep.className = 'reach-menu-sep'; menu.appendChild(sep);
  var sync = reachClipSyncEnabled();
  add((sync ? '\u2713 ' : '') + T('viewer.clip.syncAuto'), function () { reachSetClipSync(!sync); }, false);
  var note = document.createElement('div'); note.className = 'reach-menu-note';
  note.textContent = (live && !reachHas(tab, REACH.FEATURE.CLIP_TEXT))
    ? T('viewer.clip.agentTooOld')
    : T('viewer.clip.hint');
  menu.appendChild(note);
}

// ── Toolbar / viewport builders (called by renderRemoteTab) ────────────────
function reachDropdown(toolbar, id, btnInner, title, onOpen, menuClass) {
  var wrap = document.createElement('div'); wrap.className = 'toolbar-dropdown'; wrap.id = id;
  var btn = document.createElement('button'); btn.className = 'btn-sm reach-ico-btn'; btn.id = id + '-btn'; btn.title = title; btn.innerHTML = btnInner;
  var menu = document.createElement('div'); menu.className = 'toolbar-dropdown-menu' + (menuClass ? ' ' + menuClass : ''); menu.id = id + '-menu';
  btn.addEventListener('click', function () {
    var opening = !menu.classList.contains('open');
    document.querySelectorAll('.toolbar-dropdown-menu.open').forEach(function (m) { if (m !== menu) m.classList.remove('open'); });
    if (opening) onOpen(reachDisplayedTab());
    menu.classList.toggle('open', opening);
  });
  wrap.appendChild(btn); wrap.appendChild(menu);
  toolbar.appendChild(wrap);
  return wrap;
}
function reachBuildClipboardTools(toolbar) {
  reachDropdown(toolbar, 'reach-clip', REACH_ICON.clipboard, T('viewer.clip.title'), reachRenderClipMenu);
  var fb = document.createElement('button');
  fb.className = 'btn-sm'; fb.id = 'reach-file-btn'; fb.style.display = 'none';
  fb.title = T('viewer.file.sendTitle');
  fb.innerHTML = REACH_ICON.upload;
  var input = document.createElement('input');
  input.type = 'file'; input.multiple = true; input.style.display = 'none';
  input.addEventListener('change', function () {
    var tab = reachDisplayedTab();
    if (tab && input.files && input.files.length) reachUploadFiles(tab, Array.prototype.slice.call(input.files));
    input.value = '';
  });
  fb.addEventListener('click', function () {
    var tab = reachDisplayedTab();
    if (tab && tab.filesDenied) { reachToast(reachFilesDeniedText(tab), 'warn'); return; } // refused in this session
    input.click();
  });
  toolbar.appendChild(fb);
  toolbar.appendChild(input);
}
function reachBuildViewTools(toolbar) {
  var wrap = reachDropdown(toolbar, 'reach-screens', REACH_ICON.monitor, T('viewer.screens.title'), reachRenderScreensMenu, 'reach-screens-menu');
  wrap.style.display = 'none';
  var mb = document.createElement('button');
  mb.className = 'btn-sm reach-ico-btn'; mb.id = 'reach-mosaic-btn'; mb.style.display = 'none';
  mb.innerHTML = REACH_ICON.grid + '<span>' + TH('viewer.screens.mosaic') + '</span>';
  mb.title = T('viewer.screens.mosaicTitle');
  mb.addEventListener('click', function () { reachToggleMosaic(reachDisplayedTab()); });
  toolbar.appendChild(mb);
}
// View only toggle: enabled once this device has a session tab (it can be set
// before the picture arrives), reflects the tab on screen (reachRefreshViewOnly).
function reachBuildViewOnlyTool(toolbar) {
  var b = document.createElement('button');
  b.className = 'btn-sm reach-ico-btn reach-vo-btn'; b.id = 'reach-vo-btn'; b.disabled = true;
  b.title = T('viewer.viewOnlyTitle'); b.innerHTML = REACH_ICON.viewOnly;
  b.addEventListener('click', function () { reachToggleViewOnly(reachDisplayedTab()); });
  toolbar.appendChild(b);
}
// Block the remote user's mouse and keyboard: enabled while the session on
// screen streams, shows what the agent confirmed (reachRefreshInputBlock).
function reachBuildInputBlockTool(toolbar) {
  var b = document.createElement('button');
  b.className = 'btn-sm reach-ico-btn reach-block-btn'; b.id = 'reach-block-btn'; b.disabled = true; b.style.display = 'none';
  b.title = T('viewer.inputBlock.title'); b.innerHTML = REACH_ICON.unlock + '<span>' + TH('viewer.inputBlock.short') + '</span>';
  b.addEventListener('click', function () { reachToggleInputBlock(reachDisplayedTab()); });
  toolbar.appendChild(b);
}
// Lock the session when the connection ends: hidden until the session on
// screen says its state (reachRefreshLockOnEnd).
function reachBuildLockOnEndTool(toolbar) {
  var b = document.createElement('button');
  b.className = 'btn-sm reach-lockend-btn'; b.id = 'reach-lockend-btn'; b.style.display = 'none';
  b.title = T('viewer.lockOnEnd.titleOff'); b.innerHTML = REACH_ICON.unlock;
  b.addEventListener('click', function () { reachToggleLockOnEnd(reachDisplayedTab()); });
  toolbar.appendChild(b);
}
function reachBuildAudioTool(toolbar) {
  var b = document.createElement('button');
  b.className = 'btn-sm'; b.id = 'reach-audio-btn'; b.style.display = 'none';
  b.title = T('viewer.audio.play'); b.innerHTML = REACH_ICON.mute;
  b.addEventListener('click', function () { reachToggleAudio(reachDisplayedTab()); });
  toolbar.appendChild(b);
}
function reachBuildViewportExtras(vp) {
  var tiles = document.createElement('div'); tiles.className = 'reach-tiles'; tiles.id = 'reach-tiles'; vp.appendChild(tiles);
  var overlay = document.createElement('div'); overlay.className = 'reach-overlay'; overlay.id = 'reach-overlay'; vp.appendChild(overlay);
  var tray = document.createElement('div'); tray.className = 'reach-transfers'; tray.id = 'reach-transfers'; vp.appendChild(tray);
  var drop = document.createElement('div'); drop.className = 'reach-drop'; drop.id = 'reach-drop';
  drop.textContent = T('viewer.file.drop');
  vp.appendChild(drop);
  var toast = document.createElement('div'); toast.className = 'reach-toast'; toast.id = 'reach-toast'; vp.appendChild(toast);
  // View only indicator on the picture (the auto-hidden toolbar may not show
  // its button). Not clickable: a stray click never gives the control back.
  var vo = document.createElement('div'); vo.className = 'reach-vo-chip'; vo.id = 'reach-vo-chip';
  vo.innerHTML = REACH_ICON.viewOnly + '<span>' + TH('viewer.viewOnlyShort') + '</span>';
  vp.appendChild(vo);
  // Remote user's input blocked: said on the picture too (same reason).
  var bc = document.createElement('div'); bc.className = 'reach-block-chip'; bc.id = 'reach-block-chip';
  bc.innerHTML = REACH_ICON.lock + '<span>' + TH('viewer.inputBlock.chip') + '</span>';
  vp.appendChild(bc);

  var hasFiles = function (e) { var t = e.dataTransfer && e.dataTransfer.types; return !!t && Array.prototype.indexOf.call(t, 'Files') >= 0; };
  vp.addEventListener('dragover', function (e) {
    if (!hasFiles(e)) return;
    var tab = reachDisplayedTab();
    if (!tab || !tab.gotInit) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = reachHas(tab, REACH.FEATURE.FILE_UPLOAD) ? 'copy' : 'none';
    // Refused in this session: the drop zone says why (and nothing is dropped).
    var denied = !!tab.filesDenied;
    drop.classList.toggle('denied', denied);
    var dropText = denied ? reachFilesDeniedText(tab) : T('viewer.file.drop');
    if (drop.textContent !== dropText) drop.textContent = dropText;
    if (reachHas(tab, REACH.FEATURE.FILE_UPLOAD) || denied) drop.classList.add('visible');
  });
  vp.addEventListener('dragleave', function (e) { if (!e.relatedTarget || !vp.contains(e.relatedTarget)) drop.classList.remove('visible'); });
  vp.addEventListener('drop', function (e) {
    drop.classList.remove('visible');
    if (!hasFiles(e)) return;
    e.preventDefault();
    var tab = reachDisplayedTab();
    if (!tab || !tab.gotInit) return;
    var files = [], skipped = 0, items = e.dataTransfer.items;
    if (items && items.length) {
      for (var i = 0; i < items.length; i++) {
        var it = items[i];
        if (it.kind !== 'file') continue;
        var entry = it.webkitGetAsEntry ? it.webkitGetAsEntry() : null;
        if (entry && entry.isDirectory) { skipped++; continue; }
        var f = it.getAsFile();
        if (f) files.push(f);
      }
    } else {
      files = Array.prototype.slice.call(e.dataTransfer.files || []);
    }
    if (skipped) reachToast(T('viewer.file.foldersSkipped'), 'warn');
    if (files.length) reachUploadFiles(tab, files);
  });
  if (reachResizeObserver) { try { reachResizeObserver.disconnect(); } catch (x) {} }
  if (typeof ResizeObserver !== 'undefined') {
    reachResizeObserver = new ResizeObserver(function () { var t = reachDisplayedTab(); if (t) reachRenderTiles(t); });
    reachResizeObserver.observe(vp);
  }
}

// Called before the Remote pane is torn down (sub-tab switch, navigation,
// session tab switch): the session keeps its current picture, drawn back by
// reachRestoreView into the new canvas. The owner is the tab the canvas
// showed, not the active one (a session tab switch changes it first).
function reachStashPicture() {
  var c = document.getElementById('remote-canvas');
  var id = c && c._reachTabId;
  if (!id || typeof sessionTabs === 'undefined') return;
  for (var i = 0; i < sessionTabs.length; i++) {
    var t = sessionTabs[i];
    if (t.id === id) { if (t.shown && c.width > 0 && c.height > 0) t.lastCanvas = c; return; }
  }
}

// Called at the end of renderRemoteTab: puts the session of this device (if
// any) back on screen after the page was re-rendered.
function reachRestoreView() {
  if (!selectedDevice) return;
  var tab = null;
  for (var i = 0; i < sessionTabs.length; i++) if (devSame(sessionTabs[i].device, selectedDevice)) { tab = sessionTabs[i]; break; }
  if (!tab) {
    // No session for the device on screen: the globals must not keep pointing
    // at another device's background tab (the toolbar buttons stay disabled).
    remoteWs = null; remoteDecoder = null;
    reachUpdateClipboardWatch();
    return;
  }
  if (activeSessionTabId !== tab.id) { activeSessionTabId = tab.id; renderSessionTabs(); }
  remoteWs = tab.ws; remoteDecoder = tab.decoder;
  var ui = reachUI(tab);
  if (!ui) return;
  var sel = document.querySelector('#remote-pane .session-select');
  if (sel && tab.reconnectParams && tab.reconnectParams.wtsSessionId !== undefined && tab.reconnectParams.wtsSessionId !== null) sel.value = String(tab.reconnectParams.wtsSessionId);
  // (the target from now on is the new canvas; a picture shown only in a
  // mosaic's cell so far: that one, tab_groups.go)
  var lc = tab.lastCanvas || tab.groupCanvas;
  tab.lastCanvas = null;
  if (tab.ws && tab.gotInit && tab.agentW > 0) {
    ui.canvas.width = tab.agentW; ui.canvas.height = tab.agentH;
    // The agent sends a frame only when its screen changes: without the last
    // picture an idle remote screen would stay black on the new canvas.
    if (lc && lc.width === tab.agentW && lc.height === tab.agentH) {
      try { var ctx = ui.canvas.getContext('2d'); if (ctx) ctx.drawImage(lc, 0, 0); } catch (e) {}
    }
    tab.shown = false;
    reachShowStreamUI(tab);
    if (zoomLevel !== 'Fit') setZoomLevel(zoomLevel);
    if (perfInterval === null) startPerfHudTimer();
  } else {
    reachShowStopButton(tab, !!(tab.ws || tab.reconnectTimer));
  }
  if (tab.statusText && ui.status) ui.status.textContent = tab.statusText;
  reachRefreshToolbar(tab);
  reachRenderTiles(tab);
  reachRenderTransfers(tab);
  reachRenderOverlay(tab);
  reachUpdateClipboardWatch();
}

// The native clipboard watcher must never outlive this page (a reload, or the
// SSO page replacing it).
(function () {
  var off = function () {
    reachWatchState = false;
    reachNativeWatch(false);
  };
  off();
  window.addEventListener('pagehide', off);
})();

// WebView2 navigates to a file dropped anywhere outside a drop target, which
// would unload the whole app: swallow stray file drops (text drags are left alone).
(function () {
  var isFiles = function (e) { var t = e.dataTransfer && e.dataTransfer.types; return !!t && Array.prototype.indexOf.call(t, 'Files') >= 0; };
  document.addEventListener('dragover', function (e) {
    if (!isFiles(e) || e.defaultPrevented) return; // the remote viewport accepts drops itself
    e.preventDefault();
    e.dataTransfer.dropEffect = 'none';
  });
  document.addEventListener('drop', function (e) { if (isFiles(e)) e.preventDefault(); });
})();

// ═════════════════════════════════════════════════════════════════════════
// Window frame (frame_windows.go): the window has no Windows title bar. The
// top bar (the slim title bar in focus mode and in a detached window, the
// background of the sign-in page) is the title bar: a press on an empty part
// of it has Windows move the window (Aero Snap, drag from maximized), a double
// click maximizes / restores. The buttons on the right minimize, maximize /
// restore and close (a detached window: its session ends, as before); the
// band along the top edge resizes the window (the other edges are the
// standard invisible borders). Outside the app (no native bindings) none of
// it shows.
// ═════════════════════════════════════════════════════════════════════════
var reachWinState = { maximized: false, active: true };
function reachWinAvailable() { return typeof window.reachWindowDrag === 'function' && !!reachNativeToken(); }
// window.__reachOnWindowState (frame_windows.go pushState) and the state asked
// at load: the maximize / restore button, the dimmed inactive title bar.
function reachWinApply(s) {
  if (!s || typeof s !== 'object') return;
  reachWinState = { maximized: !!s.maximized, active: s.active !== false };
  var b = document.body;
  if (b && b.classList) {
    b.classList.toggle('win-max', reachWinState.maximized);
    b.classList.toggle('win-inactive', !reachWinState.active);
  }
  reachWinTitles();
  if (typeof dlWake === 'function') dlWake(); // (the device lists read at once back in front: devices_live.go)
}
// The tooltips of the window buttons (at load, at every state change and
// language switch): maximize / restore; a session window's close ends its sessions.
function reachWinTitles() {
  var set = function (id, text) { var b = document.getElementById(id); if (b) b.title = text; };
  set('wc-min', T('win.minimize'));
  set('wc-max', reachWinState.maximized ? T('win.restore') : T('win.maximize'));
  set('wc-close', reachDetachedMode ? T('win.closeDetached') : T('win.close'));
}
window.__reachOnWindowState = reachWinApply;
function reachWinControl(action) { return reachNative('reachWindowControl', action).catch(function () { return false; }); }
function reachWinCursorOf(el) {
  try { return (typeof getComputedStyle === 'function' && el && el.nodeType === 1) ? String(getComputedStyle(el).cursor || '') : ''; } catch (e) { return ''; }
}
function reachWinMouseDown(e) {
  if (!e || e.button !== 0 || e.defaultPrevented || !reachWinAvailable()) return;
  if (!reachWinDragZone(e.target, reachWinCursorOf)) return;
  e.preventDefault(); // no text selection, the focus stays where it is
  if (e.detail === 2) { reachWinControl('maximize'); return; }
  if (e.detail > 2) return;
  reachNative('reachWindowDrag', '').catch(function () {});
}
function reachWinWidth() { return (typeof window.innerWidth === 'number') ? window.innerWidth : 0; }
function reachWinResizeDown(e) {
  if (!e || e.button !== 0 || !reachWinAvailable()) return;
  e.preventDefault();
  if (typeof e.stopPropagation === 'function') e.stopPropagation();
  reachNative('reachWindowDrag', reachWinTopEdge(e.clientX, reachWinWidth())).catch(function () {});
}
function reachWinResizeHover(e) {
  var band = document.getElementById('win-resize-top');
  if (band && e) band.style.cursor = reachWinEdgeCursor(reachWinTopEdge(e.clientX, reachWinWidth()));
}
// The slim title bar names the device on screen (focus mode, detached window).
function reachWinSetTitle(dev) {
  var name = dev ? String(dev.hostname || T('viewer.deviceN', { id: dev.id })) : 'Oblireach';
  var t = document.getElementById('win-title');
  if (t) { t.textContent = name; t.title = name; }
  var d = document.getElementById('win-title-dot');
  if (!d) return;
  // A tool tab on screen (terminal, explorer): its own state. Else the
  // device: ObliReach online, the Obliance agent answering (its colour), or
  // offline.
  var tt = (dev && typeof toolActiveTab === 'function') ? toolActiveTab() : null;
  var cls = !dev ? 'hide'
    : (tt && typeof toolTabDot === 'function') ? toolTabDot(tt)
    : (dev.oblireach && dev.oblireach.online) ? 'online'
    : (typeof devAgentOnline === 'function' && devAgentOnline(dev)) ? 'agent' : 'offline';
  d.className = 'dot ' + cls;
}
(function () {
  if (typeof document === 'undefined' || !document || typeof document.getElementById !== 'function') return;
  var acts = { 'wc-min': 'minimize', 'wc-max': 'maximize', 'wc-close': 'close' };
  Object.keys(acts).forEach(function (id) {
    var b = document.getElementById(id);
    if (!b) return;
    // The focus stays where it was (the session picture keeps the keyboard).
    b.addEventListener('mousedown', function (e) { if (e && typeof e.preventDefault === 'function') e.preventDefault(); });
    b.addEventListener('click', function () { reachWinControl(acts[id]); });
  });
  var band = document.getElementById('win-resize-top');
  if (band) {
    band.addEventListener('mousedown', reachWinResizeDown);
    band.addEventListener('mousemove', reachWinResizeHover);
  }
  if (typeof document.addEventListener === 'function') document.addEventListener('mousedown', reachWinMouseDown);
  if (!reachWinAvailable() || !document.body) return;
  document.body.classList.add('win-frameless');
  reachWinTitles();
  reachNative('reachWindowState').then(reachWinApply).catch(function () {});
})();

// ═════════════════════════════════════════════════════════════════════════
// Session windows with tabs (detached_windows.go). A detached window
// holds several sessions, one per tab in its title bar, with the session tab
// machinery of the main window (sessionTabs / activeSessionTabId, one tab
// per device; switching tabs switches the picture and the toolbar, every
// session keeps running).
//   - A tab: the device name (ellipsized, the full name in its tooltip), a
//     status dot, the view-only mark, and its × (ends that session; the
//     window closes with its last tab). A middle click closes it too.
//   - More tabs than fit: the strip scrolls with the mouse wheel, by dragging
//     it, with arrows shown only then; the tab on screen is always scrolled
//     into view. The grip after "+" is always left free to move the window.
//   - "+": the device list (devPicker*, ui.go). A device that has a tab
//     anywhere (this window, another one, the main window) is brought to the
//     front on it (reachOpenTab); otherwise it connects here in a new tab, on
//     the Windows session the operator picks when there are several
//     (reachPickSession).
//   - Dragging a tab along the strip scrolls it; out of the strip (above or
//     below it by REACH_TAB_TEAR_PX) or out of the window tears it off:
//     released there, its session goes to a new window at that point, or into
//     the session window whose title bar it was dropped on (reachTearOff).
//     The only tab of a window moves the window instead.
// Every window reports its session tabs to the app (reachReportTabs): a
// device has one tab, in one window.
// ═════════════════════════════════════════════════════════════════════════
var REACH_TAB_DRAG_PX = 5;      // a press that moves less is a click
var REACH_TAB_TEAR_PX = 28;     // this far above / below the strip, a dragged tab tears off
var REACH_TAB_ARROW_SHARE = 0.7; // an arrow scrolls this share of the strip
var REACH_TAB_MARGIN = 8;       // room kept around a tab scrolled into view

// ── Pure helpers (strip geometry, gestures, sessions) ──
function reachTabsOverflow(scrollW, clientW) { return (+scrollW || 0) > (+clientW || 0) + 1; }
function reachTabsMaxScroll(scrollW, clientW) { return Math.max(0, (+scrollW || 0) - (+clientW || 0)); }
function reachTabsClamp(left, scrollW, clientW) { return reachClamp(+left || 0, 0, reachTabsMaxScroll(scrollW, clientW)); }
// The arrows: shown only when the tabs overflow, each enabled while there is
// something to scroll to on its side.
function reachTabsArrows(scrollLeft, scrollW, clientW) {
  var ov = reachTabsOverflow(scrollW, clientW), max = reachTabsMaxScroll(scrollW, clientW);
  return { show: ov, left: ov && scrollLeft > 0.5, right: ov && scrollLeft < max - 0.5 };
}
// The scroll position that shows the tab at [tabLeft, tabLeft + tabW) of the
// strip's content, with margin around it, moving as little as possible.
function reachTabsScrollInto(tabLeft, tabW, scrollLeft, clientW, margin) {
  margin = margin > 0 ? margin : 0;
  if (tabW + 2 * margin >= clientW) return Math.max(0, tabLeft - margin);
  if (tabLeft - margin < scrollLeft) return Math.max(0, tabLeft - margin);
  if (tabLeft + tabW + margin > scrollLeft + clientW) return tabLeft + tabW + margin - clientW;
  return scrollLeft;
}
// Pixels a wheel event scrolls the strip: its larger axis (a vertical wheel
// scrolls it sideways), lines (deltaMode 1) and pages (2) converted.
function reachTabsWheel(dx, dy, mode, pageW) {
  dx = +dx || 0; dy = +dy || 0;
  var d = Math.abs(dx) > Math.abs(dy) ? dx : dy;
  if (!d) return 0;
  if (mode === 1) d *= 40;
  else if (mode === 2) d *= (pageW > 0 ? pageW : 400);
  return d;
}
// What a press on a tab has become. g: dx / dy since the press, x / y the
// pointer now (client coordinates), stripTop / stripBottom, winW / winH (the
// page's size), tabs (how many in this window), moved (it moved already).
// 'click': not moved (yet); 'window': the only tab of the window, dragged:
// it moves the window; 'tear': out of the strip vertically, or out of the
// window: released there, the tab tears off; 'scroll': along the strip.
function reachTabDragMode(g) {
  var moved = !!g.moved || Math.abs(+g.dx || 0) > REACH_TAB_DRAG_PX || Math.abs(+g.dy || 0) > REACH_TAB_DRAG_PX;
  if (!moved) return 'click';
  if (!(g.tabs > 1)) return 'window';
  if (g.x < 0 || g.y < 0 || (g.winW > 0 && g.x >= g.winW) || (g.winH > 0 && g.y >= g.winH)) return 'tear';
  if (g.y < g.stripTop - REACH_TAB_TEAR_PX || g.y > g.stripBottom + REACH_TAB_TEAR_PX) return 'tear';
  return 'scroll';
}
// The Windows sessions a device's agent reports (overview): the list only
// when it is one, each an object. The agent's own report, relayed as it came:
// anything else (an object with a "length"...) counts as none.
function reachDevSessions(dev) {
  var list = (dev && dev.oblireach && Array.isArray(dev.oblireach.sessions)) ? dev.oblireach.sessions : [];
  return list.filter(function (s) { return !!s && typeof s === 'object' && !Array.isArray(s); });
}
// The choices of the Windows session picker: "Automatic" first, then each
// session the agent reports (user, state, station; a disconnected one has no
// display until its user reconnects).
function reachSessionChoices(dev) {
  var list = reachDevSessions(dev);
  var out = [{ id: -1, auto: true, user: T('viewer.pick.auto'), meta: T('viewer.pick.autoMeta') }];
  list.forEach(function (s) {
    if (!s || typeof s !== 'object') return;
    var sid = Number(s.id);
    if (!(sid >= 0) || sid !== Math.floor(sid)) return;
    var off = s.state === 'Disconnected';
    var station = s.stationName ? String(s.stationName) : (s.isConsole ? T('viewer.pick.console') : '');
    var meta = [off ? T('viewer.pick.disconnected') : wtsStateText(s.state), station, T('viewer.pick.sessionN', { id: sid })].filter(Boolean).join(' \u00B7 ');
    out.push({ id: sid, auto: false, user: String(s.username || (s.isLoginPrompt ? T('session.signInScreen') : T('session.number', { id: sid }))), meta: meta, disconnected: off });
  });
  return out;
}
// A Windows session state as the agent reports it (Active, Disconnected...),
// in words; an unknown one as it came.
function wtsStateText(state) {
  var s = String(state || ''), k = 'session.state.' + s;
  var t = s ? T(k) : '';
  return (!s || t === k) ? s : t;
}
// Several Windows sessions: the operator chooses before a session starts.
function reachNeedsSessionPick(dev) { return reachSessionChoices(dev).length > 2; }

// ── Windows session picker (reusable: "+" lists, the main list) ──
// Resolves to the Windows session chosen (-1: automatic) or null (cancelled).
// A device with a single session, or none, resolves -1 at once (no prompt).
var reachPickerDone = null; // finishes the picker on screen (a new one cancels it)
function reachPickSession(dev) {
  if (!reachNeedsSessionPick(dev)) return Promise.resolve(-1);
  if (reachPickerDone) reachPickerDone(null);
  var choices = reachSessionChoices(dev);
  return new Promise(function (resolve) {
    var ov = document.createElement('div');
    ov.className = 'modal-overlay visible reach-sess-picker'; ov.id = 'reach-sess-picker';
    var box = document.createElement('div'); box.className = 'modal-box';
    box.setAttribute('role', 'dialog'); box.setAttribute('aria-label', T('viewer.pick.title'));
    var head = document.createElement('div'); head.className = 'modal-head';
    var ico = document.createElement('div'); ico.className = 'icon'; ico.innerHTML = REACH_ICON.monitor.replace(/width="12" height="12"/, 'width="18" height="18"');
    var h = document.createElement('h3'); h.textContent = T('viewer.pick.title');
    var x = document.createElement('button'); x.className = 'close'; x.id = 'rsp-close'; x.title = T('common.cancel');
    x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    head.appendChild(ico); head.appendChild(h); head.appendChild(x);
    var desc = document.createElement('p'); desc.className = 'desc'; desc.id = 'rsp-desc';
    desc.textContent = T('viewer.pick.desc', { device: String((dev && dev.hostname) || T('common.thisDeviceCap')) });
    var list = document.createElement('div'); list.className = 'rsp-list';
    var items = [];
    var finish = function (v) {
      if (reachPickerDone !== finish) return;
      reachPickerDone = null;
      ov.classList.remove('visible');
      if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
      resolve(v);
    };
    choices.forEach(function (c) {
      var b = document.createElement('button');
      b.className = 'rsp-item' + (c.auto ? ' auto' : '') + (c.disconnected ? ' off' : '');
      b.id = c.auto ? 'rsp-auto' : 'rsp-' + c.id;
      var dot = document.createElement('span'); dot.className = 'dot ' + (c.auto ? 'online' : (c.disconnected ? 'offline' : 'online'));
      var tx = document.createElement('span'); tx.className = 'rsp-text';
      var u = document.createElement('span'); u.className = 'rsp-user'; u.textContent = c.user;
      var m = document.createElement('span'); m.className = 'rsp-meta'; m.textContent = c.meta;
      tx.appendChild(u); tx.appendChild(m);
      b.appendChild(dot); b.appendChild(tx);
      b.title = c.user + ' \u2014 ' + c.meta;
      b.addEventListener('click', function () { finish(c.id); });
      items.push(b);
      list.appendChild(b);
    });
    var acts = document.createElement('div'); acts.className = 'modal-actions';
    var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'rsp-cancel'; cancel.textContent = T('common.cancel');
    acts.appendChild(cancel);
    box.appendChild(head); box.appendChild(desc); box.appendChild(list); box.appendChild(acts);
    ov.appendChild(box);
    x.addEventListener('click', function () { finish(null); });
    cancel.addEventListener('click', function () { finish(null); });
    ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) finish(null); }); // the backdrop
    ov.addEventListener('keydown', function (e) {
      if (!e) return;
      if (e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); finish(null); return; }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        var i = items.indexOf(document.activeElement);
        var n = e.key === 'ArrowDown' ? (i + 1) % items.length : (i <= 0 ? items.length - 1 : i - 1);
        if (e.preventDefault) e.preventDefault();
        try { items[n].focus(); } catch (x2) {}
      }
    });
    reachPickerDone = finish;
    document.body.appendChild(ov);
    try { items[0].focus(); } catch (e) {}
  });
}

// ── Tab strip (detached window) ──
var reachWinTabs = { active: null, count: 0, drag: null, els: {}, pending: false };
function reachTabStatus(tab) {
  if (tab.ws && tab.ws.readyState === 1 && tab.gotInit) return 'online';
  if (tab.ws || tab.reconnectTimer || tab.connectTimer || (tab.overlay && tab.overlay.kind === 'busy')) return 'warn'; // connecting
  return 'offline';
}
// k: a device key (devKey; a bare id: a device of the server on screen).
function reachTabOfDevice(k) {
  var key = keyNorm(k);
  if (typeof sessionTabs === 'undefined') return null;
  for (var i = 0; i < sessionTabs.length; i++) if (sessionTabs[i].device && devKey(sessionTabs[i].device) === key) return sessionTabs[i];
  return null;
}
// Tabs of more than one server here: each tab shows its server's colour.
function reachTabsManyServers() {
  var seen = {}, n = 0;
  var all = sessionTabs.concat(typeof toolTabs !== 'undefined' ? toolTabs : []); // (its terminals and explorers too)
  for (var i = 0; i < all.length; i++) { var p = all[i].device ? devPid(all[i].device) : ''; if (!seen[p]) { seen[p] = true; n++; } }
  return n > 1;
}
function reachStripEl() { return document.getElementById('wt-strip'); }
function reachRenderWinTabs() {
  var strip = reachStripEl();
  if (!strip) return;
  // Not while a tab is being dragged (its element holds the pointer): after.
  if (reachWinTabs.drag) { reachWinTabs.pending = true; return; }
  var keep = strip.scrollLeft || 0; // emptying the strip would scroll it back to its start
  strip.innerHTML = '';
  reachWinTabs.els = {};
  // Multi-server mode, or tabs of several servers: each tab shows its server.
  var srvDots = (typeof serverMode !== 'undefined' && serverMode === 'multi') || reachTabsManyServers();
  // In their order: its ObliReach tabs, then its terminals and explorers
  // (terminal_viewer.go); a mosaic in place of its members (tab_groups.go).
  tgStripItems().forEach(function (it) {
    if (it.group) { strip.appendChild(tgWinGroupEl(it.group)); return; }
    var tab = it.tab;
    if (typeof toolTabs !== 'undefined' && toolTabs.indexOf(tab) >= 0) { strip.appendChild(toolWinTabEl(tab, srvDots)); return; }
    var on = tab.id === activeSessionTabId;
    var name = String(tab.label || (tab.device && tab.device.hostname) || T('win.session'));
    var el = document.createElement('div');
    el.className = 'wt-tab' + (on ? ' active' : '');
    el.setAttribute('role', 'tab');
    el.setAttribute('aria-selected', on ? 'true' : 'false');
    el.title = tab.viewOnly ? T('viewer.tabViewOnly', { name: name }) : name;
    el.dataset.tabId = tab.id;
    if (srvDots && typeof srvName === 'function') {
      var pid = devPid(tab.device), sn = srvName(pid);
      var sd = document.createElement('span'); sd.className = 'wt-tab-srv'; sd.style.background = srvColor(pid);
      if (sn) { sd.title = sn; el.title = sn + ' / ' + el.title; }
      el.appendChild(sd);
    }
    var dot = document.createElement('span'); dot.className = 'dot ' + reachTabStatus(tab);
    var nm = document.createElement('span'); nm.className = 'wt-tab-name'; nm.textContent = name;
    el.appendChild(dot); el.appendChild(nm);
    if (tab.viewOnly) {
      var vo = document.createElement('span'); vo.className = 'wt-tab-vo'; vo.title = T('viewer.viewOnlyShort'); vo.innerHTML = REACH_ICON.viewOnly;
      el.appendChild(vo);
    }
    var x = document.createElement('button');
    x.className = 'wt-tab-x'; x.tabIndex = -1; x.title = T('viewer.tabDisconnect', { name: name });
    x.innerHTML = '<svg width="9" height="9" viewBox="0 0 10 10"><path d="M1 1l8 8M9 1l-8 8" fill="none" stroke="currentColor" stroke-width="1.6"/></svg>';
    x.addEventListener('pointerdown', function (e) { if (e && e.stopPropagation) e.stopPropagation(); }); // not a tab drag
    x.addEventListener('mousedown', function (e) { if (e && e.preventDefault) e.preventDefault(); });   // the focus stays on the picture
    x.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); closeSessionTab(tab.id); });
    el.appendChild(x);
    reachWireWinTab(el, tab);
    reachWinTabs.els[tab.id] = el;
    strip.appendChild(el);
  });
  tgRefreshSubTabs(); // (a mosaic on screen: its sub-tabs follow)
  strip.scrollLeft = keep;
  var a = reachActiveTab(), tt = (!a && typeof toolActiveTab === 'function') ? toolActiveTab() : null;
  document.title = T('win.docTitle', { name: String(tt ? toolTabTitle(tt) : ((a && a.label) || (reachDetachedDevice && reachDetachedDevice.hostname) || T('win.session'))) });
  // A tab switched to, added or closed: the one on screen scrolled into view
  // (not at every status change: the operator may have scrolled elsewhere).
  var into = (activeSessionTabId !== reachWinTabs.active || winTabCount() !== reachWinTabs.count) ? activeSessionTabId : null;
  reachWinTabs.active = activeSessionTabId; reachWinTabs.count = winTabCount();
  reachWinTabsLayout(into);
}
// Arrows shown / enabled as the strip is now.
function reachWinTabsArrows() {
  var strip = reachStripEl(), wrap = document.getElementById('wt-tabs');
  if (!strip) return;
  var a = reachTabsArrows(strip.scrollLeft || 0, strip.scrollWidth, strip.clientWidth);
  if (wrap) wrap.classList.toggle('overflow', a.show);
  var l = document.getElementById('wt-left'), r = document.getElementById('wt-right');
  if (l) l.disabled = !a.left;
  if (r) r.disabled = !a.right;
}
// After a change of the tabs or of the window's size: arrows, and the tab of
// tabId (the one on screen) scrolled into view.
function reachWinTabsLayout(tabId) {
  var strip = reachStripEl(), wrap = document.getElementById('wt-tabs');
  if (!strip) return;
  if (wrap) wrap.classList.toggle('overflow', reachTabsOverflow(strip.scrollWidth, strip.clientWidth)); // the arrows take room first
  var el = tabId ? reachWinTabs.els[tabId] : null;
  if (el) {
    var to = reachTabsScrollInto(el.offsetLeft || 0, el.offsetWidth || 0, strip.scrollLeft || 0, strip.clientWidth || 0, REACH_TAB_MARGIN);
    strip.scrollLeft = reachTabsClamp(to, strip.scrollWidth, strip.clientWidth);
  }
  reachWinTabsArrows();
}
function reachWinTabsScrollBy(dir) {
  var strip = reachStripEl();
  if (!strip) return;
  var step = Math.max(120, Math.round((strip.clientWidth || 0) * REACH_TAB_ARROW_SHARE));
  var to = reachTabsClamp((strip.scrollLeft || 0) + dir * step, strip.scrollWidth, strip.clientWidth);
  if (typeof strip.scrollTo === 'function') { try { strip.scrollTo({ left: to, behavior: 'smooth' }); } catch (e) { strip.scrollLeft = to; } }
  else strip.scrollLeft = to;
  reachWinTabsArrows();
}
// The mouse wheel over the tabs scrolls them (sideways), when they overflow.
function reachWinTabsOnWheel(e) {
  var strip = reachStripEl();
  if (!e || !strip || !reachTabsOverflow(strip.scrollWidth, strip.clientWidth)) return;
  var d = reachTabsWheel(e.deltaX, e.deltaY, e.deltaMode, strip.clientWidth);
  if (!d) return;
  if (e.preventDefault) e.preventDefault();
  strip.scrollLeft = reachTabsClamp((strip.scrollLeft || 0) + d, strip.scrollWidth, strip.clientWidth);
  reachWinTabsArrows();
}
// Presses on a tab: a click shows it, a drag scrolls the strip, tears the tab
// off, or moves the window (the only tab). Pointer capture: the drag goes on
// (and ends) outside the window.
function reachWireWinTab(el, tab) {
  el.addEventListener('mousedown', function (e) { if (e && e.button === 0 && e.preventDefault) e.preventDefault(); }); // no focus change, no selection
  el.addEventListener('pointerdown', function (e) { reachWinDragStart(e, el, tab); });
  el.addEventListener('pointermove', reachWinDragMove);
  el.addEventListener('pointerup', reachWinDragUp);
  el.addEventListener('pointercancel', function () { reachWinDragEnd(); });
  el.addEventListener('lostpointercapture', function () { reachWinDragEnd(); });
  el.addEventListener('auxclick', function (e) { if (e && e.button === 1) { if (e.preventDefault) e.preventDefault(); closeSessionTab(tab.id); } });
  // A right click: "Move to window" (tab_move.go; a terminal's tab has its
  // theme menu instead: term_themes.go).
  el.addEventListener('contextmenu', function (e) { if (tab && tabMoveOffered(tab)) tabMoveMenuOpen(tab, e); });
}
function reachWinDragStart(e, el, tab) {
  if (!e || e.button !== 0 || reachWinTabs.drag) return;
  var strip = reachStripEl();
  var r = (strip && typeof strip.getBoundingClientRect === 'function') ? strip.getBoundingClientRect() : { top: 0, height: 34 };
  var bottom = typeof r.bottom === 'number' ? r.bottom : (r.top || 0) + (r.height || 0);
  reachWinTabs.drag = { tab: tab, el: el, pointerId: e.pointerId, x0: e.clientX, y0: e.clientY, scroll0: strip ? (strip.scrollLeft || 0) : 0,
    stripTop: r.top || 0, stripBottom: bottom, mode: 'click', moved: false };
  try { if (el.setPointerCapture) el.setPointerCapture(e.pointerId); } catch (x) {}
}
function reachWinDragMove(e) {
  var g = reachWinTabs.drag;
  if (!g || !e) return;
  var dx = e.clientX - g.x0, dy = e.clientY - g.y0;
  var mode = reachTabDragMode({ dx: dx, dy: dy, x: e.clientX, y: e.clientY, stripTop: g.stripTop, stripBottom: g.stripBottom,
    winW: window.innerWidth, winH: window.innerHeight, tabs: g.tab ? tgTopCount() : 2, moved: g.moved }); // (its tool tabs counted; a mosaic: one)
  if (mode === 'click') return;
  g.moved = true;
  if (!g.tab) mode = 'scroll'; // the strip itself: it only scrolls
  if (mode === 'window') {
    // The only tab: the window follows the pointer (Windows moves it).
    // "tab": dropped onto another window's tab strip, the tab joins that
    // window and this one closes (detached_windows.go tabDragEnded ->
    // __reachMergeTab).
    reachWinDragEnd();
    if (reachWinAvailable()) reachNative('reachWindowDrag', 'tab').catch(function () {});
    return;
  }
  g.mode = mode;
  if (g.el && g.el !== reachStripEl()) g.el.classList.toggle('tearing', mode === 'tear');
  document.body.classList.toggle('wt-tearing', mode === 'tear');
  if (mode === 'scroll') {
    var strip = reachStripEl();
    if (strip && reachTabsOverflow(strip.scrollWidth, strip.clientWidth)) {
      strip.scrollLeft = reachTabsClamp(g.scroll0 - dx, strip.scrollWidth, strip.clientWidth);
      reachWinTabsArrows();
    }
  }
}
function reachWinDragUp(e) {
  var g = reachWinTabs.drag;
  if (!g) return;
  reachWinDragEnd();
  if (!g.tab) return;
  if (!g.moved) {
    if (e && (e.ctrlKey || e.metaKey)) tgCtrlClick(g.tab); // (mosaics: tab_groups.go)
    else reachWinShowTab(g.tab);
    return;
  }
  if (g.mode === 'tear') reachTearOffTab(g.tab);
}
function reachWinDragEnd() {
  var g = reachWinTabs.drag;
  if (!g) return;
  reachWinTabs.drag = null;
  if (g.el && g.el.classList) g.el.classList.remove('tearing');
  if (document.body) document.body.classList.remove('wt-tearing');
  try { if (g.el && g.el.releasePointerCapture) g.el.releasePointerCapture(g.pointerId); } catch (e) {}
  if (reachWinTabs.pending) { reachWinTabs.pending = false; reachRenderWinTabs(); }
}
// A press on the strip itself (between or after the tabs): drag to scroll.
function reachWinStripDown(e) {
  var strip = reachStripEl();
  if (!e || e.target !== strip || reachWinTabs.drag) return;
  reachWinDragStart(e, strip, null);
}
function reachFocusCanvas() {
  var c = document.getElementById('remote-canvas');
  if (c && typeof c.focus === 'function') { try { c.focus({ preventScroll: true }); } catch (e) {} }
}
// A tab clicked (or asked for by the app): its session on screen (the "+"
// list, if open, goes: the picture is what the operator asked for).
function reachWinShowTab(tab) {
  if (tab && tab.members) { devPickerClose(); tgShowGroup(tab); return; } // (a mosaic's tab: tab_groups.go)
  if (tab && typeof toolTabs !== 'undefined' && toolTabs.indexOf(tab) >= 0) { // a terminal or explorer tab (terminal_viewer.go)
    devPickerClose();
    if (tab.id !== activeSessionTabId || !toolShown(tab)) toolShowTab(tab); else toolFocus(tab);
    return;
  }
  if (!tab || sessionTabs.indexOf(tab) < 0) return;
  devPickerClose();
  if (tab.id !== activeSessionTabId || !reachUI(tab)) switchSessionTab(tab.id);
  else reachWinTabsLayout(tab.id);
  reachFocusCanvas();
}

// ── Where each device's session is (every window) ──
// reachWinClaims: devices (keys) this window holds without a tab (yet): its
// boot device until its tab exists, a "+" device while its session is
// chosen, a tab moving in. Reported with the tabs, so the app never frees them.
var reachWinClaims = new Set();
var reachReportedTabs = null; // the last report (JSON): only changes are sent
function reachTabsReport() {
  var ids = []; // device keys (devKey): the app knows the devices of every server apart
  if (typeof sessionTabs !== 'undefined') sessionTabs.forEach(function (t) {
    var id = t && t.device ? devKey(t.device) : '';
    if (id && ids.indexOf(id) < 0) ids.push(id);
  });
  // Its terminals and explorers: their own keys ("<device key>~<kind>").
  if (typeof toolTabs !== 'undefined') toolTabs.forEach(function (t) { if (t && t.key && ids.indexOf(t.key) < 0) ids.push(t.key); });
  // ... and the explorers of its sessions' side panels (files_dock.go): one explorer per device, wherever it is.
  if (typeof filesDocks !== 'undefined') filesDocks.forEach(function (t) { if (t && t.key && ids.indexOf(t.key) < 0) ids.push(t.key); });
  reachWinClaims.forEach(function (id) { if (ids.indexOf(id) < 0) ids.push(id); });
  var a = reachDetachedMode ? reachActiveTab() : null;
  var tt = (reachDetachedMode && !a && typeof toolActiveTab === 'function') ? toolActiveTab() : null;
  return { ids: ids, title: a ? String(a.label || '') : (tt ? toolTabTitle(tt) : '') };
}
function reachReportTabs(force) {
  if (typeof window.reachWindowTabs !== 'function' || !reachNativeToken()) return;
  var r = reachTabsReport(), j = JSON.stringify(r);
  if (!force && j === reachReportedTabs) return;
  reachReportedTabs = j;
  reachNative('reachWindowTabs', r).catch(function () {});
}
// A detached window: the chats of its devices only. pid (optional): the
// conversation's server (the same uuid on another server is another device).
function reachDetachedHasUuid(uuid, pid) {
  if (!uuid) return false;
  var same = function (d) { return !!d && d.uuid === uuid && (pid === undefined || devPid(d) === (pid || '')); };
  if (same(reachDetachedDevice)) return true;
  for (var i = 0; i < sessionTabs.length; i++) if (same(sessionTabs[i].device)) return true;
  return false;
}
// A device of the lists by its id; pid (optional): of that server only. In
// multi-server mode, that server's own list (not only what the tenant filter
// shows).
function reachFindDevice(id, pid) {
  id = String(id);
  // A local connection: the local base's list (ui.go localDevGroups).
  if (typeof LOCAL_PID !== 'undefined' && pid === LOCAL_PID) return typeof localDevFind === 'function' ? localDevFind(id) : null;
  var groups = (typeof overview !== 'undefined' && overview && Array.isArray(overview.groups)) ? overview.groups : [];
  if (pid !== undefined && typeof serverMode !== 'undefined' && serverMode === 'multi' && typeof srv !== 'undefined' && srv.get(pid || '')) groups = srv.get(pid || '').groups || [];
  for (var i = 0; i < groups.length; i++) {
    var ds = (groups[i] && Array.isArray(groups[i].devices)) ? groups[i].devices : [];
    for (var j = 0; j < ds.length; j++) if (ds[j] && String(ds[j].id) === id && (pid === undefined || devPid(ds[j]) === (pid || ''))) return ds[j];
  }
  return null;
}
// The key of the device this window was opened for (detached window).
function reachBootKey() {
  var p = reachDetachedParams;
  return p ? devKeyOf(p.profile || '', p.deviceId) + (p.kind ? '~' + p.kind : '') + (p.vm ? '~' + vmKindOf(p.vm.id) : '') : ''; // (a tool window: that tool's key; a VM console's)
}
// A new tab here for dev, connected on the Windows session given (-1: automatic).
function reachWinConnect(dev, session, viewOnly) {
  // The device this window was opened for, still without a tab (its agent
  // was offline at boot): not this window's any more once it has others.
  var boot = reachBootKey();
  if (boot && devKey(dev) !== boot && !reachTabOfDevice(boot)) reachWinClaims.delete(boot);
  openDeviceDetail(dev, 'remote');
  var tab = addSessionTab(dev);
  tab.reconnectAttempts = 0;
  if (viewOnly) tab.viewOnly = true;
  startRemote(typeof session === 'number' && session >= 0 ? session : undefined, tab);
  reachFocusCanvas();
  return tab;
}

// "+" picked a device (devPicker, ui.go). Returns where it went: 'self' (its
// tab here), 'focused' / 'main' / 'opening' (it is elsewhere: brought to the
// front), 'here' (connected in a new tab), 'cancelled', 'offline', 'busy'.
var reachWinAdding = {};
async function reachWinAddDevice(dev) {
  if (!dev) return 'none';
  var key = devKey(dev); // (the same id on two servers: two devices)
  var mine = reachTabOfDevice(key);
  if (mine) { devPickerClose(); reachWinShowTab(mine); return 'self'; }
  if (!dev.oblireach || !dev.oblireach.online) {
    devPickerNote(T(dev.oblireach && dev.oblireach.installed ? 'viewer.pickOffline' : 'viewer.pickNotInstalled', { device: reachDevName(dev) }));
    return 'offline';
  }
  if (reachWinAdding[key]) return 'busy';
  reachWinAdding[key] = true;
  try {
    var where = 'here';
    if (typeof window.reachOpenTab === 'function' && reachNativeToken()) {
      try { where = String(await reachNative('reachOpenTab', key)); }
      catch (e) { devPickerNote(T('viewer.pickOpenFailed', { error: reachErrText(e) })); return 'error'; }
    }
    if (where === 'self') { var t = reachTabOfDevice(key); devPickerClose(); if (t) reachWinShowTab(t); return where; }
    if (where !== 'here') { devPickerClose(); return where; } // brought to the front where it is
    reachWinClaims.add(key); // the app claimed it for this window: kept while the session is chosen
    var choice;
    try { choice = await reachPickSession(dev); } finally { reachWinClaims.delete(key); }
    if (choice === null) { reachReportTabs(true); return 'cancelled'; } // released
    devPickerClose();
    reachWinConnect(dev, choice, false);
    return 'here';
  } finally {
    delete reachWinAdding[key];
  }
}

// A tab dragged out and released: its session goes to another window (a new
// one at the drop point, or the one whose title bar it was dropped on), on
// the same Windows session and view-only state; it ends here.
async function reachTearOffTab(tab, how) {
  var merge = !!(how && how.merge); // (this window's only tab joining another window)
  // into: one of a mosaic moving whole, to the window its first tab went to
  // (that tab's key: tab_groups.go tgTearOff).
  var into = (how && typeof how.into === 'string') ? how.into : '';
  // A mosaic: all its tabs (tab_groups.go).
  if (tab && tab.members) return tgTearOff(tab, how);
  // A terminal or explorer tab: its own way (a terminal keeps its shell:
  // terminal_viewer.go toolTearOff, term_park.go).
  if (tab && typeof toolTabs !== 'undefined' && toolTabs.indexOf(tab) >= 0) return toolTearOff(tab, how);
  // A tab's "Move to window" (tab_move.go): { window } a session window by
  // its id, { main } the main window (either may take the window's last
  // tab), { newWindow } a new window at the cursor.
  var toWin = (how && how.window > 0) ? how.window : 0, toMain = !!(how && how.main), newWin = !!(how && how.newWindow);
  var dest = toWin ? { window: toWin } : (toMain ? { main: true } : (newWin ? { newWindow: true } : {}));
  if (!tab || sessionTabs.indexOf(tab) < 0 || (!merge && !into && !toWin && !toMain && winTabCount() < 2)) return false;
  var sid = reachReconnectSessionId(tab);
  // Its device's side panel goes along when this was its last session here
  // (files_dock.go): handed over first, taken back if the move fails.
  var dock = filesDockLeaving(tab);
  var r;
  try {
    r = await reachNative('reachTearOff', String(tab.device.id), String(tab.label || tab.device.hostname || ''),
      Object.assign({ session: (typeof sid === 'number' && sid >= 0) ? sid : -1, viewOnly: !!tab.viewOnly, profile: devPid(tab.device) }, vmWindowOpts(tab.device), merge ? { merge: true } : {}, into ? { into: into } : {}, dest));
  } catch (e) {
    filesDockStay(tab.device, dock);
    reachToast(T('viewer.tabMoveFailed', { error: reachErrText(e) }), 'warn');
    return false;
  }
  if (r !== 'opening' && r !== 'moved') { filesDockStay(tab.device, dock); return false; }
  if (sessionTabs.indexOf(tab) >= 0) closeSessionTab(tab.id);
  return r;
}

// The window was dragged by its only tab and dropped onto another window's
// tab strip (detached_windows.go tabDragEnded): that tab moves there, like a
// tab torn off a window with several (this window, empty, then closes).
// Its only tab may be a mosaic (tab_groups.go): all its tabs go there.
window.__reachMergeTab = function () {
  if (!reachDetachedMode) return;
  var items = tgStripItems();
  if (items.length !== 1) return;
  reachTearOffTab(items[0].group || items[0].tab, { merge: true });
};

// Detached window: a tab left without its session connects again, on the
// session it had, or (pick) on the one the operator chooses among the
// device's sessions as they are now.
async function reachDetachedReconnect(tab, pick) {
  if (!tab) {
    // (no session tab for the device on screen)
    if (!selectedDevice) return;
    var c0 = await reachPickSession(selectedDevice);
    if (c0 !== null && !reachTabOfDevice(devKey(selectedDevice))) reachWinConnect(selectedDevice, c0, false);
    return;
  }
  if (sessionTabs.indexOf(tab) < 0) return;
  var sid = reachReconnectSessionId(tab);
  if (pick && !devIsVm(tab.device)) { // (a VM console has no Windows session to pick)
    try {
      var pid = devPid(tab.device);
      await loadOverview(pid);
      var fresh = reachFindDevice(tab.device.id, pid);
      if (fresh) { var was = tab.device; tab.device = fresh; if (selectedDevice && devSame(selectedDevice, was)) selectedDevice = fresh; }
    } catch (e) {}
    var choice = await reachPickSession(tab.device);
    if (choice === null) return; // idle: its Connect button stays
    sid = choice >= 0 ? choice : undefined;
  }
  if (sessionTabs.indexOf(tab) < 0) return;
  tab.reconnectAttempts = 0;
  startRemote(sid, tab);
}

// Calls from the app (detached_windows.go).
// __reachShowTab: this window was brought forward for this device (every window).
window.__reachShowTab = function (id) {
  if (keyIsTool(id) && typeof toolTabByKey === 'function') { // a tool's key (terminal_viewer.go; a VM console's is a session tab's)
    var tt = toolTabByKey(keyNorm(id));
    if (!tt) return;
    if (reachDetachedMode) devPickerClose();
    toolShowTab(tt);
    return;
  }
  var tab = reachTabOfDevice(id);
  if (!tab) return;
  if (reachDetachedMode) { devPickerClose(); reachWinShowTab(tab); return; }
  switchSessionTab(tab.id); // the main window: the device page, on its session
};
// __reachCloseTab: the main window takes this device's session back (id: its key).
window.__reachCloseTab = function (id) {
  if (!reachDetachedMode) return;
  reachWinClaims.delete(keyNorm(id));
  var tt = (keyIsTool(id) && typeof toolTabByKey === 'function') ? toolTabByKey(keyNorm(id)) : null;
  if (tt) { if (tt.kind !== 'files') filesDockLeaving(tt); toolCloseTab(tt); return; } // (a terminal or explorer tab: terminal_viewer.go)
  var tab = reachTabOfDevice(id);
  // ("Bring it back here": the device's side panel follows its session to
  // the main window, files_dock.go)
  if (tab) { filesDockLeaving(tab); closeSessionTab(tab.id); return; }
  if (!winTabCount()) reachCloseOwnWindow();
  else reachReportTabs(true);
};
// __reachAdoptTab: a tab of another window was dropped on this one (a session
// window, or the main window: detached_windows.go dropTarget treats both as
// valid drop targets), or comes back here: its new window could not open.
// {deviceId, title, session, viewOnly, profile}: connected in a new tab.
window.__reachAdoptTab = function (info) {
  if (!info || typeof info !== 'object') return;
  var id = String(info.deviceId || '');
  if (!/^[A-Za-z0-9_-]{1,64}$/.test(id)) return;
  var profile = typeof info.profile === 'string' ? info.profile : '';
  // A Hyper-V VM console (vm_console.go): its session tab here, connected again.
  if (typeof info.kind === 'string' && VM_KIND_RE.test(info.kind)) {
    return vmWinAdopt(id, info.kind.slice(3), vmCleanName(info.vmName) || info.kind.slice(3), info.viewOnly === true, profile);
  }
  // A terminal or explorer tab (toolTearOff): its tool tab here, taking a
  // terminal's parked tunnel over, or the explorer on its folder.
  if (typeof info.kind === 'string' && info.kind) {
    if (!/^(cmd|powershell|ssh|files|rdp)$/.test(info.kind)) return; // (rdp: a local RDP session, parked: rdp_view.go)
    var o = {};
    if (typeof info.park === 'string' && /^[0-9a-f]{64}$/.test(info.park)) o.park = info.park;
    if (typeof info.path === 'string' && info.path && info.path.length <= 4096) o.path = info.path;
    return reachWinAdoptTool(id, info.kind, profile, o);
  }
  var s = info.session;
  var session = (typeof s === 'number' && s >= 0 && s === Math.floor(s) && s <= 1048576) ? s : -1;
  return reachWinAdopt(id, session, info.viewOnly === true, profile);
};
async function reachWinAdopt(id, session, viewOnly, profile) {
  var key = devKeyOf(profile, id);
  var had = reachTabOfDevice(key);
  if (had) { reachWinShowTab(had); return had; }
  reachWinClaims.add(key); // the app moved it here: ours until its tab exists
  try {
    var dev = await reachAdoptFind(id, profile);
    return reachWinConnect(dev, session, viewOnly);
  } catch (e) {
    reachToast(T('viewer.adoptFailed', { error: reachErrText(e) }), 'warn');
    return null;
  } finally {
    reachWinClaims.delete(key);
    reachReportTabs();
  }
}
// A tool tab moved here (toolTearOff): created here with what it brought.
async function reachWinAdoptTool(id, kind, profile, o) {
  var key = devKeyOf(profile, id) + '~' + kind;
  var had = typeof toolTabByKey === 'function' ? toolTabByKey(key) : null;
  if (had) { toolShowTab(had); return had; }
  reachWinClaims.add(key); // the app moved it here: ours until its tab exists
  try {
    var dev = await reachAdoptFind(id, profile);
    if (!dev || !TOOL_IMPL[kind]) throw new Error(T('viewer.adopt.notFound'));
    if (reachDetachedMode) devPickerClose();
    var r = toolCreateTab(dev, kind, toolKeyOf(dev, kind), o);
    return r === 'here' ? toolTabByKey(toolKeyOf(dev, kind)) : null;
  } catch (e) {
    reachToast(T('viewer.adoptFailed', { error: reachErrText(e) }), 'warn');
    return null;
  } finally {
    reachWinClaims.delete(key);
    reachReportTabs();
  }
}
// The device of a tab moved here, as this window knows it (throws why not).
async function reachAdoptFind(id, profile) {
  // A local connection (localconn_tools.go): the vault's, whatever server.
  if (typeof LOCAL_PID !== 'undefined' && profile === LOCAL_PID) {
    var ld = await cxnFindDevice(id);
    if (!ld) throw new Error(T('viewer.adopt.notFound'));
    return ld;
  }
  var multi = typeof serverMode !== 'undefined' && serverMode === 'multi';
  var dev = null;
  if (multi) {
    // Every server here: that one must be signed in (its list read if needed).
    var known = typeof serverProfiles !== 'undefined' && serverProfiles.some(function (p) { return p.id === profile; });
    var st = known ? srvState(profile) : null;
    if (st && st.signedIn !== true) await srvLoad(profile);
    if (!st || st.signedIn !== true) throw new Error(T('viewer.adopt.serverSignedOut'));
    dev = reachFindDevice(id, profile);
    if (!dev) { await srvLoad(profile); dev = reachFindDevice(id, profile); }
  } else {
    // Device ids of two servers overlap: never on another server (and
    // looked up as that server's: this window may have listed others).
    if (reachServerId() !== profile) throw new Error(T('viewer.adopt.otherServer'));
    dev = reachFindDevice(id, profile);
    if (!dev) { await loadOverview(profile); dev = reachFindDevice(id, profile); }
  }
  if (!dev) throw new Error(T('viewer.adopt.notFound'));
  return dev;
}

// Title bar wiring (a detached window).
(function () {
  if (!reachDetachedMode || typeof document === 'undefined' || !document || typeof document.getElementById !== 'function') return;
  var keep = function (e) { if (e && typeof e.preventDefault === 'function') e.preventDefault(); }; // the focus stays on the picture
  var wrap = document.getElementById('wt-tabs'), strip = document.getElementById('wt-strip');
  var l = document.getElementById('wt-left'), r = document.getElementById('wt-right'), add = document.getElementById('wt-add');
  if (l) { l.addEventListener('mousedown', keep); l.addEventListener('click', function () { reachWinTabsScrollBy(-1); }); }
  if (r) { r.addEventListener('mousedown', keep); r.addEventListener('click', function () { reachWinTabsScrollBy(1); }); }
  if (add) {
    add.addEventListener('mousedown', keep);
    add.addEventListener('click', function () { if (devPickerIsOpen()) devPickerClose(); else devPickerOpen(); });
  }
  if (wrap) wrap.addEventListener('wheel', reachWinTabsOnWheel, { passive: false });
  if (strip) {
    strip.addEventListener('scroll', reachWinTabsArrows);
    strip.addEventListener('pointerdown', reachWinStripDown);
    strip.addEventListener('pointermove', reachWinDragMove);
    strip.addEventListener('pointerup', reachWinDragUp);
    strip.addEventListener('pointercancel', function () { reachWinDragEnd(); });
  }
  if (typeof window.addEventListener === 'function') window.addEventListener('resize', function () { reachWinTabsLayout(activeSessionTabId); });
})();
`
