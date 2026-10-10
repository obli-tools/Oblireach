package main

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed logo-login.svg
var logoLoginSVG string

//go:embed logo-icon.svg
var logoIconSVG string

//go:embed socket.io.min.js
var socketIoClientJS []byte

// buildUI returns the full single-page HTML application embedded in the desktop client.
func buildUI(cfg *Config) string {
	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>Oblireach</title>
<!--THEME_CSS-->
<style>
*{box-sizing:border-box;margin:0;padding:0}
/* The colours are the theme's (theme.go themeCSS, the <style> above): its
   variables only, no literal colour but on the remote picture. */
:root{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif}
body{background:var(--bg);color:var(--text);height:100vh;overflow:hidden;display:flex;flex-direction:column}
::-webkit-scrollbar{width:6px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--border);border-radius:3px}
::-webkit-scrollbar-thumb:hover{background:var(--border2)}

/* ── Top bar ── */
.topbar{height:44px;background:var(--chrome);display:flex;align-items:center;padding:0 16px;gap:10px;flex-shrink:0}
.topbar .logo{margin-right:auto;display:flex;align-items:center}
.topbar .logo svg{height:24px;width:auto}
.topbar .server-info{font-size:11px;color:var(--muted2);max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.btn-sm{padding:6px 13px;border-radius:10px;border:none;background:rgb(var(--ink) / .06);color:var(--muted);font-size:11px;cursor:pointer;transition:all .15s}
.btn-sm:hover{background:rgb(var(--ink) / .11);color:var(--text)}
.main{display:flex;flex:1;overflow:hidden}

/* ── Sidebar ── */
.sidebar{width:260px;flex-shrink:0;display:flex;flex-direction:column;background:var(--bg2)}
.sidebar-head{padding:10px 12px;display:flex;align-items:center;gap:6px}
.sidebar-head input{flex:1;background:var(--input-bg);border:none;border-radius:10px;padding:7px 11px;font-size:12px;color:var(--text);outline:none;transition:box-shadow .15s}
.sidebar-head input:focus{box-shadow:0 0 0 2px var(--accent)}
.sidebar-body{flex:1;overflow-y:auto;padding:4px 0}
.group-label{padding:8px 14px 4px;font-size:10px;font-weight:600;color:var(--muted2);text-transform:uppercase;letter-spacing:.8px}
.device-row{padding:7px 14px 7px 22px;cursor:pointer;display:flex;align-items:center;gap:8px;font-size:12px;border-radius:0;transition:all .1s;margin:1px 6px;border-radius:8px}
.device-row:hover{background:rgb(var(--ink) / .04)}
.device-row.active{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.dot{width:7px;height:7px;border-radius:50%%;flex-shrink:0}
.dot.online{background:var(--success)}
.dot.offline{background:var(--offline)}
.dot.warn{background:var(--warn)}

/* ── Content ── */
.content{flex:1;display:flex;flex-direction:column;overflow:hidden}

/* ── Login ── */
#login-overlay{position:fixed;inset:0;background:linear-gradient(180deg,var(--bg) 0%%,var(--bg2) 100%%);display:flex;align-items:center;justify-content:center;z-index:100}
.login-box{background:var(--bg2);border-radius:20px;padding:36px;width:400px;display:flex;flex-direction:column;gap:18px;box-shadow:0 20px 60px var(--shadow)}
.login-box h2{font-size:22px;font-weight:700;text-align:center;color:var(--accent-l);display:flex;align-items:center;justify-content:center;gap:8px}
.login-box h2 svg{width:24px;height:24px}
.login-box p{font-size:12px;color:var(--muted);text-align:center;margin-top:-8px}
.form-group{display:flex;flex-direction:column;gap:5px}
.form-group label{font-size:11px;color:var(--muted);font-weight:500;padding-left:2px}
.form-group input{background:var(--input-bg);border:none;border-radius:12px;padding:11px 15px;font-size:13px;color:var(--text);outline:none;transition:box-shadow .15s}
.form-group input:focus{box-shadow:0 0 0 2px var(--accent)}
.btn-primary{background:var(--accent-fill);border:none;color:var(--on-accent);border-radius:12px;padding:11px;font-size:13px;font-weight:600;cursor:pointer;transition:all .15s}
.btn-primary:hover{background:var(--accent-h);transform:translateY(-1px);box-shadow:0 4px 12px rgb(var(--accent-rgb) / .3)}
.btn-primary:disabled{opacity:.5;cursor:default;transform:none;box-shadow:none}
.err-msg{font-size:11px;color:var(--danger);text-align:center;min-height:16px}

/* ── Device detail ── */
.device-header{padding:12px 16px;display:flex;align-items:center;gap:10px;flex-shrink:0;background:var(--bg2)}
.device-header h2{font-size:15px;font-weight:600;flex:1}
.tabs{display:flex;gap:0;flex-shrink:0;background:var(--bg2)}
.tab-btn{padding:9px 18px;font-size:12px;font-weight:500;color:var(--muted);border:none;background:none;cursor:pointer;box-shadow:inset 0 -2px 0 transparent;transition:all .15s}
.tab-btn.active{color:var(--accent-l);box-shadow:inset 0 -2px 0 var(--accent)}
.tab-btn:hover:not(.active){color:var(--text)}
.device-header.merged{padding:0 16px;min-height:46px;align-items:stretch}
.device-header.merged>*{align-self:center}
.device-header.merged h2{flex:none}
.device-header.merged .tabs{align-self:stretch;background:none;margin-left:14px}
.device-header.merged .tab-btn{padding:0 14px}
.device-header.merged .dev-meta{margin-left:auto}
.device-header .tab-btn.side-tab.unread::after{content:'';display:inline-block;width:6px;height:6px;border-radius:3px;background:var(--accent);margin-left:6px;vertical-align:middle}
.tab-content{flex:1;overflow:hidden;display:flex;flex-direction:column}

/* ── Remote ── */
#remote-pane{flex:1;display:flex;flex-direction:column;overflow:hidden}
.remote-toolbar{padding:8px 12px;display:flex;align-items:center;gap:8px;flex-shrink:0;background:var(--bg2)}
.session-select{background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:11px;color:var(--text);outline:none;cursor:pointer}
.session-select:focus-visible{box-shadow:0 0 0 2px var(--accent)}
.remote-viewport{flex:1;background:var(--viewport-bg);position:relative;overflow:hidden}
#remote-canvas{width:100%%;height:100%%;object-fit:contain;display:block}
.remote-placeholder{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:10px;color:var(--muted2);font-size:13px}

/* ── Scripts ── */
#scripts-pane{flex:1;display:flex;overflow:hidden}
.scripts-list{width:220px;overflow-y:auto;flex-shrink:0;background:var(--bg2)}
.script-item{padding:8px 12px;cursor:pointer;font-size:12px;transition:all .1s}
.script-item:hover{background:rgb(var(--ink) / .04)}
.script-item.active{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.script-item .sname{font-weight:500}
.script-item .sdesc{font-size:10px;color:var(--muted2);margin-top:2px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.script-detail{flex:1;padding:16px;overflow-y:auto;display:flex;flex-direction:column;gap:12px}
.script-detail h3{font-size:13px;font-weight:600}
.script-code{background:var(--bg4);border-radius:10px;padding:12px;font-family:monospace;font-size:11px;white-space:pre-wrap;max-height:200px;overflow-y:auto;color:var(--text)}
.exec-output{background:var(--term-bg);border-radius:10px;padding:12px;font-family:monospace;font-size:11px;white-space:pre-wrap;max-height:200px;overflow-y:auto;color:var(--term-fg);min-height:60px}
.badge{display:inline-block;padding:2px 8px;border-radius:6px;font-size:10px;font-weight:500}
.badge.windows{background:rgb(var(--os-win-rgb) / .12);color:var(--os-win)}
.badge.linux{background:rgb(var(--os-linux-rgb) / .12);color:var(--os-linux)}
.badge.macos{background:rgb(var(--os-mac-rgb) / .12);color:var(--os-mac)}
.badge.all{background:rgb(var(--ink) / .05);color:var(--muted)}

/* ── Empty / status ── */
.empty{display:flex;flex-direction:column;align-items:center;justify-content:center;flex:1;gap:10px;color:var(--muted2);font-size:13px}
.empty-icon{font-size:40px;opacity:.25}
.status-bar{padding:4px 14px;font-size:10px;color:var(--muted2);flex-shrink:0;background:var(--chrome)}

/* ── Chat panel ── */
.chat-panel{width:0;overflow:hidden;transition:width .25s ease;display:flex;flex-direction:column;background:linear-gradient(180deg,var(--bg) 0%%,var(--bg2) 100%%);flex-shrink:0}
.chat-panel.open{width:360px;box-shadow:-8px 0 20px -12px var(--shadow)}
.chat-header{display:flex;align-items:center;gap:10px;padding:12px 14px;flex-shrink:0}
.chat-header .avatar{width:36px;height:36px;border-radius:50%%;background:var(--accent-fill);display:flex;align-items:center;justify-content:center;flex-shrink:0}
.chat-header .avatar svg{width:20px;height:20px;color:var(--on-accent)}
.chat-header .info{flex:1;min-width:0}
.chat-header .info .name{font-size:13px;font-weight:600;color:var(--text)}
.chat-header .info .status{font-size:11px;color:var(--muted);display:flex;align-items:center;gap:5px}
.chat-messages{flex:1;overflow-y:auto;padding:12px 14px;display:flex;flex-direction:column;gap:10px}
.chat-msg{display:flex;gap:8px;max-width:90%%}
.chat-msg.op{align-self:flex-start}
.chat-msg.user{align-self:flex-end;flex-direction:row-reverse}
.chat-msg .bubble{padding:8px 14px;border-radius:16px;font-size:13px;line-height:1.5;word-break:break-word}
.chat-msg.op .bubble{background:var(--accent-fill);color:var(--on-accent);border-bottom-left-radius:4px}
.chat-msg.user .bubble{background:var(--bg3);color:var(--text);border-bottom-right-radius:4px}
.chat-msg.sys{align-self:center;max-width:100%%}
.chat-msg.sys .bubble{background:rgb(var(--warn-rgb) / .1);color:var(--warn);font-size:11px;padding:4px 12px;border-radius:20px}
.chat-typing{display:none;align-self:flex-end;gap:8px;align-items:flex-end;flex-direction:row-reverse;padding:0 2px}
.chat-typing.visible{display:flex}
.chat-typing-dots{display:flex;gap:3px;padding:8px 14px;background:var(--bg3);border-radius:16px 16px 4px 16px}
.chat-typing-dots span{width:5px;height:5px;border-radius:50%%;background:rgb(var(--ink) / .5);animation:chatTypeBounce 1.2s infinite}
.chat-typing-dots span:nth-child(2){animation-delay:.2s}
.chat-typing-dots span:nth-child(3){animation-delay:.4s}
@keyframes chatTypeBounce{0%%,60%%,100%%{transform:translateY(0);opacity:.4}30%%{transform:translateY(-3px);opacity:1}}
.chat-time{text-align:center;padding:4px 0}
.chat-time span{font-size:10px;color:var(--muted);background:rgb(var(--ink) / .04);padding:2px 10px;border-radius:20px}
.chat-input-area{padding:10px 14px;flex-shrink:0;display:flex;flex-direction:column;gap:8px}
.chat-input-row{display:flex;align-items:center;gap:8px;background:var(--input-bg);border-radius:16px;padding:5px 7px 5px 15px;transition:box-shadow .15s}
.chat-input-row:focus-within{box-shadow:0 0 0 2px rgb(var(--accent-rgb) / .55)}
.chat-input-row input{flex:1;background:transparent;border:none;outline:none;font-size:13px;color:var(--text)}
.chat-input-row input::placeholder{color:var(--muted2)}
.chat-send-btn{width:32px;height:32px;border-radius:10px;background:var(--accent-fill);border:none;cursor:pointer;display:flex;align-items:center;justify-content:center;transition:background .15s;flex-shrink:0}
.chat-send-btn:hover{background:var(--accent-h)}
.chat-send-btn:disabled{opacity:.3;cursor:default}
.chat-send-btn svg{width:14px;height:14px;color:var(--on-accent)}
.chat-toggle{position:relative}
.chat-tabs{display:flex;gap:4px;padding:6px 10px;overflow-x:auto;flex-shrink:0}
.chat-tabs:empty{display:none}
.chat-tab{display:flex;align-items:center;gap:6px;padding:5px 7px 5px 9px;border-radius:8px;font-size:11px;color:var(--muted);background:rgb(var(--ink) / .05);cursor:pointer;white-space:nowrap}
.chat-tab:hover{background:rgb(var(--ink) / .09);color:var(--text)}
.chat-tab.active{color:var(--text);background:rgb(var(--accent-rgb) / .16)}
.chat-tab:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.chat-tab .ct-dot{width:6px;height:6px;border-radius:3px;background:var(--muted2);flex-shrink:0}
.chat-tab .ct-srv{width:8px;height:8px;border-radius:2px;flex-shrink:0;background:var(--muted2)}
.chat-tab .ct-dot.on{background:var(--success)}
.chat-tab .ct-dot.warn{background:var(--warn)}
.chat-tab .ct-dot.bad{background:var(--danger)}
.chat-tab .ct-unread{background:var(--accent-fill);color:var(--on-accent);border-radius:8px;padding:0 5px;font-size:9px;font-weight:700}
.chat-tab .ct-x{border:none;background:none;color:var(--muted2);cursor:pointer;padding:0 2px;font-size:13px;line-height:1}
.chat-tab .ct-x:hover{color:var(--text)}
.chat-tab.ended{opacity:.6}
.chat-toggle .badge-dot{position:absolute;top:-2px;right:-2px;width:8px;height:8px;border-radius:50%%;background:var(--accent);display:none}
.chat-req-btn{width:100%%;background:rgb(var(--accent-rgb) / .1);color:var(--accent-l);font-size:11px;padding:7px;border-radius:10px}
.chat-req-btn svg{vertical-align:-2px;margin-right:4px}
.chat-req-wait{font-size:11px;text-align:center;color:var(--warn);padding:4px}
/* Scripts, Info and Chat share one slot on the right (rightSlotTake): one
   replaces the other at once, without both widths animating under the picture. */
body.rp-swap .side-panel,body.rp-swap .chat-panel{transition:none}

/* ── Chat page (main window, nav rail "Chat"): the conversations in large ── */
.chat-page{flex:1;display:flex;flex-direction:column;min-height:0;min-width:0;overflow:hidden}
.chat-page .page-header .breadcrumb:empty{display:none}
/* Its tab bar, under the title: a tab per conversation, then "+" (the device
   picker again). */
.chat-page-tabbar{display:flex;align-items:flex-start;gap:6px;padding:0 24px 10px;flex-shrink:0}
.chat-page .chat-tabs{flex:0 1 auto;min-width:0;flex-wrap:wrap;gap:6px;padding:0;overflow:visible}
.chat-page .chat-tab{font-size:12px;padding:7px 9px 7px 11px}
.chat-page .chat-tab .ct-unread{font-size:10px}
.chat-tab-add{flex-shrink:0;display:inline-flex;align-items:center;justify-content:center;width:32px;height:30px;padding:0;border:none;border-radius:8px;background:rgb(var(--ink) / .05);color:var(--muted);cursor:pointer;transition:background .1s,color .1s}
.chat-tab-add:hover{background:rgb(var(--ink) / .1);color:var(--text)}
.chat-tab-add.on{background:rgb(var(--accent-rgb) / .16);color:var(--accent-l)}
/* The device picker (no conversation in front, or "+"): the Devices list's
   rows and filters; a click opens the chat with that device. */
.chat-picker-note{margin:0 0 14px;padding:8px 12px;border-radius:10px;background:rgb(var(--warn-rgb) / .12);color:var(--warn);font-size:12px}
.chat-page-conv{flex:1;display:flex;flex-direction:column;min-height:0;background:linear-gradient(180deg,var(--bg) 0%%,var(--bg2) 100%%)}
.chat-page .chat-header{padding:12px 24px}
.chat-page .chat-messages{padding:18px max(24px,calc((100%% - 880px) / 2))}
.chat-page .chat-msg{max-width:72%%}
.chat-page .chat-input-area{padding:12px max(24px,calc((100%% - 880px) / 2)) 18px}
.chat-page .chat-req-btn{width:auto;padding:7px 14px}

/* ── Toolbar extras ── */
.toolbar-sep{width:4px;height:20px;flex-shrink:0}
.toolbar-dropdown{position:relative;display:inline-block}
.toolbar-dropdown-menu{display:none;position:absolute;top:100%%;left:0;margin-top:4px;background:var(--bg2);border-radius:10px;padding:4px;min-width:160px;z-index:50;box-shadow:0 8px 24px var(--shadow)}
.toolbar-dropdown-menu.open{display:block}
.toolbar-dropdown-menu button{display:block;width:100%%;text-align:left;padding:6px 10px;font-size:11px;color:var(--text);background:none;border:none;border-radius:6px;cursor:pointer;white-space:nowrap}
.toolbar-dropdown-menu button:hover{background:rgb(var(--ink) / .06)}

/* ── Performance HUD ── */
.perf-hud{position:absolute;top:8px;left:8px;background:rgba(0,0,0,.7);backdrop-filter:blur(4px);border-radius:8px;padding:6px 10px;font-family:monospace;font-size:11px;color:#4ade80;z-index:20;pointer-events:none;display:none;line-height:1.6}
.perf-hud.visible{display:block}
.perf-hud .hud-row{display:flex;gap:8px}
.perf-hud .hud-label{color:var(--muted);min-width:48px}
.perf-hud .hud-val{font-weight:600}
.perf-hud .hud-warn{color:#f59e0b}
.perf-hud .hud-bad{color:#ef4444}

/* ── Recording indicator ── */
.rec-indicator{position:absolute;top:8px;right:8px;background:rgba(0,0,0,.7);border-radius:8px;padding:5px 10px;font-size:11px;color:#ef4444;z-index:20;display:none;align-items:center;gap:6px;font-weight:600}
.rec-indicator.active{display:flex}
.rec-dot{width:8px;height:8px;border-radius:50%%;background:#ef4444;animation:rec-pulse 1s ease-in-out infinite}
@keyframes rec-pulse{0%%,100%%{opacity:1}50%%{opacity:.3}}

/* ── Annotation overlay ── */
.annotation-overlay{position:absolute;inset:0;z-index:15;cursor:crosshair;display:none}
.annotation-overlay.active{display:block}
.annotation-toolbar{position:absolute;bottom:12px;left:50%%;transform:translateX(-50%%);background:var(--bg2);border-radius:12px;padding:4px 6px;display:flex;gap:2px;z-index:16;box-shadow:0 8px 24px var(--shadow)}
.annotation-toolbar button{width:32px;height:32px;border:none;border-radius:8px;background:transparent;color:var(--muted);cursor:pointer;display:flex;align-items:center;justify-content:center;font-size:14px;transition:all .1s}
.annotation-toolbar button:hover{background:rgb(var(--ink) / .06);color:var(--text)}
.annotation-toolbar button.active{background:rgb(var(--accent-rgb) / .15);color:var(--accent-l)}

/* ── Quick Actions drawer ── */
.quick-actions{position:absolute;top:0;right:0;bottom:0;width:0;overflow:hidden;transition:width .2s ease;background:var(--bg2);z-index:25;display:flex;flex-direction:column}
.quick-actions.open{width:220px}
.quick-actions-header{padding:10px 14px;display:flex;align-items:center;justify-content:space-between;flex-shrink:0}
.quick-actions-header span{font-size:12px;font-weight:600;color:var(--text)}
.quick-actions-body{flex:1;overflow-y:auto;padding:6px}
.qa-btn{display:flex;align-items:center;gap:8px;width:100%%;padding:8px 10px;border:none;background:none;color:var(--text);font-size:12px;cursor:pointer;border-radius:8px;transition:background .1s;text-align:left}
.qa-btn:hover{background:rgb(var(--ink) / .06)}
.qa-btn svg{flex-shrink:0;color:var(--muted)}

/* ── Favorites star ── */
.fav-star{opacity:0;cursor:pointer;color:var(--muted2);transition:all .15s;margin-left:auto;flex-shrink:0;font-size:13px}
.device-row:hover .fav-star{opacity:1}
.fav-star.active{opacity:1;color:var(--warn)}

/* ── Multi-tab bar ── */
.session-tabs{display:flex;align-items:center;background:var(--bg2);flex-shrink:0;overflow-x:auto;min-height:0}
.session-tabs:empty{display:none}
.session-tab{display:flex;align-items:center;gap:6px;padding:6px 12px;font-size:11px;color:var(--muted);cursor:pointer;box-shadow:inset 0 -2px 0 transparent;white-space:nowrap;transition:all .1s;flex-shrink:0}
.session-tab:hover{color:var(--text);background:rgb(var(--ink) / .03)}
.session-tab.active{color:var(--accent-l);box-shadow:inset 0 -2px 0 var(--accent)}
.session-tab .tab-close{width:14px;height:14px;border-radius:4px;display:flex;align-items:center;justify-content:center;opacity:0;transition:all .1s}
.session-tab:hover .tab-close{opacity:.6}
.session-tab .tab-close:hover{opacity:1;background:rgb(var(--ink) / .1)}

/* ── Zoom viewport ── */
.remote-viewport.zoom-scroll{overflow:auto}
.remote-viewport.zoom-scroll #remote-canvas{object-fit:unset}

/* ── Generic modal (2FA prompt, confirmation dialogs, etc.) ── */
.modal-overlay{position:fixed;inset:0;background:var(--scrim);backdrop-filter:blur(6px);z-index:300;display:none;align-items:center;justify-content:center;padding:20px}
.modal-overlay.visible{display:flex}
.modal-box{background:var(--bg2);border-radius:18px;padding:22px 24px;width:420px;max-width:100%%;display:flex;flex-direction:column;gap:14px;box-shadow:0 20px 60px var(--shadow)}
.modal-head{display:flex;align-items:center;gap:10px}
.modal-head .icon{width:36px;height:36px;border-radius:10px;background:rgb(var(--accent-rgb) / .15);color:var(--accent-l);display:flex;align-items:center;justify-content:center;flex-shrink:0}
.modal-head h3{font-size:15px;font-weight:600;color:var(--text)}
.modal-head .close{margin-left:auto;width:28px;height:28px;border-radius:8px;background:transparent;border:none;color:var(--muted);cursor:pointer;display:flex;align-items:center;justify-content:center}
.modal-head .close:hover{background:rgb(var(--ink) / .06);color:var(--text)}
.modal-box p.desc{font-size:12px;color:var(--muted);line-height:1.5}
.otp-input{background:var(--input-bg);border:none;border-radius:12px;padding:15px 17px;font-size:22px;font-family:monospace;color:var(--text);letter-spacing:10px;text-align:center;outline:none;transition:box-shadow .15s;width:100%%}
.otp-input:focus{box-shadow:0 0 0 2px var(--accent)}
.trust-row{display:flex;align-items:flex-start;gap:10px;background:rgb(var(--ink) / .05);border-radius:12px;padding:11px 13px;cursor:pointer}
.trust-row input{accent-color:var(--accent);margin-top:2px;flex-shrink:0;width:14px;height:14px;cursor:pointer}
.trust-row .labels{display:flex;flex-direction:column;gap:3px;min-width:0}
.trust-row .labels .title{font-size:12px;color:var(--text);font-weight:500}
.trust-row .labels .sub{font-size:11px;color:var(--muted2);line-height:1.4}
.trust-row .labels .ip{font-family:monospace;color:var(--muted);font-size:11px}
.modal-err{font-size:11px;color:var(--danger);min-height:14px}
.modal-actions{display:flex;gap:8px;justify-content:flex-end}
.btn-ghost{background:rgb(var(--ink) / .06);border:none;color:var(--muted);border-radius:10px;padding:9px 15px;font-size:12px;cursor:pointer;transition:all .15s}
.btn-ghost:hover{color:var(--text);background:rgb(var(--ink) / .11)}

/* ── Nav rail (left vertical navigation, Rustdesk-console style) ── */
.nav-rail{width:220px;flex-shrink:0;display:flex;flex-direction:column;background:var(--chrome);padding:8px 0;overflow-y:auto}
.nav-item{display:flex;align-items:center;gap:10px;padding:9px 14px;margin:1px 8px;border-radius:8px;cursor:pointer;color:var(--muted);font-size:12px;transition:all .1s;user-select:none}
.nav-item:hover{background:rgb(var(--ink) / .04);color:var(--text)}
.nav-item.active{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.nav-item svg{flex-shrink:0;opacity:.75}
.nav-item.active svg{opacity:1}
.nav-item .chev{margin-left:auto;opacity:.5;transition:transform .15s}
.nav-item.expanded .chev{transform:rotate(90deg)}
.nav-sub{padding:1px 0 3px 0;display:none}
.nav-sub.open{display:block}
.nav-sub .nav-item{padding-left:40px;font-size:12px;color:var(--muted2)}
.nav-sub .nav-item.active{color:var(--accent-l);background:rgb(var(--accent-rgb) / .08)}
.nav-rail{transition:width .15s}
.nav-rail.compact{width:56px}
.nav-rail.compact .nav-item{justify-content:center;padding:9px 0}
.nav-rail.compact .nav-item span,.nav-rail.compact .nav-item .chev,.nav-rail.compact .nav-item .count-pill,.nav-rail.compact .nav-sub,.nav-rail.compact .nav-group-label{display:none}
.nav-pin{margin:auto 8px 0;padding:7px 0;border:none;background:none;color:var(--muted2);cursor:pointer;border-radius:8px;font-size:13px}
.nav-pin:hover{background:rgb(var(--ink) / .04);color:var(--text)}
.nav-group-label{padding:12px 18px 4px;font-size:9.5px;font-weight:700;color:var(--muted2);text-transform:uppercase;letter-spacing:1px}
.nav-item .count-pill{margin-left:auto;font-size:10px;font-weight:600;background:rgb(var(--ink) / .06);color:var(--muted);padding:1px 6px;border-radius:8px}
.nav-item.active .count-pill{background:rgb(var(--accent-rgb) / .18);color:var(--accent-l)}
.nav-item .nav-badge{margin-left:auto;min-width:18px;height:16px;padding:0 5px;border-radius:8px;background:var(--accent-fill);color:var(--on-accent);font-size:10px;font-weight:700;line-height:16px;text-align:center}
.nav-rail.compact .nav-item{position:relative}
.nav-rail.compact .nav-item .nav-badge{display:block;position:absolute;top:3px;right:8px;min-width:14px;height:14px;padding:0 3px;font-size:9px;line-height:14px}

/* ── Page frame ── */
.page-frame{flex:1;display:flex;flex-direction:column;overflow:hidden;background:var(--bg);min-width:0}
.page{flex:1;display:none;flex-direction:column;overflow:hidden;min-width:0}
.page.active{display:flex}
.page-header{min-height:56px;padding:8px 24px;display:flex;align-items:center;gap:12px;flex-shrink:0}
.page-header h1{font-size:17px;font-weight:600;color:var(--text)}
.page-header .breadcrumb{font-size:12px;color:var(--muted)}
.page-header .breadcrumb span.sep{margin:0 6px;color:var(--muted2)}
.page-header .page-actions{margin-left:auto;display:flex;gap:8px;align-items:center}
.page-body{flex:1;overflow:auto;padding:8px 24px 20px}
.page-body.flush{padding:0}

/* ── Top bar widgets: tenant switcher + user menu ── */
.topbar-right{margin-left:auto;display:flex;align-items:center;gap:8px}
.tenant-switcher{display:flex;align-items:center;gap:6px;padding:6px 11px;border-radius:10px;background:rgb(var(--ink) / .06);color:var(--text);font-size:12px;cursor:pointer;position:relative;max-width:240px}
.tenant-switcher:hover{background:rgb(var(--ink) / .1)}
.tenant-switcher .label{max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.tenant-switcher .chev{opacity:.6}
.tenant-menu{position:absolute;top:calc(100%% + 4px);left:0;background:var(--bg2);border-radius:12px;padding:4px;min-width:240px;z-index:60;box-shadow:0 12px 30px var(--shadow);display:none;max-height:360px;overflow-y:auto}
.tenant-menu.open{display:block}
.srv-dot{width:9px;height:9px;border-radius:5px;flex-shrink:0;background:var(--muted2)}
.server-switcher{margin-right:6px}
.srv-x{margin-left:auto;border:none;background:none;color:var(--muted2);cursor:pointer;font-size:13px;padding:0 2px}
.srv-x:hover{color:var(--danger)}
/* The server menu: each server's unread chat messages (and every server's on "All servers"). */
.srv-unread{margin-inline-start:auto;min-width:16px;height:15px;padding:0 5px;border-radius:8px;background:var(--accent-fill);color:var(--on-accent);font-size:9.5px;font-weight:700;line-height:15px;text-align:center}
.srv-unread:not(.hide)+.srv-x{margin-left:4px}
html[dir=rtl] .srv-unread:not(.hide)+.srv-x{margin-left:0;margin-right:4px}
.login-servers{display:flex;flex-wrap:wrap;gap:6px;align-items:center;margin-top:12px;font-size:11px;color:var(--muted)}
.login-servers:empty{display:none}
.login-srv{display:flex;align-items:center;gap:6px;font-size:11px;padding:4px 8px}
.srv-add-form{display:flex;gap:6px;padding:6px}
.srv-add-form input{flex:1;min-width:0;background:var(--input-bg);border:none;border-radius:8px;color:var(--text);font-size:12px;padding:7px 9px;outline:none}
.srv-add-form input:focus{box-shadow:0 0 0 2px var(--accent)}
.tenant-menu-item{padding:8px 12px;border-radius:8px;font-size:12px;color:var(--text);cursor:pointer;display:flex;align-items:center;gap:8px;white-space:nowrap}
.tenant-menu-item:hover{background:rgb(var(--ink) / .06)}
.tenant-menu-item.active{color:var(--accent-l);background:rgb(var(--accent-rgb) / .1)}
/* Multi-server mode: one section per server in the tenant menu, "All servers"
   in the server menu, the server's colour next to its devices (dots and
   borders only: the colours are pastel, never a text colour). */
.tenant-menu-section{display:flex;align-items:center;gap:8px;padding:8px 12px 5px;margin-top:6px;border-radius:8px;font-size:10.5px;font-weight:700;letter-spacing:.6px;text-transform:uppercase;color:var(--text2);cursor:pointer;white-space:nowrap}
.tenant-menu-section:first-child{margin-top:0}
.tenant-menu-section:hover{background:rgb(var(--ink) / .05)}
.tenant-menu-section.active{color:var(--accent-l)}
.tenant-menu-section.off{cursor:default;color:var(--muted)}
.tenant-menu-section.off:hover{background:none}
.tenant-menu-section .tms-note{margin-inline-start:auto;font-weight:400;letter-spacing:0;text-transform:none;color:var(--muted)}
.tenant-menu-section .tms-note.err{color:var(--danger);max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.tenant-menu-item.sub{padding-inline-start:28px}
.srv-dot.all{background:var(--muted2)}
.dev-srv{display:inline-block;width:7px;height:7px;border-radius:4px;flex-shrink:0;background:var(--muted2)}
.info-srv,.dev-head-srv{display:inline-flex;align-items:center;gap:6px}
.dev-head-srv{font-size:11px;color:var(--muted);flex-shrink:0}
.st-srv{width:6px;height:6px;border-radius:3px;flex-shrink:0}
.login-to{font-size:12px;font-weight:600;color:var(--text2);text-align:center;margin:-2px 0 0}
.tfa-server{font-weight:600;color:var(--text2)}
.user-menu-trigger{display:flex;align-items:center;gap:8px;padding:4px 11px 4px 4px;border-radius:20px;background:rgb(var(--ink) / .06);color:var(--text);font-size:12px;cursor:pointer;position:relative}
.user-menu-trigger:hover{background:rgb(var(--ink) / .1)}
.user-menu-trigger .avatar-sm{width:26px;height:26px;border-radius:50%%;background:var(--accent-fill);display:flex;align-items:center;justify-content:center;flex-shrink:0;font-weight:700;font-size:10px;color:var(--on-accent);overflow:hidden}
.user-menu-trigger .avatar-sm img{width:100%%;height:100%%;object-fit:cover}
.user-menu-trigger .uname{max-width:120px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.user-menu{position:absolute;top:calc(100%% + 4px);right:0;background:var(--bg2);border-radius:12px;padding:6px;min-width:220px;z-index:60;box-shadow:0 12px 30px var(--shadow);display:none}
.user-menu.open{display:block}
.user-menu-head{padding:10px 12px;margin-bottom:4px;border-radius:8px;background:rgb(var(--ink) / .04)}
.user-menu-head .name{font-size:13px;color:var(--text);font-weight:600}
.user-menu-head .sub{font-size:11px;color:var(--muted);margin-top:2px;word-break:break-all}
.user-menu-head .um-srv{display:flex;align-items:center;gap:6px;margin-top:4px}
.user-menu-item{padding:8px 12px;border-radius:7px;font-size:12px;color:var(--text);cursor:pointer;display:flex;align-items:center;gap:8px}
.user-menu-item:hover{background:rgb(var(--ink) / .06)}
.user-menu-item.danger{color:var(--danger)}
.user-menu-item.danger:hover{background:rgb(var(--danger-rgb) / .08)}
.user-menu-item svg{opacity:.7;flex-shrink:0}

/* ── KPI cards ── */
.kpi-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:14px;margin-bottom:24px}
.kpi-card{background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow);padding:16px 18px;display:flex;flex-direction:column;gap:4px;position:relative;overflow:hidden}
.kpi-card .label{font-size:10.5px;color:var(--muted);text-transform:uppercase;letter-spacing:.8px;font-weight:700}
.kpi-card .value{font-size:28px;font-weight:700;color:var(--text);font-variant-numeric:tabular-nums;line-height:1.1}
.kpi-card.online .value{color:var(--success)}
.kpi-card.offline .value{color:var(--muted)}
.kpi-card.warn .value{color:var(--warn)}
.kpi-card.danger .value{color:var(--danger)}
.kpi-card .sub{font-size:11px;color:var(--muted2);margin-top:2px}
.kpi-card .ico{position:absolute;top:14px;right:14px;opacity:.15;color:var(--text)}

/* ── Generic panel ── */
.panel{background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow);overflow:hidden;margin-bottom:16px}
.panel-header{padding:12px 18px;display:flex;align-items:center;gap:10px;font-size:13px;font-weight:600;color:var(--text)}
.panel-header .panel-actions{margin-left:auto;display:flex;gap:6px}
.panel-body{padding:14px 18px}
.panel-body.flush{padding:0}

/* ── Data table ── */
.dt{width:100%%;border-collapse:collapse;font-size:12px}
.dt th{text-align:left;padding:10px 14px;font-size:10.5px;font-weight:700;color:var(--muted);background:var(--bg3);text-transform:uppercase;letter-spacing:.6px;white-space:nowrap}
.dt td{padding:10px 14px;color:var(--text);vertical-align:middle}
.dt tbody tr{transition:background .1s;cursor:pointer}
.dt tbody tr:hover td{background:rgb(var(--ink) / .02)}
.dt tbody tr.selected td{background:rgb(var(--accent-rgb) / .06)}
.dt .actions{display:flex;gap:6px;justify-content:flex-end}
.dt .cell-hostname{display:flex;align-items:center;gap:8px;font-weight:500}
.dt .icon-btn,.recent-acts .icon-btn{width:26px;height:26px;border-radius:7px;background:transparent;border:none;color:var(--muted);cursor:pointer;display:inline-flex;align-items:center;justify-content:center;transition:all .1s}
.dt .icon-btn:hover,.recent-acts .icon-btn:hover{background:rgb(var(--ink) / .06);color:var(--text)}
.dt .icon-btn.primary:hover,.recent-acts .icon-btn.primary:hover{color:var(--accent-l)}
.dt .icon-btn:disabled,.recent-acts .icon-btn:disabled{opacity:.25;cursor:default}
.dt .icon-btn:disabled:hover,.recent-acts .icon-btn:disabled:hover{background:transparent;color:var(--muted)}

/* ── Device rows (Devices, "+" list, Dashboard): the last user, offline greyed ── */
/* The machine's last user next to its name (devLastUserHtml): secondary, cut
   with an ellipsis (the whole value in its tooltip). */
.dev-user{flex:0 1 auto;min-width:0;max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11px;font-weight:400;color:var(--muted)}
/* An offline machine (devIsOffline): the whole row lightly greyed (star, name,
   last user, OS, agent, status, sessions), its (i) as bright and clickable as
   ever; the buttons that need the agent keep their disabled look. Hover and
   selection backgrounds of the greyed cells are raised so that, through the
   opacity, they look the same as in the (i) cell. */
.dt tbody tr.dev-offline td:not(.actions){opacity:.55}
.dt tbody tr.dev-offline:hover td:not(.actions){background:rgb(var(--ink) / .036)}
.dt tbody tr.dev-offline.selected td:not(.actions){background:rgb(var(--accent-rgb) / .11)}
.recent-item.dev-offline>:not(.recent-acts){opacity:.55}

/* ── Devices tree: tenants (master) and Obliance's device groups ── */
.dt tbody tr.tree-row td{padding-top:7px;padding-bottom:7px;background:rgb(var(--ink) / .025)}
.dt tbody tr.tree-row:hover td{background:rgb(var(--ink) / .05)}
.dt tbody tr.tree-tenant td{background:rgb(var(--accent-rgb) / .08)}
.dt tbody tr.tree-tenant:hover td{background:rgb(var(--accent-rgb) / .11)}
.dt tbody tr.tree-server td{background:var(--bg3);padding-top:10px;padding-bottom:10px}
.dt tbody tr.tree-server:hover td{background:var(--bg4)}
.tree-server .tree-head .tree-name{font-size:13px;font-weight:700;color:var(--text)}
.tree-head .spacer{flex:1}
.tree-server .srv-state{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11px;color:var(--muted)}
.tree-server .srv-state.err{color:var(--danger)}
.tree-server .srv-act{flex-shrink:0;padding:3px 10px}
.dt tbody tr.tree-row:focus-visible{outline:1px solid var(--accent);outline-offset:-1px}
.tree-head{display:flex;align-items:center;gap:7px;min-width:0}
.tree-head .tree-chev{flex-shrink:0;color:var(--muted);transition:transform .12s}
.tree-row.folded .tree-chev{transform:rotate(-90deg)}
.tree-head .tree-ico{flex-shrink:0;color:var(--accent-l)}
.tree-head .tree-ico.off{color:var(--muted)}
.tree-head .tree-name{font-size:12px;font-weight:600;color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.tree-tenant .tree-head .tree-name{font-size:10.5px;font-weight:700;color:var(--accent-l);text-transform:uppercase;letter-spacing:.9px}
.tree-head .tree-count{font-size:10.5px;color:var(--muted);flex-shrink:0;white-space:nowrap}

/* ── Tag chips ── */
.tag-chip{display:inline-flex;align-items:center;gap:4px;padding:3px 9px;border-radius:12px;font-size:10px;font-weight:600;white-space:nowrap}
.tag-chip.win{background:rgb(var(--os-win-rgb) / .12);color:var(--os-win)}
.tag-chip.linux{background:rgb(var(--os-linux-rgb) / .12);color:var(--os-linux)}
.tag-chip.mac{background:rgb(var(--os-mac-rgb) / .12);color:var(--os-mac)}
.tag-chip.on{background:rgb(var(--success-rgb) / .12);color:var(--success)}
.tag-chip.off{background:rgb(var(--ink) / .05);color:var(--muted2)}
.tag-chip.warn{background:rgb(var(--warn-rgb) / .12);color:var(--warn)}

/* ── Filter bar ── */
.filter-bar{display:flex;gap:10px;align-items:center;flex-wrap:wrap;padding:12px 16px;background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow);margin-bottom:14px}
.filter-bar input, .filter-bar select{background:var(--input-bg);border:none;border-radius:10px;padding:7px 11px;font-size:12px;color:var(--text);outline:none;min-width:140px}
.filter-bar input:focus, .filter-bar select:focus{box-shadow:0 0 0 2px var(--accent)}
.filter-bar label{font-size:11px;color:var(--muted);margin-right:2px;white-space:nowrap}
.filter-bar .spacer{flex:1}

/* ── Recent list ── */
.recent-list{display:flex;flex-direction:column}
.recent-item{padding:10px 16px;display:flex;align-items:center;gap:10px;font-size:12px;cursor:pointer;transition:background .1s}
.recent-item:hover{background:rgb(var(--ink) / .02)}
.recent-item .rname{font-weight:500;color:var(--text)}
.recent-item .rwhen{margin-left:auto;font-size:11px;color:var(--muted2)}
.recent-acts{display:inline-flex;gap:2px;margin:-4px -6px -4px 0}
.recent-empty{padding:20px;text-align:center;color:var(--muted2);font-size:12px}

/* ── Settings sub-nav ── */
.settings-layout{display:flex;gap:18px;height:100%%}
.settings-nav{width:200px;flex-shrink:0;display:flex;flex-direction:column;gap:2px}
.settings-nav-item{padding:8px 12px;border-radius:8px;font-size:12px;color:var(--muted);cursor:pointer;transition:all .1s}
.settings-nav-item:hover{background:rgb(var(--ink) / .04);color:var(--text)}
.settings-nav-item.active{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l);font-weight:500}
.settings-body{flex:1;min-width:0}

/* ── Settings form rows ── */
.setting-row{display:flex;align-items:center;gap:16px;padding:14px 0}
.setting-row .setting-label{flex:1;min-width:0}
.setting-row .setting-label .title{font-size:13px;font-weight:500;color:var(--text)}
.setting-row .setting-label .sub{font-size:11px;color:var(--muted);margin-top:2px;line-height:1.5}
.setting-row .setting-control{flex-shrink:0}

/* ── Trusted IP row ── */
.tip-row{display:flex;align-items:center;gap:12px;padding:10px 14px;border-radius:10px;margin-bottom:6px;background:var(--bg3)}
.tip-row .ipv{font-family:monospace;font-size:12px;color:var(--text);font-weight:500}
.tip-row .exp{font-size:11px;color:var(--muted)}
.tip-row .spacer{flex:1}
.tip-row button{font-size:11px}

/* ── Shared bits ── */
.hide{display:none !important}
.section-title{font-size:14px;font-weight:600;color:var(--text);margin-bottom:10px}
.muted-hint{font-size:11px;color:var(--muted)}

/* ── Update banner ── */
.update-banner{display:none;align-items:center;gap:12px;padding:8px 18px;background:linear-gradient(90deg,rgb(var(--accent-rgb) / .15),rgb(var(--accent-rgb) / .06));font-size:12px;color:var(--text);flex-shrink:0}
.update-banner.visible{display:flex}
.update-banner .ub-icon{width:24px;height:24px;border-radius:6px;background:rgb(var(--accent-rgb) / .18);color:var(--accent-l);display:flex;align-items:center;justify-content:center;flex-shrink:0}
.update-banner .ub-text{flex:1;line-height:1.4}
.update-banner .ub-text .ub-title{font-weight:600}
.update-banner .ub-text .ub-sub{color:var(--muted);font-size:11px}
.update-banner .btn-primary{padding:6px 14px;font-size:12px;border-radius:10px}
.update-banner .btn-ghost{padding:6px 12px;font-size:12px}
.update-banner .ub-progress{display:none;height:4px;margin-top:6px;max-width:420px;border-radius:2px;background:rgb(var(--ink) / .08);overflow:hidden}
.update-banner.busy .ub-progress{display:block}
.update-banner .ub-bar{height:100%%;width:0;background:var(--accent);border-radius:2px;transition:width .15s}
.update-banner .ub-bar.indeterminate{width:30%%;animation:ub-slide 1.2s linear infinite}
@keyframes ub-slide{from{margin-left:-30%%}to{margin-left:100%%}}
.update-banner.error{background:linear-gradient(90deg,rgb(var(--danger-rgb) / .14),rgb(var(--danger-rgb) / .05))}
.update-banner.error .ub-icon{background:rgb(var(--danger-rgb) / .18);color:var(--danger)}
.update-banner.done{background:linear-gradient(90deg,rgb(var(--success-rgb) / .12),rgb(var(--success-rgb) / .04))}
.update-banner.done .ub-icon{background:rgb(var(--success-rgb) / .16);color:var(--success)}
.update-banner .ub-sub{word-break:break-word}
.update-banner button:disabled{opacity:.6;cursor:default}

/* ── Right-to-left languages (Arabic; html dir, i18n.go): the text runs from
   the right; code, addresses and the remote picture stay left-to-right, and
   the few rules that pin something to the left or right follow. ── */
html[dir=rtl] .script-code,html[dir=rtl] .exec-output,html[dir=rtl] .otp-input,html[dir=rtl] #inp-server,html[dir=rtl] #inp-user,html[dir=rtl] .tip-row .ipv,html[dir=rtl] .remote-viewport,html[dir=rtl] .srv-add-form input{direction:ltr}
html[dir=rtl] .dt th,html[dir=rtl] .toolbar-dropdown-menu button,html[dir=rtl] .qa-btn,html[dir=rtl] .rsp-item{text-align:right}
html[dir=rtl] .topbar .logo{margin-right:0;margin-left:auto}
html[dir=rtl] .topbar-right{margin-left:0}
html[dir=rtl] .tenant-menu,html[dir=rtl] .toolbar-dropdown-menu{left:auto;right:0}
html[dir=rtl] .user-menu{right:auto;left:0}
html[dir=rtl] .page-header .page-actions,html[dir=rtl] .device-header.merged .dev-meta,html[dir=rtl] .recent-item .rwhen,html[dir=rtl] .fav-star,html[dir=rtl] .srv-x,html[dir=rtl] .nav-item .count-pill,html[dir=rtl] .nav-item .nav-badge,html[dir=rtl] .panel-header .panel-actions{margin-left:0;margin-right:auto}
html[dir=rtl] #remote-status{margin-left:0!important;margin-right:auto}
html[dir=rtl] .nav-item .chev{margin-left:0;margin-right:auto;transform:scaleX(-1)}
html[dir=rtl] .nav-item.expanded .chev{transform:rotate(90deg)}
html[dir=rtl] .tree-row.folded .tree-chev{transform:rotate(90deg)}
html[dir=rtl] .wt-tabs{direction:ltr}
html[dir=rtl] .nav-sub .nav-item{padding-left:14px;padding-right:40px}
html[dir=rtl] .device-header.merged .tabs{margin-left:0;margin-right:14px}
html[dir=rtl] .chat-panel.open,html[dir=rtl] .side-panel.open{box-shadow:8px 0 20px -12px var(--shadow)}
</style>
<!--REACH_CSS-->
</head>
<body>

<!-- Window frame (frame_windows.go): the window has no Windows title bar. The
     top bar is the title bar (the slim one in focus mode and in a detached
     window; the page background on the sign-in page): its empty parts move
     the window. These buttons and the band along the top edge serve them all
     (reach_viewer.go reachWin*). -->
<div class="win-resize-top" id="win-resize-top"></div>
<div class="win-ctl" id="win-ctl" data-no-drag>
  <button class="wc-btn" id="wc-min" title="{{t:win.minimize}}" tabindex="-1"><svg width="10" height="10" viewBox="0 0 10 10"><path d="M0 5.5h10" fill="none" stroke="currentColor"/></svg></button>
  <button class="wc-btn" id="wc-max" title="{{t:win.maximize}}" tabindex="-1"><svg class="wc-maximize" width="10" height="10" viewBox="0 0 10 10"><rect x=".5" y=".5" width="9" height="9" rx="1" fill="none" stroke="currentColor"/></svg><svg class="wc-restore" width="10" height="10" viewBox="0 0 10 10"><rect x=".5" y="2.5" width="7" height="7" rx="1" fill="none" stroke="currentColor"/><path d="M2.5 2.5V1.5a1 1 0 011-1h5a1 1 0 011 1v5a1 1 0 01-1 1h-1" fill="none" stroke="currentColor"/></svg></button>
  <button class="wc-btn wc-close" id="wc-close" title="{{t:win.close}}" tabindex="-1"><svg width="10" height="10" viewBox="0 0 10 10"><path d="M.5.5l9 9M9.5.5l-9 9" fill="none" stroke="currentColor"/></svg></button>
</div>

<!-- Login overlay -->
<div id="login-overlay" data-win-drag>
  <div class="login-box" data-no-drag>
    <div style="text-align:center;padding:4px 0 0" id="login-logo"></div>
    <p style="font-size:12px;color:var(--muted);text-align:center;margin-top:-4px" data-i18n="login.subtitle">{{t:login.subtitle}}</p>
    <p class="login-to" id="login-to" style="display:none"></p>
    <div class="form-group">
      <label data-i18n="login.serverUrl">{{t:login.serverUrl}}</label>
      <input id="inp-server" type="url" placeholder="https://obliance.example.com" value="%s"/>
    </div>
    <div id="sso-login-fields">
      <div class="err-msg" id="sso-err"></div>
      <button class="btn-primary" id="btn-sso" style="background:#534AB7;width:100%%">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="vertical-align:-2px;margin-right:6px"><path d="M15 3h4a2 2 0 012 2v14a2 2 0 01-2 2h-4"/><polyline points="10 17 15 12 10 7"/><line x1="15" y1="12" x2="3" y2="12"/></svg>
        <span id="btn-sso-label">{{t:login.ssoButton}}</span>
      </button>
    </div>
    <div id="or-sep" style="display:flex;align-items:center;justify-content:center;color:var(--muted2);font-size:10px;text-transform:uppercase;letter-spacing:2px;font-weight:600;margin:2px 0">
      <span data-i18n="login.orLocal">{{t:login.orLocal}}</span>
    </div>
    <div id="local-login-fields">
      <div class="form-group">
        <label data-i18n="login.username">{{t:login.username}}</label>
        <input id="inp-user" type="text" placeholder="admin" value="%s"/>
      </div>
      <div class="form-group">
        <label data-i18n="login.password">{{t:login.password}}</label>
        <input id="inp-pass" type="password" placeholder=""/>
      </div>
      <label style="display:flex;align-items:center;gap:6px;font-size:11px;color:var(--muted);cursor:pointer;padding:2px 0">
        <input type="checkbox" id="inp-remember" checked style="accent-color:var(--accent);width:14px;height:14px;cursor:pointer" /> <span data-i18n="login.remember">{{t:login.remember}}</span>
      </label>
      <div class="err-msg" id="login-err"></div>
      <button class="btn-primary" id="btn-login">{{t:login.connect}}</button>
      <div class="login-servers" id="login-servers"></div>
    </div>
    <!-- btn-local-fallback kept as hidden stub for backwards compat with removed listener -->
    <span id="btn-local-fallback" style="display:none"></span>
  </div>
</div>

<!-- Main app -->
<div id="app" style="display:none;flex-direction:column;height:100%%">
  <!-- Slim title bar: focus mode and detached windows (the top bar is hidden there).
       A detached window shows its session tabs here (reach_viewer.go reachRenderWinTabs):
       the strip scrolls (wheel, drag, arrows), "+" connects another device in this
       window, and the grip after it always leaves room to move the window. -->
  <div class="win-titlebar" id="win-titlebar" data-win-drag>
    <span class="wt-logo" id="wt-logo"></span>
    <span class="dot hide" id="win-title-dot"></span>
    <span class="wt-title" id="win-title">Oblireach</span>
    <div class="wt-tabs" id="wt-tabs" data-no-drag>
      <button class="wt-arrow" id="wt-left" title="{{t:win.tabsLeft}}" data-i18n-title="win.tabsLeft" tabindex="-1"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><polyline points="15 18 9 12 15 6"/></svg></button>
      <div class="wt-strip" id="wt-strip" role="tablist"></div>
      <button class="wt-arrow" id="wt-right" title="{{t:win.tabsRight}}" data-i18n-title="win.tabsRight" tabindex="-1"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><polyline points="9 18 15 12 9 6"/></svg></button>
    </div>
    <button class="wt-add" id="wt-add" title="{{t:win.addDevice}}" data-i18n-title="win.addDevice" tabindex="-1"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg></button>
    <span class="wt-grip" id="wt-grip"></span>
  </div>
  <div class="topbar" data-win-drag>
    <span class="logo" id="topbar-logo"></span>
    <div class="tenant-switcher server-switcher" id="server-switcher" style="display:none" title="{{t:topbar.switchServer}}" data-i18n-title="topbar.switchServer" data-no-drag>
      <span class="srv-dot" id="server-dot"></span>
      <span class="label" id="server-label">—</span>
      <svg class="chev" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><polyline points="6 9 12 15 18 9"/></svg>
      <div class="tenant-menu" id="server-menu"></div>
    </div>
    <div class="tenant-switcher" id="tenant-switcher" style="display:none" title="{{t:topbar.switchTenant}}" data-i18n-title="topbar.switchTenant" data-no-drag>
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 21h18M5 21V7l8-4v18M19 21V11l-6-4"/></svg>
      <span class="label" id="tenant-label">—</span>
      <svg class="chev" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><polyline points="6 9 12 15 18 9"/></svg>
      <div class="tenant-menu" id="tenant-menu"></div>
    </div>
    <span class="topbar-right">
      <button class="btn-sm chat-toggle" id="btn-chat" style="display:none" title="{{t:common.chat}}" data-i18n-title="common.chat">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z"/></svg>
        <span data-i18n="common.chat">{{t:common.chat}}</span>
        <span class="badge-dot" id="chat-badge"></span>
      </button>
      <button class="btn-sm" id="btn-refresh" title="{{t:common.refresh}}" data-i18n-title="common.refresh">
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M23 4v6h-6"/><path d="M1 20v-6h6"/><path d="M3.51 9a9 9 0 0114.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0020.49 15"/></svg>
      </button>
      <div class="user-menu-trigger" id="user-menu-trigger" data-no-drag>
        <span class="avatar-sm" id="user-avatar-sm">?</span>
        <span class="uname" id="user-name-top">{{t:topbar.userFallback}}</span>
        <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" style="opacity:.6"><polyline points="6 9 12 15 18 9"/></svg>
        <div class="user-menu" id="user-menu">
          <div class="user-menu-head">
            <div class="name" id="user-menu-name">—</div>
            <div class="sub" id="user-menu-sub">—</div>
            <div class="sub um-srv" id="user-menu-srv" style="display:none"></div>
          </div>
          <div class="user-menu-item" id="user-menu-settings">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 11-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09a1.65 1.65 0 00-1-1.51 1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 11-2.83-2.83l.06-.06a1.65 1.65 0 00.33-1.82 1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09a1.65 1.65 0 001.51-1 1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 112.83-2.83l.06.06a1.65 1.65 0 001.82.33H9a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 112.83 2.83l-.06.06a1.65 1.65 0 00-.33 1.82V9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg>
            <span data-i18n="nav.settings">{{t:nav.settings}}</span>
          </div>
          <div class="user-menu-item danger" id="user-menu-logout">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 21H5a2 2 0 01-2-2V5a2 2 0 012-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>
            <span id="user-menu-logout-label" data-i18n="topbar.signOut">{{t:topbar.signOut}}</span>
          </div>
        </div>
      </div>
    </span>
  </div>
  <div class="update-banner" id="update-banner">
    <div class="ub-icon">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
    </div>
    <div class="ub-text">
      <div class="ub-title" id="ub-title"></div>
      <div class="ub-sub" id="ub-sub"></div>
      <div class="ub-progress" id="ub-progress"><div class="ub-bar" id="ub-bar"></div></div>
    </div>
    <button class="btn-ghost" id="ub-dismiss">{{t:update.later}}</button>
    <button class="btn-ghost hide" id="ub-manual" title="{{t:update.manualTitle}}" data-i18n-title="update.manualTitle" data-i18n="update.manual">{{t:update.manual}}</button>
    <button class="btn-primary" id="ub-update">{{t:update.now}}</button>
  </div>
  <div class="main">
    <div class="nav-rail" id="nav-rail"></div>
    <div class="page-frame">
      <div class="session-tabs" id="session-tabs"></div>
      <div class="page active" id="page-content"></div>
    </div>
    <!-- Scripts / Info / Files side panel (next to the picture: the session stays
         on screen; Files: the session's explorer, files_dock.go). One at a time,
         same slot as the chat panel below: one or the other (rightSlotTake). -->
    <div class="side-panel" id="side-panel">
      <div class="side-panel-head">
        <div class="sp-titles"><span class="sp-title" id="side-panel-title">{{t:common.scripts}}</span><span class="sp-sub" id="side-panel-sub"></span></div>
        <span class="sp-acts" id="side-panel-acts"></span>
        <button class="btn-sm" id="side-panel-close" style="padding:4px 8px" title="{{t:sidePanel.close}}" data-i18n-title="sidePanel.close">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>
      </div>
      <div class="side-panel-body" id="side-panel-body"></div>
    </div>
    <!-- Chat panel -->
    <div class="chat-panel" id="chat-panel">
      <div class="chat-header">
        <div class="avatar" id="chat-avatar">
          <svg fill="currentColor" viewBox="0 0 24 24"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 3c1.66 0 3 1.34 3 3s-1.34 3-3 3-3-1.34-3-3 1.34-3 3-3zm0 14.2c-2.5 0-4.71-1.28-6-3.22.03-1.99 4-3.08 6-3.08 1.99 0 5.97 1.09 6 3.08-1.29 1.94-3.5 3.22-6 3.22z"/></svg>
        </div>
        <div class="info">
          <div class="name" id="chat-title">{{t:chat.supportTitle}}</div>
          <div class="status">
            <span class="dot" id="chat-status-dot" style="background:var(--muted2)"></span>
            <span id="chat-status-text">{{t:chat.status.disconnected}}</span>
          </div>
        </div>
        <button class="btn-sm" id="btn-chat-close" style="padding:4px 8px" title="{{t:chat.hidePanel}}" data-i18n-title="chat.hidePanel">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>
      </div>
      <div class="chat-tabs" id="chat-tabs" role="tablist"></div>
      <div class="chat-messages" id="chat-messages">
        <div class="chat-typing" id="chat-typing">
          <div style="width:28px;height:28px;border-radius:50%%;background:var(--bg3);display:flex;align-items:center;justify-content:center;flex-shrink:0;font-size:10px;font-weight:700;color:var(--muted)">...</div>
          <div class="chat-typing-dots"><span></span><span></span><span></span></div>
        </div>
      </div>
      <div class="chat-input-area">
        <div id="chat-request-btn-wrap"></div>
        <div class="chat-input-row">
          <input id="chat-input" type="text" placeholder="{{t:chat.placeholder}}" disabled/>
          <button class="chat-send-btn" id="chat-send" title="{{t:chat.send}}" data-i18n-title="chat.send" disabled>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>
          </button>
        </div>
      </div>
    </div>
  </div>
  <div class="status-bar" id="status-bar">{{t:status.ready}}</div>
</div>

<!-- 2FA prompt modal -->
<div class="modal-overlay" id="tfa-overlay">
  <div class="modal-box">
    <div class="modal-head">
      <div class="icon">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>
      </div>
      <h3 data-i18n="tfa.title">{{t:tfa.title}}</h3>
      <button class="close" id="tfa-cancel" title="{{t:common.cancel}}" data-i18n-title="common.cancel">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
      </button>
    </div>
    <p class="desc" id="tfa-desc"></p>
    <p class="desc tfa-server" id="tfa-server" style="display:none"></p>
    <input id="tfa-code" class="otp-input" type="text" inputmode="numeric" placeholder="000000" autocomplete="one-time-code"/>
    <label class="trust-row">
      <input type="checkbox" id="tfa-trust"/>
      <div class="labels">
        <span class="title" data-i18n="tfa.trustTitle">{{t:tfa.trustTitle}}</span>
        <span class="sub" id="tfa-trust-sub"></span>
      </div>
    </label>
    <div class="modal-err" id="tfa-error"></div>
    <div class="modal-actions">
      <button class="btn-ghost" id="tfa-cancel2" data-i18n="common.cancel">{{t:common.cancel}}</button>
      <button class="btn-primary" id="tfa-submit" style="padding:8px 18px;font-size:12px;border-radius:10px" data-i18n="tfa.verify" disabled>{{t:tfa.verify}}</button>
    </div>
  </div>
</div>

<!--REACH_LIB-->
<script>
// ── State ────────────────────────────────────────────────────────────────────
let overview = { groups: [] };
let reachScripts = [];
let selectedDevice = null;
let activeTab = 'remote';
let execAbort = null;

// Remote state (active tab)
let remoteWs = null;
let remoteDecoder = null;
let remoteTs = 0;

// Chat state
let currentOperatorId = null;
let currentOperatorName = '';
let currentOperatorAvatar = '';
let currentOperatorLang = ''; // preferredLanguage of the account (i18n.go: the app's language when automatic)
let currentTenantId = null;
let chatTypingTimer = null;
let lastChatTypingEmit = 0;

// Performance HUD state
let perfHudVisible = false;
let perfFrameCount = 0;
let perfByteCount = 0;
let perfLastTime = performance.now();
let perfFps = 0;
let perfBitrate = 0;
let perfCodec = 'H.264';
let perfInterval = null;

// Recording state
let recMediaRecorder = null;
let recTab = null; // session tab whose watermark shows REC (gets the stop mark)
let recChunks = [];
let recStartTime = 0;
let recTimerInterval = null;

// Annotation state
let annotationActive = false;
let annotationTool = 'pen';
let annotationColor = '#ef4444';
let annotationDrawing = false;
let annotationHistory = [];
let annotationCtx = null;

// Zoom state
let zoomLevel = 'Fit'; // exact label: the viewer compares with 'Fit' (letterbox mapping)

// Favorites (localStorage)
let favorites = JSON.parse(localStorage.getItem('oblireach_favorites') || '[]');
let recents = JSON.parse(localStorage.getItem('oblireach_recents') || '[]');

// Multi-tab sessions
let sessionTabs = []; // [{id, device, ws, decoder, ts, label}]
let activeSessionTabId = null;

// ── Helpers ──────────────────────────────────────────────────────────────────
// The main window: the active server (/proxy, servers.go), as always. A
// session window: ITS server, by name (/sp/<profile>): /proxy follows the
// app's active server, which multi-server mode changes while the window lives
// on (its device ids are its server's: another server has the same ids).
async function api(method, path, body) {
  const wp = winPid();
  return await apiAt(wp ? '/sp/' + wp : '/proxy', method, path, body);
}
// The server profile of this session window (from its address), '' for the
// main window (or a window opened without one).
function winPid() { return (reachDetachedMode && reachDetachedParams && reachDetachedParams.profile) || ''; }
// The server this window's own account and lists are of: a session window's
// own, else the active one.
function homePid() { return winPid() || activeServerId || ''; }
// A given server (multi-server mode): base '/proxy', '/sp/<profile>' or
// '/spt/<profile>/<tenant>' (srvBase / devBase).
async function apiAt(base, method, path, body) {
  return await _apiWithTfa(base, method, path, body, false);
}
async function _apiWithTfa(base, method, path, body, retried) {
  const opts = { method, headers: { 'Content-Type': 'application/json' } };
  if (body !== undefined) opts.body = JSON.stringify(body);
  const resp = await fetch(base + path, opts);
  if (resp.status !== 401 || retried) return resp;
  // Peek at body — use clone so the caller can still read it if we don't retry.
  let parsed = null;
  try { parsed = await resp.clone().json(); } catch { return resp; }
  if (!parsed?.twoFactorRequired) return resp;
  const tfa = await awaitTwoFactorCode(tfaActionLabel(parsed.action), parsed.currentIp || '', tfaServerName(base));
  if (!tfa) return resp; // User cancelled — propagate the original 401.
  const merged = Object.assign({}, body || {}, { twoFactorCode: tfa.code });
  if (tfa.trustIp) merged.trustIp = true;
  return _apiWithTfa(base, method, path, merged, true);
}
// The action a 2FA code confirms, in words when the app knows its key (the
// file explorer, remote sessions: asked often), else the server's key.
// (Indexed "<namespace>:<name>" for the server's "<namespace>.<name>".)
const TFA_ACTION_KEYS = {
  'command:list_directory': 'tfa.action.listDirectory',
  'command:download_file': 'tfa.action.downloadFile',
  'command:upload_file': 'tfa.action.uploadFile',
  'command:delete_file': 'tfa.action.deleteFile',
  'command:rename_file': 'tfa.action.renameFile',
  'command:create_directory': 'tfa.action.createDirectory',
  'remote:session_start': 'tfa.action.remoteSessionStart',
};
function tfaActionLabel(action) {
  const a = typeof action === 'string' ? action : '';
  if (!a) return T('tfa.thisAction');
  const m = /^([a-z]+)\.([a-z_]+)$/.exec(a);
  const k = m ? m[1] + ':' + m[2] : '';
  return (k && Object.prototype.hasOwnProperty.call(TFA_ACTION_KEYS, k)) ? T(TFA_ACTION_KEYS[k]) : a;
}
// The server a 2FA code is asked for, named in the prompt (each server has its
// own authenticator code): in multi-server mode, or for another server than
// the active one. '' otherwise.
function tfaServerName(base) {
  const m = /^\/spt?\/([^/]+)/.exec(String(base || ''));
  const pid = m ? m[1] : (activeServerId || '');
  if (serverMode !== 'multi' && pid === (activeServerId || '')) return '';
  return srvName(pid);
}

// Prompt for a 6-digit TOTP code. Resolves to { code, trustIp } on submit, null on cancel.
// Mirrors the Obliance client's components/common/TwoFactorPromptModal.tsx.
// serverName: the server it is for ('' : not said, the only one on screen).
// One prompt at a time: a request asked while one is on screen (a listing and
// a download together, two tabs) waits for it to be answered, then gets its
// own prompt. (It used to take the prompt's buttons over: the first request
// never got an answer, and whatever waited on it hung.)
const tfaWaiting = [];
let tfaOnScreen = false;
function awaitTwoFactorCode(actionLabel, currentIp, serverName) {
  return new Promise((resolve) => {
    tfaWaiting.push({ actionLabel, currentIp, serverName, resolve });
    if (!tfaOnScreen) tfaNext();
  });
}
function tfaNext() {
  const job = tfaWaiting.shift();
  if (!job) { tfaOnScreen = false; return; }
  tfaOnScreen = true;
  tfaShow(job.actionLabel, job.currentIp, job.serverName).then((result) => {
    try { job.resolve(result); } finally { tfaNext(); }
  });
}
function tfaShow(actionLabel, currentIp, serverName) {
  return new Promise((resolve) => {
    const overlay = document.getElementById('tfa-overlay');
    const input = document.getElementById('tfa-code');
    const trust = document.getElementById('tfa-trust');
    const errEl = document.getElementById('tfa-error');
    const submitBtn = document.getElementById('tfa-submit');
    const cancelBtn = document.getElementById('tfa-cancel');
    const cancelBtn2 = document.getElementById('tfa-cancel2');

    input.value = ''; trust.checked = false; errEl.textContent = '';
    // The action (named by the server) and the IP: escaped, in the sentence.
    document.getElementById('tfa-desc').innerHTML = TH('tfa.desc', { action: i18nHtml('<span id="tfa-action">' + esc(actionLabel) + '</span>') });
    document.getElementById('tfa-trust-sub').innerHTML = TH('tfa.trustSub', { ip: i18nHtml('<span class="ip" id="tfa-ip">' + esc(currentIp || T('tfa.thisIp')) + '</span>') });
    const srvEl = document.getElementById('tfa-server');
    if (srvEl) { srvEl.textContent = serverName ? T('tfa.server', { server: serverName }) : ''; srvEl.style.display = serverName ? '' : 'none'; }
    submitBtn.disabled = true;

    const done = (result) => {
      overlay.classList.remove('visible');
      submitBtn.onclick = null;
      cancelBtn.onclick = null;
      cancelBtn2.onclick = null;
      input.oninput = null;
      input.onkeydown = null;
      resolve(result);
    };
    const submit = () => { if (input.value.length === 6) done({ code: input.value, trustIp: trust.checked }); };
    const cancel = () => done(null);

    input.oninput = () => {
      input.value = input.value.replace(/\D/g, '').slice(0, 6);
      submitBtn.disabled = input.value.length !== 6;
    };
    input.onkeydown = (e) => {
      if (e.key === 'Enter') submit();
      else if (e.key === 'Escape') cancel();
    };
    submitBtn.onclick = submit;
    cancelBtn.onclick = cancel;
    cancelBtn2.onclick = cancel;
    overlay.classList.add('visible');
    setTimeout(() => input.focus(), 30);
  });
}
function setStatus(msg) { document.getElementById('status-bar').textContent = msg; }
function esc(s) {
  if (!s) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
function fmtTime(ts) {
  return new Date(ts).toLocaleTimeString(i18nLang(), { hour: '2-digit', minute: '2-digit' });
}
// Dates of the lists (logs), in the app's language.
function fmtDateTime(v) { return new Date(v).toLocaleString(i18nLang()); }

// ── Login ────────────────────────────────────────────────────────────────────
document.getElementById('btn-login').addEventListener('click', doLogin);
document.getElementById('inp-pass').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
document.getElementById('btn-sso').addEventListener('click', doSsoLogin);

let ssoCheckTimer = null;
document.getElementById('inp-server').addEventListener('input', () => {
  clearTimeout(ssoCheckTimer);
  ssoCheckTimer = setTimeout(checkSso, 500);
});

// Probes the server for Obligate SSO support and adjusts UI visibility.
// The SSO button is shown by default (optimistic) so a user can always attempt
// SSO even if detection fails. We only HIDE the SSO button if the server
// explicitly returns obligateEnabled:false.
async function checkSso() {
  const server = document.getElementById('inp-server').value.trim().replace(/\/$/, '');
  if (!server) return;
  try {
    await fetch('/local/config', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serverUrl: server }) });
  } catch {}
  const ssoEl = document.getElementById('sso-login-fields');
  const sepEl = document.getElementById('or-sep');
  try {
    const r = await fetch('/proxy/api/auth/sso-config');
    if (!r.ok) return; // leave SSO button visible — user can still try
    const d = await r.json();
    const sso = d.data || d;
    const explicitlyDisabled = sso && sso.obligateEnabled === false;
    const show = !explicitlyDisabled;
    ssoEl.style.display = show ? '' : 'none';
    sepEl.style.display = show ? 'flex' : 'none';
  } catch {
    // Network error reaching server — leave SSO button visible.
  }
}

async function doSsoLogin() {
  const server = document.getElementById('inp-server').value.trim().replace(/\/$/, '');
  const errEl = document.getElementById('sso-err');
  const btn = document.getElementById('btn-sso');
  // The label only: the button keeps its icon.
  const label = (text) => { const l = document.getElementById('btn-sso-label'); if (l) l.textContent = text; else btn.textContent = text; };
  if (!server) { errEl.textContent = T('login.err.serverRequired'); return; }
  // The SSO round trip reloads the whole page. The chats of a unified server
  // go on (the other windows of this user keep them; chat:list brings them
  // back here). Those of an older server would be lost without a chat:close,
  // their windows left open on the users' desktops: they end cleanly instead
  // (after asking).
  if (chatLeaveEndsChats() && !confirm(T('login.ssoConfirm'))) return;
  // (Multi-server mode: signing in on one server while sessions of the
  // others run in this window; the session windows stay.)
  if (winTabCount() && !confirm(T('login.ssoConfirmSessions'))) return;
  loginLeaveIfRewritten(server);
  btn.disabled = true; label(T('login.connecting')); errEl.textContent = '';
  try {
    await fetch('/local/config', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serverUrl: server }) });
    const r = await fetch('/proxy/api/auth/sso-desktop-init', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    const d = await r.json();
    if (!d.success || !d.data?.authorizeUrl) { errEl.textContent = ssoInitErrorText(r.status, d); return; }
    closeChat(undefined, true);
    window.location.href = d.data.authorizeUrl;
  } catch (err) { errEl.textContent = T('login.err.connection', { error: err.message }); }
  finally { btn.disabled = false; label(T('login.ssoButton')); }
}

// Why a sign-in failed, in the app's language. The server answers in
// English: its words only for a case the app does not know.
function loginErrorText(status, data) {
  const d = data || {};
  if (status === 401) return T('login.err.invalidCredentials');
  if (status === 403 && (d.ssoLoginRequired || d.code === 'ssoLoginRequired')) return T('login.err.ssoRequired');
  if (status === 429) return T('login.err.tooMany');
  return (typeof d.error === 'string' && d.error) || T('login.err.failed');
}
// Same for the start of an SSO sign-in (the app's proxy answers 502 when the
// server cannot be reached).
function ssoInitErrorText(status, data) {
  const d = data || {}, e = typeof d.error === 'string' ? d.error : '';
  if (status === 502) return T('login.err.connection', { error: e || ('HTTP ' + status) });
  if (e === 'SSO not configured') return T('login.err.ssoNotConfigured');
  if (e === 'Obligate unreachable') return T('login.err.obligateUnreachable');
  return e || T('login.err.ssoInit');
}

async function doLogin() {
  const server = document.getElementById('inp-server').value.trim().replace(/\/$/, '');
  const username = document.getElementById('inp-user').value.trim();
  const password = document.getElementById('inp-pass').value;
  const errEl = document.getElementById('login-err');
  const btn = document.getElementById('btn-login');
  if (!server || !username || !password) { errEl.textContent = T('login.err.allFields'); return; }
  btn.disabled = true; btn.textContent = T('login.connecting'); errEl.textContent = '';
  loginLeaveIfRewritten(server);
  try {
    await fetch('/local/config', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serverUrl: server, username }) });
    const remember = document.getElementById('inp-remember').checked;
    const r = await api('POST', '/api/auth/login', { username, password, remember });
    const data = await r.json();
    if (!r.ok || !data.success) { errEl.textContent = loginErrorText(r.status, data); return; }
    const ts = document.getElementById('top-server'); if (ts) ts.textContent = server;
    // Multi-server mode: the page on screen stays (the other servers' too).
    await enterApp({ keepPage: serverMode === 'multi' });
  } catch (err) { errEl.textContent = T('login.err.connection', { error: err.message }); }
  finally { btn.disabled = false; btn.textContent = T('login.connect'); }
}

// opts.keepPage: the page on screen is drawn again (multi-server mode: a
// sign-in on one server, the mode switched on), not the Dashboard.
async function enterApp(opts) {
  opts = opts || {};
  appReady = false; // (a language switch meanwhile draws nothing of the previous server)
  document.getElementById('login-overlay').style.display = 'none';
  document.getElementById('app').style.display = 'flex';
  loginToShow(''); // (a sign-in page for one server of several: over)

  // The active profile first: the devices table drawn by loadOverview must
  // not meet the previous server's id and favourites (chat pid, stars).
  await loadServers();
  if (serverMode === 'multi') { await enterAppMulti(opts); return; }
  if (activeServerIsLocal()) { await enterAppLocal(opts); return; }
  await loadMe();
  // Back from multi-server mode: this server's session goes back to the
  // tenant it was on (the device actions of that mode moved it).
  await homeTenantRestore(activeServerId || '');
  // The account's language (i18n.go): the app's when "Language" is automatic.
  // Before anything is drawn: the pages below come in that language.
  await i18nSyncAccount();
  loadServerLocalState();
  await loadOverview();
  await loadScripts();
  await loadTenants();
  buildNavRail();
  navigateTo(opts.keepPage && currentRoute ? currentRoute : 'dashboard', opts.keepPage ? currentRouteParams : null);
  appReady = true;
  initSocketIO();
  chatProbeServers(); // (the other servers signed in: their chats come here too)
  dlStart(); // (the lists kept up to date: devices_live.go)
  startUpdateChecker();
  reachSyncDetached(); // session windows still open (page reloaded)
}

// A local base on screen (single mode): its Connections page (connections_page.go)
// and the pages that need no server (navLocalOk). No account, no sign-in, no
// request to any Obliance server: the chats of the servers signed in go on.
async function enterAppLocal(opts) {
  currentTenantId = null; tenants = []; overview = { groups: [] };
  await i18nSyncAccount();
  loadServerLocalState();
  renderTenantSwitcher();
  userMenuServerRender();
  buildNavRail();
  const keep = opts.keepPage && currentRoute && navLocalOk(currentRoute) && currentRoute !== 'device-detail';
  navigateTo(keep ? currentRoute : 'devices', keep ? currentRouteParams : null);
  appReady = true;
  chatProbeServers(); // (the servers signed in: their chats come here too)
  dlStart(); // (nothing to read on a local base: started for a switch back)
  reachSyncDetached();
}
// The pages a local base shows: its recents and favourites (the Dashboard,
// without a server's figures), its connections, the file explorers, the
// settings of the app itself (navLocalSettingsOk).
const NAV_LOCAL_ROUTES = ['dashboard', 'devices', 'support', 'help', 'files', 'vault', 'settings'];
function navLocalOk(route) { return NAV_LOCAL_ROUTES.indexOf(route) >= 0; }
const NAV_LOCAL_SETTINGS = ['app', 'files', 'vault', 'support', 'about'];
function navLocalSettingsOk(sub) { return NAV_LOCAL_SETTINGS.indexOf(sub) >= 0; }

// Multi-server mode: every server at once (servers.go). The active one is a
// server signed in (the top bar's account, the Logs and Settings pages, the
// language); the device lists show them all, each in its section.
// The app comes as soon as every server's account is known (each waited for
// SRV_ME_MS at most): the devices of each server fill its section when they
// come (loadAllServers), one slow or unreachable server never holds the others.
async function enterAppMulti(opts) {
  loadAllServers().catch(() => {});
  await Promise.all(obServers().map(p => srvState(p.id).mePhase || null));
  const signed = obServers().filter(p => srvState(p.id).signedIn === true);
  if (!signed.length) { await showSignInPage(); return; } // none: the active server's sign-in page
  if (srvState(activeServerId || '').signedIn !== true) {
    // The active server is signed out: another one takes its place (mode kept).
    try { await fetch('/local/servers/switch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: signed[0].id }) }); } catch {}
    await loadServers();
  }
  const st = srvState(activeServerId || '');
  if (st.mePayload) srvApplyMe(activeServerId || '', st.mePayload);
  await i18nSyncAccount();
  loadServerLocalState();
  renderTenantSwitcher();
  buildNavRail();
  navigateTo(opts.keepPage && currentRoute ? currentRoute : 'dashboard', opts.keepPage ? currentRouteParams : null);
  appReady = true;
  initSocketIO();
  dlStart(); // (the lists kept up to date: devices_live.go)
  startUpdateChecker();
  reachSyncDetached();
}

// Current user (name, avatar, theme, tenant) of the active server: the main
// app and the detached session windows. false when the server session is gone.
async function loadMe() {
  // /api/auth/me returns { data: { user, permissions, currentTenantId } }
  try {
    const r = await api('GET', '/api/auth/me');
    if (!r.ok) return false;
    const d = await r.json() || {};
    const pid = homePid(); // (a session window: its own server's)
    const st = srvState(pid);
    st.me = srvApplyMe(pid, d.data || d);
    const was = st.signedIn;
    st.signedIn = true;
    if (was !== true) chatSyncSockets(); // (signed in again: its chat socket comes back)
    return true;
  } catch { return false; }
}

// /api/auth/me of server pid (payload: its data): the account, kept for that
// server. The active server's (or asActive: a session window's own server)
// is the one the app shows: operator name and picture (top bar, chats),
// language, current tenant. Its theme is told to the app for that server
// (theme.go: the app wears the theme of its source server).
// srvShownPid: a session window of multi-server mode shows its own server's
// account (detachedBootMulti), whatever server is active; null: the active one.
let srvShownPid = null;
function srvApplyMe(pid, payload, asActive) {
  payload = payload || {};
  const u = payload.user || payload;
  const me = {
    id: u.id ?? null,
    name: u.displayName || u.display_name || u.username || T('common.operator'),
    avatar: u.profilePicture || u.profile_picture || u.avatar || '',
    role: typeof u.role === 'string' ? u.role : '',
    email: u.email || u.username || '',
    lang: typeof u.preferredLanguage === 'string' ? u.preferredLanguage : (typeof u.preferred_language === 'string' ? u.preferred_language : ''),
    theme: (u.preferences && u.preferences.preferredTheme) || u.preferredTheme,
    tenantId: payload.currentTenantId ?? u.currentTenantId ?? null,
  };
  const st = srvState(pid);
  st.mePayload = payload;
  // Multi-server mode: where this server's session was before its device
  // actions moved it (single mode goes back to it: homeTenantRestore).
  if (serverMode === 'multi' && me.tenantId !== null) homeTenantNote(pid, me.tenantId);
  if (asActive || pid === (srvShownPid !== null ? srvShownPid : (activeServerId || ''))) {
    currentOperatorId = me.id;
    currentOperatorName = me.name;
    currentOperatorLang = me.lang;
    currentOperatorAvatar = me.avatar;
    currentTenantId = me.tenantId;
    // Chat header (the panel's and the Chat page's)
    chatViewsAvatar();
    // Top-bar user menu
    const initials = (currentOperatorName || '?').split(/\s+/).map(w => w[0]).join('').toUpperCase().slice(0, 2);
    const avSm = document.getElementById('user-avatar-sm');
    if (avSm) avSm.innerHTML = currentOperatorAvatar ? '<img src="' + esc(currentOperatorAvatar) + '"/>' : esc(initials);
    const nameTop = document.getElementById('user-name-top'); if (nameTop) nameTop.textContent = currentOperatorName;
    const nameMenu = document.getElementById('user-menu-name'); if (nameMenu) nameMenu.textContent = currentOperatorName;
    const subMenu = document.getElementById('user-menu-sub'); if (subMenu) subMenu.textContent = me.email;
  }
  if (pid === (activeServerId || '')) applyServerTheme(me.theme);
  else themeReport(pid, me.theme);
  return me;
}

// ── Data loading ─────────────────────────────────────────────────────────────
// The device lists. Single mode: the active server's (as always). Multi-server
// mode: every server's (pid given: that one only), each on its own.
async function loadOverview(pid) {
  if (serverMode === 'multi') {
    if (pid) { await srvLoad(pid); overviewRebuild(); overviewRedraw(); }
    else await loadAllServers();
    return;
  }
  // A local base on screen: no server to read, its connections instead
  // (localConnsLoad: the lists follow).
  if (!pid && typeof activeServerIsLocal === 'function' && activeServerIsLocal()) {
    overview = { groups: [] };
    await localConnsLoad();
    overviewRedraw();
    return;
  }
  setStatus(T('status.loadingDevices'));
  if (!pid || pid === homePid()) dlOvSeq++; // (a light read in flight is dropped: devices_live.go)
  try {
    // This window's server (a session window: its own), or the server named
    // (a session window's tab of another server): its devices, stamped with it.
    const lp = pid || homePid();
    // ?agents=1: also the devices with the Obliance agent only, and what each
    // offers (agent_tools.go); an older server ignores it.
    const path = '/api/reach/overview?agents=1';
    const r = lp === homePid() ? await api('GET', path) : await apiAt(srvBase(lp), 'GET', path);
    const rd = await dlRead(r); // (its signature and ETag kept: the light reads, devices_live.go)
    const d = rd.body;
    if (!d || typeof d !== 'object') throw new Error('HTTP ' + (r ? r.status : '?'));
    overview = d.data || { groups: [] };
    if (!Array.isArray(overview.groups)) overview.groups = [];
    srvStamp(lp, overview.groups);
    srvStampTools(overview.groups, toolsSupported(overview));
    if (r && r.ok && d.data && Array.isArray(d.data.groups) && lp === homePid()) dlNoteLoaded(lp, rd, overview.groups);
    // (a list read whole: the devices it no longer holds lose their transfers
    // and files being edited, explorer_viewer.go)
    if (r && r.ok && d && d.data && Array.isArray(d.data.groups) && typeof explorerCtxPruneGone === 'function') explorerCtxPruneGone(lp, overview.groups);
    if (typeof refreshDevicesTable === 'function') refreshDevicesTable();
    dashboardRefreshIfShown(); // its recents / favourites (greyed or not) follow too
    chatPickerRefreshIfShown(); // (the Chat page's device picker)
    filesPickerRefreshIfShown(); // (the Files page's: files_page.go)
    runPickerRefreshIfShown(); // (the Run page's: run_page.go)
    if (typeof vmsKick === 'function') vmsKick(); // (the VMs of this server: vms_page.go)
    setStatus(T('status.readyDevices', { count: countDevices() }));
  } catch (err) { setStatus(T('status.loadFailed', { error: err.message })); }
}
// The scripts of a device's server and tenant (the Scripts panel, the Run
// page): loadScripts / scriptsFor / scriptsInvalidate, scripts_panel.go.
// The Dashboard drawn again from the overview, when it is on screen.
function dashboardRefreshIfShown() {
  if (!appReady || currentRoute !== 'dashboard' || reachDetachedMode) return; // (a session window has no Dashboard)
  const pc = document.getElementById('page-content');
  if (!pc) return;
  // The page on screen updated in place (its figures and lists; the Support
  // cards, their fields and focus untouched); drawn whole only when its
  // layout changes (a local base alone has no figures).
  const s = dashState;
  const lo = serverMode !== 'multi' && activeServerIsLocal();
  const hl = lo || (serverMode === 'multi' && serverProfiles.some(p => p.local));
  if (s && s.pc === pc && s.localOnly === lo && s.hasLocal === hl && Array.prototype.indexOf.call(pc.children || [], s.body) >= 0) { dashFill(s); return; }
  pc.innerHTML = '';
  renderDashboardPage(pc);
}
function countDevices() { return overview.groups.reduce((n, g) => n + g.devices.length, 0); }
// Best-effort hostname for a device id already known to this window's
// overview (Logs > Connection: sessions only carry the raw id).
function deviceHostnameById(id) {
  if (id == null) return null;
  for (const g of overview.groups) {
    for (const d of g.devices) { if (String(d.id) === String(id)) return d.hostname || null; }
  }
  return null;
}

// ── Favorites / Recents ──────────────────────────────────────────────────────
// Per server (device ids of two servers overlap): favorites / recents are the
// active server's lists; favFor / recentsFor give any server's (multi-server
// mode lists every server's devices at once).
function toggleFavorite(deviceId) {
  const idx = favorites.indexOf(deviceId);
  if (idx >= 0) favorites.splice(idx, 1);
  else favorites.push(deviceId);
  localStorage.setItem(serverStoreKey('oblireach_favorites'), JSON.stringify(favorites));
}
const favOthers = new Map(); // profile -> its favourites (not the active server's)
function favFor(pid) {
  pid = pid || '';
  if (pid === (activeServerId || '')) return favorites;
  let l = favOthers.get(pid);
  if (!l) {
    try { l = JSON.parse(localStorage.getItem(serverStoreKeyFor(pid, 'oblireach_favorites')) || '[]'); } catch (e) { l = []; }
    if (!Array.isArray(l)) l = [];
    favOthers.set(pid, l);
  }
  return l;
}
function favHas(dev) { return !!dev && favFor(devPid(dev)).includes(dev.id); }
function favToggle(dev) {
  const pid = devPid(dev);
  if (pid === (activeServerId || '')) { toggleFavorite(dev.id); return; }
  const l = favFor(pid), idx = l.indexOf(dev.id);
  if (idx >= 0) l.splice(idx, 1); else l.push(dev.id);
  try { localStorage.setItem(serverStoreKeyFor(pid, 'oblireach_favorites'), JSON.stringify(l)); } catch (e) {}
}
// A server's recents as saved (the session windows add to them too: same
// origin, same storage), each entry with its server.
function recentsFor(pid) {
  let v = [];
  try { v = JSON.parse(localStorage.getItem(serverStoreKeyFor(pid || '', 'oblireach_recents')) || '[]'); } catch (e) { v = []; }
  return (Array.isArray(v) ? v : []).filter(r => r && typeof r === 'object');
}
// Read again before a change, and before the Dashboard lists them.
function recentsReload() {
  try {
    const v = JSON.parse(localStorage.getItem(serverStoreKey('oblireach_recents')) || '[]');
    if (Array.isArray(v)) recents = v.filter(r => r && typeof r === 'object');
  } catch (e) {}
}
// Recents go to the device's own server's list (a local connection: the
// app's own, LOCAL_PID's: localconn_tools.go; never a server's).
function addRecent(device) {
  if (!device) return;
  const pid = devPid(device);
  let list = recentsFor(pid);
  list = list.filter(r => r.id !== device.id);
  list.unshift({ id: device.id, hostname: device.hostname, ts: Date.now() });
  if (list.length > 5) list = list.slice(0, 5);
  localStorage.setItem(serverStoreKeyFor(pid, 'oblireach_recents'), JSON.stringify(list));
  if (pid === (activeServerId || '')) recents = list;
}

// ── Router & nav rail ────────────────────────────────────────────────────────
let currentRoute = 'dashboard';
let currentRouteParams = null;

// group / label: catalog keys (i18n.go), drawn with T() at every build.
const NAV_TREE = [
  { group: 'nav.overview', items: [
    { route: 'dashboard',  label: 'nav.dashboard', icon: 'home' },
    { route: 'devices',    label: 'nav.devices',   icon: 'server' },
    // The Hyper-V VMs this user may see, under their hosts (vms_page.go);
    // shown only when a server in the scope lists some (vmsNavVisible).
    { route: 'vms',        label: 'nav.vms',       icon: 'vms', shown: () => vmsNavVisible() },
    // Every open conversation, in large (renderChatPage); badge: total unread.
    { route: 'chat',       label: 'nav.chat',      icon: 'chat', badge: 'nav-chat-badge' },
    // The main window's file explorers, in large (files_page.go renderFilesPage).
    { route: 'files',      label: 'nav.files',     icon: 'folder' },
    // A script on several devices, its results followed live (run_page.go renderRunPage).
    { route: 'run',        label: 'nav.run',       icon: 'play' },
    // The password vault's entries, grouped by place (vault_page.go renderVaultPage).
    { route: 'vault',      label: 'nav.vault',     icon: 'lock' },
  ]},
  { group: 'nav.audit', items: [
    { route: 'logs',       label: 'nav.logs',      icon: 'file', sub: [
      { route: 'logs', sub: 'connection', label: 'nav.logConnection' },
      { route: 'logs', sub: 'alarm',      label: 'nav.logAlarm' },
      { route: 'logs', sub: 'console',    label: 'nav.logConsole' },
      { route: 'logs', sub: 'script',     label: 'nav.logScript' },
    ]},
  ]},
  { group: 'nav.config', items: [
    { route: 'scripts',    label: 'nav.scripts',    icon: 'code' },
    { route: 'deployment', label: 'nav.deployment', icon: 'download' },
    { route: 'settings',   label: 'nav.settings',   icon: 'cog' },
  ]},
];

const NAV_ICONS = {
  home:     '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 12l9-9 9 9M5 10v10h14V10"/></svg>',
  server:   '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="4" width="20" height="6" rx="2"/><rect x="2" y="14" width="20" height="6" rx="2"/><line x1="6" y1="7" x2="6.01" y2="7"/><line x1="6" y1="17" x2="6.01" y2="17"/></svg>',
  vms:      '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 6V4a2 2 0 012-2h12a2 2 0 012 2v8a2 2 0 01-2 2h-2"/><rect x="2" y="6" width="16" height="11" rx="2"/><line x1="7" y1="21" x2="13" y2="21"/><line x1="10" y1="17" x2="10" y2="21"/></svg>',
  chat:     '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z"/></svg>',
  file:     '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>',
  folder:   '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/></svg>',
  user:     '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 21v-2a4 4 0 00-4-4H8a4 4 0 00-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>',
  users:    '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17 21v-2a4 4 0 00-4-4H5a4 4 0 00-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 00-3-3.87"/><path d="M16 3.13a4 4 0 010 7.75"/></svg>',
  shield:   '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>',
  download: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>',
  code:     '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>',
  play:     '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="6 4 20 12 6 20 6 4"/></svg>',
  lock:     '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 018 0v4"/></svg>',
  cog:      '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 11-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09a1.65 1.65 0 00-1-1.51 1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 11-2.83-2.83l.06-.06a1.65 1.65 0 00.33-1.82 1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09a1.65 1.65 0 001.51-1 1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 112.83-2.83l.06.06a1.65 1.65 0 001.82.33H9a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 112.83 2.83l-.06.06a1.65 1.65 0 00-.33 1.82V9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg>',
};

function buildNavRail() {
  const rail = document.getElementById('nav-rail');
  rail.innerHTML = '';
  const local = activeServerIsLocal(); // (a local base: the pages that ask no server, navLocalOk)
  for (const g of NAV_TREE) {
    const items = local ? g.items.filter(it => navLocalOk(it.route)) : g.items;
    if (!items.length) continue;
    const lbl = document.createElement('div'); lbl.className = 'nav-group-label'; lbl.textContent = T(g.group); rail.appendChild(lbl);
    for (const it of items) {
      const item = document.createElement('div');
      item.className = 'nav-item';
      item.dataset.route = it.route;
      if (it.shown && !it.shown()) item.style.display = 'none'; // (an entry shown on condition: VMs)
      item.title = T(it.label);
      item.innerHTML = (NAV_ICONS[it.icon] || '') + '<span>' + TH(it.label) + '</span>' +
        (it.sub ? '<svg class="chev" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><polyline points="9 6 15 12 9 18"/></svg>' : '');
      if (it.badge) {
        // A count (chatNavBadge), shown over the icon when the rail is folded.
        const b = document.createElement('span'); b.className = 'nav-badge hide'; b.id = it.badge;
        item.appendChild(b);
      }
      if (it.sub) {
        const sub = document.createElement('div'); sub.className = 'nav-sub';
        for (const s of it.sub) {
          const si = document.createElement('div'); si.className = 'nav-item'; si.dataset.route = s.route; si.dataset.sub = s.sub;
          si.textContent = T(s.label);
          si.addEventListener('click', ev => { ev.stopPropagation(); navigateTo(s.route, { sub: s.sub }); });
          sub.appendChild(si);
        }
        item.addEventListener('click', () => {
          const isOpen = sub.classList.toggle('open');
          item.classList.toggle('expanded', isOpen);
          if (isOpen) navigateTo(it.route, { sub: it.sub[0].sub });
        });
        rail.appendChild(item); rail.appendChild(sub);
      } else {
        item.addEventListener('click', () => navigateTo(it.route));
        rail.appendChild(item);
      }
    }
  }
  const pin = document.createElement('button');
  pin.className = 'nav-pin'; pin.id = 'nav-pin';
  pin.addEventListener('click', () => {
    railPinnedOpen = !railPinnedOpen;
    try { localStorage.setItem('oblireach_rail_pinned', railPinnedOpen ? '1' : '0'); } catch (e) {}
    updateRailCompact();
  });
  rail.appendChild(pin);
  updateRailCompact();
  chatNavBadge();
}

// The rail folds to icons while a Remote session is on screen (more room for
// the picture), unless the user pinned it open; it is full size everywhere else.
// In detached mode ("Detach by default"), for the embedded viewer only
// (reachRailFolds): a session launched in its own window leaves it open.
var railPinnedOpen = false; // var: read by buildNavRail, whatever the call order
try { railPinnedOpen = localStorage.getItem('oblireach_rail_pinned') === '1'; } catch (e) {}
function updateRailCompact() {
  const rail = document.getElementById('nav-rail');
  if (!rail) return;
  const inRemote = currentRoute === 'device-detail' && activeTab === 'remote';
  const folds = inRemote && reachRailFolds(selectedDevice);
  rail.classList.toggle('compact', folds && !railPinnedOpen);
  // Focus mode only while Remote is on screen (always, in a detached window).
  if (!inRemote && !reachDetachedMode) document.body.classList.remove('reach-focus');
  const pin = document.getElementById('nav-pin');
  if (pin) {
    pin.style.display = folds ? '' : 'none';
    pin.textContent = railPinnedOpen ? '\u00AB' : '\u00BB';
    pin.title = railPinnedOpen ? T('nav.pinFold') : T('nav.pinKeep');
  }
}

function navigateTo(route, params) {
  // A local base shows only the pages that ask no Obliance server (its
  // connections, the explorers, the app's own settings): any other route
  // (a shortcut, a page kept from a server) lands on its connections.
  if (!reachDetachedMode && activeServerIsLocal()) {
    // (a local connection's own page: its terminal and explorer tabs)
    if (!navLocalOk(route) && !(route === 'device-detail' && params && (devIsLocal(params.device) || devIsSupport(params.device)))) { route = 'devices'; params = null; }
    else if (route === 'settings' && !navLocalSettingsOk((params && params.sub) || '')) params = { sub: NAV_LOCAL_SETTINGS[0] };
  }
  // The page (and the Remote pane with it) is rebuilt below: keep the picture
  // of the session on screen, and stop what is bound to its canvas.
  if (currentRoute === 'device-detail') { reachStashPicture(); reachLeaveCanvas(); }
  currentRoute = route; currentRouteParams = params || null;
  updateRailCompact();
  document.querySelectorAll('.tenant-menu.open,.user-menu.open').forEach(m => m.classList.remove('open'));
  // Connect by code and Receive help are reached from the Dashboard's cards
  // (support_dashboard.go): the Dashboard stays lit on them.
  const railRoute = route === 'support' || route === 'help' ? 'dashboard' : route;
  document.querySelectorAll('#nav-rail .nav-item').forEach(el => {
    const r = el.dataset.route, s = el.dataset.sub;
    let active = false;
    if (r === railRoute) {
      if (s) active = !!(params && params.sub === s);
      else if (!el.closest('.nav-sub')) active = true;
    }
    el.classList.toggle('active', active);
    if (r === route && s && active) {
      const parent = el.closest('.nav-sub'); if (parent) parent.classList.add('open');
      const head = parent ? parent.previousElementSibling : null;
      if (head) head.classList.add('expanded');
    }
  });
  const pc = document.getElementById('page-content');
  if (!pc) return;
  pc.innerHTML = '';
  switch (route) {
    case 'dashboard':     renderDashboardPage(pc); break;
    case 'devices':       renderDevicesPage(pc); break; // (a local base: its connections, the same page)
    case 'support':       renderSupportPage(pc, params); break; // (support_page.go: connect by code)
    case 'help':          renderHelpPage(pc); break; // (help_page.go: receive help)
    case 'vms':           renderVmsPage(pc); break; // (vms_page.go)
    case 'chat':          renderChatPage(pc); break;
    case 'files':         renderFilesPage(pc); break;
    case 'run':           renderRunPage(pc); break; // (run_page.go: a script on several devices)
    case 'vault':         renderVaultPage(pc); break; // (vault_page.go)
    case 'device-detail': renderDeviceDetail(pc, params && params.device); break;
    case 'logs':          renderLogsPage(pc, (params && params.sub) || 'connection'); break;
    case 'scripts':       renderScriptsPage(pc); break;
    case 'deployment':    renderDeploymentPage(pc); break;
    case 'settings':      renderSettingsPage(pc, (params && params.sub) || 'profile'); break;
    default:              renderDashboardPage(pc);
  }
  // The Scripts / Info panel follows the device on screen; it closes elsewhere.
  sidePanelSync();
  // So does the explorer's side panel of a session (files_dock.go).
  filesDockSync();
  // The top bar's Chat button opens the chat panel: not on the Chat page.
  chatTopButtonSync();
  // The clipboard watcher follows the session ON SCREEN: leaving it for the
  // Dashboard, the Devices list or another device's page stops it.
  reachUpdateClipboardWatch();
}

function renderStubPage(pc, title, msg) {
  const h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + esc(title) + '</h1>';
  const b = document.createElement('div'); b.className = 'page-body';
  b.innerHTML = '<div class="panel"><div class="panel-body"><div class="empty" style="padding:40px"><div class="empty-icon"><svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" style="opacity:.25"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg></div><span>' + esc(msg) + '</span></div></div></div>';
  pc.appendChild(h); pc.appendChild(b);
}

// ── Tenant switcher + user menu ──────────────────────────────────────────────
let tenants = [];
// ── Server profiles (several Obliance servers, one active; servers.go) ───────
// Like the Android app: a switcher next to the tenant one, each server with its
// identity colour (the app's closed palette); switching reloads the app on the
// other server (its own session; sessions and chats of the previous one end).
// A server's colour: its theme token (theme.go --srv-<name>): pastel on the
// dark themes, deeper on Daylight (every mark stays visible on its surfaces).
const SERVER_COLORS = { violet: 'var(--srv-violet)', teal: 'var(--srv-teal)', fuchsia: 'var(--srv-fuchsia)', indigo: 'var(--srv-indigo)', cyan: 'var(--srv-cyan)', sand: 'var(--srv-sand)', lavender: 'var(--srv-lavender)', mint: 'var(--srv-mint)' };
let serverProfiles = [];
let activeServerId = null;
let serverMode = 'single'; // 'multi': every server at once (servers.go serverMode)

// ── Multi-server mode: every server at once ──────────────────────────────────
// "All servers" in the server menu: the device lists show every server, each
// in its section (its colour, then its tenants and groups in Obliance's
// order); the tenant menu has one section per server. Each server is read on
// its own (/sp/<profile>/, its own session): one that fails or is signed out
// says so in its section, the others are there. Every action on a device goes
// to THAT device's server (devApi), its session on the device's tenant for
// what Obliance scopes by the current tenant (tenantpin.go /spt/). The active
// server is still the one of the top bar's account, Logs and Settings.
const srv = new Map(); // profile id -> { pid, signedIn: null|bool, me, mePayload, tenants, groups, scoped, error, loading, seq }
let multiFilter = { pid: '', tenantId: null }; // the tenant menu's choice (pid '': every server; tenantId null: every tenant)
try {
  const f = JSON.parse(localStorage.getItem('oblireach_multi_filter') || 'null');
  if (f && typeof f === 'object') multiFilter = { pid: typeof f.pid === 'string' ? f.pid : '', tenantId: (typeof f.tenantId === 'number' && Number.isInteger(f.tenantId)) ? f.tenantId : null };
} catch (e) {}
function srvState(pid) {
  pid = pid || '';
  let st = srv.get(pid);
  if (!st) { st = { pid, signedIn: null, me: null, mePayload: null, tenants: [], groups: [], scoped: false, error: '', loading: false, seq: 0 }; srv.set(pid, st); }
  return st;
}
function srvName(pid) { const p = serverProfiles.find(x => x.id === (pid || '')); return p ? String(p.name || '') : ''; }
// A local base (servers.go Kind "local"): the vault's direct RDP/SSH
// connections, never an Obliance server: no account, no API, no sign-in.
// Every loop that asks servers something (accounts, devices, tenants, chats)
// goes over obServers(); the lists that show the profiles keep them all.
function srvIsLocal(pid) { const p = serverProfiles.find(x => x.id === (pid || '')); return !!(p && p.local); }
function obServers() { return serverProfiles.filter(p => !p.local); }
function srvColor(pid) { const p = serverProfiles.find(x => x.id === (pid || '')); return (p && SERVER_COLORS[p.color]) || 'var(--muted2)'; }
// "All servers": the servers' colours side by side.
function srvAllDot() {
  const cols = serverProfiles.map(p => SERVER_COLORS[p.color]).filter(Boolean);
  return cols.length > 1 ? 'linear-gradient(135deg,' + cols.join(',') + ')' : (cols[0] || 'var(--muted2)');
}
// Where a server's API is: the active one through /proxy in single mode (as
// always), any server through /sp/<profile> (multi-server mode, another one).
// A session window always names the server (see api()).
function srvBase(pid) {
  pid = pid || '';
  if (pid && reachDetachedMode) return '/sp/' + pid;
  return (!pid || (serverMode !== 'multi' && pid === (activeServerId || ''))) ? '/proxy' : '/sp/' + pid;
}
// A device's tenant (its own, else its group's): a positive integer, or null.
function devTenant(dev, g) {
  const t = (dev && dev.tenantId !== undefined && dev.tenantId !== null) ? dev.tenantId : (g ? g.tenantId : null);
  const n = Number(t);
  return (t !== null && t !== undefined && t !== '' && Number.isInteger(n) && n > 0) ? n : null;
}
// Where an action on dev goes: its server. pinned (what Obliance scopes by
// the session's current tenant: a remote session, the scripts and their
// runs): in multi-server mode, on a server that lists every tenant (scoped),
// its session is put on the device's tenant first (/spt/, tenantpin.go).
function devBase(dev, pinned) {
  const pid = devPid(dev), st = srv.get(pid), t = devTenant(dev);
  return (serverMode === 'multi' && pinned && st && st.scoped && t) ? '/spt/' + pid + '/' + t : srvBase(pid);
}
// (the active server in single mode: api() itself, exactly as before)
async function devApi(dev, method, path, body, pinned) {
  // A local connection (localconn_tools.go) has no server: nothing is ever sent for it.
  if (devIsLocal(dev)) throw new Error('local connection');
  const base = devBase(dev, pinned);
  return base === '/proxy' ? api(method, path, body) : apiAt(base, method, path, body);
}
// Each group and device knows its server (and a device its tenant).
function srvStamp(pid, groups) {
  for (const g of Array.isArray(groups) ? groups : []) {
    if (!g || typeof g !== 'object') continue;
    g._pid = pid;
    for (const d of Array.isArray(g.devices) ? g.devices : []) {
      if (!d || typeof d !== 'object') continue;
      d._pid = pid;
      if ((d.tenantId === undefined || d.tenantId === null) && g.tenantId !== undefined && g.tenantId !== null) d.tenantId = g.tenantId;
    }
  }
  return groups;
}
// Reads one server: its account, then (unless opts.overview is false) its
// devices, of every tenant it lets this user see (multi-server mode:
// ?tenantId=all; an older server ignores it and lists its session's tenant,
// scoped false), and its tenants. Never throws: the outcome is its state
// (signedIn false: signed out; error: it could not be read).
async function srvLoad(pid, opts) {
  pid = pid || '';
  const st = srvState(pid);
  if (srvIsLocal(pid)) return st; // (a local base: nothing to ask, never "signed out")
  const was = st.signedIn;
  await srvLoadRun(pid, opts || {}, st);
  // Signed in or out now: its chat socket follows (chatSyncSockets).
  if (st.signedIn !== was) chatSyncSockets();
  return st;
}
// How long a server is waited for (SRV_ME_MS: its account, which decides
// whether it is signed in; SRV_LIST_MS: its devices, every tenant): one that
// accepts the connection and never answers says so in its section, and the
// request is given up (the WebView has few connections to the app's proxy).
const SRV_ME_MS = 8000, SRV_LIST_MS = 45000;
// Its time over: an error marked timeout (said in words: srvErrorText),
// never the browser's own "signal is aborted without reason". The deadline
// rejects first, then the request is aborted (an abort settles the fetch at
// once: before, the race took that AbortError and its raw text was shown).
function srvTimeoutError(ms) {
  const e = new Error(T('devices.server.timeout', { seconds: Math.round(ms / 1000) }));
  e.timeout = true;
  return e;
}
function srvFetch(base, path, ms) {
  let ac = null;
  try { if (typeof AbortController === 'function') ac = new AbortController(); } catch (e) { ac = null; }
  let timer = null, timedOut = false;
  const late = new Promise((_, reject) => {
    timer = setTimeout(() => {
      timedOut = true;
      reject(srvTimeoutError(ms));
      try { if (ac) ac.abort(); } catch (e) {}
    }, ms);
  });
  const opts = { method: 'GET', headers: { 'Content-Type': 'application/json' } };
  if (ac) opts.signal = ac.signal;
  // (an abort of ours: the timeout's error, whichever promise settles first)
  const req = fetch(base + path, opts).catch(e => { throw (timedOut || (e && e.name === 'AbortError')) ? srvTimeoutError(ms) : e; });
  return Promise.race([req, late]).finally(() => { clearTimeout(timer); });
}
// Why a server could not be read, in words: no answer in time (it may be
// restarting: an update), else what failed.
function srvErrorText(name, st) {
  if (st && st.errorTimeout) return T('devices.server.noAnswer', { server: name });
  return T('devices.server.error', { server: name, error: (st && st.error) || '' });
}
async function srvLoadRun(pid, opts, st) {
  const seq = ++st.seq;
  // st.mePhase: settled once its account is known (signed in or out, or it
  // could not be read): what enterAppMulti waits for, not its devices.
  let meDone = null;
  st.mePhase = new Promise(res => { meDone = res; });
  const meKnown = () => { if (meDone) { const f = meDone; meDone = null; f(); } };
  const signedOut = () => { st.signedIn = false; st.me = null; st.mePayload = null; st.groups = []; st.tenants = []; st.scoped = false; st.error = ''; };
  const prof = serverProfiles.find(p => p.id === pid);
  if (prof && prof.hasSession === false) { signedOut(); st.loading = false; meKnown(); return st; } // (no session kept: nothing to ask)
  st.loading = true;
  const base = srvBase(pid);
  try {
    const r = await srvFetch(base, '/api/auth/me', SRV_ME_MS);
    if (seq !== st.seq) return st;
    if (r.status === 401 || r.status === 403) { signedOut(); return st; }
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const d = await r.json() || {};
    const payload = d.data || d;
    const u = payload.user || payload;
    if (!u || u.id === undefined || u.id === null) { signedOut(); return st; }
    st.signedIn = true;
    st.me = srvApplyMe(pid, payload);
    meKnown();
    if (opts.overview === false) { st.error = ''; return st; }
    const [ro, rt] = await Promise.all([
      srvFetch(base, '/api/reach/overview?' + (serverMode === 'multi' ? 'tenantId=all&' : '') + 'agents=1', SRV_LIST_MS),
      srvFetch(base, '/api/tenants', SRV_LIST_MS).catch(() => null),
    ]);
    if (seq !== st.seq) return st;
    const rd = await dlRead(ro); // (its signature and ETag kept: the light reads, devices_live.go)
    const od = rd.body;
    if (!ro.ok) throw new Error((od && typeof od.error === 'string' && od.error) || ('HTTP ' + ro.status));
    const data = (od && od.data) || {};
    st.scoped = !!data.scope;
    st.groups = srvStampTools(srvStamp(pid, Array.isArray(data.groups) ? data.groups : []), toolsSupported(data));
    if (Array.isArray(data.groups)) dlNoteLoaded(pid, rd, st.groups);
    if (Array.isArray(data.groups) && typeof explorerCtxPruneGone === 'function') explorerCtxPruneGone(pid, st.groups); // (explorer_viewer.go)
    let tl = [];
    if (rt && rt.ok) { try { const td = await rt.json(); tl = Array.isArray(td && td.data) ? td.data : (Array.isArray(td) ? td : []); } catch (e) {} }
    st.tenants = tl.filter(t => t && typeof t === 'object' && Number.isInteger(Number(t.id)));
    st.error = '';
  } catch (e) {
    // (a load replaced by a newer one says nothing: only the last one counts)
    if (seq === st.seq) {
      st.errorTimeout = !!(e && (e.timeout || e.name === 'AbortError'));
      st.error = (e && e.timeout) ? e.message : st.errorTimeout ? srvTimeoutError(SRV_LIST_MS).message : ((e && e.message) || String(e));
    }
  } finally {
    if (seq === st.seq) st.loading = false;
    meKnown();
  }
  return st;
}
// Every server, each on its own (one failing never stops the others): each
// server's section is drawn as soon as it answers (a slow one never holds the
// others), then the lists once all are in.
let loadAllSeq = 0;
async function loadAllServers() {
  const mine = ++loadAllSeq;
  setStatus(T('status.loadingDevices'));
  const drawn = () => { if (mine !== loadAllSeq) return; overviewRebuild(); overviewRedraw(); renderTenantSwitcher(); };
  const runs = obServers().map(p => srvLoad(p.id).then(drawn, drawn));
  if (serverProfiles.some(p => p.local)) runs.push(localConnsLoad().then(drawn, drawn)); // (its section: the vault's connections)
  overviewRedraw(); // (their sections say they are loading)
  await Promise.allSettled(runs);
  if (mine !== loadAllSeq) return;
  overviewRebuild();
  overviewRedraw();
  renderTenantSwitcher();
  setStatus(T('status.readyDevices', { count: countDevices() }));
}
// overview (what the Dashboard, the counts and the lookups read): the signed-in
// servers' devices the tenant menu's choice keeps.
function overviewRebuild() {
  if (serverMode !== 'multi') return;
  const groups = [];
  for (const p of obServers()) {
    if (multiFilter.pid && p.id !== multiFilter.pid) continue;
    const st = srv.get(p.id);
    if (!st || st.signedIn !== true) continue;
    for (const g of st.groups) {
      // (an older server lists one tenant only: nothing to narrow)
      if (multiFilter.pid && multiFilter.tenantId !== null && st.scoped && Number(g.tenantId) !== multiFilter.tenantId) continue;
      groups.push(g);
    }
  }
  overview = { groups };
}
function overviewRedraw() {
  if (typeof refreshDevicesTable === 'function') refreshDevicesTable();
  if (typeof vmsKick === 'function') vmsKick(); // (the VMs of the servers listed: vms_page.go)
  dashboardRefreshIfShown();
  chatPickerRefreshIfShown();
  filesPickerRefreshIfShown();
  runPickerRefreshIfShown();
  if (devPickerBuilt && devPickerIsOpen()) devPickerRefresh();
}
// The tenant menu's choice (multi-server mode), kept on this computer.
function multiSetFilterQuiet(pid, tid) {
  multiFilter = { pid: pid || '', tenantId: (pid && Number.isInteger(tid)) ? tid : null };
  try { localStorage.setItem('oblireach_multi_filter', JSON.stringify(multiFilter)); } catch (e) {}
}
function multiSetFilter(pid, tid) {
  multiSetFilterQuiet(pid, tid);
  document.getElementById('tenant-menu').classList.remove('open');
  overviewRebuild();
  renderTenantSwitcher();
  if (currentRoute === 'dashboard' || currentRoute === 'devices' || currentRoute === 'scripts' || currentRoute === 'deployment' || currentRoute === 'vault' || currentRoute === 'vms' || currentRoute === 'run') navigateTo(currentRoute, currentRouteParams);
  setStatus(T('status.readyDevices', { count: countDevices() }));
}
// A tenant of a server picked in the tenant menu. An older server lists its
// session's tenant only: its session goes to that tenant, then it is read again.
async function multiPickTenant(pid, tid) {
  if (!srvState(pid).scoped) {
    let why = '';
    try {
      const r = await apiAt(srvBase(pid), 'POST', '/api/tenant/switch', { tenantId: tid });
      if (!r.ok) {
        let d = null;
        try { d = await r.json(); } catch (e) {}
        why = (d && typeof d.error === 'string' && d.error) || ('HTTP ' + r.status);
      }
    } catch (e) { why = (e && e.message) || String(e); }
    if (why) {
      // (refused, or the server could not be reached: said, the menu closed)
      document.getElementById('tenant-menu').classList.remove('open');
      setStatus(T('topbar.tenantSwitchFailed', { server: srvName(pid), error: why }));
      return;
    }
    await srvLoad(pid);
  }
  multiSetFilter(pid, tid);
}
function multiFilterLabel() {
  if (!multiFilter.pid) return T('topbar.allTenants');
  const server = srvName(multiFilter.pid);
  if (multiFilter.tenantId === null) return T('topbar.allTenantsOf', { server });
  const t = srvState(multiFilter.pid).tenants.find(x => Number(x.id) === multiFilter.tenantId);
  return T('topbar.serverTenant', { server, tenant: t ? String(t.name) : T('topbar.tenantFallback', { id: multiFilter.tenantId }) });
}
// Multi-server mode aligns a server's session on the tenant of the device
// acted on (tenantpin.go): the tenant it was on is kept here, and single mode
// puts it back (its tenant menu shows the session's tenant).
function homeTenantNote(pid, tid) {
  const k = 'oblireach_home_tenant:' + (pid || '');
  try { if (tid !== null && tid !== undefined && localStorage.getItem(k) === null) localStorage.setItem(k, String(tid)); } catch (e) {}
}
async function homeTenantRestore(pid) {
  const k = 'oblireach_home_tenant:' + (pid || '');
  let v = null;
  try { v = localStorage.getItem(k); if (v !== null) localStorage.removeItem(k); } catch (e) {}
  const h = Number(v);
  if (v === null || !Number.isInteger(h) || h <= 0 || h === currentTenantId) return;
  try { const r = await api('POST', '/api/tenant/switch', { tenantId: h }); if (r && r.ok) currentTenantId = h; } catch (e) {}
}
// "All servers": the mode is saved by the app (it wears the first server's
// theme then); nothing running ends.
async function setServerMode(mode) {
  if (mode !== 'multi' || serverMode === 'multi') return;
  document.querySelectorAll('.tenant-menu.open,.user-menu.open').forEach(m => m.classList.remove('open'));
  homeTenantNote(activeServerId || '', currentTenantId);
  try {
    const r = await fetch('/local/servers/mode', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: 'multi' }) });
    if (!r.ok) return;
  } catch { return; }
  sidePanelForget();
  await enterApp({ keepPage: true });
}

// The sign-in page of the active server, over the app (multi-server mode:
// one server of several; "Back to the app" returns to the others).
let loginBackTo = null; // the server the app was on before that page
let loginToPid = '';
function loginToShow(pid) {
  loginToPid = pid || '';
  const el = document.getElementById('login-to');
  if (!el) return;
  const name = loginToPid ? srvName(loginToPid) : '';
  el.textContent = name ? T('login.signInTo', { server: name }) : '';
  el.style.display = name ? '' : 'none';
}
// The server a multi-server sign-in page was shown for, and its address then.
let loginOrig = null; // { pid, url }
// That page given the address of a server not in the list: the app rewrites
// that profile to it (servers.go loginEditLocked). The sessions of that
// profile still open (its session had expired) would reach the new server's
// devices with their ids (every call names the profile): they end first.
function loginLeaveIfRewritten(server) {
  const o = loginOrig;
  if (serverMode !== 'multi' || !o || !o.pid) return;
  const norm = (u) => String(u || '').trim().replace(/[/]+$/, '').toLowerCase();
  const to = norm(server);
  if (to === norm(o.url) || serverProfiles.some(p => p.id !== o.pid && norm(p.serverUrl) === to)) return;
  reachLeaveServerOnly(o.pid);
}
async function showSignInPage() {
  let cfg = {};
  try { cfg = await (await fetch('/local/config')).json() || {}; } catch {}
  loginOrig = serverMode === 'multi' ? { pid: activeServerId || '', url: cfg.serverUrl || '' } : null;
  document.getElementById('inp-server').value = cfg.serverUrl || '';
  document.getElementById('inp-user').value = cfg.username || '';
  document.getElementById('inp-pass').value = '';
  document.getElementById('login-err').textContent = '';
  document.getElementById('app').style.display = 'none';
  document.getElementById('login-overlay').style.display = 'flex';
  loginToShow(serverMode === 'multi' ? (activeServerId || '') : '');
  renderLoginServers();
  if (cfg.serverUrl) checkSso();
}
// A server's "Sign in" (its section, the tenant menu): its sign-in page, the
// others kept (the mode too).
async function multiSignIn(pid) {
  document.querySelectorAll('.tenant-menu.open,.user-menu.open').forEach(m => m.classList.remove('open'));
  if (pid !== (activeServerId || '')) {
    loginBackTo = activeServerId;
    try {
      const r = await fetch('/local/servers/switch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: pid }) });
      if (!r.ok) return;
    } catch { return; }
    await loadServers();
  }
  await showSignInPage();
}
// "Back to the app" (multi-server mode): the app again, on a server signed in.
async function loginBack() {
  const ok = (id) => !!id && id !== (activeServerId || '') && srvState(id).signedIn === true && serverProfiles.some(p => p.id === id);
  const back = ok(loginBackTo) ? loginBackTo : ((serverProfiles.find(p => ok(p.id)) || {}).id);
  loginBackTo = null;
  if (!back) return;
  try {
    const r = await fetch('/local/servers/switch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: back }) });
    if (!r.ok) return;
  } catch { return; }
  await loadServers();
  document.getElementById('login-overlay').style.display = 'none';
  document.getElementById('app').style.display = 'flex';
  loginToShow('');
  const st = srvState(back);
  if (st.mePayload) srvApplyMe(back, st.mePayload);
  await i18nSyncAccount();
  loadServerLocalState();
  overviewRebuild();
  renderTenantSwitcher();
  buildNavRail();
  navigateTo(currentRoute || 'dashboard', currentRouteParams);
  appReady = true;
  initSocketIO();
  startUpdateChecker();
}
// Another server picked on the sign-in page.
function loginPickServer(id) {
  if (serverMode !== 'multi' || srvIsLocal(id)) { switchServer(id); return; } // (a local base: its page, no sign-in)
  if (srvState(id).signedIn === true) { loginBackTo = id; loginBack(); return; }
  multiSignIn(id);
}

async function loadServers() {
  try {
    const r = await fetch('/local/servers');
    // (no answer from the app: what is known stays)
    if (r && r.ok === false) { renderServerSwitcher(); return; }
    const d = await r.json();
    serverProfiles = Array.isArray(d.profiles) ? d.profiles : [];
    activeServerId = d.active || null;
    serverMode = d.mode === 'multi' ? 'multi' : 'single';
    // The theme the app wears now (after a switch: the new server's, at once,
    // before its /api/auth/me answers; theme.go).
    if (typeof d.theme === 'string' && d.theme) themeTake(d.theme, d.themeVersion);
  } catch { serverProfiles = []; }
  // A server removed: its state goes, and a tenant menu choice of it.
  for (const id of Array.from(srv.keys())) if (id && !serverProfiles.some(p => p.id === id)) { srv.delete(id); favOthers.delete(id); }
  if (multiFilter.pid && !serverProfiles.some(p => p.id === multiFilter.pid)) multiSetFilterQuiet('', null);
  renderServerSwitcher();
  chatSyncSockets(); // (a chat socket per server signed in; none for one removed)
}

// The user menu: in multi-server mode it names the server its account and
// its "Sign out" are of (the active one, not otherwise on screen; signing
// out ends that server's sessions only).
function userMenuServerRender() {
  const lbl = document.getElementById('user-menu-logout-label'), el = document.getElementById('user-menu-srv');
  const pid = activeServerId || '';
  const multi = serverMode === 'multi' && !!pid && !reachDetachedMode;
  if (lbl) lbl.textContent = multi ? T('topbar.signOutOf', { server: srvName(pid) }) : T('topbar.signOut');
  if (el) {
    el.innerHTML = multi ? '<span class="srv-dot" style="background:' + srvColor(pid) + '"></span>' + esc(srvName(pid)) : '';
    el.style.display = multi ? '' : 'none';
  }
  // A local base on screen: no account, nothing to sign out of.
  const out = document.getElementById('user-menu-logout');
  if (out) out.style.display = !multi && srvIsLocal(pid) && !reachDetachedMode ? 'none' : '';
}

function renderServerSwitcher() {
  userMenuServerRender();
  const sw = document.getElementById('server-switcher');
  if (!sw) return;
  const cur = serverProfiles.find(p => p.id === activeServerId);
  if (!cur) { sw.style.display = 'none'; return; }
  sw.style.display = 'flex';
  const multi = serverMode === 'multi';
  document.getElementById('server-label').textContent = multi ? T('topbar.allServers') : cur.name;
  const dot = document.getElementById('server-dot');
  dot.className = 'srv-dot' + (multi ? ' all' : '');
  dot.style.background = multi ? srvAllDot() : (SERVER_COLORS[cur.color] || 'var(--muted2)');
  const menu = document.getElementById('server-menu');
  menu.innerHTML = '';
  // "All servers" (two profiles or more, one a real server: servers.go
  // multiPossible): every server at once, the local bases' connections too.
  if (serverProfiles.length >= 2 && obServers().length) {
    const all = document.createElement('div');
    all.className = 'tenant-menu-item srv-all' + (multi ? ' active' : '');
    // (its badge: the unread chat messages of every server; each server's own below)
    all.innerHTML = '<span class="srv-dot all" style="background:' + srvAllDot() + '"></span><span>' + TH('topbar.allServers') + '</span>' + srvUnreadHtml(null);
    all.addEventListener('click', e => { e.stopPropagation(); if (!multi) setServerMode('multi'); });
    menu.appendChild(all);
  }
  for (const p of serverProfiles) {
    const mi = document.createElement('div');
    mi.className = 'tenant-menu-item' + (!multi && p.id === activeServerId ? ' active' : '');
    mi.title = p.local ? T('topbar.localBaseTitle') : p.serverUrl;
    mi.dataset.pid = p.id;
    mi.innerHTML = '<span class="srv-dot" style="background:' + (SERVER_COLORS[p.color] || 'var(--muted2)') + '"></span><span>' + esc(p.name) + '</span>' +
      srvUnreadHtml(p.id) +
      (p.id === activeServerId ? '' : '<button class="srv-x" title="' + TH('topbar.removeServer') + '">\u00D7</button>');
    mi.addEventListener('click', e => {
      e.stopPropagation();
      if (e.target.closest('.srv-x')) { removeServer(p.id); return; }
      // (from "All servers": this server only)
      if (multi || p.id !== activeServerId) switchServer(p.id);
    });
    menu.appendChild(mi);
  }
  const add = document.createElement('div');
  add.className = 'tenant-menu-item';
  add.innerHTML = '<span class="srv-dot" style="background:none;text-align:center;line-height:9px">+</span><span>' + TH('topbar.addServer') + '</span>';
  add.addEventListener('click', e => { e.stopPropagation(); showAddServerForm(); });
  menu.appendChild(add);
  const addLocal = document.createElement('div');
  addLocal.className = 'tenant-menu-item';
  addLocal.innerHTML = '<span class="srv-dot" style="background:none;text-align:center;line-height:9px">+</span><span>' + TH('topbar.addLocalServer') + '</span>';
  addLocal.addEventListener('click', e => { e.stopPropagation(); showAddLocalServerForm(); });
  menu.appendChild(addLocal);
}

function showAddServerForm() {
  const menu = document.getElementById('server-menu');
  // novalidate: the browser's own bubble would speak the language of Windows,
  // not the app's; the address is checked here and by the app.
  menu.innerHTML = '<form class="srv-add-form" novalidate><input type="url" placeholder="https://obliance.example" required/><button class="btn-sm" type="submit">' + TH('common.add') + '</button></form>';
  const form = menu.querySelector('form');
  const input = form.querySelector('input');
  input.focus();
  form.addEventListener('click', e => e.stopPropagation());
  const invalid = () => { input.setCustomValidity(T('topbar.serverUrlInvalid')); input.reportValidity(); input.setCustomValidity(''); };
  form.addEventListener('submit', async e => {
    e.preventDefault();
    if (!String(input.value || '').trim()) { invalid(); return; }
    try {
      const r = await fetch('/local/servers/add', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ serverUrl: input.value }) });
      if (!r.ok) { invalid(); return; }
    } catch { return; }
    if (serverMode === 'multi') {
      // Multi-server mode: nothing ends; the new server's sign-in page, with
      // the way back to the others.
      loginBackTo = activeServerId;
      await loadServers();
      await showSignInPage();
      return;
    }
    reachLeaveServer();
    await reenterServer(); // the new server: sign-in page
  });
}

// showAddLocalServerForm: the "local" server profile (a pure local RDP/SSH
// connection tree, servers.go's ServerProfile.Kind == "local") — a name
// only, no URL, no sign-in page: the server switches to it immediately.
function showAddLocalServerForm() {
  const menu = document.getElementById('server-menu');
  menu.innerHTML = '<form class="srv-add-form" novalidate><input type="text" placeholder="' + esc(T('topbar.localServerNamePlaceholder')) + '" required/><button class="btn-sm" type="submit">' + TH('common.add') + '</button></form>';
  const form = menu.querySelector('form');
  const input = form.querySelector('input');
  input.focus();
  form.addEventListener('click', e => e.stopPropagation());
  form.addEventListener('submit', async e => {
    e.preventDefault();
    const name = String(input.value || '').trim();
    if (!name) return;
    try {
      const r = await fetch('/local/servers/add-local', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) });
      if (!r.ok) return;
    } catch { return; }
    // The app puts single mode on the new base (servers.go /add-local): the
    // top bar, the menus and the pages follow it (enterAppLocal), from
    // "All servers" too.
    reachLeaveServer();
    await reenterServer();
  });
}

// Remote sessions are bound to one server: they end before a switch. Chats
// keep running (one socket per server) and stay in the chat panel.
function reachLeaveServer() {
  if (typeof reachDisposeAllTabs === 'function') reachDisposeAllTabs();
  toolDisposeAll(); // its terminals and explorers (terminal_viewer.go)
  reachCloseAllDetached(); // the session windows too
}
// One server's remote sessions end (its sign-out, its removal in multi-server
// mode): here and in the session windows; the other servers' go on.
function reachLeaveServerOnly(pid) {
  pid = pid || '';
  for (const t of sessionTabs.filter(t => devPid(t.device) === pid)) {
    if (recMediaRecorder && recTab === t) toggleRecording();
    reachDisposeTab(t);
    sessionTabs.splice(sessionTabs.indexOf(t), 1);
  }
  toolDisposeWhere(t => devPid(t.device) === pid); // (its terminals and explorers)
  if (activeSessionTabId && !sessionTabs.some(t => t.id === activeSessionTabId) && !toolTabById(activeSessionTabId)) { activeSessionTabId = null; remoteWs = null; remoteDecoder = null; }
  reachNative('reachCloseProfileWindows', pid).catch(() => {});
  for (const k of Array.from(reachDetachedIds)) if (keyPid(k) === pid) { reachDetachedIds.delete(k); delete reachDetachedInfo[k]; }
  renderSessionTabs();
}
// From multi-server mode to server keep: the sessions of the other servers
// end (here and in the session windows, whose tabs of keep stay).
function reachLeaveServersExcept(keep) {
  keep = keep || '';
  for (const t of sessionTabs.filter(t => devPid(t.device) !== keep)) {
    if (recMediaRecorder && recTab === t) toggleRecording();
    reachDisposeTab(t);
    sessionTabs.splice(sessionTabs.indexOf(t), 1);
  }
  toolDisposeWhere(t => devPid(t.device) !== keep); // (their terminals and explorers)
  if (activeSessionTabId && !sessionTabs.some(t => t.id === activeSessionTabId) && !toolTabById(activeSessionTabId)) { activeSessionTabId = null; remoteWs = null; remoteDecoder = null; }
  for (const p of serverProfiles) if (p.id !== keep) reachNative('reachCloseProfileWindows', p.id).catch(() => {});
  for (const k of Array.from(reachDetachedIds)) if (keyPid(k) !== keep) { reachDetachedIds.delete(k); delete reachDetachedInfo[k]; }
  renderSessionTabs();
}

// Back to single mode on server pid (it lists every tenant: its session was
// put on each device's tenant in multi-server mode): its session is on its
// own tenant again (enterApp: homeTenantRestore), the one its tenant menu
// shows. Its sessions on devices of its other tenants would now start again
// (a reconnect), run their scripts, in that tenant's context (restrictions,
// the session's records, the scripts listed): they end, here and in the
// session windows (whose devices' tenants are read in groups: pid's devices
// as multi-server mode listed them). An unknown tenant ends too. Returns the
// number of sessions ended.
function reachLeaveOtherTenants(pid, groups) {
  pid = pid || '';
  const tid = appReady && Number.isInteger(currentTenantId) ? currentTenantId : null;
  const tenantOfKey = new Map();
  for (const g of Array.isArray(groups) ? groups : []) {
    for (const d of (g && Array.isArray(g.devices)) ? g.devices : []) if (d) tenantOfKey.set(devKeyOf(pid, d.id), devTenant(d, g));
  }
  const other = (t) => tid === null || t !== tid;
  let n = 0;
  for (const t of sessionTabs.filter(t => t && t.device && devPid(t.device) === pid && other(devTenant(t.device)))) {
    if (recMediaRecorder && recTab === t) toggleRecording();
    reachDisposeTab(t);
    sessionTabs.splice(sessionTabs.indexOf(t), 1);
    n++;
  }
  n += toolDisposeWhere(t => t && t.device && devPid(t.device) === pid && other(devTenant(t.device))); // (its terminals, explorers)
  if (activeSessionTabId && !sessionTabs.some(t => t.id === activeSessionTabId) && !toolTabById(activeSessionTabId)) { activeSessionTabId = null; remoteWs = null; remoteDecoder = null; }
  const closes = [];
  for (const k of Array.from(reachDetachedIds)) {
    const kb = toolKeyBase(k); // (a tool window: its device's key, then "~<kind>")
    if (keyPid(k) !== pid || !other(tenantOfKey.has(kb) ? tenantOfKey.get(kb) : null)) continue;
    closes.push(reachNative('reachCloseWindow', k).catch(() => {}));
    reachDetachedIds.delete(k); delete reachDetachedInfo[k];
    n++;
  }
  // (the windows as they are once those closed: a list asked before, by
  // enterApp, may still name them)
  if (closes.length) Promise.allSettled(closes).then(() => reachSyncDetached());
  if (n) {
    renderSessionTabs();
    setStatus(T('topbar.tenantSessionsEnded', { server: srvName(pid) }));
  }
  return n;
}

async function switchServer(id) {
  if (serverMode === 'multi') {
    // "All servers" -> this one: back to single mode on it (its session goes
    // back to its own tenant: enterApp).
    const st = srvState(id), scoped = st.scoped, groups = st.groups.slice(); // (its devices' tenants, as multi-server mode listed them)
    reachLeaveServersExcept(id);
    try {
      const r = await fetch('/local/servers/switch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, mode: 'single' }) });
      if (!r.ok) return;
    } catch { return; }
    await reenterServer();
    if (scoped) reachLeaveOtherTenants(id, groups);
    return;
  }
  reachLeaveServer();
  try {
    const r = await fetch('/local/servers/switch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id }) });
    if (!r.ok) return;
  } catch { return; }
  await reenterServer();
}

// Shows the app for the (new) active server without reloading the page.
async function reenterServer() {
  appReady = false;
  document.querySelectorAll('.tenant-menu.open,.user-menu.open').forEach(m => m.classList.remove('open'));
  selectedDevice = null; currentTenantId = null; tenants = []; overview = { groups: [] };
  // The previous server's page goes now, and the active profile (chat pid,
  // favourites keys) follows the switch before anything of the new server is drawn.
  if (currentRoute === 'device-detail') reachLeaveCanvas();
  document.getElementById('page-content').innerHTML = '';
  sidePanelClose(); sidePanelForget(); // (device ids of two servers overlap)
  await loadServers();
  loadServerLocalState();
  if (serverMode !== 'multi' && activeServerIsLocal()) { await enterApp(); return; } // (a local base: no sign-in page)
  let cfg = {};
  try { cfg = await (await fetch('/local/config')).json(); } catch {}
  document.getElementById('inp-server').value = cfg.serverUrl || '';
  document.getElementById('inp-user').value = cfg.username || '';
  if (cfg.hasSession && cfg.serverUrl) {
    try { const r = await api('GET', '/api/auth/me'); if (r.ok) { await enterApp(); return; } } catch {}
  }
  document.getElementById('app').style.display = 'none';
  document.getElementById('login-overlay').style.display = 'flex';
  loginToShow('');
  renderLoginServers();
  if (cfg.serverUrl) checkSso();
}

// Favourites and recents are per server (device ids of two servers overlap).
// The first server keeps the pre-profiles keys: nothing is lost on upgrade.
// Its id is remembered (not its position): once it is removed, the next
// server does not inherit its lists.
function serverStoreKey(base) { return serverStoreKeyFor(activeServerId || '', base); }
// The same for any server (multi-server mode lists several).
function serverStoreKeyFor(pid, base) {
  if (!pid) return base;
  let owner = null;
  try { owner = localStorage.getItem('oblireach_legacy_owner'); } catch {}
  if (!owner && serverProfiles.length) {
    owner = serverProfiles[0].id;
    try { localStorage.setItem('oblireach_legacy_owner', owner); } catch {}
  }
  return pid === owner ? base : base + ':' + pid;
}
function loadServerLocalState() {
  // The other servers' favourites are read again when next needed: the one
  // that was active until now kept its list in favorites (not in favOthers).
  favOthers.clear();
  try { favorites = JSON.parse(localStorage.getItem(serverStoreKey('oblireach_favorites')) || '[]'); } catch { favorites = []; }
  try { recents = JSON.parse(localStorage.getItem(serverStoreKey('oblireach_recents')) || '[]'); } catch { recents = []; }
}

// Sign-in page: the other servers, to go back (e.g. after adding one by mistake).
function renderLoginServers() {
  const el = document.getElementById('login-servers');
  if (!el) return;
  loginToShow(loginToPid); // (in the language on screen)
  const others = serverProfiles.filter(p => p.id !== activeServerId);
  el.innerHTML = '';
  // Multi-server mode with another server signed in: back to the app.
  if (serverMode === 'multi' && others.some(p => srvState(p.id).signedIn === true)) {
    const back = document.createElement('button');
    back.type = 'button'; back.className = 'btn-ghost login-srv'; back.id = 'login-back';
    back.textContent = T('login.backToApp');
    back.addEventListener('click', () => { loginBack(); });
    el.appendChild(back);
  }
  if (!others.length) return;
  const lbl = document.createElement('span'); lbl.textContent = T('login.otherServers'); el.appendChild(lbl);
  for (const p of others) {
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'btn-ghost login-srv'; b.title = p.serverUrl;
    b.innerHTML = '<span class="srv-dot" style="background:' + (SERVER_COLORS[p.color] || 'var(--muted2)') + '"></span>' + esc(p.name);
    b.addEventListener('click', () => loginPickServer(p.id));
    el.appendChild(b);
  }
}

async function removeServer(id) {
  // Its chat tabs and its socket go: /sp/<id>/ answers 404 once the profile
  // is removed. The chats of a unified server go on in this user's other
  // windows; those of an older one end (chat:close while its proxy still exists).
  closeChat(id, true);
  chatDropSocket(id);
  chatRetryClear(id);
  // Its remote sessions end, here and in the session windows (multi-server
  // mode shows them next to the others'); what the page kept of it goes.
  const wasMulti = serverMode === 'multi';
  reachLeaveServerOnly(id);
  await toolEndsWait(); // (its terminals' POST /end: /sp/<id>/ is gone once it is removed)
  srv.delete(id);
  favOthers.delete(id);
  if (multiFilter.pid === id) multiSetFilterQuiet('', null);
  try {
    for (const base of ['oblireach_favorites', 'oblireach_recents', 'oblireach_devtree_folded']) localStorage.removeItem(base + ':' + id);
    localStorage.removeItem('oblireach_home_tenant:' + id);
  } catch (e) {}
  try { await fetch('/local/servers/remove', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id }) }); } catch {}
  await loadServers();
  if (!appReady) return;
  if (wasMulti && serverMode !== 'multi') { await reenterServer(); return; } // one server left: single mode
  if (serverMode === 'multi') { overviewRebuild(); overviewRedraw(); renderTenantSwitcher(); }
}

// ── Theme (theme.go) ─────────────────────────────────────────────────────────
// The whole app wears the Obliance theme its operator chose on the server
// (the active one; in multi-server mode the first of the list): backgrounds,
// surfaces, borders, texts, statuses, title bars, the viewer, the dialogs.
// Its variables are theme.go's (one block per theme on <body data-theme>);
// the accent stays the app's own red family whatever the theme.
// In multi-server mode and on a local base alone on screen, a theme chosen
// in Settings > Application > Appearance wins (theme.go themeOverride).
const THEMES = ['obli-operator', 'obli-daylight', 'obli-dim', 'modern', 'neon'];
// The Obli suite's themes as the settings offer them (theme.go themeCatalog:
// id, name, light, swatch = page, surface, text, accent).
const THEME_CATALOG = []/*THEME_CATALOG*/;
function themeNormalize(t) { t = String(t || '').trim().toLowerCase(); return THEMES.indexOf(t) >= 0 ? t : 'obli-operator'; }
// On <body>: <html> carries its lang / dir only.
function themeApply(t) { t = themeNormalize(t); try { document.body.dataset.theme = t; } catch (e) {} return t; }
// The app's change count of the theme worn here (theme.go themeGen; -1: none
// yet): its answers and notices can cross (two servers reported at once), an
// older one is ignored.
let themeVer = -1;
function themeTake(t, v) {
  if (typeof v === 'number' && isFinite(v)) {
    if (v < themeVer) return null;
    themeVer = v;
  }
  return themeApply(t);
}
// The app says the theme changed (every window at once: theme.go syncTheme).
window.__reachOnTheme = function (t, v) { themeTake(t, v); };
// The Desktop's own theme settings, as the app last said them (theme.go
// themeState multiTheme / localTheme; '' : follow the servers).
let themeSettings = { multi: '', local: '' };
function themeSettingsTake(st) {
  if (!st || typeof st !== 'object') return;
  const ok = (t) => (typeof t === 'string' && THEMES.indexOf(t) >= 0) ? t : '';
  if (typeof st.multiTheme === 'string') themeSettings.multi = ok(st.multiTheme);
  if (typeof st.localTheme === 'string') themeSettings.local = ok(st.localTheme);
}
// The theme a setting chooses now ('' : none, the servers' rule): the
// multi-server one in multi-server mode, the local base's one on a local
// base alone on screen; a real server on screen: its account's, always.
function themeOverrideNow() {
  if (serverMode === 'multi') return themeSettings.multi;
  if (typeof activeServerIsLocal === 'function' && activeServerIsLocal()) return themeSettings.local;
  return '';
}
// pid's account's theme (loadMe), told to the app: it keeps it for that
// server and re-themes every window that must change; its answer (the theme
// it wears now) is applied here at once. Without the app, or when it cannot
// be told: the same rule here (the active server's theme; in multi-server
// mode, the first server's).
async function themeReport(pid, theme) {
  const known = typeof theme === 'string' ? theme : '';
  let st = null;
  try {
    const r = await fetch('/local/theme', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile: pid || '', theme: known }) });
    if (r && r.ok) st = await r.json();
  } catch (e) {}
  if (st && typeof st.theme === 'string') { themeSettingsTake(st); themeTake(st.theme, st.version); return; }
  if (themeVer >= 0) return; // (the app answered before: this report only failed; its notices keep the theme)
  const p = serverProfiles.find(x => x.id === (pid || activeServerId));
  if (p) p.theme = known ? themeNormalize(known) : '';
  const ov = themeOverrideNow();
  if (ov) { themeApply(ov); return; } // (a theme chosen in the settings: theme.go themeOverride)
  if (serverMode === 'multi' && obServers().length) { themeApply(obServers()[0].theme); return; } // (theme.go themeSource)
  if (!pid || pid === (activeServerId || '')) themeApply(known);
}
// The active server's account's theme (loadMe).
function applyServerTheme(theme) { themeReport(activeServerId || '', theme); }

document.getElementById('server-switcher').addEventListener('click', e => {
  if (e.target.closest('.tenant-menu')) return;
  document.getElementById('tenant-menu').classList.remove('open');
  document.getElementById('user-menu').classList.remove('open');
  const m = document.getElementById('server-menu');
  if (!m.classList.contains('open')) renderServerSwitcher();
  m.classList.toggle('open');
});
document.addEventListener('click', e => {
  if (!e.target.closest('#server-switcher')) document.getElementById('server-menu')?.classList.remove('open');
});

async function loadTenants() {
  try {
    const r = await api('GET', '/api/tenants');
    const d = await r.json();
    tenants = Array.isArray(d.data) ? d.data : (Array.isArray(d) ? d : []);
    renderTenantSwitcher();
  } catch {}
}

function renderTenantSwitcher() {
  if (serverMode === 'multi') { renderTenantSwitcherMulti(); return; }
  const sw = document.getElementById('tenant-switcher');
  const lbl = document.getElementById('tenant-label');
  const menu = document.getElementById('tenant-menu');
  if (!tenants.length) { sw.style.display = 'none'; return; }
  sw.style.display = 'flex';
  const cur = tenants.find(t => t.id === currentTenantId) || tenants[0];
  lbl.textContent = cur?.name || T('topbar.tenantFallback', { id: currentTenantId || '?' });
  menu.innerHTML = '';
  menu._tmSig = null; // (the multi-server menu's signature: drawn again whole next time)
  for (const t of tenants) {
    const mi = document.createElement('div');
    mi.className = 'tenant-menu-item' + (t.id === currentTenantId ? ' active' : '');
    mi.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" style="' + (t.id === currentTenantId ? '' : 'visibility:hidden') + '"><polyline points="20 6 9 17 4 12"/></svg><span>' + esc(t.name) + '</span>';
    mi.addEventListener('click', e => { e.stopPropagation(); switchTenant(t.id); });
    menu.appendChild(mi);
  }
}

// Multi-server mode: "All", then one section per server (its colour; a click:
// all its tenants), its tenants below; a server signed out says so, with its
// "Sign in". Nothing is switched on the servers: the lists are narrowed
// (multiSetFilter), the actions go to each device's tenant (devApi).
function renderTenantSwitcherMulti() {
  const sw = document.getElementById('tenant-switcher');
  const lbl = document.getElementById('tenant-label');
  const menu = document.getElementById('tenant-menu');
  if (!serverProfiles.length) { sw.style.display = 'none'; return; }
  sw.style.display = 'flex';
  domText(lbl, multiFilterLabel());
  // Drawn again only when what it lists changed (called at every refresh of
  // the lists: an open menu keeps its items under the mouse).
  const sig = JSON.stringify([multiFilter, T('topbar.allTenants'), obServers().map(p => {
    const st = srvState(p.id);
    return [p.id, p.name, srvColor(p.id), st.signedIn, st.error || '', st.errorTimeout || false, (st.tenants || []).map(t => [t.id, t.name])];
  })]);
  if (menu._tmSig === sig && menu.children && menu.children.length) return;
  menu._tmSig = sig;
  menu.innerHTML = '';
  const tick = (on) => '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" style="' + (on ? '' : 'visibility:hidden') + '"><polyline points="20 6 9 17 4 12"/></svg>';
  const item = (cls, html, on, fn) => {
    const mi = document.createElement('div');
    mi.className = cls + (on ? ' active' : '');
    mi.innerHTML = html;
    if (fn) mi.addEventListener('click', e => { e.stopPropagation(); fn(); });
    menu.appendChild(mi);
    return mi;
  };
  item('tenant-menu-item', tick(!multiFilter.pid) + '<span>' + TH('topbar.allTenants') + '</span>', !multiFilter.pid, () => multiSetFilter('', null));
  for (const p of obServers()) { // (a local base has no tenant: its section in the lists only)
    const st = srvState(p.id);
    const dot = '<span class="srv-dot" style="background:' + srvColor(p.id) + '"></span>';
    // Not signed in (its "Sign in"), unreachable (its "Try again"), or not
    // known yet: as its section in the device list says.
    if (st.signedIn === false) {
      const off = item('tenant-menu-section off', dot + '<span>' + esc(p.name) + '</span><span class="tms-note">' + TH('topbar.notSignedIn') + '</span>', false, null);
      off.dataset.pid = p.id;
      item('tenant-menu-item sub', '<span>' + TH('topbar.signIn') + '</span>', false, () => multiSignIn(p.id)).dataset.pid = p.id;
      continue;
    }
    if (st.signedIn !== true) {
      const note = st.error ? esc(srvErrorText(String(p.name || ''), st)) : TH('common.loading');
      const off = item('tenant-menu-section off', dot + '<span>' + esc(p.name) + '</span><span class="tms-note' + (st.error ? ' err' : '') + '" title="' + note + '">' + note + '</span>', false, null);
      off.dataset.pid = p.id;
      if (st.error) {
        item('tenant-menu-item sub', '<span>' + TH('common.tryAgain') + '</span>', false,
          () => srvLoad(p.id).then(() => { overviewRebuild(); overviewRedraw(); renderTenantSwitcher(); })).dataset.pid = p.id;
      }
      continue;
    }
    const allOn = multiFilter.pid === p.id && multiFilter.tenantId === null;
    const sec = item('tenant-menu-section', dot + '<span>' + esc(p.name) + '</span>', allOn, () => multiSetFilter(p.id, null));
    sec.title = T('topbar.allTenantsOf', { server: p.name });
    sec.dataset.pid = p.id;
    for (const t of st.tenants) {
      const tid = Number(t.id);
      const on = multiFilter.pid === p.id && multiFilter.tenantId === tid;
      const mi = item('tenant-menu-item sub', tick(on) + '<span>' + esc(t.name) + '</span>', on, () => multiPickTenant(p.id, tid));
      mi.dataset.pid = p.id; mi.dataset.tid = String(tid);
    }
  }
}

async function switchTenant(tenantId) {
  try {
    const r = await api('POST', '/api/tenant/switch', { tenantId });
    if (!r.ok) return;
    currentTenantId = tenantId;
    // The server reads the tenant once, at the chat socket's handshake: renew
    // this server's socket (now, or when its last chat ends) for the new one.
    chatRenewSocketWhenIdle(activeServerId);
    document.getElementById('tenant-menu').classList.remove('open');
    renderTenantSwitcher();
    await loadOverview();
    if (currentRoute === 'dashboard' || currentRoute === 'devices' || currentRoute === 'scripts' || currentRoute === 'deployment' || currentRoute === 'vault' || currentRoute === 'vms' || currentRoute === 'run') navigateTo(currentRoute, currentRouteParams);
  } catch {}
}

document.getElementById('tenant-switcher').addEventListener('click', e => {
  if (e.target.closest('.tenant-menu')) return;
  document.getElementById('user-menu').classList.remove('open');
  document.getElementById('tenant-menu').classList.toggle('open');
});
document.getElementById('user-menu-trigger').addEventListener('click', e => {
  if (e.target.closest('.user-menu')) return;
  document.getElementById('tenant-menu').classList.remove('open');
  document.getElementById('user-menu').classList.toggle('open');
});
document.addEventListener('click', e => {
  if (!e.target.closest('#tenant-switcher')) document.getElementById('tenant-menu')?.classList.remove('open');
  if (!e.target.closest('#user-menu-trigger')) document.getElementById('user-menu')?.classList.remove('open');
});
document.getElementById('user-menu-settings').addEventListener('click', () => navigateTo('settings'));

// ── Update checker, one-click update ─────────────────────────────────────────
// The server says which Desktop version it has (/api/oblireach-desktop/version,
// checked at sign-in and hourly). "Update now" hands over to the app
// (updater_windows.go, reachInstallUpdate): it downloads the installer from
// this server itself (the page gives no URL), checks its signature, asks
// Windows once (UAC), closes Oblireach, installs it and starts it again. The
// page only shows how it goes (window.__reachOnUpdate). Without the native
// side (an older host), "Update now" opens the installer in the browser.
let updateInfo = null;        // latest version info from /api/oblireach-desktop/version
let updateCheckInterval = null;
let updateRun = null;         // the app's status of the update being run (or failed), null: none
let updateResultShown = null; // how the last update went (told after the restart), until OK

function cmpSemver(a, b) {
  const pa = String(a || '0').split('.').map(n => parseInt(n, 10) || 0);
  const pb = String(b || '0').split('.').map(n => parseInt(n, 10) || 0);
  for (let i = 0; i < 3; i++) {
    const x = pa[i] || 0, y = pb[i] || 0;
    if (x !== y) return x > y ? 1 : -1;
  }
  return 0;
}

function updateDismissed() {
  try { return localStorage.getItem('oblireach_dismissed_update'); } catch (e) { return null; }
}
function updateDismiss(v) {
  try { if (v) localStorage.setItem('oblireach_dismissed_update', v); else localStorage.removeItem('oblireach_dismissed_update'); } catch (e) {}
}
// A newer version than this one, not put off with "Later".
function updateAvailable() {
  return !!(updateInfo && updateInfo.version && cmpSemver(updateInfo.version, window.__reach_version || '0.0.0') > 0 &&
    updateDismissed() !== updateInfo.version);
}
function updateBusy() {
  return !!updateRun && ['downloading', 'verifying', 'elevating', 'installing'].indexOf(updateRun.state) >= 0;
}

// The server the version is asked from: the active one; in multi-server mode,
// one signed in if the active one is not.
function updatePid() {
  const a = activeServerId || '';
  if (serverMode !== 'multi' || srvState(a).signedIn === true) return a;
  const p = serverProfiles.find(x => srvState(x.id).signedIn === true);
  return p ? p.id : a;
}
async function checkForUpdate() {
  try {
    const pid = updatePid();
    const r = await fetch((pid === (activeServerId || '') ? '/proxy' : '/sp/' + pid) + '/api/oblireach-desktop/version');
    if (!r.ok) return false;
    const d = await r.json();
    const info = d.data || d;
    if (!info || !info.version) return false;
    info._pid = pid;
    updateInfo = info;
    renderUpdateBanner();
    return cmpSemver(info.version, window.__reach_version || '0.0.0') > 0;
  } catch { return false; }
}

// The app's status of the update (updater_windows.go set): the latest one
// wins (seq): a push can come before the answer of reachInstallUpdate.
window.__reachOnUpdate = function (s) {
  if (!s || typeof s !== 'object') return;
  if (updateRun && (Number(s.seq) || 0) < (Number(updateRun.seq) || 0)) return;
  updateRun = s.state === 'idle' ? null : s;
  renderUpdateBanner();
  if (s.state === 'installing') setStatus(T('update.installingStatus'));
};

function renderUpdateBanner() {
  const el = document.getElementById('update-banner');
  if (!el) return;
  const title = document.getElementById('ub-title'), sub = document.getElementById('ub-sub'), bar = document.getElementById('ub-bar');
  const later = document.getElementById('ub-dismiss'), manual = document.getElementById('ub-manual'), go = document.getElementById('ub-update');
  el.classList.remove('busy', 'error', 'done', 'visible');
  bar.classList.remove('indeterminate'); bar.style.width = '0';
  manual.classList.add('hide'); later.classList.remove('hide'); go.classList.remove('hide');
  go.disabled = false; later.textContent = T('update.later'); go.textContent = T('update.now');
  const s = updateRun;
  const v = (s && s.version) || (updateInfo && updateInfo.version) || '';
  // A text naming the version, or its variant without one (not known yet).
  const tv = (key, params) => T(v ? key : key + 'NoVersion', Object.assign({ version: v }, params || {}));
  if (updateBusy()) {
    el.classList.add('busy', 'visible');
    later.classList.add('hide'); go.disabled = true; go.textContent = T('update.updating');
    if (s.state === 'downloading') {
      const pct = Number(s.percent);
      title.textContent = pct >= 0 ? tv('update.downloadingPct', { percent: pct }) : tv('update.downloading');
      sub.textContent = s.total > 0 ? T('update.downloadedOf', { received: reachFmtBytes(s.received), total: reachFmtBytes(s.total) }) : T('update.fromServer');
      if (pct >= 0) bar.style.width = Math.min(100, pct) + '%%'; else bar.classList.add('indeterminate');
    } else if (s.state === 'verifying') {
      title.textContent = T('update.verifying');
      sub.textContent = T('update.verifyingSub');
      bar.classList.add('indeterminate');
    } else if (s.state === 'elevating') {
      title.textContent = tv('update.elevating');
      sub.textContent = T('update.elevatingSub');
      bar.style.width = '100%%';
    } else {
      title.textContent = tv('update.installing');
      sub.textContent = T('update.installingSub');
      bar.classList.add('indeterminate');
    }
    return;
  }
  if (s && (s.state === 'error' || s.state === 'cancelled')) {
    el.classList.add('visible');
    if (s.state === 'error') el.classList.add('error');
    title.textContent = s.state === 'cancelled' ? T('update.cancelledTitle') : T('update.failedTitle');
    // By its key (s.text) when the app sent one: in the language on screen,
    // also after a switch (s.message: the words of the language of then).
    sub.textContent = i18nMsgText(s.text) || s.message || '';
    go.textContent = T('common.tryAgain');
    if (s.state === 'error' && updateInfo && updateInfo.downloadUrl) manual.classList.remove('hide');
    return;
  }
  if (updateResultShown) {
    const r = updateResultShown;
    el.classList.add('visible', r.ok ? 'done' : 'error');
    title.textContent = r.ok ? T('update.doneTitle', { version: r.version }) : T('update.notInstalledTitle', { version: r.version });
    const text = r.ok ? T('update.doneSub') : (i18nMsgText(r.text) || r.message || '');
    sub.textContent = r.log ? T('update.withLog', { text: text, log: r.log }) : text;
    later.textContent = T('common.ok'); go.classList.add('hide');
    return;
  }
  if (updateAvailable()) {
    el.classList.add('visible');
    title.textContent = T('update.availableVersion', { version: updateInfo.version });
    const note = updateInfo.releaseNotes ? (String(updateInfo.releaseNotes).split(/\r?\n/).find(l => l.trim()) || '') : '';
    sub.textContent = note ? note.slice(0, 120) : T('update.availableSub');
  }
}

// "Later" / "OK": the result goes; an update put off stays off for that version.
document.getElementById('ub-dismiss').addEventListener('click', () => {
  if (updateResultShown && !updateRun) { updateResultShown = null; renderUpdateBanner(); return; }
  if (updateBusy()) return;
  updateRun = null;
  if (updateInfo && updateInfo.version) updateDismiss(updateInfo.version);
  renderUpdateBanner();
});

// "Update now": the app does it all (see above). Open sessions end with it:
// asked first.
async function updateStart() {
  if (updateBusy()) return;
  const n = winTabCount() + (typeof reachDetachedIds !== 'undefined' ? reachDetachedIds.size : 0); // (its terminals and explorers too)
  if (n > 0 && !confirm(T('update.confirmSessions', { count: n }))) return;
  let s;
  try {
    s = await reachNative('reachInstallUpdate');
  } catch (e) {
    if (e === 'unavailable') { updateOpenInstaller(); return; }
    const text = { key: 'update.couldNotStart', params: { error: reachErrText(e) } };
    window.__reachOnUpdate({ state: 'error', percent: -1, seq: updateRun ? (Number(updateRun.seq) || 0) : 0, message: i18nMsgText(text), text: text });
    return;
  }
  if (s && s.state) window.__reachOnUpdate(s);
}
document.getElementById('ub-update').addEventListener('click', updateStart);

// The installer in the browser (no native side, or after a failure): the
// server's download URL, opened by the app's browser launcher (/local/open-url
// only opens the configured server's URLs).
async function updateOpenInstaller() {
  if (!updateInfo || !updateInfo.downloadUrl) return;
  let abs = updateInfo.downloadUrl;
  // (from another server than the active one: that server's address, and the
  // app told which server may be opened)
  const other = typeof updateInfo._pid === 'string' && updateInfo._pid && updateInfo._pid !== (activeServerId || '') ? updateInfo._pid : '';
  if (abs.startsWith('/')) {
    try {
      const prof = other ? serverProfiles.find(x => x.id === other) : null;
      const cfg = prof ? { serverUrl: prof.serverUrl } : await (await fetch('/local/config')).json();
      if (cfg.serverUrl) abs = cfg.serverUrl.replace(/\/$/, '') + abs;
    } catch {}
  }
  try { await fetch('/local/open-url?url=' + encodeURIComponent(abs) + (other ? '&profile=' + encodeURIComponent(other) : '')); } catch {}
}
document.getElementById('ub-manual').addEventListener('click', updateOpenInstaller);

function startUpdateChecker() {
  if (updateCheckInterval) return;
  // After a restart: how the last update went (told once); a reloaded page
  // during an update: where it is.
  reachNative('reachUpdateState').then(s => {
    if (s && s.result) updateResultShown = s.result;
    if (s && s.state && s.state !== 'idle') window.__reachOnUpdate(s);
    renderUpdateBanner();
    if (s && s.result) setStatus(s.result.message || '');
  }).catch(() => {});
  checkForUpdate(); // immediate check on launch
  updateCheckInterval = setInterval(checkForUpdate, 60 * 60 * 1000); // hourly
}

// ── Dashboard page ───────────────────────────────────────────────────────────
function renderDashboardPage(pc) {
  const h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + TH('nav.dashboard') + '</h1><div class="breadcrumb">' + TH('dashboard.welcome') + '</div>';
  pc.appendChild(h);
  const body = document.createElement('div'); body.className = 'page-body';
  // This computer's ID and code, and connecting to another one by code
  // (support_dashboard.go): first, with a server and on a local base alike.
  supDashRender(body);

  // The local base's connections (localconn_tools.go): in the recents and
  // favourites too; a local base alone has no server's figures.
  const dashLocalOnly = serverMode !== 'multi' && activeServerIsLocal();
  const dashHasLocal = dashLocalOnly || (serverMode === 'multi' && serverProfiles.some(p => p.local));
  const kpi = document.createElement('div'); kpi.className = 'kpi-grid';
  if (!dashLocalOnly) body.appendChild(kpi);

  const recPanel = document.createElement('div'); recPanel.className = 'panel';
  recPanel.innerHTML =
    '<div class="panel-header">' + TH('dashboard.recent') + '<div class="panel-actions"><button class="btn-sm" id="clear-recents-btn">' + TH('common.clear') + '</button></div></div>' +
    '<div class="panel-body flush"><div class="recent-list" id="recent-list"></div></div>';
  body.appendChild(recPanel);

  const favPanel = document.createElement('div'); favPanel.className = 'panel';
  favPanel.innerHTML =
    '<div class="panel-header">' + TH('dashboard.favorites') + '</div>' +
    '<div class="panel-body flush"><div class="recent-list" id="fav-list"></div></div>';
  body.appendChild(favPanel);

  pc.appendChild(body);
  const s = dashState = { pc, body, kpi, recList: body.querySelector('#recent-list'), favList: body.querySelector('#fav-list'), localOnly: dashLocalOnly, hasLocal: dashHasLocal };
  body.querySelector('#clear-recents-btn').addEventListener('click', () => {
    recents = []; localStorage.setItem(serverStoreKey('oblireach_recents'), '[]');
    // (multi-server mode: every server's list; the local connections' too)
    if (serverMode === 'multi') for (const p of serverProfiles) { try { localStorage.setItem(serverStoreKeyFor(p.id, 'oblireach_recents'), '[]'); } catch (e) {} }
    if (dashHasLocal) { try { localStorage.setItem(serverStoreKeyFor(LOCAL_PID, 'oblireach_recents'), '[]'); } catch (e) {} }
    dashFill(s); // (in place: the page is never appended a second time)
  });
  dashFill(s);
}
// The Dashboard on screen (renderDashboardPage): its parts, updated in place
// by dashFill at each refresh of the lists (the Support cards above them,
// their fields and focus, are never rebuilt by it).
let dashState = null;
function dashFill(s) {
  const { kpi, recList, favList, hasLocal: dashHasLocal } = s;
  // A server that lists the Obliance agent's tools (_tools): online = usable
  // (ObliReach or the Obliance agent answers), offline = the greyed rows (the
  // Devices "Offline" filter). An older server: ObliReach alone, as before.
  let online = 0, offline = 0, notInstalled = 0, updates = 0, withTools = false;
  const allDevs = [];
  for (const g of overview.groups) for (const d of g.devices) {
    allDevs.push(d);
    if (!d.oblireach?.installed) notInstalled++;
    if (d && d._tools) {
      withTools = true;
      if (devUsable(d)) online++;
      else offline++;
    } else if (d.oblireach?.installed) {
      if (d.oblireach?.online) online++;
      else offline++;
    }
    if (d.oblireach?.updateAvailable) updates++;
  }

  if (dashHasLocal && !localConns.state && !localConns.loading) localConnsLoad().then(() => dashboardRefreshIfShown());
  const localDevs = dashHasLocal && localConns.state === 'ok' ? localDevAll() : [];
  domMorph(kpi,
    '<div class="kpi-card online"><div class="label">' + TH('dashboard.kpi.online') + '</div><div class="value">' + online + '</div><div class="sub">' + TH(withTools ? 'dashboard.kpi.onlineSubTools' : 'dashboard.kpi.onlineSub') + '</div></div>' +
    '<div class="kpi-card offline"><div class="label">' + TH('dashboard.kpi.offline') + '</div><div class="value">' + offline + '</div><div class="sub">' + TH(withTools ? 'dashboard.kpi.offlineSubTools' : 'dashboard.kpi.offlineSub') + '</div></div>' +
    '<div class="kpi-card warn"><div class="label">' + TH('dashboard.kpi.updates') + '</div><div class="value">' + updates + '</div><div class="sub">' + TH('dashboard.kpi.updatesSub') + '</div></div>' +
    '<div class="kpi-card"><div class="label">' + TH('dashboard.kpi.total') + '</div><div class="value">' + allDevs.length + '</div><div class="sub">' + TH('dashboard.kpi.totalSub', { count: notInstalled }) + '</div></div>');
  const dashDevs = allDevs.concat(localDevs);

  recentsReload(); // (sessions opened in the session windows too)
  const recentDevs = [];
  for (const r of dashRecents()) {
    const dev = dashDevs.find(d => String(d.id) === String(r.id) && devPid(d) === r._pid);
    if (dev && !recentDevs.some(x => x.dev === dev)) recentDevs.push({ dev, ts: r.ts });
  }
  dashFillList(recList, recentDevs.slice(0, 8).map(it => ({ dev: it.dev, when: relTime(it.ts || 0) })), 'dashboard.recentEmpty');
  dashFillList(favList, dashDevs.filter(favHas).map(dev => ({ dev,
    when: (dev._tools || devIsLocal(dev)) ? devStateChipHtml(dev) : '<span class="tag-chip ' + (dev.oblireach.online ? 'on' : 'off') + '">' + (dev.oblireach.online ? TH('devices.state.online') : TH('devices.state.offline')) + '</span>' })), 'dashboard.favEmpty');
}
// A Dashboard list (recents, favourites): its rows kept by device, built
// again only when what they show changed; its empty line otherwise.
function dashFillList(list, its, emptyKey) {
  if (!list) return;
  if (!its.length) { domMorph(list, '<div class="recent-empty">' + TH(emptyKey) + '</div>'); return; }
  const launch = reachDetachOn();
  domKeyed(list, its.map(it => {
    const p = dashDeviceRowParts(it.dev, it.when);
    return { key: devKey(it.dev), sig: p.cls + '\u0001' + launch + '\u0001' + p.html, data: it.dev, build: () => dashDeviceRow(it.dev, it.when, p) };
  }));
}

// The recents the Dashboard lists, most recent first, each with its server
// (_pid): the active server's; every server's in multi-server mode.
function dashRecents() {
  // (a local base: its connections' recents, LOCAL_PID's list)
  const ofPid = pid => recentsFor(pid).map(r => Object.assign({}, r, { _pid: pid }));
  if (serverMode !== 'multi') return activeServerIsLocal() ? ofPid(LOCAL_PID) : recents.map(r => Object.assign({}, r, { _pid: activeServerId || '' }));
  const all = [];
  for (const p of serverProfiles) all.push(...ofPid(p.local ? LOCAL_PID : p.id));
  return all.sort((a, b) => (Number(b.ts) || 0) - (Number(a.ts) || 0));
}
// Multi-server mode: a device's server, as a dot in its colour (its name in
// the tooltip).
function devSrvDot(dev) {
  if (serverMode !== 'multi' || !dev) return '';
  const pid = devPid(dev);
  return '<span class="dev-srv" style="background:' + srvColor(pid) + '" title="' + esc(srvName(pid)) + '"></span>';
}
// Multi-server mode: the pages of the active server only (Logs, Settings) say which.
function activeServerNote() {
  if (serverMode !== 'multi' || !activeServerId) return '';
  return ' <span class="dev-head-srv"><span class="srv-dot" style="background:' + srvColor(activeServerId) + '"></span>' + esc(srvName(activeServerId)) + '</span>';
}

// A device of the Dashboard (recents, favourites; when: the HTML of its right
// column). A click opens it like a row of the Devices list (devicesOpen): in
// detached mode it launches its session in its own window, and the row then
// has the list's ▶ (its page here) and i (its information); otherwise it
// opens its page, as before.
function dashDeviceRowParts(dev, when) {
  const online = devUsable(dev); // ObliReach, or the Obliance agent (agent_tools.go)
  const local = devIsLocal(dev); // (a local connection: its type, its host answering)
  const cls = 'recent-item' + (online ? '' : ' dev-offline'); // greyed, not its i
  const html = devSrvDot(dev) + '<span class="dot ' + (local ? devDotClass(dev) : online ? (devReachOnline(dev) ? 'online' : 'agent') : 'warn') + '"></span><span class="rname">' + esc(dev.hostname) + '</span>' + devPrivacyIcon(dev) + devLastUserHtml(dev) +
    (local ? '<span class="tag-chip ' + (cxnIsSsh(dev) ? 'linux' : 'win') + '">' + cxnTypeName(dev) + '</span>'
      : '<span class="tag-chip ' + osTag(dev.osType) + '">' + esc(dev.osType || '?') + '</span>') +
    '<span class="rwhen">' + when + '</span>';
  return { cls, html, online };
}
// Its listeners read the device it shows now (row._domData: the row is kept
// across refreshes by dashFillList).
function dashDeviceRow(dev, when, parts) {
  const p = parts || dashDeviceRowParts(dev, when), online = p.online;
  const row = document.createElement('div'); row.className = p.cls;
  row.innerHTML = p.html;
  row._domData = dev;
  if (reachDetachOn()) {
    row.title = T('devices.rowLaunchTitle');
    const acts = document.createElement('span'); acts.className = 'recent-acts';
    [['remote', T('devices.openPageHere')], ['info', T('common.info')]].forEach(([act, title]) => {
      const b = document.createElement('button');
      b.className = 'icon-btn' + (act === 'remote' ? ' primary' : '');
      b.title = title; b.dataset.act = act; b.innerHTML = DEV_ROW_ICONS[act];
      b.disabled = act === 'remote' && !online; // as in the Devices list
      acts.appendChild(b);
    });
    row.appendChild(acts);
  }
  row.addEventListener('click', e => {
    const d = row._domData || dev;
    const btn = e && e.target && typeof e.target.closest === 'function' ? e.target.closest('button[data-act]') : null;
    if (btn) {
      if (typeof e.stopPropagation === 'function') e.stopPropagation();
      if (!btn.disabled) devicesOpen(d, btn.dataset.act);
      return;
    }
    devicesOpen(d);
  });
  return row;
}

function relTime(ts) {
  if (!ts) return '';
  const diff = Date.now() - ts;
  const m = Math.round(diff / 60000);
  if (m < 1) return T('time.justNow');
  if (m < 60) return T('time.minutesAgo', { count: m });
  const h = Math.round(m / 60);
  if (h < 24) return T('time.hoursAgo', { count: h });
  return T('time.daysAgo', { count: Math.round(h / 24) });
}

function osTag(osType) {
  const s = String(osType || '').toLowerCase();
  if (s.includes('windows') || s.includes('win')) return 'win';
  if (s.includes('linux')) return 'linux';
  if (s.includes('mac') || s.includes('darwin')) return 'mac';
  return 'off';
}

// ── Devices page (Rustdesk-style table) ──────────────────────────────────────
let devicesFilter = { q: '', online: '', os: '' };
function renderDevicesPage(pc) {
  const h = document.createElement('div'); h.className = 'page-header';
  // A local base on screen (single mode) or in "All servers": its
  // management with the page's actions (new folder, new connection, import,
  // export: in the base's root; a folder's row adds in it).
  const localOnly = serverMode !== 'multi' && activeServerIsLocal();
  const hasLocal = localOnly || (serverMode === 'multi' && serverProfiles.some(p => p.local));
  const lbtn = (id, key, cls) => '<button class="' + (cls || 'btn-sm') + '" id="' + id + '">' + TH(key) + '</button>';
  const manage = hasLocal && !reachDetachedMode ? lbtn('local-new-folder', 'connections.newFolder') + lbtn('local-new-conn', 'connections.newConnection') +
    (typeof cimpAvailable === 'function' && cimpAvailable() ? lbtn('local-import', 'connImport.import') + lbtn('local-export', 'connImport.export') : '') : '';
  h.innerHTML = '<h1>' + TH('nav.devices') + '</h1><div class="breadcrumb">' + TH('devices.inventory') + (serverMode === 'multi' ? ' \u00B7 ' + esc(multiFilterLabel()) : localOnly ? activeServerNote() : '') + '</div>' +
    '<div class="page-actions">' + manage + '<button class="btn-sm" id="devices-refresh">' + TH('common.refresh') + '</button></div>';
  pc.appendChild(h);
  const hb = (id, fn) => { const b = h.querySelector('#' + id); if (b) b.addEventListener('click', fn); };
  hb('local-new-folder', () => cxnOpenFolderModal(null, localConnsChanged, ''));
  hb('local-new-conn', () => cxnOpenConnectionModal(null, localConnsChanged, ''));
  hb('local-import', () => cimpOpenSources('', localConnsChanged));
  hb('local-export', () => cimpOpenExport());
  supAddEntry(h); // (connect by code: support_page.go)
  const body = document.createElement('div'); body.className = 'page-body';
  const bar = document.createElement('div'); bar.className = 'filter-bar';
  bar.innerHTML =
    '<input id="dq" style="min-width:220px" placeholder="' + TH('devices.searchPlaceholder') + '" value="' + esc(devicesFilter.q) + '"/>' +
    '<select id="dq-online"><option value="">' + TH('devices.filter.allStates') + '</option><option value="online">' + TH('devices.filter.online') + '</option><option value="offline">' + TH('devices.filter.offline') + '</option></select>' +
    '<select id="dq-os"><option value="">' + TH('devices.filter.allOs') + '</option>' +
    (localOnly ? '' : '<option value="windows">Windows</option><option value="linux">Linux</option><option value="macos">macOS</option>') +
    (hasLocal ? '<option value="ssh">SSH</option><option value="rdp">RDP</option>' : '') + '</select>' +
    '<span class="spacer"></span>' +
    '<span class="muted-hint"><span id="dev-count">0</span> <span id="dev-count-label">' + TH('devices.resultsWord', { count: 0 }) + '</span></span>';
  body.appendChild(bar);
  const panel = document.createElement('div'); panel.className = 'panel';
  panel.innerHTML =
    '<div class="panel-body flush" style="overflow-x:auto"><table class="dt" id="devices-table">' +
    '<thead>' + devTableHead() + '</thead>' +
    '<tbody id="devices-tbody"></tbody></table></div>';
  body.appendChild(panel);
  if (dsOn()) { body.appendChild(dsBarEl()); dsWireTbody(document.getElementById('devices-tbody')); } // (several devices ticked: devices_select.go)
  pc.appendChild(body);

  bar.querySelector('#dq').addEventListener('input', e => devicesSetQuery(e.target.value));
  bar.querySelector('#dq-online').value = devicesFilter.online;
  bar.querySelector('#dq-online').addEventListener('change', e => { devicesFilter.online = e.target.value; refreshDevicesTable(); });
  bar.querySelector('#dq-os').value = devicesFilter.os;
  bar.querySelector('#dq-os').addEventListener('change', e => { devicesFilter.os = e.target.value; refreshDevicesTable(); });
  document.getElementById('devices-refresh').addEventListener('click', async () => {
    if (hasLocal) { await localConnsLoad(); localDevProbe(true); }
    if (!localOnly) await loadOverview();
    refreshDevicesTable();
  });

  refreshDevicesTable();
  // A local base: its connections read again (the vault locked or unlocked,
  // edited in another list meanwhile).
  if (hasLocal) localConnsLoad().then(() => refreshDevicesTable());
}

// ── Device rows: last user, offline ──────────────────────────────────────────
// The last user seen logged in on the machine (GET /api/reach/overview
// lastLoggedInUser = Obliance devices.last_logged_in_user; absent from an
// older server): as received (domain accounts, user@domain...), trimmed; '' when
// none. Reported by the machine's agent: untrusted text, it only ever reaches
// the page through esc() (text and attribute alike) or textContent.
function devLastUser(d) {
  const v = d && d.lastLoggedInUser;
  return typeof v === 'string' ? v.trim() : '';
}
// The last user next to a device name in a list (Devices, "+" list,
// Dashboard): muted, cut with an ellipsis, the whole value in its tooltip
// ("Last user: ...", devices.lastUserTip). Nothing when there is none.
function devLastUserHtml(d) {
  const v = devLastUser(d);
  return v ? '<span class="dev-user" title="' + TH('devices.lastUserTip', { user: v }) + '">' + esc(v) + '</span>' : '';
}
// The header row of the device tables (Devices page, "+" list): its texts
// carry their keys, so a language switch redraws them (i18nApply).
function devTableHead() {
  const th = (key, style) => '<th' + (style ? ' style="' + style + '"' : '') + (key ? ' data-i18n="' + key + '">' + TH(key) : '>') + '</th>';
  return '<tr>' + th('', 'width:40px') + th('devices.col.hostname') + th('devices.col.os') + th('devices.col.agent') + th('devices.col.status') +
    th('devices.col.sessions') + th('devices.col.actions', 'text-align:end') + '</tr>'; // (the side of the buttons, also right-to-left)
}
// The Obliance agent's status of a device (dev.status), in words; an unknown
// one as it came.
function devStatusText(s) {
  const k = 'devices.agentStatus.' + String(s || '');
  const t = T(k);
  return t === k ? String(s || '') : t;
}
// Offline, for the Desktop: the machine's ObliReach agent is not reachable
// (oblireach.online false: neither connected to the server's ObliReach hub
// nor seen within 2 minutes, reach.routes.ts; or not installed). That is what
// already says "offline" in the Status column and disables ▶ / Chat / Connect,
// since sessions and chat go through that agent: its row is greyed (CSS
// .dev-offline), its (i) stays as it is. The Obliance agent's own status
// (dev.status: online, warning, critical, updating = reachable; offline,
// update_error = not; pending, suspended) plays no part, as before: it is
// another service, a machine whose ObliReach agent answers can be reached
// with it down, one whose ObliReach agent is down cannot, whatever it says.
// Rows follow each reload of the overview (the lists are drawn from it).
function devIsOffline(d) { return !(d && d.oblireach && d.oblireach.online); }

// Search box: hostname, last user, or the group's path (on the master it
// starts with the tenant name), so a tenant or a group name lists its
// machines. Case-insensitive, anywhere in the text.
function devicesFilterMatch(d, g) { return devFilterMatch(devicesFilter, d, g); }
// f: a list's filters { q, online, os } (the Devices page's, a "+" list's).
function devFilterMatch(f, d, g) {
  if (typeof f.only === 'function' && !f.only(d, g)) return false; // (a list bounded to a place: the vault's machine picker)
  if (devIsLocal(d)) return localDevMatch(f, d, g);
  const q = String(f.q || '').trim().toLowerCase();
  if (q && !String(d.hostname || '').toLowerCase().includes(q) &&
      !devLastUser(d).toLowerCase().includes(q) &&
      !String((g && g.name) || '').toLowerCase().includes(q) &&
      !devTreeGroupPath(g).toLowerCase().includes(q) && // "Ungrouped" as it is shown ("Sans groupe"...)
      !String((g && g.tenantName) || '').toLowerCase().includes(q) &&
      !(serverMode === 'multi' && g && srvName(g._pid).toLowerCase().includes(q)) && // (its server's name)
      !(typeof f.qMore === 'function' && f.qMore(d, q))) return false; // (a list's own: the "+" list's VMs, vms_page.go dpVmQMore)
  // Online: ObliReach, or the Obliance agent (devUsable: the greyed rows are
  // exactly the offline ones). reachOnly (the Chat page): ObliReach, and its
  // devices only, as before. filesOnly (the Files page, files_page.go): the
  // devices whose explorer this user may use (filesListed), online when it
  // opens now (filesUsable: the others are its greyed rows).
  if (f.reachOnly && d._tools && !d.oblireach?.installed) return false;
  if (f.filesOnly && !filesListed(d)) return false;
  const on = f.reachOnly ? !devIsOffline(d) : f.filesOnly ? filesUsable(d) : devUsable(d);
  if (f.online === 'online' && !on) return false;
  if (f.online === 'offline' && on) return false;
  if (f.os) {
    const s = String(d.osType || '').toLowerCase();
    if (f.os === 'windows' && !(s.includes('windows') || s.includes('win'))) return false;
    if (f.os === 'linux' && !s.includes('linux')) return false;
    if (f.os === 'macos' && !(s.includes('mac') || s.includes('darwin'))) return false;
  }
  return true;
}

// A local connection against a list's filters: the search in its name,
// host, user, folder path or type; the state (its host answering); the
// "OS" (its type: SSH / RDP, no OS known; a real OS: none of them).
function localDevMatch(f, d, g) {
  const q = String(f.q || '').trim().toLowerCase();
  const n = d._node || {};
  if (q && ![d.hostname, n.host, n.username, g && g.name, cxnTypeName(d), serverMode === 'multi' ? srvName(LOCAL_PID) : ''].some(v => String(v || '').toLowerCase().includes(q))) return false;
  if (f.filesOnly && !filesListed(d)) return false;
  if (f.reachOnly) return false; // (the Chat page: ObliReach devices only)
  if (f.online === 'online' && d._reach !== true) return false;
  if (f.online === 'offline' && d._reach !== false) return false;
  if (f.os) {
    if (f.os === 'ssh') return cxnIsSsh(d);
    if (f.os === 'rdp') return !cxnIsSsh(d);
    return false;
  }
  return true;
}

// ── Devices tree: Obliance's tenants and device groups ──────────────────────
// GET /api/reach/overview lists the groups in Obliance's order: tenants
// (master god view: tenantName on each group) Default first then by name,
// inside a tenant the group tree depth-first (a group, then its sub-groups,
// each with its depth), "Ungrouped" last. An older server sends a flat list
// (name only): flat groups. Everything is expanded by default; the folded
// tenants / groups are remembered per server, like the favourites.
let devTreeStore = { key: null, folded: new Set() };
let devTreeSearchFolded = new Set(); // folding while a search is typed (not saved)
const DEV_TREE_INDENT = 16, DEV_TREE_MAX_DEPTH = 6;

function devTreeGroupDepth(g) {
  const n = Number(g && g.depth);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0;
}
function devTreeGroupLabel(g) {
  if (devTreeIsUngrouped(g)) return T('devices.ungrouped'); // (the server names it in English)
  return (typeof g.groupName === 'string' && g.groupName) ? g.groupName : String(g.name || T('devices.ungrouped'));
}
// A group's path as the tree shows it (header tooltip, search): the server's
// ("Tenant / Parent / Group"), with "Ungrouped" (named in English by the
// server) in the language on screen.
function devTreeGroupPath(g) {
  const p = String((g && g.name) || '');
  return g && devTreeIsUngrouped(g) ? p.replace(/(^|\/ )Ungrouped$/, (m, pre) => pre + T('devices.ungrouped')) || T('devices.ungrouped') : p;
}
function devTreeGroupKey(g) {
  const t = (g.tenantId !== undefined && g.tenantId !== null) ? String(g.tenantId) : '';
  const id = (g.id !== undefined && g.id !== null) ? String(g.id) : 'n:' + String(g.name || '');
  return 'g:' + t + ':' + id;
}
function devTreeIsUngrouped(g) {
  if (g && g._pid === LOCAL_PID) return false; // (a local base's folder)
  return typeof g.id === 'number' ? g.id <= 0 : (g.id === undefined || g.id === null) && g.name === 'Ungrouped';
}

// The rows of the Devices table, from the overview groups (in the server's
// order). match(dev, group) filters the devices: a group is listed when it
// or one of its sub-groups holds a match, a tenant when one of its groups
// does. folded(key) says whether a tenant / group is folded. Like Obliance,
// the sub-groups of a group come before its own devices.
// Returns { rows: [{kind: 'tenant'|'group'|'device', ...}], total: matches,
// devs: every match }; a tenant / group row's devs: every match under it
// (folded or not: the "+" list's boxes, picker_multi.go).
// opts (optional): { keepEmpty } a group with no device listed anyway (a
// local base's empty folder, where a connection can be added). A group
// marked _flat (a local base's root connections) has no row of its own.
function devTreeRows(groups, match, folded, opts) {
  const keepEmpty = !!(opts && opts.keepEmpty);
  const rows = [], allDevs = [];
  let total = 0;
  const buckets = [], byTenant = new Map();
  for (const g of Array.isArray(groups) ? groups : []) {
    if (!g) continue;
    const header = typeof g.tenantName === 'string' && g.tenantName !== '' && g.tenantId !== undefined && g.tenantId !== null;
    const bk = header ? 't:' + g.tenantId : '';
    let b = byTenant.get(bk);
    if (!b) { b = { key: bk, name: header ? g.tenantName : '', header, groups: [] }; byTenant.set(bk, b); buckets.push(b); }
    b.groups.push(g);
  }
  for (const b of buckets) {
    // Matches of each group, counted in each of its ancestors too.
    const items = [], stack = [], bDevs = [];
    let count = 0;
    for (const g of b.groups) {
      const depth = devTreeGroupDepth(g);
      while (stack.length && stack[stack.length - 1].depth >= depth) stack.pop();
      const devs = (Array.isArray(g.devices) ? g.devices : []).filter(d => d && match(d, g));
      const it = { g, depth, devs, all: devs.slice(), count: devs.length, key: devTreeGroupKey(g), shown: false, folded: false };
      for (const a of stack) { a.count += devs.length; a.all.push(...devs); }
      stack.push(it); items.push(it);
      count += devs.length;
      bDevs.push(...devs);
    }
    total += count;
    allDevs.push(...bDevs);
    if (!count && !keepEmpty) continue;
    const bFolded = b.header && !!folded(b.key);
    if (b.header) rows.push({ kind: 'tenant', key: b.key, name: b.name, count, folded: bFolded, devs: bDevs });
    if (bFolded) continue;
    // A group's own devices go out once its sub-groups are listed.
    const open = [];
    const close = (depth) => {
      while (open.length && open[open.length - 1].depth >= depth) {
        const it = open.pop();
        if (it.shown && !it.folded) for (const d of it.devs) rows.push({ kind: 'device', dev: d, depth: it.depth + 1, group: it.g });
      }
    };
    for (const it of items) {
      close(it.depth);
      if (!it.count && !keepEmpty) continue; // nothing matches in it nor below it
      const parent = open.length ? open[open.length - 1] : null;
      it.shown = !parent || (parent.shown && !parent.folded);
      it.folded = !it.g._flat && !!folded(it.key);
      if (it.g._flat) it.depth = -1; // (its devices at the top level)
      else if (it.shown) rows.push({ kind: 'group', key: it.key, name: devTreeGroupLabel(it.g), path: devTreeGroupPath(it.g),
        depth: it.depth, count: it.count, folded: it.folded, ungrouped: devTreeIsUngrouped(it.g), devs: it.all,
        lfolder: it.g._folder || null }); // (a local base's folder: its management, localFolderButtons)
      open.push(it);
    }
    close(-1);
  }
  return { rows, total, devs: allDevs };
}

// Saved folding of the server on screen (read again after a server switch);
// multi-server mode has its own (its servers, then their tenants and groups).
function devTreeSaved() {
  const k = serverMode === 'multi' ? 'oblireach_devtree_folded:multi' : serverStoreKey('oblireach_devtree_folded');
  if (devTreeStore.key !== k) {
    let arr = [];
    try { arr = JSON.parse(localStorage.getItem(k) || '[]'); } catch (e) { arr = []; }
    devTreeStore = { key: k, folded: new Set(Array.isArray(arr) ? arr.filter(x => typeof x === 'string') : []) };
  }
  return devTreeStore.folded;
}
// A list of devices drawn like the Devices table: the Devices page's, the
// "+" list of a session window (devPicker*) and the Chat page's device picker
// (chatPicker*): same rows, same tree, each its own filters. view: { tbodyId,
// countId (or tbodyEl() / countEl() / countLabelEl(): elements without an
// id, the Chat page's), filter() (its { q, online, os }), searchFolded()
// (what is folded during a search), actions (row buttons: [key, title]),
// actDisabled(dev, act) (optional: which buttons are off; by default ▶ and
// Chat of an offline machine), primary (optional: the button drawn in the
// accent besides ▶), rowTitle (tooltip of a device row, optional),
// toolButtons(dev, act) (optional: the buttons of 'tools' / 'files'; by
// default toolsRowButtons; the Files page's picker: files_page.go),
// refresh(), open(dev, act) (act: a row button, undefined for the row
// itself) }. The saved folding is the server's, shared by all.
function devViewMain() {
  const launch = reachDetachOn();
  // 'tools' / 'files': the Obliance agent's terminals and explorer
  // (agent_tools.go toolsRowButtons; nothing with an older server).
  return { tbodyId: 'devices-tbody', countId: 'dev-count', filter: () => devicesFilter, searchFolded: () => devTreeSearchFolded,
    actions: [['remote', launch ? T('devices.openPageHere') : T('common.remote')], ['tools', ''], ['files', ''], ['chat', T('common.chat')], ['info', T('common.info')]],
    remoteTitleAgent: launch ? '' : T('tools.openPage'),
    rowTitle: launch ? T('devices.rowLaunchTitle') : '', refresh: refreshDevicesTable, open: devicesOpen,
    localConns: true, // (the local bases' connections too: localTreeRows)
    manage: true,     // (... with their management: edit, delete, folders)
    // Boxes to tick several devices, its bar (devices_select.go).
    select: dsOn() ? dsSelectHooks : null, onTree: dsOn() ? dsOnTree : null };
}
// A device of the main window's lists (Devices, Dashboard): act is a row
// button, undefined for the row itself. In detached mode ("Detach by
// default", reachDetachOn) the row launches its session in a session window
// and the main window stays on its list (reachLaunchDevice); its ▶ (remote)
// opens its page here, as before, its i its information. Otherwise the row
// opens its page, as before.
function devicesOpen(dev, act) {
  if (devIsLocal(dev)) return localDevOpen(dev, act); // (a local connection: localconn_tools.go)
  // A tool button (CMD / PS / SSH): here, or in a session window with
  // "Detach by default" ('auto'); Files: the Files page (files_page.go).
  if (toolsIsKind(act)) return toolOpen(dev, act, 'auto');
  if (act === 'remote') openDeviceDetail(dev, 'remote');
  else if (act === 'chat') { openDeviceDetail(dev); chatOpenFor(dev); }
  else if (act === 'info') openDeviceDetail(dev, 'info');
  else if (reachDetachOn()) return devLaunch(dev); // (Reach as before; else the agent's tools)
  else openDeviceDetail(dev);
}
function devViewSearching(view) { return String(view.filter().q || '').trim() !== ''; }
function devTreeSearching() { return devViewSearching(devViewMain()); }
// A search opens every folded tenant / group holding a match; what is folded
// during the search is forgotten with it, the saved folding comes back.
function devViewFolded(view, key) {
  return devViewSearching(view) ? view.searchFolded().has(key) : devTreeSaved().has(key);
}
function devTreeFolded(key) { return devViewFolded(devViewMain(), key); }
function devViewToggle(view, key) {
  if (devViewSearching(view)) {
    const f = view.searchFolded();
    if (!f.delete(key)) f.add(key);
    return;
  }
  const s = devTreeSaved();
  if (!s.delete(key)) {
    s.add(key);
    while (s.size > 2000) s.delete(s.values().next().value); // oldest first
  }
  try { localStorage.setItem(devTreeStore.key, JSON.stringify([...s])); } catch (e) {}
}
function devTreeToggle(key) { devViewToggle(devViewMain(), key); }
function devicesSetQuery(v) {
  devicesFilter.q = v;
  devTreeSearchFolded = new Set();
  refreshDevicesTable();
}

const DEV_TREE_ICONS = {
  chev: '<svg class="tree-chev" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="6 9 12 15 18 9"/></svg>',
  tenant: '<svg class="tree-ico" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 22V4a2 2 0 012-2h8a2 2 0 012 2v18z"/><path d="M6 12H4a2 2 0 00-2 2v6a2 2 0 002 2h2"/><path d="M18 9h2a2 2 0 012 2v9a2 2 0 01-2 2h-2"/><path d="M10 6h4M10 10h4M10 14h4M10 18h4"/></svg>',
  group: '<svg class="tree-ico" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/></svg>',
  ungrouped: '<svg class="tree-ico off" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/><line x1="9.5" y1="11.5" x2="14.5" y2="16.5"/><line x1="14.5" y1="11.5" x2="9.5" y2="16.5"/></svg>',
};

// ── The local base's connections: devices of the lists (localconn_tools.go) ──
// A local base (servers.go Kind "local") is no Obliance server: its devices
// are the vault's direct SSH / RDP connections (the same tree for every
// local base: vault.go ParentID), read from the app (reachVaultState /
// reachVaultByParent), never from a server, never a "Sign in". They are
// listed EXACTLY like a server's devices (the same table, rows, search,
// filters, tree: devTreeRows): each folder is a group, a connection a
// device (cxnDevice: SSH = its terminal and files, RDP = its view). In
// single mode on the base, its Devices and Files pages list them; in "All
// servers", its section of those lists; in a session window, its "+" list.
// localConns.state: '' (not read yet), 'ok', 'locked', 'absent', 'unreadable'.
let localConns = { state: '', nodes: [], seq: 0, loading: false };
const LOCAL_CONNS_MAX = 2000, LOCAL_CONNS_DEPTH = 16;
async function localConnsLoad() {
  const mine = ++localConns.seq;
  localConns.loading = true;
  let state = 'unreadable', nodes = [];
  try {
    const st = (typeof vaultSyncCall === 'function' && typeof window.reachVaultByParent === 'function' && reachNativeToken())
      ? await vaultSyncCall('reachVaultState') : null;
    state = st && typeof st.state === 'string' ? (st.state === 'unlocked' ? 'ok' : st.state) : 'unreadable';
    if (state === 'ok') nodes = await localConnsWalk();
  } catch (e) { state = 'unreadable'; nodes = []; }
  if (mine !== localConns.seq) return localConns;
  localConns = { state, nodes, seq: mine, loading: false };
  localDevProbe(); // (their hosts answering or not: the Status column)
  return localConns;
}
// The whole tree, folder by folder (breadth first, bounded): [{ n, parent }].
async function localConnsWalk() {
  const out = [], seen = new Set();
  let level = [''];
  for (let depth = 0; level.length && depth < LOCAL_CONNS_DEPTH && out.length < LOCAL_CONNS_MAX; depth++) {
    const kids = await Promise.all(level.map(pid => vaultSyncCall('reachVaultByParent', pid).then(r => ({ pid, r }), () => ({ pid, r: null }))));
    const next = [];
    for (const { pid, r } of kids) {
      for (const n of Array.isArray(r) ? r : []) {
        if (!n || typeof n !== 'object' || typeof n.id !== 'string' || seen.has(n.id) || out.length >= LOCAL_CONNS_MAX) continue;
        seen.add(n.id);
        out.push({ n, parent: pid });
        if (n.kind === CXN_KIND_FOLDER) next.push(n.id);
      }
    }
    level = next;
  }
  return out;
}
// The connections as overview groups (devTreeRows reads them): each folder,
// depth first, a group (its path as name, its label as groupName), then the
// connections of the root (a group without a header row: _flat). The
// devices keep their identity across loads (the probe's answers on them).
let localDevCache = { seq: -1, groups: [], byId: new Map() };
function localDevGroups() {
  if (localDevCache.seq === localConns.seq) return localDevCache.groups;
  const nodes = localConns.state === 'ok' ? localConns.nodes : [];
  const byParent = new Map(), old = localDevCache.byId, byId = new Map(), groups = [];
  for (const x of nodes) { if (!byParent.has(x.parent)) byParent.set(x.parent, []); byParent.get(x.parent).push(x.n); }
  const devOf = (n) => {
    const d = cxnDevice(n), was = old.get(d.id);
    if (was && was._node.host === d._node.host && was._node.port === d._node.port) { d._reach = was._reach; d._reachAt = was._reachAt; }
    byId.set(d.id, d);
    return d;
  };
  const sortConns = (a, b) => String(a.label || '').localeCompare(String(b.label || ''));
  const walk = (pid, depth, path) => {
    for (const n of (byParent.get(pid) || []).filter(n => n.kind === CXN_KIND_FOLDER)) {
      const p = path ? path + ' / ' + n.label : String(n.label || '');
      const devs = (byParent.get(n.id) || []).filter(c => c.kind !== CXN_KIND_FOLDER).sort(sortConns).map(devOf);
      groups.push({ id: 'f:' + n.id, _pid: LOCAL_PID, _folder: n, name: p, groupName: String(n.label || ''), depth, devices: devs });
      if (depth < LOCAL_CONNS_DEPTH) walk(n.id, depth + 1, p);
    }
  };
  walk('', 0, '');
  const roots = (byParent.get('') || []).filter(c => c.kind !== CXN_KIND_FOLDER).sort(sortConns).map(devOf);
  if (roots.length) groups.push({ id: 'f:', _pid: LOCAL_PID, _flat: true, name: '', groupName: '', depth: 0, devices: roots });
  localDevCache = { seq: localConns.seq, groups, byId };
  return groups;
}
function localDevFind(id) { localDevGroups(); return localDevCache.byId.get(String(id)) || null; }
function localDevAll() { localDevGroups(); return Array.from(localDevCache.byId.values()); }
// Its hosts answering (a TCP connection to host:port, from this computer:
// reachConnProbe), once a minute at most each: the Status column, the
// greyed rows and the "Online" / "Offline" filter. Unknown until answered.
const LOCAL_PROBE_MS = 60000;
let localProbing = false;
async function localDevProbe(force) {
  if (localProbing || typeof window.reachConnProbe !== 'function' || !reachNativeToken()) return;
  const now = Date.now();
  const due = localDevAll().filter(d => force || !d._reachAt || now - d._reachAt > LOCAL_PROBE_MS);
  if (!due.length) return;
  localProbing = true;
  try {
    const res = await vaultAsyncCallWithin(30000, 'reachConnProbe', [JSON.stringify(due.map(d => d.id).slice(0, 500))]);
    const at = Date.now();
    for (const d of due) if (res && Object.prototype.hasOwnProperty.call(res, d.id)) { d._reach = res[d.id] === true; d._reachAt = at; }
  } catch (e) {} finally { localProbing = false; }
  overviewRedraw();
}
// The rows of a local base's list (devTreeRows on its groups). Empty folders
// show while nothing filters (a connection goes into them from their row).
function localTreeRows(view, f, keyPrefix) {
  const match = (d, g) => devFilterMatch(f, d, g);
  const plain = !devViewSearching(view) && !f.online && !f.os;
  return devTreeRows(localDevGroups(), match, key => devViewFolded(view, (keyPrefix || '') + key), { keepEmpty: plain && !!view.manage });
}
// "Unlock" of a local base: the vault's own form (Windows Hello included), in
// a dialog; its connections then fill the lists. "Create": its first code.
function localConnsUnlock() {
  cxnUnlockThen(() => { localConnsLoad().then(() => overviewRedraw()); });
}
function localVaultCreate() {
  const m = scriptsBaseModal('vault.create.title');
  const wrap = document.createElement('div'); wrap.className = 'vault-form';
  m.box.appendChild(wrap);
  vaultMenuCreateForm(wrap, null, () => { m.close(); localConnsLoad().then(() => overviewRedraw()); });
}
// The local base changed (a connection or folder saved, deleted, imported):
// read again, every list follows.
function localConnsChanged() { localConnsLoad().then(() => overviewRedraw()); }
// A local connection of the main lists: its row (▶: SSH its terminal, RDP
// its view), its tool buttons, its management (edit / delete).
function localDevOpen(dev, act) {
  if (toolsIsKind(act)) return toolOpen(dev, act, cxnWhere());
  if (act === 'info') { openDeviceDetail(dev, 'info'); return 'none'; } // (its Info panel: localconn_info.go)
  if (act === 'edit') { cxnOpenConnectionModal(dev._node, localConnsChanged); return 'none'; }
  if (act === 'delete') { cxnConfirmDelete(dev._node, localConnsChanged); return 'none'; }
  return cxnOpenNode(dev._node, cxnWhere());
}
// The row buttons of a local connection (devViewDeviceRow), in the view's
// order: ▶ (connect), its tools, then edit / delete where the list manages.
const LOCAL_ROW_ICONS = {
  edit: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 013 3L7 19l-4 1 1-4z"/></svg>',
  delete: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 01-2 2H8a2 2 0 01-2-2L5 6"/><path d="M10 11v6M14 11v6"/><path d="M9 6V4a1 1 0 011-1h4a1 1 0 011 1v2"/></svg>',
  add: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>',
};
function localRowButtons(dev, view) {
  const online = devUsable(dev);
  let out = '';
  for (const [act, title] of view.actions) {
    if (act === 'tools' || act === 'files') { out += view.toolButtons ? view.toolButtons(dev, act) : toolsRowButtons(dev, act); continue; }
    if (act === 'remote') {
      out += '<button class="icon-btn primary" title="' + esc(T(cxnIsSsh(dev) ? 'devices.local.openSsh' : 'devices.local.openRdp')) + '" data-act="remote"' + (online ? '' : ' disabled') + '>' + DEV_ROW_ICONS.remote + '</button>';
    }
  }
  if (view.manage && !reachDetachedMode) {
    out += '<button class="icon-btn" title="' + TH('connections.editConnection') + '" data-act="edit">' + LOCAL_ROW_ICONS.edit + '</button>';
    out += '<button class="icon-btn" title="' + TH('connections.deleteConnection') + '" data-act="delete">' + LOCAL_ROW_ICONS.delete + '</button>';
  }
  return out;
}
// A folder's row (devTreeHeaderRow) in a list that manages: a connection in
// it, its name, deleting it.
function localFolderButtons(row, view) {
  if (!row.lfolder || !view.manage || reachDetachedMode) return '';
  return '<span class="spacer"></span>' +
    '<button class="icon-btn" title="' + TH('connections.newConnection') + '" data-lfact="add">' + LOCAL_ROW_ICONS.add + '</button>' +
    '<button class="icon-btn" title="' + TH('connections.editFolder') + '" data-lfact="edit">' + LOCAL_ROW_ICONS.edit + '</button>' +
    '<button class="icon-btn" title="' + TH('connections.deleteFolder') + '" data-lfact="delete">' + LOCAL_ROW_ICONS.delete + '</button>';
}
function localFolderAct(folder, act) {
  if (!folder) return;
  if (act === 'add') cxnOpenConnectionModal(null, localConnsChanged, folder.id);
  else if (act === 'edit') cxnOpenFolderModal(folder, localConnsChanged);
  else if (act === 'delete') cxnConfirmDelete(folder, localConnsChanged);
}
// What a local base's list says when it lists nothing: locked (its unlock),
// no vault yet (its creation), unreadable, loading.
function localStateRow(tbody) {
  const st = localConns.state;
  const tr = document.createElement('tr');
  const td = document.createElement('td'); td.colSpan = 7; td.style.cssText = 'text-align:center;padding:30px;color:var(--muted2)';
  const txt = document.createElement('div');
  txt.textContent = st === 'locked' ? T('devices.local.locked') : st === 'absent' ? T('devices.local.absentHere') : st === 'unreadable' ? T('vault.locked.unreadable') : T('common.loading');
  td.appendChild(txt);
  if (st === 'locked' || st === 'absent') {
    const b = document.createElement('button'); b.className = 'btn-sm'; b.style.marginTop = '12px'; b.id = 'local-state-act';
    b.textContent = T(st === 'locked' ? 'vault.locked.submit' : 'devices.local.create');
    b.addEventListener('click', () => { if (st === 'locked') localConnsUnlock(); else localVaultCreate(); });
    td.appendChild(b);
  }
  tr.appendChild(td);
  if (tbody) tbody.appendChild(tr);
  return tr;
}

// Multi-server mode: the rows of every server (the tenant menu's choice),
// each under a large separator (its name and colour, its count, and why it
// shows nothing: signed out with its "Sign in", unreachable with "Try
// again", loading; an older server: its current tenant only), then its own
// tree (devTreeRows). During a search, a server with no match is left out.
function multiTreeRows(view, f) {
  const rows = [], allDevs = [];
  let total = 0;
  const searching = devViewSearching(view);
  const match = (d, g) => devFilterMatch(f, d, g);
  const q = String(f.q || '').trim().toLowerCase();
  for (const p of serverProfiles) {
    if (multiFilter.pid && p.id !== multiFilter.pid) continue;
    const sk = 's:' + p.id;
    if (p.local) {
      // A local base: its connections, listed like a server's devices
      // (localTreeRows: its folders as groups), in the lists that take them
      // (view.localConns: Devices, Files, a session window's "+" list).
      if (!view.localConns) continue;
      const lc = localConns;
      const ok = lc.state === 'ok';
      const sub = ok ? localTreeRows(view, f, sk + '|') : { rows: [], total: 0, devs: [] };
      if (searching && !sub.total) continue; // (a search: no match, no section)
      total += sub.total;
      const folded = devViewFolded(view, sk);
      const state = (lc.loading && !lc.state) || !lc.state ? 'loading' : ok ? 'local' : 'local-' + lc.state;
      allDevs.push(...sub.devs); // (its connections: boxes too, picker_multi.go)
      rows.push({ kind: 'server', key: sk, pid: p.id, name: String(p.name || ''), color: srvColor(p.id), count: sub.total, state, local: true, folded, devs: sub.devs });
      if (folded) continue;
      for (const r of sub.rows) { if (r.key) r.key = sk + '|' + r.key; rows.push(r); }
      continue;
    }
    const st = srvState(p.id);
    // (signed in, its devices not read yet: loading, not "an older server")
    const state = st.signedIn === false ? 'signedOut' : st.error ? 'error'
      : st.signedIn === true ? (st.scoped ? 'ok' : (st.loading && !st.groups.length) ? 'loading' : 'old') : 'loading';
    const narrow = multiFilter.pid === p.id && multiFilter.tenantId !== null && st.scoped;
    const groups = st.signedIn === true ? st.groups.filter(g => !narrow || Number(g.tenantId) === multiFilter.tenantId) : [];
    const sub = devTreeRows(groups, match, key => devViewFolded(view, sk + '|' + key));
    if (searching && !sub.total) continue;
    total += sub.total;
    const folded = devViewFolded(view, sk);
    const listed = state === 'ok' || state === 'old';
    if (listed) allDevs.push(...sub.devs);
    rows.push({ kind: 'server', key: sk, pid: p.id, name: String(p.name || ''), color: srvColor(p.id), count: sub.total, state, error: st.error, errorText: srvErrorText(String(p.name || ''), st), folded, devs: listed ? sub.devs : [] });
    if (folded || !listed) continue;
    for (const r of sub.rows) { if (r.key) r.key = sk + '|' + r.key; rows.push(r); }
  }
  return { rows, total, devs: allDevs };
}

// Server / tenant / group row: click, Enter or Space folds or unfolds it.
// Its parts (class, tooltip, markup: its signature) come first, the row is
// built only when they changed (devViewFill: dom_patch.go domKeyed); its
// listeners read the row and view it shows now (tr._domData).
function devTreeHeaderRowParts(row, view) {
  const cls = 'tree-row tree-' + row.kind + (row.folded ? ' folded' : '');
  // The "+" list: a three-state box ticking every connectable device under it (picker_multi.go).
  const chk = view.select ? view.select.group(row.devs || []) : '';
  const tr = { title: '', innerHTML: '' };
  if (row.kind === 'server') {
    tr.title = row.name;
    // (a session window signs in nowhere: its main window does)
    const signIn = reachDetachedMode ? '' : '<button class="btn-sm srv-act" data-srv-act="signin">' + TH('topbar.signIn') + '</button>';
    // (its state's text cut to one line: all of it on hover)
    const stateSpan = (cls, html) => '<span class="srv-state' + cls + '" title="' + html + '">' + html + '</span>';
    const localBtn = (act, key) => reachDetachedMode ? '' : '<button class="btn-sm srv-act" data-srv-act="' + act + '">' + TH(key) + '</button>';
    const state = row.local ? (row.state === 'local' ? localBtn('open', 'devices.local.open')
        : row.state === 'local-locked' ? stateSpan('', TH('devices.local.locked')) + localBtn('unlock', 'vault.locked.submit')
        : row.state === 'local-absent' ? stateSpan('', TH('devices.local.absent')) + localBtn('open', 'devices.local.open')
        : row.state === 'loading' ? stateSpan('', TH('common.loading'))
        : stateSpan(' err', TH('vault.locked.unreadable')))
      : row.state === 'signedOut' ? stateSpan('', TH('devices.server.signInHint', { server: row.name })) + signIn
      : row.state === 'error' ? stateSpan(' err', esc(row.errorText || T('devices.server.error', { server: row.name, error: row.error }))) + '<button class="btn-sm srv-act" data-srv-act="retry">' + TH('common.tryAgain') + '</button>'
      : row.state === 'loading' ? stateSpan('', TH('common.loading'))
      : row.state === 'old' ? stateSpan('', TH('devices.server.currentTenantOnly')) : '';
    tr.innerHTML = '<td colspan="7" style="border-inline-start:4px solid ' + row.color + '"><div class="tree-head">' + DEV_TREE_ICONS.chev + chk +
      '<span class="srv-dot" style="background:' + row.color + '"></span><span class="tree-name">' + esc(row.name) + '</span>' +
      '<span class="tree-count">' + TH(row.local ? 'devices.local.count' : 'devices.count', { count: row.count }) + '</span><span class="spacer"></span>' + state + '</div></td>';
  } else if (row.kind === 'tenant') {
    tr.title = row.name;
    tr.innerHTML = '<td colspan="7"><div class="tree-head">' + DEV_TREE_ICONS.chev + chk + DEV_TREE_ICONS.tenant +
      '<span class="tree-name">' + esc(row.name) + '</span>' +
      '<span class="tree-count">' + TH('devices.count', { count: row.count }) + '</span></div></td>';
  } else {
    tr.title = row.path || row.name;
    const pad = Math.min(row.depth, DEV_TREE_MAX_DEPTH) * DEV_TREE_INDENT;
    tr.innerHTML = '<td></td><td colspan="6"><div class="tree-head" style="padding-inline-start:' + pad + 'px">' + DEV_TREE_ICONS.chev + chk +
      (row.ungrouped ? DEV_TREE_ICONS.ungrouped : DEV_TREE_ICONS.group) +
      '<span class="tree-name">' + esc(row.name) + '</span>' +
      '<span class="tree-count">(' + row.count + ')</span>' + localFolderButtons(row, view) + '</div></td>';
  }
  return { cls, title: tr.title, html: tr.innerHTML, sig: cls + '\u0001' + tr.title + '\u0001' + (row.pid || '') + '\u0001' + tr.innerHTML };
}
function devTreeHeaderRow(row, view, parts) {
  view = view || devViewMain();
  const p = parts || devTreeHeaderRowParts(row, view);
  const tr = document.createElement('tr');
  tr.className = p.cls;
  tr.dataset.treeKey = row.key;
  tr.tabIndex = 0;
  tr.setAttribute('aria-expanded', row.folded ? 'false' : 'true');
  tr.title = p.title;
  if (row.kind === 'server') tr.dataset.pid = row.pid;
  tr.innerHTML = p.html;
  tr._domData = { row, view };
  return devTreeHeaderWire(tr, row, view);
}
function devTreeHeaderWire(tr, row0, view0) {
  const cur = () => tr._domData || { row: row0, view: view0 };
  const toggle = (refocus) => {
    const { row, view } = cur();
    devViewToggle(view, row.key);
    view.refresh();
    if (!refocus) return;
    const tbody = devViewTbody(view);
    for (const r of (tbody && tbody.children) || []) if (r.dataset && r.dataset.treeKey === row.key) { r.focus(); break; }
  };
  tr.addEventListener('click', (e) => {
    const { row, view } = cur();
    if (view.select && dpIsCheck(e)) { // (its box: ticks, never folds)
      if (typeof e.stopPropagation === 'function') e.stopPropagation();
      view.select.toggleMany(row.devs || []);
      view.refresh();
      return;
    }
    // A local base's folder: its management buttons (localFolderButtons).
    const lf = row.lfolder && e && e.target && typeof e.target.closest === 'function' ? e.target.closest('[data-lfact]') : null;
    if (lf) {
      if (typeof e.stopPropagation === 'function') e.stopPropagation();
      localFolderAct(row.lfolder, lf.dataset.lfact);
      return;
    }
    // A server separator's own button: sign in on it, or read it again.
    const act = row.kind === 'server' && e && e.target && typeof e.target.closest === 'function' ? e.target.closest('[data-srv-act]') : null;
    if (act) {
      if (typeof e.stopPropagation === 'function') e.stopPropagation();
      if (act.dataset.srvAct === 'signin') multiSignIn(row.pid);
      else if (act.dataset.srvAct === 'unlock') localConnsUnlock(); // (a local base: its vault)
      else if (act.dataset.srvAct === 'open') switchServer(row.pid); // (a local base: its own Devices page)
      else if (act.dataset.srvAct === 'create') localVaultCreate(); // (a local base without its vault yet)
      else srvLoad(row.pid).then(() => { overviewRebuild(); overviewRedraw(); renderTenantSwitcher(); });
      return;
    }
    toggle(false);
  });
  tr.addEventListener('keydown', e => {
    const { row, view } = cur();
    if (view.select && (e.key === ' ' || e.key === 'Enter') && dpIsCheck(e)) {
      e.preventDefault();
      view.select.toggleMany(row.devs || []);
      view.refresh();
      dpRefocus('treeKey', row.key, devViewTbody(view));
      return;
    }
    // (a key on one of its buttons, Sign in / Try again, is that button's)
    if (e && e.target && e.target !== tr) return;
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(true); }
  });
  return tr;
}

function refreshDevicesTable() { devViewFill(devViewMain()); }

const DEV_ROW_ICONS = {
  remote: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="6 3 21 12 6 21 6 3"/></svg>',
  chat: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z"/></svg>',
  info: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>',
};

// The elements of a device list (by id, or given by the view).
function devViewTbody(view) { return view.tbodyEl ? view.tbodyEl() : document.getElementById(view.tbodyId); }
// Fills a device list (view: devViewMain) from the overview, as a tree.
function devViewFill(view) {
  const tbody = devViewTbody(view);
  if (!tbody) return;
  const f = view.filter();
  if (view.prepare) view.prepare(f); // (what a view reads once per drawing: the "+" list's VMs, vms_page.go dpVmPrepare)
  // Single mode on a local base: its connections (localTreeRows), in the
  // lists that take them; what it says when it lists nothing (locked...).
  const localOnly = serverMode !== 'multi' && typeof activeServerIsLocal === 'function' && activeServerIsLocal();
  if (localOnly && view.localConns && localConns.state !== 'ok') {
    const cnt0 = view.countEl ? view.countEl() : document.getElementById(view.countId); if (cnt0 && cnt0.textContent !== 0) cnt0.textContent = 0;
    const lbl0 = view.countLabelEl ? view.countLabelEl() : document.getElementById(view.countId + '-label');
    if (lbl0) domText(lbl0, T('devices.resultsWord', { count: 0 }));
    if (view.onTree) view.onTree({ rows: [], total: 0, devs: [] });
    const st = localConns.state;
    domKeyed(tbody, [{ key: 'local-state', sig: st + '\u0001' + T('common.loading'), data: null, build: () => localStateRow(null) }]);
    return;
  }
  const tree = serverMode === 'multi' ? multiTreeRows(view, f)
    : (localOnly && view.localConns) ? localTreeRows(view, f, '')
    : devTreeRows(overview.groups, (d, g) => devFilterMatch(f, d, g), (key) => devViewFolded(view, key));
  if (view.onTree) view.onTree(tree); // (the "+" list: its selection follows what is listed)
  const cnt = view.countEl ? view.countEl() : document.getElementById(view.countId); if (cnt && cnt.textContent !== tree.total) cnt.textContent = tree.total;
  const cntLbl = view.countLabelEl ? view.countLabelEl() : document.getElementById(view.countId + '-label');
  if (cntLbl) domText(cntLbl, T('devices.resultsWord', { count: tree.total }));
  if (tree.total === 0 && !tree.rows.length) { // (multi-server mode: the separators say why a server shows nothing)
    domMorph(tbody, '<tr><td colspan="7" style="text-align:center;padding:30px;color:var(--muted2)">' + TH('devices.noMatch') + '</td></tr>');
    return;
  }
  // Rows kept by key (a device's server and id, a header's key), built again
  // only when what they show changed (dom_patch.go): a refresh with nothing
  // new touches no node; hover, focus and tooltips stay.
  const seen = new Map();
  const items = [];
  tree.rows.forEach(row => {
    const dev = row.kind === 'device';
    let key = dev ? 'd:' + devKey(row.dev) : 'h:' + row.key;
    const n = seen.get(key) || 0; seen.set(key, n + 1);
    if (n) key += '#' + n; // (the same device listed twice)
    const p = dev ? devViewDeviceRowParts(row, view) : devTreeHeaderRowParts(row, view);
    items.push({ key, sig: p.sig, data: { row, view }, build: () => dev ? devViewDeviceRow(row, view, p) : devTreeHeaderRow(row, view, p) });
    // A view's own rows under a device (a Hyper-V host's VMs: the vault's
    // machine picker, vault_devices_ui.go; the "+" list, vms_page.go
    // dpVmRows): { key, sig, data, build }.
    if (dev && n === 0 && view.children) (view.children(row, view) || []).forEach(c => items.push(c));
  });
  domKeyed(tbody, items);
}

// A local connection (localconn_tools.go) in a device list: the same row,
// its columns what it has (its type, no agent, its host answering, its
// tabs open), its buttons (connect, tools, edit / delete).
function localDevRowParts(row, view) {
  const dev = row.dev, n = dev._node || {};
  const cls = devUsable(dev) ? '' : 'dev-offline';
  const ssh = cxnIsSsh(dev);
  const tabs = toolTabs.filter(t => devSame(t.device, dev)).length;
  const sub = (n.username ? n.username + '@' : '') + n.host + ':' + n.port;
  const isFav = favHas(dev); // (the app's own favourites: LOCAL_PID's)
  const html =
    '<td><span class="fav-star ' + (isFav ? 'active' : '') + '" title="' + TH('devices.toggleFavorite') + '">' + (isFav ? '★' : '☆') + '</span></td>' +
    '<td><div class="cell-hostname" style="padding-inline-start:' + Math.min(Math.max(row.depth, 0), DEV_TREE_MAX_DEPTH + 1) * DEV_TREE_INDENT + 'px">' + (view.select ? view.select.dev(dev) : '') + '<span class="dot ' + devDotClass(dev) + '"></span>' +
      esc(dev.hostname) + '<span class="dev-user" title="' + esc(cxnNodeText(n)) + '">' + esc(sub) + '</span></div></td>' +
    '<td><span class="tag-chip ' + (ssh ? 'linux' : 'win') + '">' + cxnTypeName(dev) + '</span></td>' +
    '<td><span class="muted-hint">-</span></td>' +
    '<td>' + devStateChipHtml(dev) + '</td>' +
    '<td>' + tabs + '</td>' +
    '<td class="actions">' + localRowButtons(dev, view) + '</td>';
  const title = cxnNodeText(n);
  return { cls, title, html, sig: cls + '\u0001' + title + '\u0001' + (view.select ? 1 : 0) + '\u0001' + html };
}
// A device row's element from its parts (devViewDeviceRowParts /
// localDevRowParts); its listeners read the row and view it shows now
// (tr._domData: kept across refreshes by devViewFill).
function devViewRowEl(row, view, p) {
  const tr = document.createElement('tr');
  if (p.cls) tr.className = p.cls;
  tr.innerHTML = p.html;
  if (p.title) tr.title = p.title;
  tr._domData = { row, view };
  const cur = () => tr._domData || { row, view };
  const star = tr.querySelector('.fav-star');
  if (star) star.addEventListener('click', e => { e.stopPropagation(); const c = cur(); favToggle(c.row.dev); c.view.refresh(); });
  if (view.select) tr.dataset.devKey = devKey(row.dev);
  tr.addEventListener('click', e => {
    const { row: r, view: v } = cur(), dev = r.dev;
    if (v.select && dpIsCheck(e)) { e.stopPropagation(); v.select.toggleDev(dev, e); v.refresh(); return; } // (its box: ticks, opens nothing)
    const btn = e.target.closest('button[data-act]');
    if (btn) {
      e.stopPropagation();
      if (btn.disabled) return;
      v.open(dev, btn.dataset.act);
      return;
    }
    v.open(dev);
  });
  if (view.select) devViewBoxKeys(tr, cur);
  return tr;
}
function localDevRow(row, view, parts) { return devViewRowEl(row, view, parts || localDevRowParts(row, view)); }
// Space / Enter on a row's box ticks it (the focus back on it once redrawn).
function devViewBoxKeys(tr, cur) {
  tr.addEventListener('keydown', e => {
    const { row, view } = cur(), dev = row.dev;
    if ((e.key === ' ' || e.key === 'Enter') && dpIsCheck(e)) { e.preventDefault(); view.select.toggleDev(dev, e); view.refresh(); dpRefocus('devKey', devKey(dev), devViewTbody(view)); }
  });
}
function devViewDeviceRow(row, view, parts) {
  return devViewRowEl(row, view, parts || devViewDeviceRowParts(row, view));
}
function devViewDeviceRowParts(row, view) {
  const dev = row.dev;
  if (devIsLocal(dev)) return localDevRowParts(row, view);
  const tr = { className: '', innerHTML: '', title: '' };
  const isFav = favHas(dev); // (its server's favourites)
  // Greyed (and offline for the filter): neither ObliReach nor the Obliance
  // agent answers (devUsable, agent_tools.go; ObliReach alone with an older
  // server); a view may say otherwise (the Chat page: ObliReach).
  const online = view.usable ? view.usable(dev) : devUsable(dev);
  const reachOn = !devIsOffline(dev); // (Chat goes through ObliReach)
  const dc = devDotClass(dev);
  const sessions = reachDevSessions(dev).length; // a number, whatever the agent sent
  const starChar = isFav ? '★' : '☆';
  if (!online) tr.className = 'dev-offline'; // the whole row greyed, not its (i)
  // ▶ of a device without ObliReach opens its page (its terminals, explorer).
  const actTitle = (act, title) => (act === 'remote' && view.remoteTitleAgent && dev._tools && !dev.oblireach?.installed) ? view.remoteTitleAgent : title;
  tr.innerHTML =
    '<td><span class="fav-star ' + (isFav ? 'active' : '') + '" title="' + TH('devices.toggleFavorite') + '">' + starChar + '</span></td>' +
    '<td><div class="cell-hostname" style="padding-inline-start:' + Math.min(row.depth, DEV_TREE_MAX_DEPTH + 1) * DEV_TREE_INDENT + 'px">' + (view.select ? view.select.dev(dev) : '') + '<span class="dot ' + dc + '"></span>' + esc(dev.hostname) + devPrivacyIcon(dev) + devNewHtml(dev) + devLastUserHtml(dev) + (dev.oblireach?.updateAvailable ? ' <span class="tag-chip warn" style="margin-inline-start:6px">' + TH('devices.updateChip') + '</span>' : '') + '</div></td>' +
    '<td><span class="tag-chip ' + osTag(dev.osType) + '">' + esc(dev.osType || '?') + '</span></td>' +
    '<td>' + (dev.oblireach?.version ? 'v' + esc(dev.oblireach.version) : '<span class="muted-hint">-</span>') + '</td>' +
    '<td>' + devStateChipHtml(dev) + '</td>' +
    '<td>' + sessions + '</td>' +
    '<td class="actions">' + view.actions.map(([act, title]) => (act === 'tools' || act === 'files') ? (view.toolButtons ? view.toolButtons(dev, act) : toolsRowButtons(dev, act)) :
      '<button class="icon-btn' + (act === 'remote' || view.primary === act ? ' primary' : '') + '" title="' + esc(actTitle(act, title)) + '" data-act="' + act + '" ' +
      ((view.actDisabled ? view.actDisabled(dev, act) : (act === 'remote' && !online) || (act === 'chat' && !reachOn)) ? 'disabled' : '') + '>' + DEV_ROW_ICONS[act] + '</button>').join('') +
    '</td>';
  if (view.rowTitle) tr.title = view.rowTitle;
  return { cls: tr.className, title: tr.title, html: tr.innerHTML,
    sig: tr.className + '\u0001' + tr.title + '\u0001' + (view.select ? 1 : 0) + '\u0001' + tr.innerHTML };
}

function openDeviceDetail(dev, tab) {
  // Scripts and Info are side panels next to the Remote pane: the page is
  // the Remote one, with that panel open.
  if (tab === 'scripts' || tab === 'info') { sidePanelKind = tab; tab = 'remote'; }
  if (tab) activeTab = tab;
  navigateTo('device-detail', { device: dev });
}

// ── Device detail page (wraps existing Remote/Scripts/Info tabs) ─────────────
function renderDeviceDetail(pc, dev) {
  if (!dev) { renderDevicesPage(pc); return; }
  selectedDevice = dev; if (!devIsVm(dev) && !devIsSupport(dev)) addRecent(dev); // (a VM console, a code session: not devices of the lists)
  pc.innerHTML = '';
  selectDeviceInto(dev, pc);
}

// ── Logs pages ───────────────────────────────────────────────────────────────
function renderLogsPage(pc, sub) {
  const titles = { connection: T('logs.title.connection'), alarm: T('logs.title.alarm'), console: T('logs.title.console'), script: T('logs.title.script') };
  const hints = {
    connection: T('logs.hint.connection'),
    alarm:      T('logs.hint.alarm'),
    console:    T('logs.hint.console'),
    script:     T('logs.hint.script'),
  };
  const h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + TH('nav.logs') + '</h1><div class="breadcrumb">' + esc(titles[sub] || sub) + activeServerNote() + '</div>';
  pc.appendChild(h);
  const body = document.createElement('div'); body.className = 'page-body';
  const panel = document.createElement('div'); panel.className = 'panel';
  panel.innerHTML = '<div class="panel-header">' + esc(titles[sub] || sub) + '<span class="muted-hint" style="margin-left:10px;font-weight:400">' + esc(hints[sub] || '') + '</span></div>' +
    '<div class="panel-body flush" style="overflow-x:auto"><table class="dt"><thead id="logs-thead"></thead><tbody id="logs-tbody"></tbody></table></div>';
  body.appendChild(panel);
  pc.appendChild(body);
  loadLogs(sub);
}

// Server statuses in words (an unknown one as it came): remote sessions
// (Logs > Connection log) and script runs (Logs > Script log, the Scripts
// panel).
const LOG_SESSION_STATUS = {
  waiting: 'logs.status.waiting', connecting: 'logs.status.connecting', active: 'logs.status.active', closed: 'logs.status.closed',
  failed: 'logs.status.failed', timeout: 'logs.status.timeout', expired: 'logs.status.expired',
};
const EXEC_STATUS = {
  pending: 'scripts.state.pending', sent: 'scripts.state.sent', running: 'scripts.state.running', success: 'scripts.state.success',
  failure: 'scripts.state.failure', timeout: 'scripts.state.timeout', skipped: 'scripts.state.skipped', cancelled: 'scripts.state.cancelled',
};
function statusWord(map, s) { return reachCodeText(map, s) || String(s || '-'); }
// A duration in seconds as the language on screen writes it (frac decimals).
function fmtSeconds(sec, frac) { return T('time.seconds', { n: i18nNum(sec, frac || 0, frac || 0) }); }

async function loadLogs(sub) {
  const thead = document.getElementById('logs-thead');
  const tbody = document.getElementById('logs-tbody');
  if (!thead || !tbody) return;
  const empty = (key) => '<tr><td colspan="6" style="text-align:center;padding:20px;color:var(--muted2)">' + TH(key) + '</td></tr>';
  const head = (...keys) => '<tr>' + keys.map(k => '<th>' + TH(k) + '</th>').join('') + '</tr>';
  tbody.innerHTML = empty('common.loading');
  try {
    if (sub === 'connection') {
      thead.innerHTML = head('logs.col.started', 'logs.col.device', 'logs.col.protocol', 'logs.col.operator', 'logs.col.status', 'logs.col.duration');
      const r = await api('GET', '/api/remote/sessions');
      const d = await r.json();
      const items = d?.data?.items || [];
      tbody.innerHTML = '';
      if (!items.length) { tbody.innerHTML = empty('logs.empty.sessions'); return; }
      for (const s of items.slice(0, 100)) {
        const started = s.startedAt ? fmtDateTime(s.startedAt) : '-';
        let dur = '-';
        if (typeof s.durationSeconds === 'number') {
          dur = fmtSeconds(s.durationSeconds);
        } else if (s.startedAt) {
          const ended = s.endedAt ? new Date(s.endedAt).getTime() : Date.now();
          dur = fmtSeconds(Math.round((ended - new Date(s.startedAt).getTime()) / 1000));
        }
        const device = deviceHostnameById(s.deviceId) || String(s.deviceId || '-');
        const operator = s.startedByUser?.displayName || s.startedByUser?.username || String(s.startedBy || '-');
        tbody.innerHTML += '<tr><td>' + esc(started) + '</td><td>' + esc(device) + '</td><td>' + esc(s.protocol || '-') + '</td><td>' + esc(operator) + '</td><td><span class="tag-chip ' + (s.status === 'active' ? 'on' : 'off') + '">' + esc(statusWord(LOG_SESSION_STATUS, s.status)) + '</span></td><td>' + esc(dur) + '</td></tr>';
      }
    } else if (sub === 'console' || sub === 'alarm') {
      thead.innerHTML = head('logs.col.time', 'logs.col.actor', 'logs.col.action', 'logs.col.resource', 'logs.col.ip', 'logs.col.details');
      const r = await api('GET', '/api/audit?limit=100' + (sub === 'alarm' ? '&category=security' : ''));
      const d = await r.json();
      const items = d?.data?.items || d?.items || [];
      tbody.innerHTML = '';
      if (!items.length) { tbody.innerHTML = empty('logs.empty.entries'); return; }
      for (const e of items) {
        const ts = e.createdAt || '';
        const resource = (e.resourceName || e.resourcePath) ? esc(e.resourceType || '-') + ' / ' + esc(e.resourceName || e.resourcePath) : esc(e.resourceType || '-');
        tbody.innerHTML += '<tr><td>' + esc(ts ? fmtDateTime(ts) : '-') + '</td><td>' + esc(e.username || (e.userId != null ? '#' + e.userId : '-')) + '</td><td>' + esc(e.action || '-') + '</td><td>' + resource + '</td><td>' + esc(e.ipAddress || '-') + '</td><td class="muted-hint" style="max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + esc(JSON.stringify(e.details || {})) + '</td></tr>';
      }
    } else if (sub === 'script') {
      thead.innerHTML = head('logs.col.time', 'logs.col.script', 'logs.col.device', 'logs.col.status', 'logs.col.exit', 'logs.col.duration');
      const r = await api('GET', '/api/executions?limit=100');
      const d = await r.json();
      const items = d?.data?.items || d?.items || [];
      tbody.innerHTML = '';
      if (!items.length) { tbody.innerHTML = empty('logs.empty.scripts'); return; }
      for (const x of items) {
        const ts = x.createdAt || x.triggeredAt || x.startedAt || '';
        const scriptName = x.scriptSnapshot?.name || (x.scriptId != null ? '#' + x.scriptId : '-');
        const device = deviceHostnameById(x.deviceId) || String(x.deviceId || '-');
        let dur = '-';
        if (x.startedAt && x.finishedAt) dur = fmtSeconds((new Date(x.finishedAt).getTime() - new Date(x.startedAt).getTime()) / 1000, 1);
        tbody.innerHTML += '<tr><td>' + esc(ts ? fmtDateTime(ts) : '-') + '</td><td>' + esc(scriptName) + '</td><td>' + esc(device) + '</td><td><span class="tag-chip ' + (x.status === 'success' ? 'on' : 'off') + '">' + esc(statusWord(EXEC_STATUS, x.status)) + '</span></td><td>' + esc(String(x.exitCode ?? '-')) + '</td><td>' + esc(dur) + '</td></tr>';
      }
    }
  } catch (err) {
    tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;padding:20px;color:var(--danger)">' + TH('status.loadFailed', { error: err.message || String(err) }) + '</td></tr>';
  }
}

// ── Settings page ────────────────────────────────────────────────────────────
function renderSettingsPage(pc, sub) {
  const h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + TH('nav.settings') + '</h1>' + (activeServerNote() ? '<div class="breadcrumb">' + activeServerNote() + '</div>' : '');
  pc.appendChild(h);
  const body = document.createElement('div'); body.className = 'page-body';
  body.innerHTML = '<div class="settings-layout">' +
    '<div class="settings-nav">' +
    '<div class="settings-nav-item ' + (sub === 'profile' ? 'active' : '') + '" data-sub="profile">' + TH('settings.nav.profile') + '</div>' +
    '<div class="settings-nav-item ' + (sub === 'sessions' ? 'active' : '') + '" data-sub="sessions">' + TH('settings.nav.sessions') + '</div>' +
    '<div class="settings-nav-item ' + (sub === 'app' ? 'active' : '') + '" data-sub="app">' + TH('settings.nav.app') + '</div>' +
    '<div class="settings-nav-item ' + (sub === 'files' ? 'active' : '') + '" data-sub="files">' + TH('settings.nav.files') + '</div>' +
    '<div class="settings-nav-item ' + (sub === 'trusted-ips' ? 'active' : '') + '" data-sub="trusted-ips">' + TH('settings.nav.trustedIps') + '</div>' +
    (reachVaultManageAvailable() ? '<div class="settings-nav-item ' + (sub === 'vault' ? 'active' : '') + '" data-sub="vault">' + TH('settings.nav.vault') + '</div>' : '') +
    '<div class="settings-nav-item ' + (sub === 'support' ? 'active' : '') + '" data-sub="support">' + TH('settings.nav.support') + '</div>' +
    '<div class="settings-nav-item ' + (sub === 'server' ? 'active' : '') + '" data-sub="server">' + TH('settings.nav.server') + '</div>' +
    '<div class="settings-nav-item ' + (sub === 'about' ? 'active' : '') + '" data-sub="about">' + TH('settings.nav.about') + '</div>' +
    '</div>' +
    '<div class="settings-body" id="settings-body"></div></div>';
  pc.appendChild(body);
  // A local base: the app's own settings only (no account, no server).
  if (activeServerIsLocal()) body.querySelectorAll('.settings-nav-item').forEach(el => { if (!navLocalSettingsOk(el.dataset.sub)) el.remove(); });
  body.querySelectorAll('.settings-nav-item').forEach(el => {
    el.addEventListener('click', () => navigateTo('settings', { sub: el.dataset.sub }));
  });
  const sb = body.querySelector('#settings-body');
  if (sub === 'sessions') renderSessionsSection(sb);
  else if (sub === 'files') renderFilesSection(sb); // (edit_page.go: the editor, the local pane)
  else if (sub === 'app') renderAppSection(sb);
  else if (sub === 'trusted-ips') renderTrustedIpsSection(sb);
  else if (sub === 'vault' && reachVaultManageAvailable()) renderVaultSection(sb); // (vault_ui.go, main window only)
  else if (sub === 'support') renderSupportSection(sb); // (support_page.go)
  else if (sub === 'server') renderServerSection(sb);
  else if (sub === 'about') renderAboutSection(sb);
  else renderProfileSection(sb);
}

function renderProfileSection(sb) {
  const panel = document.createElement('div'); panel.className = 'panel';
  panel.innerHTML = '<div class="panel-header">' + TH('settings.nav.profile') + '</div><div class="panel-body" id="profile-body"></div>';
  sb.appendChild(panel);
  const b = panel.querySelector('#profile-body');
  b.innerHTML =
    '<div class="setting-row"><div class="setting-label"><div class="title">' + esc(currentOperatorName || T('common.operator')) + '</div><div class="sub" id="prof-email">-</div></div>' +
    '<div class="setting-control"><span id="prof-avatar" style="display:inline-block;width:44px;height:44px;border-radius:50%%;background:var(--accent-fill);text-align:center;line-height:44px;color:var(--on-accent);font-weight:700">' + esc((currentOperatorName || '?').slice(0,2).toUpperCase()) + '</span></div></div>' +
    '<div class="setting-row"><div class="setting-label"><div class="title">' + TH('settings.profile.displayName') + '</div><div class="sub" id="prof-display">-</div></div></div>' +
    '<div class="setting-row"><div class="setting-label"><div class="title">' + TH('settings.profile.role') + '</div><div class="sub" id="prof-role">-</div></div></div>' +
    '<div class="setting-row"><div class="setting-label"><div class="title">' + TH('settings.profile.auth') + '</div><div class="sub" id="prof-auth">-</div></div><div class="setting-control" id="prof-auth-action"></div></div>';
  // Populate from /api/auth/me
  api('GET', '/api/auth/me').then(r => r.json()).then(d => {
    const u = (d?.data?.user) || d?.data || {};
    if (u.profilePicture || u.profile_picture || u.avatar) {
      b.querySelector('#prof-avatar').innerHTML = '<img src="' + esc(u.profilePicture || u.profile_picture || u.avatar) + '" style="width:44px;height:44px;border-radius:50%%;object-fit:cover"/>';
    }
    b.querySelector('#prof-email').textContent = u.email || '-';
    b.querySelector('#prof-display').textContent = u.displayName || u.display_name || u.username || '-';
    b.querySelector('#prof-role').textContent = u.role === 'admin' ? T('settings.profile.roleAdmin') : u.role === 'user' ? T('settings.profile.roleUser') : (u.role || '-');
    const isObligate = (u.foreignSource || u.foreign_source) === 'obligate';
    b.querySelector('#prof-auth').textContent = isObligate ? T('settings.profile.authSso') : T('settings.profile.authLocal');
  }).catch(() => {});
}

// Remote sessions: "Detach by default" (this computer, localStorage: the local
// port and with it the page origin are stable, the choice survives restarts).
// On unless the operator turned it off (reachDetachDefault).
function renderSessionsSection(sb) {
  const panel = document.createElement('div'); panel.className = 'panel';
  const head = document.createElement('div'); head.className = 'panel-header'; head.textContent = T('settings.nav.sessions');
  const pb = document.createElement('div'); pb.className = 'panel-body';
  const row = document.createElement('div'); row.className = 'setting-row';
  const lbl = document.createElement('div'); lbl.className = 'setting-label';
  const title = document.createElement('div'); title.className = 'title'; title.textContent = T('settings.sessions.detach');
  const sub = document.createElement('div'); sub.className = 'sub';
  sub.textContent = T('settings.sessions.detachSub');
  lbl.appendChild(title); lbl.appendChild(sub);
  const ctl = document.createElement('div'); ctl.className = 'setting-control';
  const cb = document.createElement('input');
  cb.type = 'checkbox'; cb.id = 'set-detach-default';
  cb.style.cssText = 'accent-color:var(--accent);width:16px;height:16px;cursor:pointer';
  cb.checked = reachDetachDefault();
  cb.title = T('settings.sessions.detach');
  if (!reachCanDetach()) { cb.disabled = true; sub.textContent = T('settings.withUnavailable', { text: sub.textContent }); }
  cb.addEventListener('change', () => {
    reachSetDetachDefault(cb.checked);
    setStatus(cb.checked ? T('settings.sessions.detachOn') : T('settings.sessions.detachOff'));
  });
  ctl.appendChild(cb);
  row.appendChild(lbl); row.appendChild(ctl);
  pb.appendChild(row);
  pb.appendChild(renderMultiWindowSetting());
  pb.appendChild(renderSessionFilesSetting()); // (files_dock.go)
  pb.appendChild(renderFilesDblClickSetting()); // (explorer_viewer.go explorerOpenItem)
  pb.appendChild(renderKeepaliveSetting());
  pb.appendChild(renderLockOnEndSetting());
  pb.appendChild(reachRenderKeepResSetting()); // (reach_resolution.go)
  renderMacKeysSetting(pb);
  // The terminals' default theme of each type (term_themes.go).
  pb.appendChild(renderTermThemeSetting('ssh', 'settings.sessions.themeSsh'));
  pb.appendChild(renderTermThemeSetting('cmd', 'settings.sessions.themeCmd'));
  pb.appendChild(renderTermThemeSetting('powershell', 'settings.sessions.themePowershell'));
  panel.appendChild(head); panel.appendChild(pb);
  sb.appendChild(panel);
}

// "Keep sessions alive" (reach_viewer.go reachKeepalivePolicy): the choice
// is read at every keepalive tick, by the main window and the detached ones.
// "Connecting several devices" (devices_select.go dsPlanWindow): in detached
// mode, Connect from the Devices selection opens one window or one per device.
function renderMultiWindowSetting() {
  const row = document.createElement('div'); row.className = 'setting-row';
  const lbl = document.createElement('div'); lbl.className = 'setting-label';
  const title = document.createElement('div'); title.className = 'title'; title.textContent = T('settings.sessions.multiWindow');
  const sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('settings.sessions.multiWindowSub');
  lbl.appendChild(title); lbl.appendChild(sub);
  const ctl = document.createElement('div'); ctl.className = 'setting-control';
  const sel = document.createElement('select');
  sel.id = 'set-multi-window';
  sel.title = T('settings.sessions.multiWindow');
  sel.style.cssText = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
  [['one', T('settings.sessions.multiWindowOne')], ['each', T('settings.sessions.multiWindowEach')]].forEach(([v, text]) => {
    const o = document.createElement('option'); o.value = v; o.textContent = text; sel.appendChild(o);
  });
  sel.value = reachMultiWindowMode();
  sel.addEventListener('change', () => {
    reachSetMultiWindowMode(sel.value);
    setStatus(reachMultiWindowMode() === 'each' ? T('settings.sessions.multiWindowStatusEach') : T('settings.sessions.multiWindowStatusOne'));
  });
  ctl.appendChild(sel);
  row.appendChild(lbl); row.appendChild(ctl);
  return row;
}

// "File explorer of a session" (files_dock.go filesSessionMode): a session's
// Files button opens a side panel next to it (default) or a tab.
// Double click on a file in an explorer: always open it in the editor, or
// always download it (explorer_viewer.go explorerDblAction).
function renderFilesDblClickSetting() {
  const row = document.createElement('div'); row.className = 'setting-row';
  const lbl = document.createElement('div'); lbl.className = 'setting-label';
  const title = document.createElement('div'); title.className = 'title'; title.textContent = T('settings.files.dblClick');
  const sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('settings.files.dblClickSub');
  lbl.appendChild(title); lbl.appendChild(sub);
  const ctl = document.createElement('div'); ctl.className = 'setting-control';
  const sel = document.createElement('select');
  sel.id = 'set-files-dblclick';
  sel.title = T('settings.files.dblClick');
  sel.style.cssText = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
  [['open', T('settings.files.dblClickOpen')], ['download', T('settings.files.dblClickDownload')]].forEach(([v, text]) => {
    const o = document.createElement('option'); o.value = v; o.textContent = text; sel.appendChild(o);
  });
  sel.value = explorerDblAction();
  sel.addEventListener('change', () => {
    explorerSetDblAction(sel.value);
    setStatus(explorerDblAction() === 'download' ? T('settings.files.dblClickStatusDownload') : T('settings.files.dblClickStatusOpen'));
  });
  ctl.appendChild(sel);
  row.appendChild(lbl); row.appendChild(ctl);
  return row;
}

function renderSessionFilesSetting() {
  const row = document.createElement('div'); row.className = 'setting-row';
  const lbl = document.createElement('div'); lbl.className = 'setting-label';
  const title = document.createElement('div'); title.className = 'title'; title.textContent = T('settings.sessions.filesOpen');
  const sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('settings.sessions.filesOpenSub');
  lbl.appendChild(title); lbl.appendChild(sub);
  const ctl = document.createElement('div'); ctl.className = 'setting-control';
  const sel = document.createElement('select');
  sel.id = 'set-session-files';
  sel.title = T('settings.sessions.filesOpen');
  sel.style.cssText = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
  [['panel', T('settings.sessions.filesOpenPanel')], ['tab', T('settings.sessions.filesOpenTab')]].forEach(([v, text]) => {
    const o = document.createElement('option'); o.value = v; o.textContent = text; sel.appendChild(o);
  });
  sel.value = filesSessionMode();
  sel.addEventListener('change', () => {
    filesSetSessionMode(sel.value);
    setStatus(filesSessionMode() === 'tab' ? T('settings.sessions.filesOpenStatusTab') : T('settings.sessions.filesOpenStatusPanel'));
  });
  ctl.appendChild(sel);
  row.appendChild(lbl); row.appendChild(ctl);
  return row;
}

function renderKeepaliveSetting() {
  const row = document.createElement('div'); row.className = 'setting-row';
  const lbl = document.createElement('div'); lbl.className = 'setting-label';
  const title = document.createElement('div'); title.className = 'title'; title.textContent = T('settings.keepalive.title');
  const sub = document.createElement('div'); sub.className = 'sub';
  sub.textContent = T('settings.keepalive.sub');
  lbl.appendChild(title); lbl.appendChild(sub);
  const ctl = document.createElement('div'); ctl.className = 'setting-control';
  const sel = document.createElement('select');
  sel.id = 'set-keepalive';
  sel.title = T('settings.keepalive.title');
  sel.style.cssText = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
  [['always', T('settings.keepalive.always')], ['visible', T('settings.keepalive.visible')], ['never', T('settings.keepalive.never')]].forEach(([v, text]) => {
    const o = document.createElement('option'); o.value = v; o.textContent = text; sel.appendChild(o);
  });
  sel.value = reachKeepalivePolicy();
  sel.addEventListener('change', () => {
    reachSetKeepalivePolicy(sel.value);
    const p = reachKeepalivePolicy();
    setStatus(p === 'always' ? T('settings.keepalive.statusAlways')
      : p === 'never' ? T('settings.keepalive.statusNever')
      : T('settings.keepalive.statusVisible'));
  });
  ctl.appendChild(sel);
  row.appendChild(lbl); row.appendChild(ctl);
  return row;
}

// "Offer to lock the session when the connection ends" (reach_viewer.go
// reachLockOfferEnabled): on by default; off, the padlock is hidden and every
// open session (main and detached windows) is told not to lock at the end.
function renderLockOnEndSetting() {
  const row = document.createElement('div'); row.className = 'setting-row';
  const lbl = document.createElement('div'); lbl.className = 'setting-label';
  const title = document.createElement('div'); title.className = 'title'; title.textContent = T('settings.sessions.lockOnEnd');
  const sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('settings.sessions.lockOnEndSub');
  lbl.appendChild(title); lbl.appendChild(sub);
  const ctl = document.createElement('div'); ctl.className = 'setting-control';
  const cb = document.createElement('input');
  cb.type = 'checkbox'; cb.id = 'set-lock-on-end';
  cb.style.cssText = 'accent-color:var(--accent);width:16px;height:16px;cursor:pointer';
  cb.checked = reachLockOfferEnabled();
  cb.title = T('settings.sessions.lockOnEnd');
  cb.addEventListener('change', () => {
    reachSetLockOfferEnabled(cb.checked);
    setStatus(cb.checked ? T('settings.sessions.lockOnEndOn') : T('settings.sessions.lockOnEndOff'));
  });
  ctl.appendChild(cb);
  row.appendChild(lbl); row.appendChild(ctl);
  return row;
}

// "Mac keyboard mapping" (reach_viewer.go reachMacKeysPolicy / reachKeyMsg):
// Automatic / Off, and "Ctrl -> Cmd for shortcuts". Read at every key, so a
// change applies at once to the open sessions (main and detached windows).
function renderMacKeysSetting(pb) {
  const selStyle = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
  const mkRow = (titleKey, subKey) => {
    const row = document.createElement('div'); row.className = 'setting-row';
    const lbl = document.createElement('div'); lbl.className = 'setting-label';
    const title = document.createElement('div'); title.className = 'title'; title.textContent = T(titleKey);
    const sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T(subKey);
    lbl.appendChild(title); lbl.appendChild(sub);
    const ctl = document.createElement('div'); ctl.className = 'setting-control';
    row.appendChild(lbl); row.appendChild(ctl);
    pb.appendChild(row);
    return ctl;
  };
  const sel = document.createElement('select');
  sel.id = 'set-mac-keys';
  sel.title = T('settings.macKeys.title');
  sel.style.cssText = selStyle;
  [['auto', T('settings.macKeys.auto')], ['off', T('settings.macKeys.off')]].forEach(([v, text]) => {
    const o = document.createElement('option'); o.value = v; o.textContent = text; sel.appendChild(o);
  });
  sel.value = reachMacKeysPolicy();
  mkRow('settings.macKeys.title', 'settings.macKeys.sub').appendChild(sel);
  const cb = document.createElement('input');
  cb.type = 'checkbox'; cb.id = 'set-mac-keys-ctrl';
  cb.title = T('settings.macKeys.ctrlCmd');
  cb.style.cssText = 'accent-color:var(--accent);width:16px;height:16px;cursor:pointer';
  cb.checked = reachMacKeysCtrlCmd();
  mkRow('settings.macKeys.ctrlCmd', 'settings.macKeys.ctrlCmdSub').appendChild(cb);
  const sync = () => { cb.disabled = reachMacKeysPolicy() === 'off'; };
  sync();
  sel.addEventListener('change', () => {
    reachSetMacKeysPolicy(sel.value);
    sync();
    setStatus(reachMacKeysPolicy() === 'off' ? T('settings.macKeys.statusOff') : T('settings.macKeys.statusAuto'));
  });
  cb.addEventListener('change', () => {
    reachSetMacKeysCtrlCmd(cb.checked);
    setStatus(cb.checked ? T('settings.macKeys.statusCtrlOn') : T('settings.macKeys.statusCtrlOff'));
  });
}

// Settings > Application (startup_windows.go): "Start with Windows" is what
// the registry says (HKCU Run), "Start minimized" (at Windows start-up) and
// "Every launch" are the app's; minimized means in the taskbar, or hidden in
// the notification area with "Keep in the notification area" (tray.go: the
// icon next to the clock; minimizing or closing hides the window there). And
// the version, with a manual update check.
function renderAppSection(sb) {
  const panel = document.createElement('div'); panel.className = 'panel';
  const head = document.createElement('div'); head.className = 'panel-header'; head.textContent = T('settings.nav.app');
  const pb = document.createElement('div'); pb.className = 'panel-body';
  const row = (id, titleText, subText) => {
    const r = document.createElement('div'); r.className = 'setting-row';
    const lbl = document.createElement('div'); lbl.className = 'setting-label';
    const t = document.createElement('div'); t.className = 'title'; t.textContent = titleText;
    const s = document.createElement('div'); s.className = 'sub'; s.id = id + '-sub'; s.textContent = subText;
    lbl.appendChild(t); lbl.appendChild(s);
    const ctl = document.createElement('div'); ctl.className = 'setting-control';
    r.appendChild(lbl); r.appendChild(ctl);
    pb.appendChild(r);
    return { sub: s, ctl: ctl };
  };
  const check = (r, id, title) => {
    const cb = document.createElement('input');
    cb.type = 'checkbox'; cb.id = id; cb.title = title; cb.disabled = true;
    cb.style.cssText = 'accent-color:var(--accent);width:16px;height:16px;cursor:pointer';
    r.ctl.appendChild(cb);
    return cb;
  };
  // Language (i18n.go): Automatic, or one of the languages in its own name.
  renderLanguageSetting(pb);
  // How often the device lists are read again (devices_live.go).
  pb.appendChild(dlRenderSetting());
  const RUN_SUB = T('settings.app.runSub');
  const MIN_SUB = T('settings.app.minSub');
  const ALWAYS_SUB = T('settings.app.alwaysSub');
  const rRun = row('set-run-at-login', T('settings.app.run'), RUN_SUB);
  const cbRun = check(rRun, 'set-run-at-login', T('settings.app.run'));
  const rMin = row('set-start-min', T('settings.app.min'), MIN_SUB);
  const cbMin = check(rMin, 'set-start-min', T('settings.app.min'));
  const rAlways = row('set-start-min-always', T('settings.app.always'), ALWAYS_SUB);
  const cbAlways = check(rAlways, 'set-start-min-always', T('settings.app.alwaysTitle'));
  const rTray = row('set-tray', T('settings.app.tray'), T('settings.app.traySub'));
  const cbTray = check(rTray, 'set-tray', T('settings.app.tray'));
  const rVer = row('set-version', T('settings.app.version'), 'v' + (window.__reach_version || '-'));
  const btn = document.createElement('button');
  btn.className = 'btn-ghost'; btn.id = 'set-check-update'; btn.textContent = T('settings.app.checkUpdates');
  rVer.ctl.appendChild(btn);
  panel.appendChild(head); panel.appendChild(pb);
  sb.appendChild(panel);
  renderThemeSettings(sb); // (Appearance: the themes of multi-server mode and of the local base)

  const apply = (st) => {
    if (!st) return;
    cbRun.checked = !!st.runAtLogin; cbMin.checked = !!st.minimized; cbAlways.checked = !!st.minimizedAlways; cbTray.checked = !!st.tray;
    cbRun.disabled = false; cbMin.disabled = false; cbAlways.disabled = !st.minimized; cbTray.disabled = false;
    rMin.sub.textContent = st.tray ? T('settings.app.minSubTray') : MIN_SUB; // (where it starts: the notification area, or the taskbar)
    let t = RUN_SUB;
    if (st.disabledInTaskManager) t = T('settings.app.runTaskManager', { text: RUN_SUB });
    else if (st.runAtLogin && st.otherCopy) t = T('settings.app.runOtherCopy', { text: RUN_SUB, path: st.otherCopy });
    rRun.sub.textContent = t;
  };
  const unavailable = () => {
    [cbRun, cbMin, cbAlways, cbTray].forEach(cb => { cb.disabled = true; });
    rRun.sub.textContent = T('settings.withUnavailable', { text: RUN_SUB });
  };
  reachNative('reachStartupGet').then(apply).catch(unavailable);
  const change = (cb, what, done) => cb.addEventListener('change', () => {
    const on = !!cb.checked;
    reachNative('reachStartupSet', what, on).then(st => { apply(st); setStatus(done(on)); })
      .catch(e => {
        setStatus(T('settings.app.notSaved', { error: reachErrText(e) }));
        reachNative('reachStartupGet').then(apply).catch(unavailable);
      });
  });
  change(cbRun, 'runAtLogin', on => on ? T('settings.app.runOn') : T('settings.app.runOff'));
  change(cbMin, 'minimized', on => on ? T('settings.app.minOn') : T('settings.app.minOff'));
  change(cbAlways, 'minimizedAlways', on => on ? T('settings.app.alwaysOn') : T('settings.app.alwaysOff'));
  change(cbTray, 'tray', on => on ? T('settings.app.trayOn') : T('settings.app.trayOff'));

  // A check now: the banner shows an update (even one put off with "Later").
  btn.addEventListener('click', async () => {
    btn.disabled = true;
    rVer.sub.textContent = T('settings.app.checking', { version: window.__reach_version || '-' });
    const newer = await checkForUpdate();
    btn.disabled = false;
    if (newer && updateInfo) {
      if (updateDismissed() === updateInfo.version) updateDismiss(null);
      renderUpdateBanner();
      rVer.sub.textContent = T('settings.app.newer', { version: window.__reach_version || '-', newer: updateInfo.version });
    } else {
      rVer.sub.textContent = T(updateInfo ? 'settings.app.upToDate' : 'settings.app.noAnswer', { version: window.__reach_version || '-' });
    }
  });
}

function renderTrustedIpsSection(sb) {
  const panel = document.createElement('div'); panel.className = 'panel';
  panel.innerHTML = '<div class="panel-header">' + TH('settings.tip.title') + '<div class="panel-actions"><button class="btn-sm" id="revoke-all-tip">' + TH('settings.tip.revokeAll') + '</button></div></div>' +
    '<div class="panel-body">' +
    '<p class="muted-hint" style="margin-bottom:12px">' + TH('settings.tip.hint') + '</p>' +
    '<div id="tip-list"></div></div>';
  sb.appendChild(panel);
  const list = panel.querySelector('#tip-list');
  list.innerHTML = '<div class="muted-hint" style="padding:14px">' + TH('common.loading') + '</div>';
  api('GET', '/api/profile/trusted-ips').then(r => r.json()).then(d => {
    const items = d?.data || d || [];
    if (!Array.isArray(items) || items.length === 0) {
      list.innerHTML = '<div class="muted-hint" style="padding:14px">' + TH('settings.tip.empty') + '</div>';
      return;
    }
    list.innerHTML = '';
    for (const it of items) {
      const row = document.createElement('div'); row.className = 'tip-row';
      const trustedUntil = it.trustedUntil || it.trusted_until;
      const remaining = trustedUntil ? Math.max(0, new Date(trustedUntil).getTime() - Date.now()) : 0;
      const mins = Math.floor(remaining / 60000);
      const exp = mins > 60 ? T('settings.tip.hoursMinutes', { h: Math.round(mins / 60), m: mins %% 60 }) : T('settings.tip.minutes', { m: mins });
      row.innerHTML = '<span class="ipv">' + esc(it.ip) + '</span><span class="exp">' + TH('settings.tip.expiresIn', { time: exp }) + '</span><span class="spacer"></span>' +
        '<button class="btn-ghost">' + TH('settings.tip.revoke') + '</button>';
      row.querySelector('button').addEventListener('click', async () => {
        await api('DELETE', '/api/profile/trusted-ips/' + encodeURIComponent(it.ip));
        renderTrustedIpsSection(sb);
      });
      list.appendChild(row);
    }
  }).catch(() => {
    list.innerHTML = '<div class="muted-hint" style="padding:14px">' + TH('settings.tip.unavailable') + '</div>';
  });
  panel.querySelector('#revoke-all-tip').addEventListener('click', async () => {
    if (!confirm(T('settings.tip.revokeAllConfirm'))) return;
    await api('DELETE', '/api/profile/trusted-ips');
    renderTrustedIpsSection(sb);
  });
}

function renderServerSection(sb) {
  const panel = document.createElement('div'); panel.className = 'panel';
  panel.innerHTML = '<div class="panel-header">' + TH('settings.server.title') + '</div><div class="panel-body">' +
    '<div class="setting-row"><div class="setting-label"><div class="title">' + TH('settings.server.url') + '</div><div class="sub" id="srv-url">-</div></div>' +
    '<div class="setting-control"><button class="btn-ghost" id="btn-change-srv">' + TH('settings.server.change') + '</button></div></div>' +
    '<div class="setting-row"><div class="setting-label"><div class="title">' + TH('settings.server.connection') + '</div><div class="sub">' + TH('settings.server.connectionSub') + '</div></div>' +
    '<div class="setting-control"><button class="btn-ghost" id="btn-reload-srv">' + TH('settings.server.reload') + '</button></div></div>' +
    '</div>';
  sb.appendChild(panel);
  fetch('/local/config').then(r => r.json()).then(c => { panel.querySelector('#srv-url').textContent = c.serverUrl || '-'; }).catch(() => {});
  panel.querySelector('#btn-change-srv').addEventListener('click', () => document.getElementById('user-menu-logout').click());
  panel.querySelector('#btn-reload-srv').addEventListener('click', async () => { await loadOverview(); await loadTenants(); setStatus(T('settings.server.reloaded')); });
}

function renderAboutSection(sb) {
  const panel = document.createElement('div'); panel.className = 'panel';
  const ver = window.__reach_version || '-';
  panel.innerHTML = '<div class="panel-header">' + TH('settings.about.title') + '</div><div class="panel-body">' +
    '<div class="setting-row"><div class="setting-label"><div class="title">' + TH('settings.about.version') + '</div><div class="sub">v' + esc(ver) + '</div></div></div>' +
    '<div class="setting-row"><div class="setting-label"><div class="title">' + TH('settings.about.tagline') + '</div><div class="sub">' + TH('settings.about.built') + '</div></div></div>' +
    '</div>';
  sb.appendChild(panel);
}

// Settings > Application > Appearance (theme.go themeOverride):
// the theme of the whole app in multi-server mode, and on a local base alone
// on screen. Each one: "Follow ..." (the servers' rule, the default: the
// first server's account's theme) or one of the Obli suite's themes
// (THEME_CATALOG), each option with its preview (dropdown.go data-swatch).
// Saved by the app (its settings, every server), worn at once in every
// window (the app's answer here, __reachOnTheme in the others); a real
// server on screen keeps its account's theme whatever these say.
function themeName(id) { const e = THEME_CATALOG.find(x => x.id === id); return e ? e.name : id; }
function themeSwatchAttr(id) { const e = THEME_CATALOG.find(x => x.id === id); return e && Array.isArray(e.swatch) ? e.swatch.join(',') : ''; }
// The theme "Follow the servers" gives: the first server's account's.
function themeFollowed() { const f = obServers()[0]; return themeNormalize(f && f.theme); }
function renderThemeSettings(sb) {
  const panel = document.createElement('div'); panel.className = 'panel'; panel.id = 'set-theme-panel';
  const head = document.createElement('div'); head.className = 'panel-header'; head.textContent = T('settings.theme.title');
  const pb = document.createElement('div'); pb.className = 'panel-body';
  const mk = (kind, titleKey, subKey, followKey) => {
    const r = document.createElement('div'); r.className = 'setting-row';
    const lbl = document.createElement('div'); lbl.className = 'setting-label';
    const t = document.createElement('div'); t.className = 'title'; t.textContent = T(titleKey);
    const s = document.createElement('div'); s.className = 'sub'; s.id = 'set-theme-' + kind + '-sub'; s.textContent = T(subKey);
    lbl.appendChild(t); lbl.appendChild(s);
    const ctl = document.createElement('div'); ctl.className = 'setting-control';
    const sel = document.createElement('select');
    sel.id = 'set-theme-' + kind; sel.title = T(titleKey);
    sel.style.cssText = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
    ctl.appendChild(sel);
    r.appendChild(lbl); r.appendChild(ctl);
    pb.appendChild(r);
    return { kind: kind, sel: sel, sub: s, subKey: subKey, followKey: followKey };
  };
  const rows = [
    mk('multi', 'settings.theme.multi', 'settings.theme.multiSub', 'settings.theme.followFirst'),
    mk('local', 'settings.theme.local', 'settings.theme.localSub', 'settings.theme.followServers'),
  ];
  let st = null; // GET / POST /local/theme/settings (null: what the page knows)
  const fill = () => {
    const followed = themeFollowed();
    for (const r of rows) {
      const cur = r.kind === 'multi' ? themeSettings.multi : themeSettings.local;
      r.sel.innerHTML = '';
      const f = document.createElement('option'); f.value = 'follow';
      f.textContent = T(r.followKey, { theme: themeName(followed) });
      f.setAttribute('data-swatch', themeSwatchAttr(followed));
      r.sel.appendChild(f);
      const g = document.createElement('optgroup'); g.label = T('settings.theme.group');
      for (const e of THEME_CATALOG) {
        const o = document.createElement('option'); o.value = e.id; o.textContent = e.name;
        o.setAttribute('data-swatch', themeSwatchAttr(e.id));
        g.appendChild(o);
      }
      r.sel.appendChild(g);
      r.sel.value = cur || 'follow';
      r.sub.textContent = T(r.subKey) + ((st && st.setting === r.kind) ? ' ' + T('settings.theme.inUse') : '');
    }
  };
  fill();
  fetch('/local/theme/settings').then(res => (res && res.ok) ? res.json() : null).then(s => { if (s) { st = s; themeSettingsTake(s); fill(); } }).catch(() => {});
  for (const r of rows) {
    r.sel.addEventListener('change', async () => {
      const body = {};
      body[r.kind === 'multi' ? 'multiTheme' : 'localTheme'] = r.sel.value === 'follow' ? '' : r.sel.value;
      let s = null;
      try {
        const res = await fetch('/local/theme/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
        if (res && res.ok) s = await res.json();
      } catch (e) {}
      if (!s || typeof s.theme !== 'string') { fill(); setStatus(T('settings.theme.notSaved')); return; }
      st = s;
      themeSettingsTake(s);
      themeTake(s.theme, s.version); // (this window at once; the others by __reachOnTheme)
      fill();
      setStatus(T('settings.theme.saved'));
    });
  }
  panel.appendChild(head); panel.appendChild(pb);
  sb.appendChild(panel);
}

// ── Language (i18n.go) ───────────────────────────────────────────────────────
// T(key, params) / TH(key, params) come with the page (its first script). The
// app's language: the "Language" setting (below), else the preferred language
// of the Obliance account signed in on the active server, else Windows', else
// English. It changes without a restart: the setting, a server switch, an
// account whose language differs (i18nSyncAccount); the app then tells every
// window (__reachOnLang, i18n.go), and I18N.onChange draws everything again.
// Transient texts (the status line, toasts, a session's overlay and status,
// an open 2FA prompt) keep the language they were shown in until they change.
let appReady = false; // the app's pages are drawn (enterApp / detachedBoot done)

// After sign-in (enterApp): this account's preferred language, kept by the
// app for this server profile. Resolves once the page is in the app's language.
async function i18nSyncAccount() {
  let st = null;
  try {
    const r = await fetch('/local/i18n', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile: activeServerId || '', account: currentOperatorLang || '' }) });
    if (r && r.ok) st = await r.json();
  } catch (e) {}
  if (st && st.lang) { try { await i18nUse(st.lang); } catch (e) {} }
}

// Settings > Application > Language: Automatic, or a language in its own
// name. Saved in the app's settings (every window, every server).
function renderLanguageSetting(pb) {
  const r = document.createElement('div'); r.className = 'setting-row';
  const lbl = document.createElement('div'); lbl.className = 'setting-label';
  const t = document.createElement('div'); t.className = 'title'; t.textContent = T('settings.lang.title');
  const s = document.createElement('div'); s.className = 'sub'; s.id = 'set-language-sub'; s.textContent = T('settings.lang.sub');
  lbl.appendChild(t); lbl.appendChild(s);
  const ctl = document.createElement('div'); ctl.className = 'setting-control';
  const sel = document.createElement('select');
  sel.id = 'set-language'; sel.title = T('settings.lang.title');
  sel.style.cssText = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
  // st: GET /local/i18n ({ setting, lang, account, system }); null: what the
  // page knows. "Automatic" names the language it gives: the account's, else
  // Windows', else English (whatever the setting is now).
  const fill = (st) => {
    sel.innerHTML = '';
    const eff = st ? (st.account || st.system || 'en') : i18nLang();
    const cur = I18N.langs.find(l => l.code === eff);
    const auto = document.createElement('option'); auto.value = 'auto';
    auto.textContent = T('settings.lang.auto', { lang: cur ? cur.name : eff });
    sel.appendChild(auto);
    for (const l of I18N.langs) {
      const o = document.createElement('option'); o.value = l.code; o.textContent = l.name;
      if (l.rtl) o.dir = 'rtl';
      sel.appendChild(o);
    }
    sel.value = (st && typeof st.setting === 'string' && st.setting) ? st.setting : 'auto';
  };
  fill(null);
  fetch('/local/i18n').then(res => (res && res.ok) ? res.json() : null).then(st => { if (st) fill(st); }).catch(() => {});
  sel.addEventListener('change', async () => {
    let st = null;
    try {
      const res = await fetch('/local/i18n', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ setting: sel.value }) });
      if (res && res.ok) st = await res.json();
    } catch (e) {}
    if (!st || !st.lang) { setStatus(T('settings.lang.notSaved')); return; }
    await i18nUse(st.lang); // (a new language draws this page again)
    setStatus(T('settings.lang.saved'));
  });
  ctl.appendChild(sel);
  r.appendChild(lbl); r.appendChild(ctl);
  pb.appendChild(r);
  return sel;
}

// A new language: everything drawn by script is drawn again (the static
// markup was, by i18nApply). The page on screen is rebuilt in place, as a
// tab switch does (a session keeps running, its picture stays).
function i18nRedrawApp() {
  if (typeof reachWinTitles === 'function') reachWinTitles();
  renderLoginServers();
  const ssoBtn = document.getElementById('btn-sso'), ssoLbl = document.getElementById('btn-sso-label');
  if (ssoBtn && ssoLbl && !ssoBtn.disabled) ssoLbl.textContent = T('login.ssoButton');
  const loginBtn = document.getElementById('btn-login');
  if (loginBtn && !loginBtn.disabled) loginBtn.textContent = T('login.connect');
  renderUpdateBanner();
  if (chatPageView) i18nApply(chatPageView.root); // (kept while another page is on screen)
  for (const v of chatViews()) v.renderedKey = null; // system lines in the new language
  chatRender();
  chatPickerRefreshIfShown(); // (its rows: states, tooltips, the count's word)
  filesPageI18n(); // (the Files page, kept while another page is on screen: its texts, tabs, picker)
  runPageI18n(); // (the Run page, kept the same way: run_page.go)
  if (devPickerBuilt && devPickerIsOpen()) devPickerRefresh();
  if (!appReady) {
    if (reachDetachedMode && reachDetachedNoticeKeys) reachDetachedNotice(...reachDetachedNoticeKeys);
    return;
  }
  renderServerSwitcher();
  renderTenantSwitcher();
  if (!reachDetachedMode) buildNavRail();
  // The Scripts panels not in use are built again; one with a script picked
  // keeps its run and output (in the language it was started in).
  for (const [k, s] of Array.from(sidePanelScripts)) if (!s.picked) sidePanelScripts.delete(k);
  // The page on screen, rebuilt as a tab switch does. Not a device page while
  // a recording or an annotation runs on it (rebuilding would end them): it
  // takes the new language at its next redraw. The picture keeps the keyboard.
  const onDevice = currentRoute === 'device-detail';
  if (!(onDevice && (recMediaRecorder || annotationActive))) {
    const hadFocus = !!(document.activeElement && document.activeElement.id === 'remote-canvas');
    navigateTo(currentRoute, currentRouteParams);
    if (hadFocus && typeof reachFocusCanvas === 'function') reachFocusCanvas();
  }
  if (sidePanelOpen()) sidePanelRender();
  renderSessionTabs();
}
I18N.onChange = i18nRedrawApp;

// ── Multi-tab sessions ───────────────────────────────────────────────────────
function renderSessionTabs() {
  // The explorers' side panels follow the sessions (files_dock.go: one whose
  // session went closes), before the report below.
  filesDockSync();
  // Every window tells the app which devices it has a session tab for (a
  // device has one tab, in one window: reachReportTabs, reach_viewer.go).
  reachReportTabs();
  // A detached window shows its tabs in its title bar.
  if (reachDetachedMode) { reachRenderWinTabs(); return; }
  const bar = document.getElementById('session-tabs');
  bar.innerHTML = '';
  // In their order: the ObliReach tabs, then the terminals (terminal_viewer.go);
  // a mosaic in place of its members (tab_groups.go).
  for (const it of tgStripItems()) {
    if (it.group) { bar.appendChild(tgMainGroupEl(it.group)); continue; }
    const tab = it.tab;
    if (toolTabById(tab.id)) { bar.appendChild(toolMainTabEl(tab)); continue; }
    const el = document.createElement('div');
    el.className = 'session-tab' + (tab.id === activeSessionTabId ? ' active' : '');
    const tp = serverMode === 'multi' && tab.device ? devPid(tab.device) : null; // (multi-server mode: its server)
    el.innerHTML = (tp !== null ? '<span class="st-srv" style="background:' + srvColor(tp) + '" title="' + esc(srvName(tp)) + '"></span>' : '') +
      '<span class="dot ' + (tab.ws?.readyState === WebSocket.OPEN ? 'online' : 'offline') + '" style="width:6px;height:6px"></span>' +
      '<span>' + esc(tab.label) + '</span>' +
      (tab.viewOnly ? '<span class="st-vo" title="' + TH('viewer.viewOnlyShort') + '">' + REACH_ICON.viewOnly + '</span>' : '') +
      (tab.inputBlocked ? '<span class="st-ib" title="' + TH('viewer.inputBlock.chip') + '">' + REACH_ICON.lock + '</span>' : '') +
      '<span class="tab-close" title="' + TH('common.close') + '">&times;</span>';
    el.querySelector('.tab-close').addEventListener('click', e => { e.stopPropagation(); closeSessionTab(tab.id); });
    el.addEventListener('click', e => { if (e && (e.ctrlKey || e.metaKey)) tgCtrlClick(tab); else switchSessionTab(tab.id); });
    el.addEventListener('contextmenu', e => { if (tabMoveOffered(tab)) tabMoveMenuOpen(tab, e); }); // "Move to window" (tab_move.go)
    bar.appendChild(el);
  }
  tgRefreshSubTabs(); // (a mosaic on screen: its sub-tabs follow)
}

function switchSessionTab(tabId) {
  const tab = sessionTabs.find(t => t.id === tabId);
  if (!tab) { const tt = toolTabById(tabId); if (tt) toolShowTab(tt); return; } // (a tool tab)
  tgNoteShown(tab); // (a member of a mosaic: its mosaic on screen, focused on it: tab_groups.go)
  activeSessionTabId = tabId;
  // Restore device context and re-render (always on the Remote tab: another
  // tab of the device page would stop the session).
  selectedDevice = tab.device;
  remoteWs = tab.ws;
  remoteDecoder = tab.decoder;
  renderSessionTabs();
  openDeviceDetail(tab.device, 'remote');
  reachUpdateClipboardWatch();
}

function closeSessionTab(tabId) {
  const grpTab = tgById(tabId);
  if (grpTab) { tgCloseGroup(grpTab); return; } // (a mosaic's tab: tab_groups.go)
  const idx = sessionTabs.findIndex(t => t.id === tabId);
  if (idx < 0) { const tt = toolTabById(tabId); if (tt) toolCloseTab(tt); return; } // (a tool tab: terminal_viewer.go)
  const tab = sessionTabs[idx];
  const wasShown = !!reachUI(tab);
  const grp = tgOf(tab.id), grpShown = !!grp && tgShown() === grp, wasActive = activeSessionTabId === tabId;
  if (recMediaRecorder && recTab === tab) toggleRecording();
  reachDisposeTab(tab);
  sessionTabs.splice(idx, 1);
  // Its mosaic, if any: without it (one tab left: that tab).
  const grpNext = tgOnGone(tab.id);
  // A detached window closes with its last tab (its × ends that session),
  // its tool tabs counted.
  if (reachDetachedMode && winTabCount() === 0) {
    activeSessionTabId = null; remoteWs = null; remoteDecoder = null;
    stopPerfHudTimer();
    renderSessionTabs();
    reachCloseOwnWindow();
    return;
  }
  // A member of the mosaic on screen: the mosaic goes on (or its last tab).
  if (grpShown && (grpNext || !wasActive)) {
    if (wasActive) { activeSessionTabId = null; remoteWs = null; remoteDecoder = null; stopPerfHudTimer(); tgShowTab(grpNext); } else tgRerender();
    reachUpdateClipboardWatch();
    if (reachDetachedMode) chatSyncSockets();
    return;
  }
  if (activeSessionTabId === tabId) {
    if (sessionTabs.length > 0) {
      switchSessionTab(sessionTabs[Math.min(idx, sessionTabs.length - 1)].id);
    } else if (reachDetachedMode && toolTabs.length) {
      toolShowTab(toolTabs[toolTabs.length - 1]); // (a session window: its terminal or explorer left)
    } else {
      activeSessionTabId = null;
      remoteWs = null; remoteDecoder = null;
      stopPerfHudTimer();
      if (recMediaRecorder) toggleRecording();
      if (wasShown && currentRoute === 'device-detail' && activeTab === 'remote') openDeviceDetail(tab.device, 'remote');
    }
  }
  renderSessionTabs();
  reachUpdateClipboardWatch();
  if (reachDetachedMode) { reachFocusCanvas(); chatSyncSockets(); } // the keyboard goes to the tab now on screen; its chat sockets follow its tabs
}

function addSessionTab(device) {
  // One tab per device (the same id on two servers: two devices).
  const existing = sessionTabs.find(t => devSame(t.device, device));
  if (existing) { if (existing.id !== activeSessionTabId) switchSessionTab(existing.id); return existing; }
  const tab = reachNewTab(device);
  sessionTabs.push(tab);
  tgAdoptCheck(tab); // (one of a mosaic moving here: in it again, tab_groups.go)
  activeSessionTabId = tab.id;
  renderSessionTabs();
  if (reachDetachedMode) chatSyncSockets(); // (a session window: its tabs' servers' chat sockets)
  filesDockAdoptSoon(device); // (it came from another window with its explorer's side panel: files_dock.go)
  return tab;
}

// ── Device detail ────────────────────────────────────────────────────────────
function selectDeviceInto(dev, area) {
  // A local connection: its own header (no Scripts / Info / Chat, no server).
  if (devIsLocal(dev)) { cxnSelectInto(dev, area); return; }
  if (devIsSupport(dev)) { supSelectInto(dev, area); return; } // (a code session: support_page.go)
  // Close previous chat if switching device
  // (Switching machines keeps the chats open: they live in the chat panel tabs.)
  selectedDevice = dev;

  // Show/hide chat button
  const chatBtn = document.getElementById('btn-chat');
  chatBtn.style.display = ((dev.oblireach?.online && !devIsVm(dev)) || chatConvs.size) ? '' : 'none';

  area.style.display = 'flex';
  area.style.flexDirection = 'column';
  area.style.overflow = 'hidden';

  // Header
  const hdr = document.createElement('div');
  hdr.className = 'device-header merged';
  // A Hyper-V VM console (vm_console.go): its name, its host; nothing of the
  // host's own (scripts, info, chat, tools act on the host, from its page).
  const vmc = devIsVm(dev);
  // (the Obliance agent answering: its own colour, as in the lists)
  const dc = vmc ? 'online' : devDotClass(dev);
  hdr.innerHTML = '<span class="dot ' + dc + '" style="width:9px;height:9px"></span>' +
    '<h2>' + esc(dev.hostname) + '</h2>' +
    (dev.oblireach.updateAvailable ? '<span class="tag-chip warn">' + TH('devices.updateChip') + '</span>' : '') +
    '<span class="dev-meta" style="font-size:11px;color:var(--muted)">' + (vmc ? TH('vm.headerMeta', { host: dev._vm.host }) : esc(dev.osType) + ' \u00B7 ' + esc(devStatusText(dev.status))) + '</span>';
  area.appendChild(hdr);
  const back = document.createElement('button');
  back.className = 'btn-sm'; back.style.cssText = 'padding:5px 8px'; back.title = T('detail.back');
  back.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="15 18 9 12 15 6"/></svg>';
  back.addEventListener('click', () => navigateTo('devices'));
  hdr.insertBefore(back, hdr.firstChild);

  // Scripts, Info and Chat open as side panels next to the picture, one at a
  // time (rightSlotTake): the Remote pane, and its session, stay on screen.
  // Chat: the conversation with the user of this machine (an agent online,
  // or a conversation already open with it).
  const tabs = document.createElement('div');
  tabs.className = 'tabs';
  const hdrTabs = vmc ? [] : [['scripts', T('common.scripts'), T('detail.scriptsTip')], ['info', T('common.info'), T('detail.infoTip')]];
  if (!vmc && (dev.oblireach.online || chatConvs.has(chatKey(devPid(dev), dev.uuid)))) hdrTabs.push(['chat', T('common.chat'), T('detail.chatTip')]);
  // Multi-server mode: its server, next to its name.
  if (serverMode === 'multi') {
    const chip = document.createElement('span'); chip.className = 'dev-head-srv';
    chip.innerHTML = '<span class="srv-dot" style="background:' + srvColor(devPid(dev)) + '"></span>' + esc(srvName(devPid(dev)));
    hdr.insertBefore(chip, hdr.querySelector('.dev-meta'));
  }
  hdrTabs.forEach(([k, label, tip]) => {
    const btn = document.createElement('button');
    btn.className = 'tab-btn side-tab'; btn.id = 'hdr-' + k;
    btn.textContent = label; btn.title = tip;
    btn.addEventListener('click', () => { if (k === 'chat') chatToggleFor(dev); else sidePanelToggle(k); });
    tabs.appendChild(btn);
  });
  // On the header line, right after the name (one band less above the picture)
  hdr.insertBefore(tabs, hdr.querySelector('.dev-meta'));
  // Then the Obliance agent's tools: CMD / PS or SSH, Files (agent_tools.go;
  // none with an older server).
  const toolsGroup = toolsHeaderGroup(dev);
  if (toolsGroup) hdr.insertBefore(toolsGroup, hdr.querySelector('.dev-meta'));
  // A Hyper-V host: its VMs' consoles, once its server lists them for this
  // user (vm_console.go).
  vmHeaderTools(dev, hdr);

  const tc = document.createElement('div');
  tc.className = 'tab-content';
  tc.id = 'tab-content';
  area.appendChild(tc);
  switchTab(activeTab, area);
  reachSessionChatRefresh(); // the header's Chat tab (an offline device has no toolbar)
  toolsPageOpened(dev); // (privacy mode: its unlocks read, the lock badges follow)
}

// Back-compat alias
function selectDevice(dev) { openDeviceDetail(dev); }

function switchTab(tab, area) {
  // Scripts and Info are no longer tabs that replace the Remote pane: they
  // open as a side panel, and the session stays on screen.
  if (tab === 'scripts' || tab === 'info') { sidePanelToggle(tab); return; }
  activeTab = 'remote';
  // The Remote pane is rebuilt: the session keeps running (reachRestoreView
  // re-attaches it) and its last picture is kept.
  reachStashPicture();
  const tc = document.getElementById('tab-content');
  tc.innerHTML = '';
  renderRemoteTab(tc);
  reachUpdateClipboardWatch();
  updateRailCompact();
}

// What is bound to the Remote pane's canvas stops when the pane goes away
// (another sub-tab, another page or device): HUD timer, recording, Windows
// key capture, annotation. The session itself keeps running.
function reachLeaveCanvas() {
  stopPerfHudTimer();
  if (recMediaRecorder) toggleRecording();
  if (typeof reachSetKeyCapture === 'function') reachSetKeyCapture(false);
  annotationActive = false; annotationDrawing = false; annotationHistory = [];
}

// ── Remote tab ───────────────────────────────────────────────────────────────
function renderRemoteTab(tc) {
  // A new, inactive annotation overlay is built below: the state must match
  // (a stale annotationActive silently swallows all remote input).
  annotationActive = false; annotationDrawing = false; annotationHistory = [];
  reachWinSetTitle(selectedDevice); // the slim title bar (focus mode, detached window)
  // A mosaic on screen (tab_groups.go): its tabs at once, in a grid, in place
  // of the usual single pane — whatever device each one is of.
  if (tgRenderShown(tc)) return;
  // A terminal or file explorer tab of this device on screen (terminal_viewer.go).
  const tt = toolActiveTab();
  if (tt && selectedDevice && devSame(tt.device, selectedDevice)) { toolRenderPane(tc, tt); return; }
  // A local connection with none of its tabs on screen: its tools (localconn_tools.go).
  if (devIsLocal(selectedDevice)) { cxnRenderLocalPane(tc, selectedDevice); return; }
  if (!selectedDevice?.oblireach?.online) {
    // Its terminals and explorer, through the Obliance agent (agent_tools.go),
    // when its server lists them.
    if (selectedDevice && selectedDevice._tools) { toolsRenderAgentPane(tc, selectedDevice); return; }
    tc.innerHTML = '<div class="empty"><div class="empty-icon"><svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" style="opacity:.3"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 014 10 15.3 15.3 0 01-4 10 15.3 15.3 0 01-4-10 15.3 15.3 0 014-10z"/></svg></div>' +
      '<span>' + (selectedDevice?.oblireach?.installed ? TH('remote.agentOffline') : TH('remote.agentNotInstalled')) + '</span></div>';
    return;
  }
  // Main window: this device's session runs in its own window (reach_viewer.go).
  if (reachIsDetached(selectedDevice)) { reachRenderDetachedPane(tc, selectedDevice); return; }
  const pane = document.createElement('div');
  pane.id = 'remote-pane';

  // ── Primary toolbar (connection) ──
  const toolbar = document.createElement('div');
  toolbar.className = 'remote-toolbar';

  // The device name, shown in focus mode (and so in a detached window), where
  // the device header is hidden.
  const devName = document.createElement('span');
  devName.className = 'tb-devname'; devName.id = 'tb-devname'; devName.title = selectedDevice.hostname || '';
  devName.innerHTML = '<span class="dot online"></span><span>' + esc(selectedDevice.hostname) + '</span>';
  toolbar.appendChild(devName);

  // A detached window has no session selector nor Connect / Disconnect: each
  // tab is one session, chosen before it starts (reachPickSession), and its
  // tab's × (or the window's close button) ends it.
  // A Hyper-V VM console (vm_console.go): no Windows session to pick, none of
  // ObliReach's own tools (clipboard sync, files, sound, screens, the remote
  // user's input, presence, quick actions); its own: "Type text", "Refresh".
  const vmc = devIsVm(selectedDevice);
  const sup = devIsSupport(selectedDevice); // (a code session, support_page.go: no Windows session to pick, nothing of a server's)
  if (!reachDetachedMode) {
    let sessSelect = null;
    if (!vmc && !sup) {
      const sessions = reachDevSessions(selectedDevice);
      sessSelect = document.createElement('select');
      sessSelect.className = 'session-select';
      const oa = document.createElement('option'); oa.value = ''; oa.textContent = T('remote.autoSession'); sessSelect.appendChild(oa);
      for (const s of sessions) {
        const opt = document.createElement('option'); opt.value = s.id;
        // A disconnected session has no display: the stream waits for the user.
        const st = s.state === 'Disconnected' ? T('remote.stateDisconnectedLong') : wtsStateText(s.state);
        opt.textContent = (s.username || (s.isLoginPrompt ? T('session.signInScreen') : T('session.number', { id: s.id }))) + ' (' + st + (s.stationName ? ' \u00B7 ' + s.stationName : '') + ')';
        sessSelect.appendChild(opt);
      }
      toolbar.appendChild(sessSelect);
    }

    const startBtn = document.createElement('button');
    startBtn.className = 'btn-sm';
    startBtn.style.cssText = 'background:var(--accent-fill);color:var(--on-accent)';
    startBtn.textContent = '\u25B6 ' + T('common.connect');
    // Here, or in the device's own window with "Detach by default" (Settings).
    if (sup) supToolbarStart(startBtn, selectedDevice);
    else startBtn.addEventListener('click', () => reachConnect(sessSelect && sessSelect.value ? parseInt(sessSelect.value) : undefined));
    toolbar.appendChild(startBtn);

    const stopBtn = document.createElement('button');
    stopBtn.id = 'stop-btn'; stopBtn.className = 'btn-sm';
    stopBtn.style.cssText = 'background:rgb(var(--danger-rgb) / .12);color:var(--danger);display:none';
    stopBtn.textContent = '\u25A0 ' + T('common.disconnect');
    stopBtn.addEventListener('click', stopRemote);
    toolbar.appendChild(stopBtn);

    // ── Toolbar separator ──
    const sep1 = document.createElement('div'); sep1.className = 'toolbar-sep'; toolbar.appendChild(sep1);
  }

  // ── View only: nothing typed or pointed here reaches the remote (reach_viewer.go) ──
  reachBuildViewOnlyTool(toolbar);

  if (!vmc) {
    // ── Block the remote user's mouse and keyboard (reach_viewer.go) ──
    reachBuildInputBlockTool(toolbar);

    // ── Hide the remote screen, other operators on the machine (reach_presence.go) ──
    reachBuildBlankTool(toolbar);
    reachBuildPresenceTool(toolbar);
  }

  // ── Screenshot button ──
  const ssBtn = document.createElement('button');
  ssBtn.className = 'btn-sm'; ssBtn.title = T('remote.screenshot');
  ssBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="12" cy="13" r="4"/><path d="M5 3v2M19 3v2"/></svg>';
  ssBtn.addEventListener('click', takeScreenshot);
  toolbar.appendChild(ssBtn);

  // ── Clipboard menu + send files (reach_viewer.go); a VM console: its "Type text" ──
  if (vmc) vmBuildTools(toolbar);
  else reachBuildClipboardTools(toolbar);

  // ── Password vault (vault_ui.go) ──
  if (!sup) reachBuildVaultTool(toolbar);

  // ── Separator ──
  const sep2 = document.createElement('div'); sep2.className = 'toolbar-sep'; toolbar.appendChild(sep2);

  // ── Zoom dropdown ──
  const zoomDrop = document.createElement('div'); zoomDrop.className = 'toolbar-dropdown';
  const zoomBtn = document.createElement('button');
  zoomBtn.className = 'btn-sm'; zoomBtn.id = 'zoom-btn'; zoomBtn.textContent = zoomText(zoomLevel);
  const zoomMenu = document.createElement('div'); zoomMenu.className = 'toolbar-dropdown-menu';
  ['Fit', '50%%', '75%%', '100%%', '150%%'].forEach(label => {
    const b = document.createElement('button');
    b.textContent = zoomText(label);
    b.addEventListener('click', () => { setZoomLevel(label); zoomMenu.classList.remove('open'); });
    zoomMenu.appendChild(b);
  });
  zoomBtn.addEventListener('click', () => zoomMenu.classList.toggle('open'));
  zoomDrop.appendChild(zoomBtn); zoomDrop.appendChild(zoomMenu);
  toolbar.appendChild(zoomDrop);

  if (!vmc) {
    // ── Remote screen resolution (reach_resolution.go; display_modes agents) ──
    reachBuildResolutionTool(toolbar);

    // ── Screens (mini-map) + Mosaic (reach_viewer.go; shown on multi-monitor) ──
    reachBuildViewTools(toolbar);
  }

  // ── Ctrl+Alt+Del in one click: the System Keys entry's own
  // path (sendSystemKeys: {type:'sas'}, the agent routes it through its
  // service where Windows requires it); the menu entry stays. ──
  const cadBtn = document.createElement('button');
  cadBtn.className = 'btn-sm'; cadBtn.id = 'reach-cad-btn'; cadBtn.disabled = true; // (enabled while it streams: reachRefreshToolbar)
  cadBtn.title = T('remote.key.cad'); cadBtn.setAttribute('aria-label', T('remote.key.cad'));
  cadBtn.innerHTML = REACH_ICON.cad;
  cadBtn.addEventListener('click', () => { sendSystemKeys(REACH_CAD_KEYS); });
  toolbar.appendChild(cadBtn);

  // ── System Keys dropdown ──
  const sysKeysDrop = document.createElement('div'); sysKeysDrop.className = 'toolbar-dropdown';
  const sysKeysBtn = document.createElement('button');
  sysKeysBtn.className = 'btn-sm'; sysKeysBtn.title = T('remote.systemKeys');
  // Enabled by reachRefreshToolbar only while THIS device's session streams.
  sysKeysBtn.id = 'reach-syskeys-btn'; sysKeysBtn.disabled = true;
  sysKeysBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="6" width="20" height="12" rx="2"/><line x1="6" y1="10" x2="6" y2="10"/><line x1="10" y1="10" x2="10" y2="10"/><line x1="14" y1="10" x2="14" y2="10"/><line x1="18" y1="10" x2="18" y2="10"/><line x1="8" y1="14" x2="16" y2="14"/></svg>';
  const sysKeysMenu = document.createElement('div'); sysKeysMenu.className = 'toolbar-dropdown-menu'; sysKeysMenu.id = 'reach-syskeys-menu';
  const sysKeys = [
    { label: 'remote.key.cad', keys: REACH_CAD_KEYS },
    { label: 'remote.key.altTab', keys: [{key:'Alt',code:'AltLeft'},{key:'Tab',code:'Tab'}] },
    { label: 'remote.key.altF4', keys: [{key:'Alt',code:'AltLeft'},{key:'F4',code:'F4'}] },
    { label: 'remote.key.win', keys: [{key:'Meta',code:'MetaLeft'}] },
    { label: 'remote.key.taskMgr', keys: [{key:'Control',code:'ControlLeft'},{key:'Shift',code:'ShiftLeft'},{key:'Escape',code:'Escape'}] },
    { label: 'remote.key.prtScn', keys: [{key:'PrintScreen',code:'PrintScreen'}] },
  ];
  for (const sk of sysKeys) {
    const b = document.createElement('button');
    b.textContent = T(sk.label); // (the keyboard's own names: Ctrl+Alt+Suppr...)
    b.addEventListener('click', () => { sendSystemKeys(sk.keys); sysKeysMenu.classList.remove('open'); });
    sysKeysMenu.appendChild(b);
  }
  sysKeysBtn.addEventListener('click', () => sysKeysMenu.classList.toggle('open'));
  sysKeysDrop.appendChild(sysKeysBtn); sysKeysDrop.appendChild(sysKeysMenu);
  toolbar.appendChild(sysKeysDrop);

  // ── Separator ──
  const sep3 = document.createElement('div'); sep3.className = 'toolbar-sep'; toolbar.appendChild(sep3);

  // ── Performance HUD toggle ──
  const hudBtn = document.createElement('button');
  hudBtn.className = 'btn-sm'; hudBtn.title = T('remote.hud.title'); hudBtn.id = 'hud-toggle';
  hudBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 20V10"/><path d="M18 20V4"/><path d="M6 20v-4"/></svg>';
  hudBtn.addEventListener('click', togglePerfHud);
  if (perfHudVisible) hudBtn.style.color = 'var(--accent-l)'; // HUD state survives the re-render
  toolbar.appendChild(hudBtn);

  // ── Recording button ──
  const recBtn = document.createElement('button');
  recBtn.className = 'btn-sm'; recBtn.title = T('remote.record'); recBtn.id = 'rec-toggle';
  recBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><circle cx="12" cy="12" r="4" fill="currentColor"/></svg>';
  recBtn.addEventListener('click', toggleRecording);
  toolbar.appendChild(recBtn);

  // ── Annotation toggle ──
  const annoBtn = document.createElement('button');
  annoBtn.className = 'btn-sm'; annoBtn.title = T('remote.anno.title'); annoBtn.id = 'anno-toggle';
  annoBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 19l7-7 3 3-7 7-3-3z"/><path d="M18 13l-1.5-7.5L2 2l3.5 14.5L13 18l5-5z"/><path d="M2 2l7.586 7.586"/></svg>';
  annoBtn.addEventListener('click', toggleAnnotation);
  toolbar.appendChild(annoBtn);

  // ── Lock the session when the connection ends (reach_viewer.go; lock_on_end agents) ──
  if (!vmc) reachBuildLockOnEndTool(toolbar);

  // ── Quick Actions toggle ──
  const qaBtn = document.createElement('button');
  qaBtn.className = 'btn-sm'; qaBtn.title = T('remote.qa.title'); qaBtn.id = 'qa-toggle'; qaBtn.disabled = true;
  qaBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M13 2L3 14h9l-1 8 10-12h-9l1-8z"/></svg>';
  qaBtn.addEventListener('click', toggleQuickActions);
  if (!vmc) toolbar.appendChild(qaBtn);

  // ── Remote sound (off by default) ──
  if (!vmc) reachBuildAudioTool(toolbar);

  const statusSpan = document.createElement('span');
  statusSpan.id = 'remote-status';
  statusSpan.style.cssText = 'font-size:11px;color:var(--muted);margin-left:auto';
  toolbar.appendChild(statusSpan);
  // ── Video codec, next to the readout (reach_codec.go; shown when the agent offers a choice) ──
  if (!vmc) reachBuildCodecTool(toolbar);

  // Pin (toolbar stays visible) and focus mode (the picture only)
  const tbPin = document.createElement('button');
  tbPin.className = 'btn-sm tb-pin'; tbPin.id = 'tb-pin'; tbPin.textContent = '\uD83D\uDCCC';
  tbPin.addEventListener('click', reachToggleToolbarPin);
  toolbar.appendChild(tbPin);
  const tbFocus = document.createElement('button');
  tbFocus.className = 'btn-sm tb-focus'; tbFocus.id = 'tb-focus'; tbFocus.textContent = '\u26F6';
  tbFocus.addEventListener('click', reachToggleFocus);
  toolbar.appendChild(tbFocus);
  // Detach: this session in its own window (main window only). A code
  // session stays here (its link is this window's tunnel): its path instead.
  if (sup) { supToolbarChip(toolbar, selectedDevice); supChatToolButton(toolbar); } // (its chat: support_chat.go)
  else reachBuildDetachTool(toolbar);

  // Scripts, Info and the chat with the user of THIS machine, set apart at the
  // end: in focus mode (and in a detached window) the auto-hiding toolbar is
  // the only chrome left, so they live here too (the device header and top
  // bar buttons stay as they are). They open side panels: the picture stays.
  if (!vmc && !sup) {
    const sepChat = document.createElement('div'); sepChat.className = 'toolbar-sep'; toolbar.appendChild(sepChat);
    reachBuildSideTools(toolbar);
    const chatBtn = document.createElement('button');
    chatBtn.className = 'btn-sm reach-chat-btn'; chatBtn.id = 'reach-chat-btn';
    chatBtn.title = T('chat.withUser');
    chatBtn.innerHTML = REACH_ICON.chat + '<span>' + TH('common.chat') + '</span><span class="reach-chat-dot" id="reach-chat-dot"></span>';
    chatBtn.addEventListener('click', reachSessionChat);
    toolbar.appendChild(chatBtn);
    // Files: this device's explorer (files_page.go: the Files page of the main
    // window, a tab of a session window), when its server lists the agent's tools.
    toolsSessionFilesButton(toolbar, selectedDevice);
    // A Hyper-V host: its VMs' consoles (vm_console.go; the header is hidden
    // in focus mode and in session windows).
    vmToolbarButton(toolbar, selectedDevice);
  }

  pane.appendChild(toolbar);

  // ── Viewport ──
  const vp = document.createElement('div');
  vp.className = 'remote-viewport'; vp.id = 'remote-viewport';
  const canvas = document.createElement('canvas');
  canvas.id = 'remote-canvas'; canvas.style.display = 'none';
  vp.appendChild(canvas);

  // Performance HUD overlay
  const hud = document.createElement('div');
  hud.className = 'perf-hud' + (perfHudVisible ? ' visible' : ''); hud.id = 'perf-hud';
  hud.innerHTML = '<div class="hud-row"><span class="hud-label">' + TH('remote.hud.fps') + '</span><span class="hud-val" id="hud-fps">0</span></div>' +
    '<div class="hud-row"><span class="hud-label">' + TH('remote.hud.bitrate') + '</span><span class="hud-val" id="hud-bitrate">' + TH('remote.hud.mbps', { value: 0 }) + '</span></div>' +
    '<div class="hud-row"><span class="hud-label">' + TH('remote.hud.codec') + '</span><span class="hud-val" id="hud-codec">H.264</span></div>';
  vp.appendChild(hud);

  // Recording indicator
  const recInd = document.createElement('div');
  recInd.className = 'rec-indicator'; recInd.id = 'rec-indicator';
  recInd.innerHTML = '<span class="rec-dot"></span><span id="rec-timer">00:00</span>';
  vp.appendChild(recInd);

  // Annotation canvas overlay
  const annoCanvas = document.createElement('canvas');
  annoCanvas.id = 'annotation-canvas'; annoCanvas.className = 'annotation-overlay';
  vp.appendChild(annoCanvas);

  // Annotation toolbar
  const annoToolbar = document.createElement('div');
  annoToolbar.className = 'annotation-toolbar'; annoToolbar.id = 'annotation-toolbar'; annoToolbar.style.display = 'none';
  annoToolbar.innerHTML =
    '<button data-tool="pen" class="active" title="' + TH('remote.anno.pen') + '">\u270F</button>' +
    '<button data-tool="arrow" title="' + TH('remote.anno.arrow') + '">\u2197</button>' +
    '<button data-tool="circle" title="' + TH('remote.anno.circle') + '">\u25CB</button>' +
    '<button data-tool="text" title="' + TH('remote.anno.text') + '">T</button>' +
    '<button data-tool="eraser" title="' + TH('remote.anno.eraser') + '">\u2702</button>' +
    '<input type="color" value="#ef4444" style="width:28px;height:28px;border:none;background:none;cursor:pointer;padding:0" title="' + TH('remote.anno.color') + '"/>' +
    '<button data-action="undo" title="' + TH('remote.anno.undo') + '">\u21B6</button>' +
    '<button data-action="clear" title="' + TH('common.clear') + '">\u2715</button>';
  vp.appendChild(annoToolbar);

  // Quick Actions drawer
  const qaDrawer = document.createElement('div');
  qaDrawer.className = 'quick-actions'; qaDrawer.id = 'quick-actions';
  qaDrawer.innerHTML =
    '<div class="quick-actions-header"><span>' + TH('remote.qa.title') + '</span><button class="btn-sm" style="padding:3px 6px" id="qa-close" title="' + TH('common.close') + '">&times;</button></div>' +
    '<div class="quick-actions-body">' +
    '<button class="qa-btn" data-qa="lock"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0110 0v4"/></svg>' + TH('remote.qa.lock') + '</button>' +
    '<button class="qa-btn" data-qa="taskmgr"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>' + TH('remote.qa.taskmgr') + '</button>' +
    '<button class="qa-btn" data-qa="cmd"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="4,17 10,11 4,5"/><line x1="12" y1="19" x2="20" y2="19"/></svg>' + TH('remote.qa.cmd') + '</button>' +
    '<button class="qa-btn" data-qa="powershell"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="4,17 10,11 4,5"/><line x1="12" y1="19" x2="20" y2="19"/></svg>' + TH('remote.qa.powershell') + '</button>' +
    '<button class="qa-btn" data-qa="reboot"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M23 4v6h-6"/><path d="M1 20v-6h6"/><path d="M3.51 9a9 9 0 0114.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0020.49 15"/></svg>' + TH('remote.qa.reboot') + '</button>' +
    '</div>';
  vp.appendChild(qaDrawer);

  const ph = document.createElement('div');
  ph.className = 'remote-placeholder'; ph.id = 'remote-placeholder';
  const phIcon = '<svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" style="opacity:.2"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>';
  if (reachDetachedMode) {
    // No Connect in the toolbar here: a tab left without its session (another
    // session asked for, then cancelled) connects again from the picture.
    ph.innerHTML = phIcon + '<span>' + TH('remote.notConnected') + '</span>';
    const again = document.createElement('button');
    again.className = 'btn-sm reach-btn-primary'; again.id = 'reach-reconnect'; again.textContent = T('common.connect');
    again.addEventListener('click', () => reachDetachedReconnect(reachActiveTab(), true));
    ph.appendChild(again);
  } else {
    ph.innerHTML = phIcon + '<span>' + TH('remote.clickConnect') + '</span>';
  }
  vp.appendChild(ph);
  // Mosaic tile labels, session overlay, transfer tray, drop zone, toasts.
  reachBuildViewportExtras(vp);
  if (sup) supChatBuildPanel(vp); // (a code session's chat: support_chat.go)
  pane.appendChild(vp);
  tc.appendChild(pane);
  reachWireToolbarAuto(pane, toolbar);
  reachToolbarState(false);
  reachSessionChatRefresh();

  // ── Canvas input events (pointer capture, paste sync, mosaic gaps) ──
  reachWireCanvas(canvas);

  // ── Annotation events ──
  initAnnotationEvents(annoCanvas, annoToolbar);

  // ── Quick Actions events ──
  initQuickActionsEvents(qaDrawer);

  // A session of this device may still be running (tab switch, navigation).
  reachRestoreView();
}

// Close the toolbar dropdowns on an outside click. Registered once: a listener
// added per render kept every old Remote pane (canvases included) alive.
document.addEventListener('click', e => {
  document.querySelectorAll('.toolbar-dropdown-menu.open').forEach(m => {
    if (!m.parentElement.contains(e.target)) m.classList.remove('open');
  });
});

// Remote session engine (startRemote, stopRemote, scheduleRemoteReconnect,
// handleRemoteMessage, sendInput, sendInputKey, decoders): reach_viewer.go.

// ── Feature: Screenshot ──────────────────────────────────────────────────────
function takeScreenshot() {
  const canvas = document.getElementById('remote-canvas');
  if (!canvas || canvas.style.display === 'none') return;
  canvas.toBlob(blob => {
    if (!blob) return;
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    a.download = 'screenshot-' + (selectedDevice?.hostname || 'remote') + '-' + ts + '.png';
    a.click();
    URL.revokeObjectURL(a.href);
  }, 'image/png');
}

// ── Feature: Clipboard Sync ──────────────────────────────────────────────────
// clipboardPasteToRemote / clipboardCopyFromRemote and the native clipboard
// sync live in reach_viewer.go.

// ── Feature: System Keys ─────────────────────────────────────────────────────
// Target: the given tab, else the live session ON SCREEN (reachLiveTab). Never
// the global remoteWs: it may belong to another device's background tab.
function sendSystemKeys(keys, tab) {
  tab = tab || reachLiveTab();
  const ws = tab && tab.ws;
  if (!ws || ws.readyState !== 1) return false;
  // Ctrl+Alt+Del requires SAS — send special command
  const isCAD = keys.length === 3 &&
    keys.some(k => k.key === 'Control') &&
    keys.some(k => k.key === 'Alt') &&
    keys.some(k => k.key === 'Delete');
  if (isCAD) {
    ws.send(JSON.stringify({ type: 'sas' }));
    return true;
  }
  // Agent expects: type:"key", action:"down"/"up". The flags carry the WHOLE
  // chord, like a real keyboard (while Win is held, R arrives with
  // meta:true): the agent releases a modifier that a key message says is up
  // ("stuck Ctrl" repair), so per-key flags turned Win+R into Start + "r".
  const flagOf = { Shift: 'shift', Control: 'ctrl', Alt: 'alt', Meta: 'meta' };
  const held = { shift: false, ctrl: false, alt: false, meta: false };
  const send = (action, k) => ws.send(JSON.stringify(reachKeyMsg(tab, { type: 'key', action: action, key: k.key, code: k.code, shift: held.shift, ctrl: held.ctrl, alt: held.alt, meta: held.meta })));
  for (const k of keys) {
    if (flagOf[k.key]) held[flagOf[k.key]] = true;
    send('down', k);
  }
  for (const k of [...keys].reverse()) {
    if (flagOf[k.key]) held[flagOf[k.key]] = false;
    send('up', k);
  }
  return true;
}

// Helper: type a string character by character via key events (same target
// rule as sendSystemKeys).
function typeString(str, tab) {
  tab = tab || reachLiveTab();
  const ws = tab && tab.ws;
  if (!ws || ws.readyState !== 1) return false;
  for (const ch of str) {
    const key = ch === '\r' ? 'Enter' : ch;
    const code = ch === '\r' ? 'Enter' : ch === ' ' ? 'Space' : ch === '-' ? 'Minus' : ch === '.' ? 'Period' : ch === '/' ? 'Slash' : ch === '\\' ? 'Backslash' : 'Key' + ch.toUpperCase();
    ws.send(JSON.stringify(reachKeyMsg(tab, { type: 'key', action: 'down', key, code, shift: false, ctrl: false, alt: false, meta: false })));
    ws.send(JSON.stringify(reachKeyMsg(tab, { type: 'key', action: 'up', key, code, shift: false, ctrl: false, alt: false, meta: false })));
  }
  return true;
}

// ── Feature: Performance HUD ─────────────────────────────────────────────────
function togglePerfHud() {
  perfHudVisible = !perfHudVisible;
  const hud = document.getElementById('perf-hud');
  const btn = document.getElementById('hud-toggle');
  if (hud) hud.classList.toggle('visible', perfHudVisible);
  if (btn) btn.style.color = perfHudVisible ? 'var(--accent-l)' : '';
}

function startPerfHudTimer() {
  stopPerfHudTimer();
  perfFrameCount = 0; perfByteCount = 0; perfLastTime = performance.now();
  perfInterval = setInterval(() => {
    const now = performance.now();
    const elapsed = (now - perfLastTime) / 1000;
    if (elapsed > 0) {
      perfFps = Math.round(perfFrameCount / elapsed);
      perfBitrate = i18nNum((perfByteCount * 8) / elapsed / 1000000, 1, 1); // (the language's decimal separator)
    }
    perfFrameCount = 0; perfByteCount = 0; perfLastTime = now;
    const fpsEl = document.getElementById('hud-fps');
    const brEl = document.getElementById('hud-bitrate');
    const codecEl = document.getElementById('hud-codec');
    if (fpsEl) {
      fpsEl.textContent = perfFps;
      fpsEl.className = 'hud-val' + (perfFps < 5 ? ' hud-bad' : perfFps < 15 ? ' hud-warn' : '');
    }
    if (brEl) brEl.textContent = T('remote.hud.mbps', { value: perfBitrate });
    if (codecEl) codecEl.textContent = perfCodec;
  }, 500);
}

function stopPerfHudTimer() {
  if (perfInterval) { clearInterval(perfInterval); perfInterval = null; }
}

// ── Feature: Zoom/Scale ──────────────────────────────────────────────────────
// A zoom level on screen: 'Fit' (the value the viewer compares with) in words.
function zoomText(label) { return label === 'Fit' ? T('remote.zoomFit') : label; }
function setZoomLevel(label) {
  zoomLevel = label;
  const btn = document.getElementById('zoom-btn');
  if (btn) btn.textContent = zoomText(label);
  const vp = document.getElementById('remote-viewport');
  const canvas = document.getElementById('remote-canvas');
  if (!vp || !canvas) return;
  if (label === 'Fit') {
    vp.classList.remove('zoom-scroll');
    canvas.style.width = '100%%'; canvas.style.height = '100%%';
    canvas.style.objectFit = 'contain';
  } else {
    vp.classList.add('zoom-scroll');
    const pct = parseInt(label) / 100;
    canvas.style.width = Math.round(canvas.width * pct) + 'px';
    canvas.style.height = Math.round(canvas.height * pct) + 'px';
    canvas.style.objectFit = 'unset';
  }
  // Mosaic labels follow the picture (the canvas is always frame-sized, so the
  // pointer mapping in sendInput needs nothing else).
  const t = reachDisplayedTab(); if (t) reachRenderTiles(t);
}

// ── Feature: Session Recording ───────────────────────────────────────────────
function toggleRecording() {
  const recInd = document.getElementById('rec-indicator');
  const recBtn = document.getElementById('rec-toggle');
  if (recMediaRecorder) {
    // Stop recording
    recMediaRecorder.stop();
    recMediaRecorder = null;
    clearInterval(recTimerInterval); recTimerInterval = null;
    if (recInd) recInd.classList.remove('active');
    if (recBtn) recBtn.style.color = '';
    // Tell the agent of the RECORDED session (not whatever is on screen now)
    // to switch its watermark back to LIVE.
    if (recTab) reachSend(recTab, { type: 'set_recording', recording: false });
    recTab = null;
    return;
  }
  // Start recording (only the live session on screen)
  const canvas = document.getElementById('remote-canvas');
  if (!canvas || canvas.style.display === 'none') return;
  const liveTab = reachLiveTab();
  if (!liveTab) return;
  try {
    const stream = canvas.captureStream(15);
    recMediaRecorder = new MediaRecorder(stream, { mimeType: 'video/webm;codecs=vp9' });
  } catch {
    try {
      const stream = canvas.captureStream(15);
      recMediaRecorder = new MediaRecorder(stream, { mimeType: 'video/webm' });
    } catch (err) { console.warn('Recording not supported', err); return; }
  }
  recChunks = [];
  recStartTime = Date.now();
  recMediaRecorder.ondataavailable = e => { if (e.data.size > 0) recChunks.push(e.data); };
  recMediaRecorder.onstop = () => {
    if (recChunks.length === 0) return;
    const blob = new Blob(recChunks, { type: 'video/webm' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    a.download = 'recording-' + (selectedDevice?.hostname || 'remote') + '-' + ts + '.webm';
    a.click();
    URL.revokeObjectURL(a.href);
    recChunks = [];
  };
  recMediaRecorder.start(1000);
  if (recInd) recInd.classList.add('active');
  if (recBtn) recBtn.style.color = 'var(--danger)';
  // Tell agent to switch watermark to REC
  recTab = liveTab;
  reachSend(recTab, { type: 'set_recording', recording: true });
  recTimerInterval = setInterval(() => {
    const elapsed = Math.floor((Date.now() - recStartTime) / 1000);
    const mm = String(Math.floor(elapsed / 60)).padStart(2, '0');
    const ss = String(elapsed %% 60).padStart(2, '0');
    const timerEl = document.getElementById('rec-timer');
    if (timerEl) timerEl.textContent = mm + ':' + ss;
  }, 1000);
}

// ── Feature: Quick Actions ───────────────────────────────────────────────────
function toggleQuickActions() {
  const drawer = document.getElementById('quick-actions');
  if (drawer) drawer.classList.toggle('open');
}

function initQuickActionsEvents(drawer) {
  drawer.querySelector('#qa-close')?.addEventListener('click', () => drawer.classList.remove('open'));
  drawer.querySelectorAll('.qa-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const action = btn.dataset.qa;
      drawer.classList.remove('open');
      // The live session ON SCREEN only, pinned for the delayed typing: a
      // background tab of another device never receives these keystrokes.
      const tab = reachLiveTab();
      if (!tab) { reachToast(T('remote.noLiveSession'), 'warn'); return; }
      const ws = tab.ws;
      const runDialog = (text) => {
        if (!sendSystemKeys([{key:'Meta',code:'MetaLeft'},{key:'r',code:'KeyR'}], tab)) return;
        setTimeout(() => { if (tab.ws === ws) typeString(text, tab); }, 600);
      };
      if (action === 'lock') sendSystemKeys([{key:'Meta',code:'MetaLeft'},{key:'l',code:'KeyL'}], tab);
      else if (action === 'taskmgr') sendSystemKeys([{key:'Control',code:'ControlLeft'},{key:'Shift',code:'ShiftLeft'},{key:'Escape',code:'Escape'}], tab);
      else if (action === 'cmd') runDialog('cmd\r');
      else if (action === 'powershell') runDialog('powershell\r');
      else if (action === 'reboot') {
        // The name shown is the device the keystrokes go to; still the same
        // live session after the (blocking) confirmation.
        if (confirm(T('remote.qa.rebootConfirm', { device: tab.device.hostname || T('common.thisDevice') })) && tab.ws === ws && reachLiveTab() === tab) runDialog('shutdown -r -t 0\r');
      }
    });
  });
}

// ── Feature: Annotation/Whiteboard ───────────────────────────────────────────
function toggleAnnotation() {
  annotationActive = !annotationActive;
  const canvas = document.getElementById('annotation-canvas');
  const toolbar = document.getElementById('annotation-toolbar');
  const btn = document.getElementById('anno-toggle');
  if (canvas) canvas.classList.toggle('active', annotationActive);
  if (toolbar) toolbar.style.display = annotationActive ? 'flex' : 'none';
  if (btn) btn.style.color = annotationActive ? 'var(--accent-l)' : '';
  if (annotationActive) {
    const vp = document.getElementById('remote-viewport');
    if (vp && canvas) { canvas.width = vp.clientWidth; canvas.height = vp.clientHeight; }
    annotationCtx = canvas?.getContext('2d');
  }
}

function initAnnotationEvents(canvas, toolbar) {
  let startX, startY, lastImageData;

  canvas.addEventListener('mousedown', e => {
    if (!annotationActive) return;
    annotationDrawing = true;
    startX = e.offsetX; startY = e.offsetY;
    if (annotationTool === 'pen' || annotationTool === 'eraser') {
      annotationCtx.beginPath();
      annotationCtx.moveTo(startX, startY);
    }
    if (annotationTool === 'arrow' || annotationTool === 'circle') {
      lastImageData = annotationCtx.getImageData(0, 0, canvas.width, canvas.height);
    }
    if (annotationTool === 'text') {
      const text = prompt(T('remote.anno.enterText'));
      if (text) {
        annotationCtx.font = '16px sans-serif';
        annotationCtx.fillStyle = annotationColor;
        annotationCtx.fillText(text, startX, startY);
        annotationHistory.push(annotationCtx.getImageData(0, 0, canvas.width, canvas.height));
      }
      annotationDrawing = false;
    }
  });

  canvas.addEventListener('mousemove', e => {
    if (!annotationDrawing || !annotationActive) return;
    const x = e.offsetX, y = e.offsetY;
    if (annotationTool === 'pen') {
      annotationCtx.strokeStyle = annotationColor;
      annotationCtx.lineWidth = 3;
      annotationCtx.lineCap = 'round';
      annotationCtx.lineTo(x, y);
      annotationCtx.stroke();
    } else if (annotationTool === 'eraser') {
      annotationCtx.strokeStyle = 'rgba(0,0,0,1)';
      annotationCtx.globalCompositeOperation = 'destination-out';
      annotationCtx.lineWidth = 20;
      annotationCtx.lineCap = 'round';
      annotationCtx.lineTo(x, y);
      annotationCtx.stroke();
      annotationCtx.globalCompositeOperation = 'source-over';
    } else if (annotationTool === 'arrow' && lastImageData) {
      annotationCtx.putImageData(lastImageData, 0, 0);
      annotationCtx.strokeStyle = annotationColor;
      annotationCtx.lineWidth = 3;
      annotationCtx.beginPath();
      annotationCtx.moveTo(startX, startY);
      annotationCtx.lineTo(x, y);
      annotationCtx.stroke();
      // Arrowhead
      const angle = Math.atan2(y - startY, x - startX);
      annotationCtx.beginPath();
      annotationCtx.moveTo(x, y);
      annotationCtx.lineTo(x - 12 * Math.cos(angle - 0.5), y - 12 * Math.sin(angle - 0.5));
      annotationCtx.moveTo(x, y);
      annotationCtx.lineTo(x - 12 * Math.cos(angle + 0.5), y - 12 * Math.sin(angle + 0.5));
      annotationCtx.stroke();
    } else if (annotationTool === 'circle' && lastImageData) {
      annotationCtx.putImageData(lastImageData, 0, 0);
      annotationCtx.strokeStyle = annotationColor;
      annotationCtx.lineWidth = 3;
      const rx = Math.abs(x - startX) / 2, ry = Math.abs(y - startY) / 2;
      const cx = (startX + x) / 2, cy = (startY + y) / 2;
      annotationCtx.beginPath();
      annotationCtx.ellipse(cx, cy, rx, ry, 0, 0, 2 * Math.PI);
      annotationCtx.stroke();
    }
  });

  canvas.addEventListener('mouseup', () => {
    if (annotationDrawing && annotationActive) {
      annotationDrawing = false;
      annotationHistory.push(annotationCtx.getImageData(0, 0, canvas.width, canvas.height));
    }
  });

  toolbar.querySelectorAll('button[data-tool]').forEach(btn => {
    btn.addEventListener('click', () => {
      annotationTool = btn.dataset.tool;
      toolbar.querySelectorAll('button[data-tool]').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
    });
  });

  toolbar.querySelector('input[type="color"]')?.addEventListener('input', e => { annotationColor = e.target.value; });

  toolbar.querySelector('button[data-action="undo"]')?.addEventListener('click', () => {
    if (annotationHistory.length > 0) {
      annotationHistory.pop();
      if (annotationHistory.length > 0) {
        annotationCtx.putImageData(annotationHistory[annotationHistory.length - 1], 0, 0);
      } else {
        annotationCtx.clearRect(0, 0, canvas.width, canvas.height);
      }
    }
  });

  toolbar.querySelector('button[data-action="clear"]')?.addEventListener('click', () => {
    annotationCtx?.clearRect(0, 0, canvas.width, canvas.height);
    annotationHistory = [];
  });
}

// ── Scripts / Info / Files side panel ────────────────────────────────────────
// A drawer on the right, next to the picture (like the chat panel), instead
// of tabs that replaced the Remote pane: the session stays on screen and
// connected. It shows the device on screen, follows it from device to
// device and closes on the other pages. Opened from the device header and from
// the remote toolbar (focus mode, detached windows); Files from a session's
// Files button (files_page.go toolsSessionFilesButton, files_dock.go).
//
// Scripts, Info, Files and Chat share ONE slot on the right (one or the
// other, not side by side), in the main window and in the session windows:
// opening one closes the other (rightSlotTake; Scripts, Info and Files are
// the same panel), the open one's button closes it, and each keeps its state
// meanwhile: the chat its conversations (the panel is only hidden), Scripts
// the script picked, a run and its output (sidePanelScripts: its elements
// are kept, per server and device), Files its explorer (files_dock.go: it
// lives as long as the device has a session in this window, on its folder,
// its transfers going on).
let sidePanelKind = null;   // 'scripts' | 'info' | 'files' | null: what the panel shows (or will show)
let sidePanelShown = null;  // what it shows now
let sidePanelDeviceId = null;
let sidePanelShownScripts = null; // the sidePanelScripts entry on screen
const sidePanelScripts = new Map(); // device key (devKey: '<server>:<device id>') -> { scripts, nodes, picked, scroll }
function sidePanelOpen() { const p = document.getElementById('side-panel'); return !!(p && p.classList.contains('open')); }
function sidePanelToggle(kind) {
  // Files: the session's explorer (opened first when it has none: files_dock.go).
  if (kind === 'files') { if (selectedDevice) filesSessionOpen(selectedDevice); return; }
  if (kind !== 'scripts' && kind !== 'info') return;
  if (sidePanelOpen() && sidePanelShown === kind && selectedDevice && sidePanelDeviceId === devKey(selectedDevice)) { sidePanelClose(); return; }
  sidePanelShow(kind);
}
function sidePanelShow(kind) {
  sidePanelKind = kind;
  sidePanelRender();
}
function sidePanelClose() {
  sidePanelKeep();
  infoLiveClose(); // (its live metrics stop: agent_tools.go)
  if (typeof cxnInfoClose === 'function') cxnInfoClose(); // (a local connection's: localconn_info.go)
  if (sidePanelShown === 'files') filesPanelLeave(); // (its explorer kept: files_dock.go)
  sidePanelKind = null; sidePanelShown = null; sidePanelDeviceId = null; sidePanelShownScripts = null;
  const p = document.getElementById('side-panel');
  p.classList.remove('open'); p.classList.remove('sp-files');
  document.getElementById('side-panel-body').innerHTML = '';
  document.getElementById('side-panel-acts').innerHTML = '';
  sidePanelButtons();
}
function sidePanelRender() {
  const dev = selectedDevice;
  if (!sidePanelKind || !dev || currentRoute !== 'device-detail') { sidePanelClose(); return; }
  const panel = document.getElementById('side-panel');
  const body = document.getElementById('side-panel-body');
  const acts = document.getElementById('side-panel-acts');
  if (sidePanelKind === 'files') {
    // The explorer of the session on screen (files_dock.go); a device without
    // one (another session of this window, a mosaic): the panel hidden, Files
    // still chosen (back on the device that has one: there again).
    const d = filesDockFor();
    if (!d) { sidePanelClose(); sidePanelKind = 'files'; return; }
    rightSlotTake('side');
    // (moved in when it changed, not at every call: the operator may be
    // typing in its filter)
    if (!(sidePanelShown === 'files' && sidePanelDeviceId === devKey(dev) && d.box && d.box.parentElement === body && sidePanelOpen())) {
      sidePanelKeep();
      infoLiveClose(); if (typeof cxnInfoClose === 'function') cxnInfoClose(); // (Info left the panel)
      if (sidePanelShown === 'files') filesPanelLeave();
      body.innerHTML = '';
      sidePanelShown = 'files'; sidePanelDeviceId = devKey(dev); sidePanelShownScripts = null;
      filesPanelEnter(d, body);
    }
    document.getElementById('side-panel-title').textContent = T('tools.files');
    document.getElementById('side-panel-sub').textContent = dev.hostname || '';
    filesPanelHead(d, acts);
    panel.classList.add('sp-files'); panel.classList.add('open');
    sidePanelButtons();
    return;
  }
  rightSlotTake('side');
  sidePanelKeep();
  if (sidePanelShown === 'files') filesPanelLeave();
  sidePanelShown = sidePanelKind; sidePanelDeviceId = devKey(dev); sidePanelShownScripts = null;
  document.getElementById('side-panel-title').textContent = sidePanelKind === 'scripts' ? T('common.scripts') : T('sidePanel.deviceInfo');
  document.getElementById('side-panel-sub').textContent = dev.hostname || '';
  acts.innerHTML = '';
  panel.classList.remove('sp-files');
  body.innerHTML = '';
  if (sidePanelKind === 'scripts') {
    infoLiveClose(); // (Info left the panel)
    const s = sidePanelScriptsFor(dev, true); // (opening, or another device: its list read again unless just read)
    for (const n of s.nodes) body.appendChild(n);
    body.scrollTop = s.scroll || 0;
    sidePanelShownScripts = s;
  } else renderInfoTab(body);
  panel.classList.add('open');
  sidePanelButtons();
}
// After a page change (navigateTo). A local connection: its Info and its
// explorer (an SSH connection's SFTP), no scripts.
function sidePanelSync() {
  if (!sidePanelKind) { sidePanelButtons(); return; }
  if (currentRoute !== 'device-detail' || !selectedDevice || devIsVm(selectedDevice) || (devIsLocal(selectedDevice) && sidePanelKind === 'scripts')) { sidePanelClose(); return; }
  if (sidePanelKind === 'files') { sidePanelRender(); return; } // (its explorer: the device's, or hidden)
  if (!sidePanelOpen() || sidePanelShown !== sidePanelKind || sidePanelDeviceId !== devKey(selectedDevice)) sidePanelRender();
  else sidePanelButtons();
}
// Header tabs and toolbar buttons show which panel is open.
function sidePanelButtons() {
  const open = sidePanelOpen();
  for (const k of ['scripts', 'info']) {
    const on = open && sidePanelShown === k;
    document.getElementById('hdr-' + k)?.classList.toggle('active', on);
    document.getElementById('tb-' + k)?.classList.toggle('on', on);
  }
  filesDockButtons(); // (the session's Files button: files_dock.go)
}
document.getElementById('side-panel-close').addEventListener('click', sidePanelClose);

// The one slot on the right: 'side' (Scripts / Info) or 'chat' takes it,
// the other panel closes (the chat panel is only hidden: its conversations
// go on). One panel replaces the other at once: both widths animating
// together would make the picture shrink and grow again (two refits).
function rightSlotTake(kind) {
  // (Files chosen, hidden on a device without an explorer: the chat takes the slot)
  if (kind === 'chat' && !sidePanelOpen()) sidePanelKind = null;
  const other = kind === 'chat' ? sidePanelOpen() : chatPanelOpen();
  if (!other) return;
  document.body.classList.add('rp-swap');
  setTimeout(() => document.body.classList.remove('rp-swap'), 80);
  if (kind === 'chat') sidePanelClose(); else chatPanelHide();
}

// Scripts leave the panel (another panel, another device, closed): their
// elements are kept for the next time, with the scroll position.
function sidePanelKeep() {
  const s = sidePanelShownScripts;
  if (!s || sidePanelShown !== 'scripts') return;
  const body = document.getElementById('side-panel-body');
  if (body) s.scroll = body.scrollTop || 0;
}
// The Scripts of a device, the panel's elements and the runs:
// sidePanelScriptsFor / sidePanelForget / renderScriptsPanel /
// showScriptDetail / executeScript, scripts_panel.go.

// ── Info tab ─────────────────────────────────────────────────────────────────
function renderInfoTab(tc) {
  if (!selectedDevice) return;
  const d = selectedDevice;
  if (devIsLocal(d)) { infoLiveClose(); cxnRenderInfo(tc, d); return; } // (a local connection: localconn_info.go)
  if (typeof cxnInfoClose === 'function') cxnInfoClose();
  let html = '<div style="padding:16px;display:flex;flex-direction:column;gap:10px;font-size:13px">';
  if (d.oblireach.updateAvailable) {
    html += '<div style="background:rgb(var(--warn-rgb) / .1);border-radius:10px;padding:10px 14px;display:flex;align-items:center;gap:8px">' +
      '<span style="color:var(--warn);font-weight:600;font-size:12px">' + TH('info.updateAvailable') + '</span>' +
      '<span style="font-size:11px;color:var(--muted)">' + esc(d.oblireach.version || '?') + '</span></div>';
  }
  // key: the label's catalog key ("Hostname" -> "Hostname: ").
  const row = (key, val) => '<div><span style="color:var(--muted2)">' + TH('info.rowLabel', { label: T(key) }) + ' </span>' + val + '</div>';
  html += row('info.hostname', esc(d.hostname));
  if (serverMode === 'multi') {
    // Its server and tenant (several of each are on screen).
    const pid = devPid(d), tid = devTenant(d);
    html += row('info.server', '<span class="info-srv"><span class="srv-dot" style="background:' + srvColor(pid) + '"></span>' + esc(srvName(pid)) + '</span>');
    if (tid !== null) {
      const t = srvState(pid).tenants.find(x => Number(x.id) === tid);
      html += row('info.tenant', esc(t ? String(t.name) : T('topbar.tenantFallback', { id: tid })));
    }
  }
  const lastUser = devLastUser(d); // reported by the agent: escaped like the sessions below
  if (lastUser) html += row('devices.lastUser', esc(lastUser));
  html += row('info.deviceId', esc(String(d.id)));
  html += row('info.uuid', '<span style="font-family:monospace;font-size:11px">' + esc(d.uuid) + '</span>');
  html += toolsOsRows(d, row); // name, version, Windows build (agent_tools.go)
  html += toolsNetRowsHost(d); // LAN / WAN addresses, filled once read (agent_tools.go)
  html += row('info.status', esc(devStatusText(d.status)));
  html += row('info.oblireach', (d.oblireach.installed ? (d.oblireach.online ? '<span style="color:var(--success)">' + TH('info.online') + '</span>' : TH('info.offline')) : TH('info.notInstalled')) +
    (d.oblireach.version ? ' <span style="font-size:11px;color:var(--muted)">v' + esc(d.oblireach.version) + '</span>' : ''));
  html += toolsInfoRows(d, row); // the Obliance agent, privacy mode (agent_tools.go)
  const sessList = reachDevSessions(d);
  if (sessList.length) {
    // Reported by the machine's agent: never trusted as markup.
    html += row('info.sessions', sessList.map(s => esc(s.username) + ' (' + esc(wtsStateText(s.state)) + ')').join(', '));
  }
  html += '</div>';
  tc.innerHTML = html;
  infoLiveOpen(d, tc); // live CPU / RAM / disks while the panel shows it (agent_tools.go)
  infoNetLoad(d, row);
}

// ── Socket.io / Chat ─────────────────────────────────────────────────────────
let socketState = 'idle';      // idle | loading | connecting | connected | error
let socketLoadStarted = false;

function setChatHeaderStatus(text, color) {
  for (const v of chatViews()) {
    if (v.txt) v.txt.textContent = text;
    if (v.dot) v.dot.style.background = color;
  }
}

// One chat socket per server profile (/sp/<id>/socket.io, servers.go): the
// conversations of a server keep running while another server is on screen.
// The identity is the proxied session's (the server never trusts auth.userId).
const chatSockets = {}; // profile id -> socket.io client
function chatSock(pid) { return chatSockets[pid || ''] || null; }

// ── Aggregated chat: a socket for EVERY server signed in ──
// The main window keeps one chat socket per server signed in, in both modes
// (the active one, the others probed at start-up: chatProbeServers), not only
// for the server on screen and the servers of an open tab: a chat opened for
// this user from another window (Obliance web) on any of its servers gets its
// tab here (chat:opened), its server's colour on it. chatSyncSockets opens
// what is missing and lets go of the sockets of a server signed out or
// removed (its tabs stay: see chatServerSignedOut). A session window keeps
// the sockets of the servers of its own tabs only (chatSyncDetached).
let chatSyncOn = false; // (from the app's first initSocketIO: no socket before)
// A socket the server refused (expired session: socket.io gives up on it) is
// tried again after CHAT_RETRY_MS (the last one repeated; back to the first at
// a connect), once the server's session is known to be alive.
const CHAT_RETRY_MS = [3000, 10000, 30000, 60000];
const chatRetry = {}; // profile id -> { n: tries so far, timer }
function chatRetryWaiting(pid) { const r = chatRetry[pid || '']; return !!(r && r.timer); }
function chatRetryClear(pid) {
  const r = chatRetry[pid || ''];
  if (r && r.timer) clearTimeout(r.timer);
  delete chatRetry[pid || ''];
}
function chatRetrySchedule(pid) {
  const key = pid || '';
  const r = chatRetry[key] || (chatRetry[key] = { n: 0, timer: null });
  if (r.timer) return;
  const ms = CHAT_RETRY_MS[Math.min(r.n, CHAT_RETRY_MS.length - 1)];
  r.n++;
  r.timer = setTimeout(() => { r.timer = null; if (chatRetry[key] === r) chatSocketRecheck(key, r); }, ms);
}
// The refused socket's server: signed out (its tabs wait for the next sign-in),
// or still signed in (a new socket), or unreachable (tried again later).
async function chatSocketRecheck(key, r) {
  const cur = chatSockets[key];
  if (cur && cur.active) return; // (a new one meanwhile: its connect starts the delays over)
  if (chatSocketUnwanted(key)) { chatRetryClear(key); return; } // (signed out, removed meanwhile)
  let status = 0;
  try { const resp = await fetch(srvBase(key) + '/api/auth/me'); status = resp ? resp.status : 0; } catch (e) { status = 0; }
  if (chatRetry[key] !== r) return; // (signed out, removed, or connected meanwhile)
  const now = chatSockets[key];
  if (now && now.active) return;
  if (status === 401 || status === 403) { chatServerSignedOut(key); return; }
  if (status >= 200 && status < 300) { chatSocketFor(key); return; } // (refused again: the next delay)
  chatRetrySchedule(key);
}
// A server known not to want a socket: removed, no session kept (the app
// knows it has no cookie for it), or signed out.
function chatSocketUnwanted(pid) {
  const key = pid || '';
  if (key && serverProfiles.length && !serverProfiles.some(p => p.id === key)) return true;
  const prof = key ? serverProfiles.find(p => p.id === key) : null;
  if (prof && (prof.local || prof.hasSession === false)) return true; // (a local base: no server, no chat)
  const st = srv.get(key);
  return !!(st && st.signedIn === false);
}
function chatSyncSockets() {
  if (!chatSyncOn || !window.io) return;
  if (reachDetachedMode) { chatSyncDetached(); return; }
  const want = new Set();
  const act = activeServerId || '';
  if (!chatSocketUnwanted(act)) want.add(act);
  for (const [pid, st] of srv) if (st && st.signedIn === true && !chatSocketUnwanted(pid)) want.add(pid);
  for (const pid of want) {
    if (chatRetryWaiting(pid)) continue; // (refused: its next try is on its way)
    const s = chatSockets[pid];
    if (!s || !s.active) chatSocketFor(pid);
  }
  for (const pid of Object.keys(chatSockets)) {
    if (want.has(pid) || !chatSocketUnwanted(pid)) continue;
    chatRetryClear(pid);
    chatSocketLeave(pid); // (its tabs, if any, wait for its next sign-in)
    chatRender();
  }
}
// A session window: its tabs' servers (and those of the chats it shows).
function chatSyncDetached() {
  const want = new Set();
  if (reachDetachedDevice) want.add(devPid(reachDetachedDevice));
  for (const t of sessionTabs) if (t && t.device) want.add(devPid(t.device));
  for (const t of toolTabs) if (t && t.device) want.add(devPid(t.device)); // (its terminals' and explorers' servers)
  for (const pid of want) {
    if (chatSocketUnwanted(pid) || chatRetryWaiting(pid)) continue;
    const s = chatSockets[pid];
    if (!s || !s.active) chatSocketFor(pid);
  }
  for (const pid of Object.keys(chatSockets)) {
    if (want.has(pid)) continue;
    let used = false;
    for (const c of chatConvs.values()) if (c.pid === pid) used = true;
    if (!used) { chatRetryClear(pid); chatDropSocket(pid); }
  }
}
// Single mode, main window: the other servers with a session kept are asked
// for their account (the probe), so their chats come here too.
function chatProbeServers() {
  if (serverMode === 'multi' || reachDetachedMode) return;
  for (const p of serverProfiles) {
    if (!p || p.id === (activeServerId || '') || p.hasSession !== true) continue;
    srvLoad(p.id, { overview: false }).then(() => chatSyncSockets()).catch(() => {});
  }
}
// A server's session is over (its refused socket's probe answered 401/403):
// its socket goes; its tabs stay, waiting (their chats are rejoined by the
// socket of the next sign-in: chatRejoin, then chat:list); in multi-server
// mode its section shows its "Sign in".
function chatServerSignedOut(pid) {
  const key = pid || '';
  chatRetryClear(key);
  const st = srvState(key);
  st.seq++; st.signedIn = false; st.me = null; st.mePayload = null; st.groups = []; st.tenants = []; st.scoped = false; st.error = '';
  chatSocketLeave(key);
  chatRender();
  if (serverMode === 'multi' && !reachDetachedMode && appReady) {
    try { overviewRebuild(); overviewRedraw(); renderTenantSwitcher(); } catch (e) {}
  }
}
// A signed-out server's socket goes; its conversations stay, waiting: a live
// chat is rejoined by the next sign-in's socket (chatRejoin), one that was
// opening is opened then (pending), each told once.
function chatSocketLeave(pid) {
  const key = pid || '';
  chatDropSocket(key);
  for (const c of chatConvs.values()) {
    if (c.pid !== key || c.ended) continue;
    if (c.chatId) { c.rejoin = true; c.connected = false; }
    else if (c.opening) { c.attempt = (c.attempt || 0) + 1; c.opening = false; c.pending = true; }
    // (once until its server's next socket connects)
    if ((c.chatId || c.pending) && !c.signedOutNoted) { c.signedOutNoted = true; chatAddMsg(c, 'System', { k: 'chat.sys.signedOut' }, true); }
  }
}

function initSocketIO() {
  chatSyncOn = true;
  if (window.io) { chatSyncSockets(); return; }
  if (socketLoadStarted) return;
  socketLoadStarted = true;
  socketState = 'loading';
  // Load socket.io client from our own embedded bundle (version pinned to
  // match the Obliance server's socket.io version). Avoids depending on the
  // upstream serving /socket.io/socket.io.js.
  const s = document.createElement('script');
  s.src = '/local/socket.io.min.js';
  s.onload = () => {
    if (!window.io) {
      socketState = 'error';
      console.warn('socket.io: client loaded but window.io missing');
      setChatHeaderStatus(T('chat.realtimeUnavailable'), 'var(--danger)');
      return;
    }
    chatSyncSockets();
    chatOpenPending();
  };
  s.onerror = () => {
    socketState = 'error';
    console.warn('socket.io client script failed to load');
    setChatHeaderStatus(T('chat.realtimeUnavailable'), 'var(--danger)');
  };
  document.head.appendChild(s);
}

function chatRejoin(key, sock) {
  for (const c of chatConvs.values()) {
    if (c.pid !== key || !c.rejoin || !c.chatId || c.ended) continue;
    c.rejoin = false;
    const attempt = c.attempt = (c.attempt || 0) + 1;
    let answered = false;
    const reopen = () => {
      if (!chatConvs.has(c.key) || c.attempt !== attempt) return;
      c.chatId = null; c.connected = false; c.pending = true; c.userClosed = false; c.remoteAsked = false;
      chatAddMsg(c, 'System', { k: 'chat.sys.resumeFailed' }, true);
      chatStart(c);
    };
    // A server of the unified chat answers {ok, history} (the messages missed
    // while disconnected are added) or {ok: false, ended} (the chat ended
    // meanwhile: never opened again, the user closed that window).
    sock.emit('join', 'chat:' + c.chatId, (res) => {
      answered = true;
      if (chatConvs.get(c.key) !== c || c.attempt !== attempt) return;
      if (res && res.ok) {
        c.connected = true;
        if (Array.isArray(res.history)) sock._unified = true;
        const missed = chatMergeHistory(c, c.chatId, res.history);
        chatAddMsg(c, 'System', { k: 'chat.sys.reconnected' }, true);
        if (missed) chatAttention(c, T('chat.attn.newMessage'), missed); else chatRender();
      }
      else if (res && res.ended) { sock._unified = true; chatMarkEnded(c, res.ended, res); }
      // The right to the chat was lost (capability 'remote', tenant): opening
      // it again would be refused too.
      else if (res && res.error === 'forbidden') { sock._unified = true; chatLost(c, chatErrorText('forbidden')); }
      else reopen();
    });
    setTimeout(() => { if (!answered) reopen(); }, 3000);
  }
}

function chatSocketFor(pid) {
  const key = pid || '';
  // A socket the server refused (expired session: socket.io destroys it, no
  // more reconnects) or one disconnected on purpose is replaced, never reused.
  const cur = chatSockets[key];
  if (cur && cur.active) return cur;
  if (cur) chatDropSocket(key);
  if (!window.io) { initSocketIO(); return null; }
  socketState = 'connecting';
  const sock = io({
    path: pid ? '/sp/' + pid + '/socket.io' : '/proxy/socket.io',
    transports: ['polling', 'websocket'],
    reconnection: true,
    forceNew: true,
  });
  chatSockets[key] = sock;
  toolsWireSocket(sock, key); // (the terminals' session updates, the explorer's results: terminal_viewer.go)
  // At every connect: rejoin, start what waited for realtime, then ask the
  // server for this user's live chats (opened from another window meanwhile).
  sock.on('connect', () => {
    socketState = 'connected';
    chatRetryClear(key); // (tries of a refused socket: from the first delay again next time)
    for (const c of chatConvs.values()) if (c.pid === key) c.signedOutNoted = false;
    chatRejoin(key, sock); chatOpenPending(); chatListSync(key, sock); chatRender();
  });
  // The server keeps each chat (chatId -> machine, opener) across reconnects:
  // after a drop the chats of this server are rejoined (chatRejoin). One the
  // server no longer knows (restart), or an older server that does not answer
  // the join, is opened again (a new chat window on the user's side).
  sock.on('disconnect', (reason) => {
    console.warn('socket.io disconnected:', reason);
    if (chatSockets[key] !== sock) { chatRender(); return; } // dropped on purpose (chatDropSocket)
    for (const c of chatConvs.values()) {
      if (c.pid !== key) continue;
      if (c.chatId && !c.ended) { c.rejoin = true; c.connected = false; chatAddMsg(c, 'System', { k: 'chat.sys.connectionLost' }, true); }
      else if (c.opening) { c.attempt = (c.attempt || 0) + 1; c.opening = false; c.pending = true; } // a late ack of the lost attempt is ignored
    }
    // Ended by the server (its session is gone: socket.io does not reconnect
    // then): tried again later, as a refused socket (chatSocketRecheck).
    if (!sock.active) chatRetrySchedule(key);
    chatRender();
  });
  sock.on('connect_error', (err) => {
    const msg = (err && err.message) || String(err);
    console.warn('socket.io connect_error:', msg);
    // In the header of the conversation on screen when it is this server's
    // (with none: the active server's), its server named in multi-server mode.
    const on = chatActive();
    if (chatSockets[key] === sock && (on ? on.pid === key : key === (activeServerId || ''))) {
      const name = serverMode === 'multi' ? srvName(key) : '';
      setChatHeaderStatus(name ? T('chat.realtimeErrorOn', { server: name, error: msg }) : T('chat.realtimeError', { error: msg }), 'var(--danger)');
    }
    // Refused by the server (socket.io gives up: an expired session...): tried
    // again later, once its server's session is known (chatSocketRecheck).
    if (chatSockets[key] === sock && !sock.active) chatRetrySchedule(key);
  });
  // (Every event names its socket's server: the same chat id on two servers
  // is two chats.)
  sock.on('chat:message', (d) => onChatMessage(d, key));
  sock.on('chat:closed', (d) => onChatClosed(d, sock, key));
  sock.on('chat:opened', (d) => onChatOpened(key, sock, d));
  sock.on('chat:ended', (d) => onChatEnded(key, sock, d));
  sock.on('chat:remote_response', (d) => onChatRemoteResponse(d, key));
  sock.on('chat:typing', (d) => onChatTyping(d, key));
  return sock;
}

document.getElementById('btn-chat').addEventListener('click', () => toggleChat());
document.getElementById('btn-chat-close').addEventListener('click', () => toggleChat(false));

// ── Chat views: the chat panel and the Chat page ─────────────────────────────
// One renderer draws the conversations into a view: the chat panel (#chat-panel,
// in every window) or the Chat page of the main window (nav rail "Chat": built
// at its first visit, kept while another page is on screen). A view holds its
// own elements (no id is shared between them) and binds its handlers once,
// when it is made. Both show the conversation in front (chatActiveKey, the
// same unread counts): the Chat page hides the panel, which would show it twice.
function chatViewWire(v) {
  if (v.send) v.send.addEventListener('click', () => sendChatMessage(v));
  if (v.input) {
    v.input.addEventListener('keydown', e => { if (e.key === 'Enter') sendChatMessage(v); });
    v.input.addEventListener('input', emitChatTyping);
  }
  return v;
}
const chatPanelView = chatViewWire({
  kind: 'panel', renderedKey: null,
  tabs: document.getElementById('chat-tabs'), name: document.getElementById('chat-title'), avatar: document.getElementById('chat-avatar'),
  dot: document.getElementById('chat-status-dot'), txt: document.getElementById('chat-status-text'),
  box: document.getElementById('chat-messages'), typing: document.getElementById('chat-typing'),
  input: document.getElementById('chat-input'), send: document.getElementById('chat-send'), req: document.getElementById('chat-request-btn-wrap'),
});
let chatPageView = null;
// The Chat page's device picker (chatPicker*): asked for ("+"), its filters.
let chatPagePicking = false;
let chatPickerFilter = { q: '', online: '', os: '', reachOnly: true }; // (chat: ObliReach devices, devFilterMatch)
let chatPickerSearchFolded = new Set();
function chatViews() { return chatPageView ? [chatPanelView, chatPageView] : [chatPanelView]; }
const CHAT_PERSON_SVG = '<svg fill="currentColor" viewBox="0 0 24 24"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 3c1.66 0 3 1.34 3 3s-1.34 3-3 3-3-1.34-3-3 1.34-3 3-3zm0 14.2c-2.5 0-4.71-1.28-6-3.22.03-1.99 4-3.08 6-3.08 1.99 0 5.97 1.09 6 3.08-1.29 1.94-3.5 3.22-6 3.22z"/></svg>';
// The operator's picture in the chat headers (loadMe).
function chatViewsAvatar() {
  if (!currentOperatorAvatar) return;
  for (const v of chatViews()) if (v.avatar) v.avatar.innerHTML = '<img src="' + esc(currentOperatorAvatar) + '" style="width:36px;height:36px;border-radius:50%%;object-fit:cover" />';
}

// The Chat page: page header, the tabs of every conversation (server colour,
// status, unread, × to end) with "+" after them, then the conversation in
// front (history, input, Request Remote Control) or, with none in front or
// after "+", the device picker: the Devices list (same rows, filters and
// multi-server separators), a click opens the chat with that device.
function chatPageBuild() {
  const mk = (tag, cls) => { const e = document.createElement(tag); if (cls) e.className = cls; return e; };
  const root = mk('div', 'chat-page'); root.id = 'chat-page';
  const head = mk('div', 'page-header');
  const h1 = mk('h1'); h1.textContent = T('nav.chat'); h1.setAttribute('data-i18n', 'nav.chat');
  const count = mk('span', 'breadcrumb');
  // (the picker's: the machines as they are now)
  const actions = mk('div', 'page-actions');
  const refresh = mk('button', 'btn-sm'); refresh.textContent = T('common.refresh'); refresh.setAttribute('data-i18n', 'common.refresh');
  refresh.addEventListener('click', async () => { try { await loadOverview(); } catch (e) {} chatPickerRefreshIfShown(); });
  actions.appendChild(refresh);
  head.appendChild(h1); head.appendChild(count); head.appendChild(actions);
  const tabbar = mk('div', 'chat-page-tabbar');
  const tabs = mk('div', 'chat-tabs'); tabs.setAttribute('role', 'tablist');
  const add = mk('button', 'chat-tab-add');
  add.title = T('chat.page.newChat'); add.setAttribute('data-i18n-title', 'chat.page.newChat');
  add.setAttribute('aria-label', T('chat.page.newChat')); add.setAttribute('data-i18n-aria', 'chat.page.newChat');
  add.innerHTML = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>';
  add.addEventListener('click', chatPickerToggle);
  tabbar.appendChild(tabs); tabbar.appendChild(add);
  const conv = mk('div', 'chat-page-conv');
  const hdr = mk('div', 'chat-header');
  const avatar = mk('div', 'avatar'); avatar.innerHTML = CHAT_PERSON_SVG;
  const info = mk('div', 'info');
  const name = mk('div', 'name'); name.textContent = T('chat.supportTitle');
  const status = mk('div', 'status');
  const dot = mk('span', 'dot'); dot.style.background = 'var(--muted2)';
  const txt = mk('span'); txt.textContent = T('chat.status.disconnected');
  status.appendChild(dot); status.appendChild(txt);
  info.appendChild(name); info.appendChild(status);
  hdr.appendChild(avatar); hdr.appendChild(info);
  const box = mk('div', 'chat-messages');
  const typing = mk('div', 'chat-typing');
  typing.innerHTML = '<div style="width:28px;height:28px;border-radius:50%%;background:var(--bg3);display:flex;align-items:center;justify-content:center;flex-shrink:0;font-size:10px;font-weight:700;color:var(--muted)">...</div>' +
    '<div class="chat-typing-dots"><span></span><span></span><span></span></div>';
  box.appendChild(typing);
  const area = mk('div', 'chat-input-area');
  const req = mk('div');
  const row = mk('div', 'chat-input-row');
  const input = mk('input'); input.type = 'text'; input.placeholder = T('chat.placeholder'); input.disabled = true;
  const send = mk('button', 'chat-send-btn'); send.disabled = true; send.title = T('chat.send'); send.setAttribute('data-i18n-title', 'chat.send');
  send.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>';
  row.appendChild(input); row.appendChild(send);
  area.appendChild(req); area.appendChild(row);
  conv.appendChild(hdr); conv.appendChild(box); conv.appendChild(area);
  // The device picker: the Devices page's filters and table (devViewFill),
  // its own filters, no id (the elements are the view's).
  const picker = mk('div', 'page-body chat-picker');
  const bar = mk('div', 'filter-bar');
  const q = mk('input'); q.style.minWidth = '220px';
  q.placeholder = T('devices.searchPlaceholder'); q.setAttribute('data-i18n-ph', 'devices.searchPlaceholder'); q.value = chatPickerFilter.q;
  q.addEventListener('input', e => { chatPickerFilter.q = e.target.value; chatPickerSearchFolded = new Set(); chatPickerNote(''); chatPickerFill(); });
  bar.appendChild(q);
  bar.appendChild(devPickerSelect('', [['', 'devices.filter.allStates'], ['online', 'devices.filter.online'], ['offline', 'devices.filter.offline']], chatPickerFilter.online, val => { chatPickerFilter.online = val; chatPickerFill(); }));
  bar.appendChild(devPickerSelect('', [['', 'devices.filter.allOs'], ['windows', 'Windows'], ['linux', 'Linux'], ['macos', 'macOS']], chatPickerFilter.os, val => { chatPickerFilter.os = val; chatPickerFill(); }));
  const spacer = mk('span', 'spacer'); bar.appendChild(spacer);
  const hint = mk('span', 'muted-hint');
  const pickCount = mk('span'); pickCount.textContent = '0';
  const pickCountLabel = mk('span'); pickCountLabel.textContent = T('devices.resultsWord', { count: 0 });
  pickCountLabel.style.marginInlineStart = '.25em'; // (the count, then the word)
  hint.appendChild(pickCount); hint.appendChild(pickCountLabel);
  bar.appendChild(hint);
  const pickNote = mk('div', 'chat-picker-note hide');
  const list = mk('div', 'panel');
  const pb = mk('div', 'panel-body flush'); pb.style.overflowX = 'auto';
  const table = mk('table', 'dt');
  const thead = mk('thead'); thead.innerHTML = devTableHead();
  const pickBody = mk('tbody');
  table.appendChild(thead); table.appendChild(pickBody); pb.appendChild(table); list.appendChild(pb);
  picker.appendChild(bar); picker.appendChild(pickNote); picker.appendChild(list);
  // Esc: back to the conversation in front (when there is one).
  picker.addEventListener('keydown', e => { if (e && e.key === 'Escape' && chatActive()) { e.preventDefault(); chatPickerToggle(); } });
  root.appendChild(head); root.appendChild(tabbar); root.appendChild(conv); root.appendChild(picker);
  chatPageView = chatViewWire({ kind: 'page', renderedKey: null, root, count, tabbar, tabs, add, refresh, conv,
    picker, pickQ: q, pickBody, pickCount, pickCountLabel, pickNote, pickSig: null,
    name, avatar, dot, txt, box, typing, input, send, req });
  chatViewsAvatar();
  return chatPageView;
}
function chatPageShown() { return currentRoute === 'chat' && !!chatPageView; }
function renderChatPage(pc) {
  const v = chatPageView || chatPageBuild();
  pc.appendChild(v.root); // the same elements at every visit: nothing bound twice
  chatPanelHide(); // the page is the chat
  // A visit shows the conversation in front, the picker when there is none.
  chatPagePicking = !chatActive();
  chatPickerNote('');
  v.pickSig = null; // (the list drawn again: the machines may have changed meanwhile)
  const c = chatActive();
  if (c) c.unread = 0; // on screen now: read, everywhere
  chatRender();
  if (chatPagePicking) { chatPickerFocus(); return; }
  if (v.box) v.box.scrollTop = v.box.scrollHeight; // (back in the page: the latest messages)
  if (v.input && !v.input.disabled) { try { v.input.focus(); } catch (e) {} }
}

// ── Chat page: the device picker ─────────────────────────────────────────────
// Shown when no conversation is in front (none open, or the last one ended
// with ×) and after "+": the tabs stay above it, none of them active. A
// device's row (or its Chat button) opens the chat with it as the Devices
// list's Chat button does (chatOpenFor), with the same rule: an offline
// machine (its ObliReach agent unreachable, devIsOffline) cannot chat, its
// row greyed and its button off. A device that already has a tab switches
// to that tab (online or not: the conversation is there, and its "Start a
// new chat" when it ended). A chat opened elsewhere meanwhile (chat:opened)
// gets its tab, unread, and leaves the picker on screen.
function chatPickerShown() { return !!chatPageView && (chatPagePicking || !chatActive()); }
function chatDevConvKey(dev) { return dev && dev.uuid ? chatKey(devPid(dev), dev.uuid) : null; }
function chatPickerHasTab(dev) { const k = chatDevConvKey(dev); return !!(k && chatConvs.has(k)); }
function chatPickerCan(dev) { return !!(dev && dev.uuid) && (chatPickerHasTab(dev) || !devIsOffline(dev)); }
function chatPickerView() {
  const v = chatPageView;
  return { tbodyEl: () => v && v.pickBody, countEl: () => v && v.pickCount, countLabelEl: () => v && v.pickCountLabel,
    filter: () => chatPickerFilter, searchFolded: () => chatPickerSearchFolded,
    actions: [['chat', T('chat.withUser')]], primary: 'chat', actDisabled: (dev) => !chatPickerCan(dev), usable: (dev) => !devIsOffline(dev),
    refresh: chatPickerFill, open: chatPickerChoose };
}
function chatPickerFill() { if (chatPageView) devViewFill(chatPickerView()); }
// The overview read again (a refresh, a server's devices in), the language:
// the picker on screen follows.
function chatPickerRefreshIfShown() { if (chatPageShown() && chatPickerShown()) chatPickerFill(); }
function chatPickerNote(text) {
  const n = chatPageView && chatPageView.pickNote;
  if (!n) return;
  n.textContent = text || '';
  n.classList.toggle('hide', !text);
}
function chatPickerFocus() {
  const q = chatPageView && chatPageView.pickQ;
  if (q) { try { q.focus(); } catch (e) {} }
}
// "+" (and Esc in the picker): the picker, or back to the conversation in front.
function chatPickerToggle() {
  const c = chatActive();
  chatPagePicking = !(chatPickerShown() && c);
  chatPickerNote('');
  if (!chatPagePicking && c) c.unread = 0;
  chatRender();
  if (chatPagePicking) { chatPickerFocus(); return; }
  const v = chatPageView;
  if (v && v.input && !v.input.disabled) { try { v.input.focus(); } catch (e) {} }
}
// A device picked (its row, or its Chat button).
function chatPickerChoose(dev) {
  if (!dev || !dev.uuid) return;
  const key = chatDevConvKey(dev);
  const c = chatConvs.get(key);
  if (c) {
    chatActiveKey = key; chatPagePicking = false; c.unread = 0;
    chatPickerNote('');
    chatRender();
  } else if (devIsOffline(dev)) {
    // (as the Devices list: no chat with a machine whose agent is not reachable)
    chatPickerNote(T(dev.oblireach && dev.oblireach.installed ? 'viewer.pickOffline' : 'viewer.pickNotInstalled', { device: dev.hostname || dev.uuid }));
    return;
  } else {
    chatPickerNote('');
    chatOpenFor(dev); // (the conversation in front: the picker leaves)
  }
  const v = chatPageView;
  if (v && v.input && !v.input.disabled) { try { v.input.focus(); } catch (e) {} }
}
// The top bar's Chat button shows the chat panel: hidden on the Chat page.
function chatTopButtonSync() {
  const btn = document.getElementById('btn-chat');
  if (btn) btn.classList.toggle('hide', currentRoute === 'chat');
}
// The nav rail's Chat entry: every conversation's unread messages.
function chatNavBadge() {
  const b = document.getElementById('nav-chat-badge');
  if (!b) return;
  let unread = 0;
  for (const c of chatConvs.values()) unread += c.unread;
  b.textContent = unread > 99 ? '99+' : String(unread);
  b.title = T('chat.unreadTitle', { count: unread });
  b.classList.toggle('hide', !unread);
}

// ── Chat: one conversation per machine, in tabs ──────────────────────────────
// The panel keeps every open conversation: switching machines or pages no
// longer closes the user's chat window. One tab per machine with its unread
// count; the top-bar button shows the panel, the device header's Chat button
// opens (or brings forward) the conversation with that machine. Keyed by
// server + device so the multi-server switch can add conversations
// of other servers next to these.
//
// Unified chat (Obliance server with chat:list / chat:opened / chat:ended):
// ONE conversation per (operator, machine), shared by every window of the
// same user (Obliance web tabs, this app). chat:open resumes the live chat
// (ack {resumed, history}), the operator's messages sent from another window
// arrive with fromOperator (own bubbles, no sound), a chat opened elsewhere
// gets an unread tab here (chat:opened, and chat:list at every connect), and
// a chat that ended (closed by the operator anywhere, or the user closed the
// window) stays as an ended tab: input off, the Chat button starts a new one.
// An older server sends none of these: the previous behaviour stays.
const chatConvs = new Map();
let chatActiveKey = null;
let chatBindSeq = 0; // bumped each time a conversation gets its chat (chat:list races)

function chatKey(pid, uuid) { return (pid || 'current') + ':' + uuid; }
function chatServer(pid) { return (typeof serverProfiles !== 'undefined' && serverProfiles.find(p => p.id === pid)) || null; }
function chatManyServers() { const set = new Set(); for (const c of chatConvs.values()) set.add(c.pid); return set.size > 1; }
function chatActive() { return chatActiveKey ? (chatConvs.get(chatActiveKey) || null) : null; }
// pid given: only a conversation of that server.
function chatConvById(id, pid) {
  if (!id) return null;
  for (const c of chatConvs.values()) if (c.chatId === id && (pid === undefined || c.pid === (pid || ''))) return c;
  return null;
}
function chatPanelOpen() { const p = document.getElementById('chat-panel'); return !!(p && p.classList.contains('open')); }
function chatOnScreen(c) {
  // #chat-panel and the Chat page live in #app, hidden while a sign-in page is up.
  // (The Chat page's picker hides the conversation in front.)
  return (chatPanelOpen() || (chatPageShown() && !chatPickerShown())) && chatActiveKey === c.key && document.getElementById('app').style.display !== 'none';
}
// Shows the chat panel, in the slot of the Scripts / Info panel (which
// closes). false on the Chat page: the page shows the conversation.
function chatPanelShow() {
  if (chatPageShown()) return false;
  const p = document.getElementById('chat-panel');
  if (!p.classList.contains('open')) { rightSlotTake('chat'); p.classList.add('open'); }
  return true;
}
// Hides it (Scripts / Info take the slot, the Chat page): the conversations go on.
function chatPanelHide() {
  const p = document.getElementById('chat-panel');
  if (!p || !p.classList.contains('open')) return;
  p.classList.remove('open');
  reachSessionChatRefresh();
}

// The server whose account this window shows (currentOperator*): the
// active one; a session window's own (srvShownPid).
function shownPid() { return srvShownPid !== null ? srvShownPid : (activeServerId || ''); }
// The operator name shown to the user for a conversation of this server.
function chatOpNameFor(pid) {
  if ((pid || '') === shownPid()) return currentOperatorName;
  const st = srv.get(pid || ''); // (its account, read in multi-server mode)
  if (st && st.me && st.me.name) return st.me.name;
  for (const c of chatConvs.values()) if (c.pid === pid && c.opName) return c.opName;
  const s = chatSockets[pid || ''];
  return (s && s._opName) || '';
}

// The operator's picture sent with a chat of this server (its account's).
function chatOpAvatarFor(pid) {
  if ((pid || '') === shownPid()) return currentOperatorAvatar || '';
  const st = srv.get(pid || '');
  return (st && st.me && st.me.avatar) || '';
}

function chatNewConv(pid, uuid, deviceId, name) {
  const srv = chatServer(pid);
  return { key: chatKey(pid, uuid), pid: pid, serverName: srv ? srv.name : '', serverColor: srv ? srv.color : '', opName: chatOpNameFor(pid),
    deviceUuid: uuid, deviceId: deviceId, deviceName: name || uuid, tenantId: null, chatId: null,
    connected: false, userClosed: false, failed: false, opening: false, pending: false, remoteAsked: false, ended: null,
    messages: [], unread: 0 };
}
// Conversations of several servers (multi-server mode, or chats of another
// server than the active one): each tab shows its server (colour dot, name).
function chatShowServers() { return serverMode === 'multi' || chatManyServers(); }
// A conversation's machine in a status line or a notification: its server
// first when that is not obvious (multi-server mode, another server's chat).
function chatDevLabel(c) {
  return ((serverMode === 'multi' || c.pid !== (activeServerId || '')) && c.serverName ? c.serverName + ' / ' : '') + c.deviceName;
}
// A positive integer tenant id, or null.
function chatTenantOf(v) { const n = Number(v); return (v !== null && v !== undefined && v !== '' && Number.isInteger(n) && n > 0) ? n : null; }

// A conversation opened from another window knows only the machine's uuid and
// name: its device id (chat target session) and tenant are looked up lazily,
// in the device list of its server (multi-server mode: every server's; single
// mode: the server on screen's, or what was read of another one).
function chatResolveDevice(c) {
  if (c.deviceId !== undefined && c.deviceId !== null && c.tenantId) return;
  const pid = c.pid || '';
  const st = srv.get(pid);
  let groups;
  if (serverMode !== 'multi' && pid === (activeServerId || '')) groups = (typeof overview !== 'undefined' && overview && overview.groups) || [];
  else groups = (st && Array.isArray(st.groups)) ? st.groups : [];
  for (const g of groups) for (const d of ((g && g.devices) || [])) {
    if (!d || d.uuid !== c.deviceUuid || devPid(d) !== pid) continue;
    if (c.deviceId === undefined || c.deviceId === null) c.deviceId = d.id;
    if (!c.tenantId) c.tenantId = devTenant(d, g);
    return;
  }
}

function toggleChat(forceOpen) {
  const panel = document.getElementById('chat-panel');
  const shouldOpen = typeof forceOpen === 'boolean' ? forceOpen : !panel.classList.contains('open');
  if (!shouldOpen) { panel.classList.remove('open'); reachSessionChatRefresh(); return; }
  chatPanelShow();
  // Nothing open yet: start a conversation with the machine on screen.
  if (!chatConvs.size && selectedDevice && selectedDevice.oblireach && selectedDevice.oblireach.online) { chatOpenFor(selectedDevice); return; }
  const c = chatActive();
  if (c) c.unread = 0;
  chatRender();
}

// Opens (or brings forward) the conversation with this machine: in the chat
// panel, or in the Chat page when it is on screen.
function chatOpenFor(dev) {
  if (!dev || !dev.uuid) return;
  const pid = devPid(dev); // (the device's server)
  const key = chatKey(pid, dev.uuid);
  let c = chatConvs.get(key);
  if (!c) {
    c = chatNewConv(pid, dev.uuid, dev.id, dev.hostname || dev.uuid);
    chatConvs.set(key, c);
  } else if (c.deviceId === undefined || c.deviceId === null) c.deviceId = dev.id; // tab opened elsewhere
  const tid = devTenant(dev); // (multi-server mode: its server's session is put on it before chat:open)
  if (tid !== null) c.tenantId = tid;
  chatActiveKey = key;
  chatPagePicking = false; // (the Chat page: the conversation, not its picker)
  c.unread = 0;
  chatPanelShow();
  chatRestart(c);
}
// An ended chat is never reused: a new one starts in the same tab (the
// device's Chat button, or "Start a new chat" in the conversation). A tab
// whose chat could not open tries again.
function chatRestart(c) {
  if (chatConvs.get(c.key) !== c) return;
  if (c.ended) { c.ended = null; c.chatId = null; c.userClosed = false; c.failed = false; }
  if (!c.chatId && !c.opening) chatStart(c);
  chatRender();
}
function chatCanRestart(c) { return !!c && (!!c.ended || (c.failed && !c.chatId && !c.opening && !c.pending)); }

// Remote toolbar "Chat": the conversation with the machine of the session on
// screen. Opens or brings it forward; a second click, while the panel already
// shows that conversation, hides the panel (the chat itself keeps running).
// The device header's Chat tab does the same for its device.
function reachSessionChatDevice() {
  const t = typeof reachDisplayedTab === 'function' ? reachDisplayedTab() : null;
  return (t && t.device && t.device.uuid) ? t.device : selectedDevice;
}
function reachSessionChat() { chatToggleFor(reachSessionChatDevice()); }
function chatToggleFor(dev) {
  if (!dev || !dev.uuid) return;
  if (chatPanelOpen() && chatActiveKey === chatKey(devPid(dev), dev.uuid)) { toggleChat(false); return; }
  chatOpenFor(dev);
}
// Unread dot and "shown" state of that button, and of the device header's
// Chat tab (chatRender, toggleChat, renderRemoteTab, selectDeviceInto).
function reachSessionChatRefresh() {
  const h = document.getElementById('hdr-chat');
  if (h) {
    const hk = selectedDevice && selectedDevice.uuid ? chatKey(devPid(selectedDevice), selectedDevice.uuid) : null;
    const hc = hk ? chatConvs.get(hk) : null;
    h.classList.toggle('active', !!(hc && chatPanelOpen() && chatActiveKey === hk));
    h.classList.toggle('unread', !!(hc && hc.unread));
  }
  const b = document.getElementById('reach-chat-btn');
  if (!b) return;
  const dev = reachSessionChatDevice();
  const key = dev && dev.uuid ? chatKey(devPid(dev), dev.uuid) : null;
  const c = key ? chatConvs.get(key) : null;
  const shown = !!(c && chatPanelOpen() && chatActiveKey === key);
  b.disabled = !key;
  b.classList.toggle('on', shown);
  b.title = shown ? T('chat.hide') : T('chat.withUser');
  const dot = document.getElementById('reach-chat-dot');
  if (dot) dot.style.display = (c && c.unread) ? 'block' : 'none';
}

// Chat target: the session of a remote session of this machine started on an
// explicit session; otherwise none, and the agent picks the active console /
// user session (findCaptureSession), like the Obliance web chat. The first
// session of the list may be disconnected or the sign-in screen: the chat
// window then opened where nobody sees it.
// (pid: the conversation's server: the same id on two servers is two devices)
function chatTargetSessionId(pid, deviceId) {
  if (typeof sessionTabs === 'undefined' || deviceId === undefined || deviceId === null) return undefined;
  const key = devKeyOf(pid || '', deviceId);
  for (const t of sessionTabs) {
    if (t.device && devKey(t.device) === key) {
      const id = t.reconnectParams ? t.reconnectParams.wtsSessionId : undefined;
      return (id === undefined || id === null) ? undefined : id;
    }
  }
  return undefined;
}

// Multi-server mode: Obliance opens a chat in its session's CURRENT tenant
// (chat:open reads it when emitted): on a server listing every tenant
// (scoped), that session is put on the machine's tenant first (tenantpin.go
// /local/tenant-align). 'ok', 'denied' (no access to that tenant any more),
// or 'failed' (the open is tried anyway: the server answers what it can).
function chatNeedsAlign(c) {
  const st = srv.get(c.pid || '');
  return serverMode === 'multi' && !!(st && st.scoped) && !!chatTenantOf(c.tenantId);
}
async function chatAlign(c) {
  try {
    const r = await fetch('/local/tenant-align', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile: c.pid || '', tenantId: chatTenantOf(c.tenantId) }) });
    if (r && r.status === 403) return 'denied';
    return r && r.ok ? 'ok' : 'failed';
  } catch (e) { return 'failed'; }
}

function chatStart(c) {
  const sock = chatSocketFor(c.pid);
  if (!sock || !sock.connected) { c.pending = true; chatRender(); return; }
  if (!sock._opName && c.opName) sock._opName = c.opName;
  chatResolveDevice(c);
  c.pending = false; c.opening = true; c.failed = false;
  // Ack and timer belong to THIS attempt of THIS conversation object: a tab
  // closed and reopened, or a retry, must not take a stale answer.
  const attempt = c.attempt = (c.attempt || 0) + 1;
  const live = () => chatConvs.get(c.key) === c && c.attempt === attempt;
  const align = chatNeedsAlign(c);
  const denied = () => { c.opening = false; chatLost(c, { k: 'devices.server.tenantDenied' }); };
  let emits = 0;
  const open = (again) => {
    const mine = ++emits;
    sock.emit('chat:open', {
      deviceUuid: c.deviceUuid,
      sessionId: chatTargetSessionId(c.pid, c.deviceId),
      // (that server's account; unknown: the server uses its own)
      operatorName: c.opName || chatOpNameFor(c.pid) || undefined,
      operatorAvatar: chatOpAvatarFor(c.pid) || undefined,
    }, (res) => {
      if (!live()) {
        // Tab closed / attempt superseded: a chat this open created is closed
        // (its window would stay open with nobody). A resumed one is shared
        // with another window, and one a tab here now shows is kept.
        if (res && res.chatId && !res.resumed && !chatConvById(res.chatId, c.pid)) sock.emit('chat:close', { chatId: res.chatId });
        return;
      }
      if (mine !== emits) return;
      // Aligned, yet the server does not find the machine: another request
      // moved this server's session to another tenant in between. Aligned
      // again and opened once more (only once).
      if (align && !again && res && !res.chatId && res.error === 'device not found') {
        chatAlign(c).then((a) => {
          if (!live() || !c.opening) return;
          if (a === 'denied') { denied(); return; }
          open(true);
        });
        return;
      }
      c.opening = false;
      if (res && res.chatId) {
        if (typeof res.resumed === 'boolean' || Array.isArray(res.history)) sock._unified = true;
        c.chatId = res.chatId; c.boundSeq = ++chatBindSeq;
        c.connected = true; c.userClosed = false; c.failed = false; c.ended = null;
        sock.emit('join', 'chat:' + c.chatId);
        if (res.resumed) {
          // This user already chats with the machine from another window
          // (Obliance web...): the SAME chat, no second window on the user's
          // side, and the exchange so far is shown.
          chatMergeHistory(c, res.chatId, res.history);
          chatAddMsg(c, 'System', { k: 'chat.sys.resumed' }, true);
        } else chatAddMsg(c, 'System', { k: 'chat.sys.opened' }, true);
      } else {
        c.failed = true;
        chatAddMsg(c, 'System', chatErrorText(res && res.error), true);
      }
      chatRender();
    });
    setTimeout(() => {
      if (!live() || !c.opening || mine !== emits) return;
      c.attempt++; // the answer, if it still comes, is dropped (and its chat closed)
      c.opening = false; c.failed = true;
      chatAddMsg(c, 'System', { k: 'chat.sys.timeout' }, true);
      chatRender();
    }, 5000);
  };
  if (!align) { open(false); return; }
  chatRender(); // (connecting: the alignment first)
  chatAlign(c).then((a) => {
    // (a drop of the socket meanwhile made this attempt stale: pending again)
    if (!live() || !c.opening) return;
    if (a === 'denied') { denied(); return; }
    if (chatSockets[c.pid || ''] !== sock || !sock.connected) { c.opening = false; c.pending = true; chatRender(); return; }
    open(false);
  });
}

// chat:open / join refusals, in words (a unified server names them): the
// message of a system line, { k: key, p: params } (chatAddMsg).
function chatErrorText(err) {
  // One chat per operator and machine: on a multi-session host, the live one
  // is with another Windows session (the server does not resume it here).
  if (err === 'other_session') return { k: 'chat.err.otherSession' };
  if (err === 'forbidden') return { k: 'chat.err.forbidden' };
  return err ? { k: 'chat.err.failed', p: { error: err } } : { k: 'chat.err.agentOffline' };
}

// The tab loses its chat (unknown to the server, or no right to it any more):
// input off; the Chat button tries to open one again.
function chatLost(c, text) {
  c.chatId = null; c.connected = false; c.failed = true; c.remoteAsked = false;
  chatAddMsg(c, 'System', text, true);
  chatRender();
}

// Realtime (re)connected: start the conversations that were waiting for it.
// (Not those of a server signed out, or whose refused socket waits for its
// next try: they wait for that server's next socket.)
function chatOpenPending() {
  for (const c of chatConvs.values()) if (c.pending && !chatSocketUnwanted(c.pid) && !chatRetryWaiting(c.pid)) chatStart(c);
}

// keepShared (sign-out, server removal, SSO reload: the app leaves, the
// operator did not end the chat): on a unified server only the tab goes. The
// chat is shared with this user's other windows (Obliance web...), where
// chat:list still finds it; it ends when the user closes its window. An older
// server still gets chat:close (nobody else could ever reach that chat).
function chatCloseConv(key, keepShared) {
  const c = chatConvs.get(key);
  if (!c) return;
  const sock = chatSock(c.pid);
  // Ends it for every window of this user too (chat:ended); an ended one is only a tab.
  if (sock && c.chatId && !c.ended && !(keepShared && sock._unified)) sock.emit('chat:close', { chatId: c.chatId });
  chatConvs.delete(key);
  if (chatActiveKey === key) { const first = chatConvs.keys().next(); chatActiveKey = first.done ? null : first.value; }
  if (sock && sock._renew) chatRenewSocketWhenIdle(c.pid);
  chatRender();
}

// Closes the conversations of one server (its sign-out, its removal), or of
// every server when no pid is given (the SSO page reload). keepShared: see
// chatCloseConv (the chats of a unified server go on in the other windows).
function closeChat(pid, keepShared) {
  for (const [k, c] of Array.from(chatConvs)) if (pid === undefined || c.pid === (pid || '')) chatCloseConv(k, keepShared);
  if (!chatConvs.size) document.getElementById('chat-panel').classList.remove('open');
}

// True when leaving (keepShared) would END a running chat: one on an older
// server, where chat:close is the only clean end (its window would stay open).
function chatLeaveEndsChats(pid) {
  for (const c of chatConvs.values()) {
    if (pid !== undefined && c.pid !== (pid || '')) continue;
    const s = chatSock(c.pid);
    if (c.chatId && !c.ended && !(s && s._unified)) return true;
  }
  return false;
}

// Drops a server's socket for good (sign-out: the server authenticated it
// once, at the handshake, as the user who is leaving).
function chatDropSocket(pid) {
  const key = pid || '';
  const s = chatSockets[key];
  if (!s) return;
  delete chatSockets[key];
  try { s.disconnect(); } catch (e) {}
}

// Tenant switch: the socket keeps the tenant of its handshake. Replaced now if
// no chat runs on it, else as soon as its last chat ends.
function chatRenewSocketWhenIdle(pid) {
  const key = pid || '';
  const s = chatSockets[key];
  if (!s) return;
  for (const c of chatConvs.values()) if (c.pid === key && ((c.chatId && !c.ended) || c.opening)) { s._renew = true; return; }
  // A file explorer command on its way: its answer goes to this socket
  // (explorer_viewer.go renews it once nothing is pending).
  if (typeof explorerBusy === 'function' && explorerBusy(key)) { s._renew = true; return; }
  chatDropSocket(key);
  // The server on screen keeps a socket: chats opened from another window
  // (chat:opened) still reach this one; so does every server signed in.
  if (key === (activeServerId || '')) chatSocketFor(key);
  chatSyncSockets();
  chatOpenPending(); // conversations waiting for realtime get the new socket
}

// Chat events on a conversation that is not on screen (panel hidden, another
// tab, or the app in the background): unread count + badge, sound, a status
// line naming the machine, and the taskbar button flashes (native bridge)
// until the window comes to the front.
function chatAttention(c, text, count) {
  const onScreen = chatOnScreen(c);
  if (!onScreen) c.unread += count > 0 ? count : 1;
  chatRender();
  if (onScreen && document.hasFocus()) return;
  // Main window, machine with its own session window: that window has the
  // same chat (unified server) and alerts; here only the unread count.
  if (chatInDetachedWindow(c)) return;
  playNotifSound();
  const line = T('chat.attn.line', { device: chatDevLabel(c), text: text });
  if (text) setStatus(line);
  // Focus mode hides the status bar: the line shows over the picture instead.
  if (text && !onScreen && document.body.classList.contains('reach-focus') && typeof reachToast === 'function') reachToast(line);
  if (typeof window.reachAttention === 'function' && typeof reachNativeToken === 'function' && reachNativeToken()) {
    try { const p = window.reachAttention(reachNativeToken()); if (p && p.catch) p.catch(function () {}); } catch (e) {}
  }
  // The main window hidden in the notification area (tray_windows.go): a
  // notification of its icon (shown only then; a click opens the Chat page).
  if (!reachDetachedMode && typeof window.reachTrayNotify === 'function' && typeof reachNativeToken === 'function' && reachNativeToken()) {
    try { const p = window.reachTrayNotify(reachNativeToken(), chatDevLabel(c), text ? String(text) : T('tray.chatNew')); if (p && p.catch) p.catch(function () {}); } catch (e) {}
  }
}

// The unread count, to the notification-area icon (its tooltip and menu):
// main window only, when it changes.
var trayUnreadSent = -1;
function trayReportUnread(n) {
  if (reachDetachedMode || typeof window.reachTrayUnread !== 'function' || typeof reachNativeToken !== 'function' || !reachNativeToken()) return;
  n = n > 0 ? Math.floor(n) : 0;
  if (n === trayUnreadSent) return;
  trayUnreadSent = n;
  try { const p = window.reachTrayUnread(reachNativeToken(), n); if (p && p.catch) p.catch(function () {}); } catch (e) {}
}
// The icon's menu line "unread chat messages", or a notification clicked:
// the Chat page, on the first conversation with unread messages.
window.__reachTrayOpenChat = function () {
  if (reachDetachedMode) return;
  for (const c of chatConvs.values()) if (c.unread > 0) { chatActiveKey = c.key; chatPagePicking = false; break; }
  if (typeof appReady !== 'undefined' && appReady) navigateTo('chat');
};

function chatInDetachedWindow(c) {
  if (reachDetachedMode || c.deviceId === undefined || c.deviceId === null) return false;
  const sock = chatSock(c.pid);
  return !!(sock && sock._unified) && reachDetachedIds.has(devKeyOf(c.pid, c.deviceId)); // (its server's device)
}

// extra (optional): fromOp (true: this operator's message, from here or from
// another window of the same user), chatId, timestamp (history). A system
// line's text is { k: catalog key, p: params }: drawn again in the language
// on screen (chatSysText).
function chatAddMsg(c, sender, text, isSystem, extra) {
  const msg = { sender: sender, text: text, timestamp: (extra && extra.timestamp) || Date.now(), isSystem: isSystem };
  if (text && typeof text === 'object') { msg.tk = String(text.k || ''); msg.tp = text.p || null; msg.text = T(msg.tk, msg.tp); }
  if (!isSystem) {
    msg.chatId = (extra && extra.chatId) || c.chatId;
    if (extra && typeof extra.fromOp === 'boolean') msg.fromOp = extra.fromOp;
  }
  c.messages.push(msg);
  if (c.messages.length > 500) c.messages.shift();
  if (chatActiveKey !== c.key) return;
  for (const v of chatViews()) if (v.renderedKey === c.key) chatRenderMsg(v, c, msg, c.messages.length - 1);
}

// Draws every chat view (panel, Chat page), then the badges: the top bar's
// Chat button, the nav rail's Chat entry, the session toolbar's Chat button.
function chatRender() {
  for (const v of chatViews()) chatRenderView(v);
  const btn = document.getElementById('btn-chat');
  if (btn && chatConvs.size) btn.style.display = '';
  let unread = 0;
  for (const conv of chatConvs.values()) unread += conv.unread;
  trayReportUnread(unread);
  const badge = document.getElementById('chat-badge');
  if (badge) badge.style.display = unread ? 'block' : 'none';
  chatNavBadge();
  chatServerBadges();
  reachSessionChatRefresh();
}

// Unread messages of one server's conversations (null: every server's): the
// server menu's badges (renderServerSwitcher), kept up to date while it is open.
function chatUnreadOf(pid) {
  let n = 0;
  for (const c of chatConvs.values()) if (pid === null || c.pid === (pid || '')) n += c.unread;
  return n;
}
function srvUnreadHtml(pid) {
  const n = chatUnreadOf(pid);
  return '<span class="srv-unread' + (n ? '' : ' hide') + '" data-unread-pid="' + esc(pid === null ? '*' : pid) + '" title="' + TH('chat.unreadTitle', { count: n }) + '">' + (n > 99 ? '99+' : n) + '</span>';
}
function chatServerBadges() {
  const menu = document.getElementById('server-menu');
  if (!menu || typeof menu.querySelectorAll !== 'function') return;
  for (const el of menu.querySelectorAll('[data-unread-pid]')) {
    const p = el.getAttribute('data-unread-pid');
    const n = chatUnreadOf(p === '*' ? null : p);
    el.textContent = n > 99 ? '99+' : String(n);
    el.title = T('chat.unreadTitle', { count: n });
    el.classList.toggle('hide', !n);
  }
}

// One tab of the conversation strip (a new element at every render).
// activeKey: the tab shown as active (none while the Chat page's picker is up).
// A tab names its server (a dot in its colour, never a border: borderless
// design) when that is not obvious: several servers' conversations, or a
// chat of another server than the active one (as chatDevLabel).
function chatTabShowsServer(conv) {
  return !!conv.serverName && (chatShowServers() || (conv.pid || '') !== (activeServerId || ''));
}
function chatTabEl(conv, activeKey) {
  const t = document.createElement('div');
  t.className = 'chat-tab' + (conv.key === activeKey ? ' active' : '') + (conv.ended ? ' ended' : '');
  t.setAttribute('role', 'tab'); t.setAttribute('aria-selected', conv.key === activeKey ? 'true' : 'false');
  // Reachable from the keyboard (Tab), Enter / Space bring it to the front.
  t.tabIndex = 0; t.setAttribute('data-chat-key', conv.key);
  const tip = [conv.serverName, conv.ended ? T('chat.status.ended') : ''].filter(Boolean).join(' · ');
  if (tip) t.title = tip;
  const st = conv.ended ? '' : conv.connected ? (conv.userClosed ? 'warn' : 'on') : (conv.failed ? 'bad' : '');
  const srvDot = chatTabShowsServer(conv)
    ? '<span class="ct-srv" style="background:' + ((typeof SERVER_COLORS !== 'undefined' && SERVER_COLORS[conv.serverColor]) || 'var(--muted2)') + '" title="' + esc(conv.serverName) + '"></span>' : '';
  t.innerHTML = srvDot + '<span class="ct-dot ' + st + '"></span><span>' + esc(conv.deviceName) + '</span>' +
    (conv.unread ? '<span class="ct-unread">' + conv.unread + '</span>' : '') +
    '<button class="ct-x" title="' + (conv.ended ? TH('chat.closeTab') : TH('chat.endChat')) + '">×</button>';
  const front = () => {
    chatActiveKey = conv.key; conv.unread = 0; chatPagePicking = false; // (the Chat page: its picker leaves)
    chatRender(); // read: cleared everywhere
  };
  t.addEventListener('click', (e) => {
    if (e.target.closest('.ct-x')) { e.stopPropagation(); chatCloseConv(conv.key); return; }
    front();
  });
  t.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    if (e.target && e.target !== t) return; // (its × button: its own key)
    e.preventDefault();
    const strip = t.parentElement;
    front();
    // The strip is built anew: the focus goes back to this conversation's tab.
    if (strip && strip.children) for (const el of strip.children) {
      if (el.getAttribute && el.getAttribute('data-chat-key') === conv.key) { if (typeof el.focus === 'function') el.focus(); break; }
    }
  });
  return t;
}

let chatTabSeq = 0; // (each conversation's tab identity: chatRenderView's signature)
function chatRenderView(v) {
  const c = chatActive();
  // The Chat page: nothing in front, the picker (and it stays up when a chat
  // opened elsewhere comes meanwhile: its tab, unread).
  if (v.kind === 'page' && !c) chatPagePicking = true;
  const picking = v.kind === 'page' && chatPickerShown();
  if (v.tabs) {
    // The strip drawn again only when a tab would show something else (not
    // at every message: hover and focus on a tab stay).
    const ak = picking ? null : chatActiveKey;
    const sig = JSON.stringify([ak, T('chat.endChat'), Array.from(chatConvs.values()).map(conv => [conv.key, conv._ctId || (conv._ctId = ++chatTabSeq), conv.deviceName, conv.unread, !!conv.ended, !!conv.connected,
      !!conv.userClosed, !!conv.failed, conv.serverName || '', conv.serverColor || '', chatTabShowsServer(conv)])]);
    if (v.tabs._ctSig !== sig || (chatConvs.size && !(v.tabs.children && v.tabs.children.length))) {
      v.tabs._ctSig = sig;
      v.tabs.innerHTML = '';
      for (const conv of chatConvs.values()) v.tabs.appendChild(chatTabEl(conv, ak));
    }
  }
  if (v.name) v.name.textContent = c ? c.deviceName + (chatShowServers() && c.serverName ? ' \u00B7 ' + c.serverName : '') : T('chat.supportTitle');
  if (c && v.dot && v.txt) {
    // (its server signed out: it waits for the next sign-in's socket)
    const off = srv.get(c.pid || '') && srv.get(c.pid || '').signedIn === false;
    const s = c.ended ? ['chat.status.ended', 'var(--muted2)']
      : c.connected ? (c.userClosed ? ['chat.status.userClosed', 'var(--warn)'] : ['chat.status.connected', 'var(--success)'])
      : c.opening ? ['chat.status.connecting', 'var(--warn)']
      : (c.pending || off) ? ['chat.status.waitingRealtime', 'var(--warn)']
      : c.failed ? ['chat.status.failed', 'var(--danger)'] : ['chat.status.disconnected', 'var(--muted2)'];
    v.txt.textContent = T(s[0]); v.dot.style.background = s[1];
  }
  const can = !!(c && c.connected && !c.ended);
  if (v.input) {
    v.input.disabled = !can;
    v.input.placeholder = (c && c.ended) ? T('chat.endedPlaceholder') : T('chat.placeholder');
  }
  if (v.send) v.send.disabled = !can;
  if (v.renderedKey !== chatActiveKey) {
    v.renderedKey = chatActiveKey;
    if (v.box) {
      Array.from(v.box.children).forEach(ch => { if (ch !== v.typing) ch.remove(); });
      if (v.typing) v.typing.classList.remove('visible');
      if (c) c.messages.forEach((m, i) => chatRenderMsg(v, c, m, i));
    }
  }
  chatRenderRequest(v, c);
  if (v.kind === 'page') {
    // The page: its count (none open: what to do); the conversation in
    // front, or the device picker with its Refresh; the tabs and "+" when
    // there is a conversation.
    const n = chatConvs.size;
    let unread = 0;
    for (const conv of chatConvs.values()) unread += conv.unread;
    v.count.textContent = n ? T('chat.page.count', { count: n }) + (unread ? ' \u00B7 ' + T('chat.page.unread', { count: unread }) : '') : T('chat.page.pickHint');
    v.conv.classList.toggle('hide', picking);
    v.picker.classList.toggle('hide', !picking);
    v.refresh.classList.toggle('hide', !picking);
    v.tabbar.classList.toggle('hide', !n);
    v.add.classList.toggle('on', picking);
    v.add.setAttribute('aria-pressed', picking ? 'true' : 'false');
    // Its rows: drawn when it comes on screen and when the conversations
    // change (a device with a tab switches to it, even offline), not at
    // every message.
    const sig = picking && chatPageShown() ? Array.from(chatConvs.keys()).join('\n') : null;
    if (sig !== null && sig !== v.pickSig) { v.pickSig = sig; chatPickerFill(); }
    else if (sig === null) v.pickSig = null;
  }
}

function chatRenderMsg(v, c, msg, idx) {
  const container = v.box;
  if (!container) return;
  const typingEl = v.typing && v.typing.parentElement === container ? v.typing : null;
  const put = (el) => { if (typingEl) container.insertBefore(el, typingEl); else container.appendChild(el); };

  // Time separator
  // (History entries carry their own, older, time: a jump either way shows it.)
  const prev = idx > 0 ? c.messages[idx - 1] : null;
  if (!prev || Math.abs(msg.timestamp - prev.timestamp) > 300000) {
    const timeDiv = document.createElement('div');
    timeDiv.className = 'chat-time';
    timeDiv.innerHTML = '<span>' + fmtTime(msg.timestamp) + '</span>';
    put(timeDiv);
  }

  const div = document.createElement('div');
  if (msg.isSystem) {
    div.className = 'chat-msg sys';
    div.innerHTML = '<div class="bubble">' + esc(msg.tk ? T(msg.tk, msg.tp) : msg.text) + '</div>';
  } else {
    // fromOp known (unified server): this operator's own message, whatever
    // the name the other window sent it with; else the name tells.
    const isOp = typeof msg.fromOp === 'boolean' ? msg.fromOp : msg.sender === (c.opName || currentOperatorName);
    div.className = 'chat-msg ' + (isOp ? 'op' : 'user');
    if (isOp) {
      const opAv = chatOpAvatarFor(c.pid); // (that conversation's server's account)
      const avatarEl = opAv
        ? '<img src="' + esc(opAv) + '" style="width:28px;height:28px;border-radius:50%%;object-fit:cover;flex-shrink:0" />'
        : '<div class="avatar" style="width:28px;height:28px;background:var(--accent-fill);color:var(--on-accent);border-radius:50%%;display:flex;align-items:center;justify-content:center;flex-shrink:0">' +
          '<svg width="15" height="15" fill="currentColor" viewBox="0 0 24 24"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 3c1.66 0 3 1.34 3 3s-1.34 3-3 3-3-1.34-3-3 1.34-3 3-3zm0 14.2c-2.5 0-4.71-1.28-6-3.22.03-1.99 4-3.08 6-3.08 1.99 0 5.97 1.09 6 3.08-1.29 1.94-3.5 3.22-6 3.22z"/></svg></div>';
      div.innerHTML = avatarEl + '<div class="bubble">' + esc(msg.text) + '</div>';
    } else {
      const initials = String(msg.sender || '?').split(/\s+/).map(w => w[0] || '').join('').toUpperCase().slice(0, 2);
      div.innerHTML = '<div class="bubble">' + esc(msg.text) + '</div>' +
        '<div style="width:28px;height:28px;border-radius:50%%;background:var(--bg3);display:flex;align-items:center;justify-content:center;flex-shrink:0;font-size:10px;font-weight:700;color:var(--muted);margin-top:auto">' + esc(initials) + '</div>';
    }
  }
  put(div);
  container.scrollTop = container.scrollHeight;
}

// view: the panel or the Chat page (its own input); the panel when omitted.
function sendChatMessage(view) {
  const v = view && view.input ? view : chatPanelView;
  const c = chatActive();
  const input = v.input;
  const text = input.value.trim();
  const sock = c ? chatSock(c.pid) : null;
  if (!c || !text || !c.chatId || !sock || c.ended) return;
  chatAddMsg(c, c.opName || currentOperatorName, text, false, { fromOp: true });
  sock.emit('chat:message', { chatId: c.chatId, message: text, operatorName: c.opName || chatOpNameFor(c.pid) || undefined });
  input.value = '';
  input.focus();
  if (c.userClosed) { c.userClosed = false; chatRender(); }
}

// pid: the server whose socket brought the event (undefined: any server).
function onChatMessage(data, pid) {
  const c = data ? chatConvById(data.chatId, pid) : null;
  if (!c) return;
  if (data.fromOperator === true) {
    // Sent by this same user from another window (Obliance web, another
    // tab): shown as an own message, no sound, not counted unread.
    chatAddMsg(c, data.sender || c.opName || currentOperatorName, data.message, false, { fromOp: true });
    c.userClosed = false;
    chatRender();
    return;
  }
  if (chatActiveKey === c.key) hideChatTyping();
  chatAddMsg(c, data.sender, data.message, false, data.fromOperator === false ? { fromOp: false } : undefined);
  c.userClosed = false;
  chatAttention(c, T('chat.attn.messageFrom', { name: data.sender || T('chat.attn.theUser') }));
}

// The end user closed the chat window. A server of the unified chat follows
// with chat:ended (that chat is over: see chatMarkEnded); an older one keeps
// it open (the next message opens the window again).
function onChatClosed(data, sock, pid) {
  const c = data ? chatConvById(data.chatId, pid) : null;
  if (!c || c.ended || (sock && sock._unified)) return;
  c.userClosed = true;
  chatAddMsg(c, 'System', { k: 'chat.sys.userClosed' }, true);
  chatAttention(c, T('chat.attn.userClosed'));
}

// chat:ended {chatId, reason}: closed by this user from another window
// ('operator') or the end user closed the window ('user'). Never reused.
// 'forbidden' is different: this user lost the right to the chat (capability
// 'remote', tenant) while the chat itself goes on, so the tab loses it as on
// a refused join, and is not marked ended.
function onChatEnded(key, sock, data) {
  sock._unified = true;
  const c = data ? chatConvById(data.chatId, key) : null;
  if (!c) return;
  if (data.reason === 'forbidden') {
    if (c.ended) return;
    c.attempt = (c.attempt || 0) + 1; // a late ack of a join in flight is ignored
    c.opening = false; c.pending = false; c.rejoin = false;
    if (chatActiveKey === c.key) hideChatTyping();
    chatLost(c, chatErrorText('forbidden'));
    const s = chatSock(c.pid);
    if (s && s._renew) chatRenewSocketWhenIdle(c.pid);
    return;
  }
  chatMarkEnded(c, data.reason, data);
}

// 'unavailable' (server chat:ended / join ack): the chat window could not
// open on the machine. {code, installing, state, detail, wv2Reason,
// installOffer}: the exact reason (Microsoft Edge WebView2 Runtime missing /
// being installed / failed..., nobody signed in), never "the user closed it".
// Several system lines: the reason; why an install failed, in words
// (wv2Reason); what the device page really offers (installOffer: button /
// disabled / no_installer — never an Install button that is not there); the
// agent's own English text only as a "technical detail" line.
const CHAT_WV2_CAUSES = {
  network: 'chat.sys.cause.network', signature: 'chat.sys.cause.signature',
  bootstrapper_missing: 'chat.sys.cause.bootstrapperMissing', timeout: 'chat.sys.cause.timeout',
  access_denied: 'chat.sys.cause.accessDenied', installer_failed: 'chat.sys.cause.installerError',
  exit_code: 'chat.sys.cause.installerError', launch: 'chat.sys.cause.installerError',
  not_detected: 'chat.sys.cause.installerError', cancelled: 'chat.sys.cause.installerError',
};
function chatUnavailableHint(info, failed) {
  const offer = info && typeof info.installOffer === 'string' ? info.installOffer : '';
  if (offer === 'disabled') return { k: 'chat.sys.unavailableHintDisabled' };
  if (offer === 'no_installer') return { k: 'chat.sys.unavailableHintNoInstaller' };
  if (offer === 'button') return { k: failed ? 'chat.sys.unavailableHintRetry' : 'chat.sys.unavailableHintInstall' };
  return null;
}
function chatUnavailableMsgs(info) {
  const code = info && typeof info.code === 'string' ? info.code : '';
  const state = info && typeof info.state === 'string' ? info.state : '';
  const detail = info && typeof info.detail === 'string' ? info.detail.trim() : '';
  const wv2Reason = info && typeof info.wv2Reason === 'string' ? info.wv2Reason : '';
  const technical = detail ? { k: 'chat.sys.unavailableTechnical', p: { detail: detail } } : null;
  const out = [];
  const add = (m) => { if (m) out.push(m); };
  if (code === 'webview2_missing') {
    if ((info && info.installing === true) || state === 'installing') return [{ k: 'chat.sys.unavailableInstalling' }];
    if (state === 'blocked') return [{ k: 'chat.sys.unavailableBlocked' }];
    if (state === 'unsupported') return [{ k: 'chat.sys.unavailableUnsupported' }];
    if (state === 'failed') {
      const cause = Object.prototype.hasOwnProperty.call(CHAT_WV2_CAUSES, wv2Reason) ? CHAT_WV2_CAUSES[wv2Reason] : '';
      add({ k: 'chat.sys.unavailableFailed' });
      add(cause ? { k: cause } : technical);
      add(chatUnavailableHint(info, true));
      return out;
    }
    add({ k: wv2Reason === 'per_user_only' ? 'chat.sys.unavailablePerUserOnly' : 'chat.sys.unavailableMissing' });
    add(chatUnavailableHint(info, false));
    return out;
  }
  if (code === 'no_user') return [{ k: 'chat.sys.unavailableNoUser' }];
  if (code === 'webview2_failed') { add({ k: 'chat.sys.unavailableNotStarted' }); add(technical); return out; }
  if (code === 'webview2_timeout') return [{ k: 'chat.sys.unavailableNotStarted' }];
  add({ k: 'chat.sys.unavailableHelper' });
  add(technical);
  return out;
}

function chatMarkEnded(c, reason, info) {
  if (c.ended) return;
  c.ended = reason === 'user' ? 'user' : reason === 'unavailable' ? 'unavailable' : 'operator';
  c.attempt = (c.attempt || 0) + 1; // nothing still in flight brings it back
  c.connected = false; c.opening = false; c.pending = false; c.rejoin = false; c.remoteAsked = false;
  c.userClosed = c.ended === 'user';
  if (chatActiveKey === c.key) hideChatTyping();
  if (c.ended === 'user') {
    chatAddMsg(c, 'System', { k: 'chat.sys.endedByUser' }, true);
    chatAttention(c, T('chat.attn.userClosed'));
  } else if (c.ended === 'unavailable') {
    for (const m of chatUnavailableMsgs(info)) chatAddMsg(c, 'System', m, true);
    chatAttention(c, T('chat.attn.unavailable'));
  } else {
    chatAddMsg(c, 'System', { k: 'chat.sys.endedElsewhere' }, true);
    chatRender();
  }
  const sock = chatSock(c.pid);
  if (sock && sock._renew) chatRenewSocketWhenIdle(c.pid);
}

// chat:opened {chatId, deviceUuid, deviceName, tenantId}: this user opened a
// chat from another window (Obliance web, another tab). The server already
// put this socket in its room: an unread tab here, no sound.
function onChatOpened(key, sock, data) {
  sock._unified = true;
  if (chatSockets[key] !== sock || !data || typeof data.chatId !== 'string' || !data.chatId ||
      typeof data.deviceUuid !== 'string' || !data.deviceUuid) return;
  if (chatConvById(data.chatId, key)) return;
  const c = chatAdoptRemote(key, sock, data, { k: 'chat.sys.openedElsewhere' });
  if (!c) return;
  setStatus(T('chat.attn.line', { device: chatDevLabel(c), text: T('chat.attn.openedElsewhere') }));
  chatRender();
}

// A live chat of this user opened elsewhere gets its tab here (created with
// the uuid and name the server sent: the device may not be loaded), or takes
// the machine's existing tab (ended, failed, or holding a chat the server no
// longer runs: one live chat per user and machine). A tab whose own chat:open
// is in flight is left alone: that open resumes this same chat.
function chatAdoptRemote(pid, sock, info, note) {
  // A detached session window only shows the chats of its own machines (the
  // devices of its tabs; the main window has them all).
  if (reachDetachedMode && !reachDetachedHasUuid(info.deviceUuid, pid)) return null;
  const key = chatKey(pid, info.deviceUuid);
  let c = chatConvs.get(key);
  if (c && ((c.chatId === info.chatId && !c.ended) || c.opening)) return null;
  if (!c) {
    c = chatNewConv(pid, info.deviceUuid, null, typeof info.deviceName === 'string' && info.deviceName ? info.deviceName : info.deviceUuid);
    chatConvs.set(key, c);
  }
  const tid = chatTenantOf(info.tenantId); // (the chat's tenant, as the server says)
  if (tid !== null) c.tenantId = tid;
  chatResolveDevice(c);
  chatAttach(c, sock, info.chatId, note);
  if (!chatActiveKey || !chatConvs.has(chatActiveKey)) chatActiveKey = key;
  if (!chatOnScreen(c) && !c.unread) c.unread = 1;
  return c;
}

// Binds a tab to a live chat the server already joined this socket to, and
// fetches the exchange so far (join ack {ok, history} of a unified server).
function chatAttach(c, sock, chatId, note) {
  const attempt = c.attempt = (c.attempt || 0) + 1; // acks still in flight for this tab are stale now
  c.chatId = chatId; c.boundSeq = ++chatBindSeq;
  c.connected = true; c.opening = false; c.pending = false; c.failed = false; c.rejoin = false;
  c.userClosed = false; c.ended = null; c.remoteAsked = false;
  let noted = false;
  const addNote = () => { if (noted) return; noted = true; if (note) chatAddMsg(c, 'System', note, true); chatRender(); };
  sock.emit('join', 'chat:' + chatId, (res) => {
    if (chatConvs.get(c.key) !== c || c.attempt !== attempt) return;
    if (res && !res.ok && res.ended) { noted = true; chatMarkEnded(c, res.ended, res); return; }
    if (res && res.ok === false) { // gone meanwhile, or no right to it any more: the Chat button starts a new one
      noted = true;
      chatLost(c, res.error === 'forbidden' ? chatErrorText('forbidden') : { k: 'chat.sys.gone' });
      return;
    }
    if (res && res.ok) chatMergeHistory(c, chatId, res.history);
    addNote();
  });
  setTimeout(() => { if (chatConvs.get(c.key) === c && c.attempt === attempt) addNote(); }, 3000);
}

// chat:list (ack) -> [{chatId, deviceUuid, deviceName, tenantId}]: the live
// chats of this user on this server, asked at every connect (after the
// rejoin). The ones opened elsewhere while this socket was away get their
// tab. An older server does not answer: ignored after 3 s.
function chatListSync(key, sock) {
  const since = chatBindSeq;
  let done = false;
  sock.emit('chat:list', {}, (list) => {
    if (done) return;
    done = true;
    if (chatSockets[key] !== sock || !Array.isArray(list)) return;
    sock._unified = true;
    let added = 0;
    for (const it of list) {
      if (!it || typeof it.chatId !== 'string' || !it.chatId || typeof it.deviceUuid !== 'string' || !it.deviceUuid) continue;
      if (chatConvById(it.chatId, key)) continue;
      // A tab that got its chat after this question (its own open resumed,
      // chat:opened) is newer than this answer.
      const cur = chatConvs.get(chatKey(key, it.deviceUuid));
      if (cur && cur.chatId && !cur.ended && cur.boundSeq > since) continue;
      if (chatAdoptRemote(key, sock, it, { k: 'chat.sys.continued' })) added++;
    }
    if (added) chatRender();
  });
  setTimeout(() => { done = true; }, 3000);
}

// Adds the messages of the chat's history (server, oldest first) this tab
// does not show yet: all of them when it shows none of that chat, else the
// ones after its last messages (aligned on up to 5, never duplicated: when
// the alignment fails nothing is added). Returns the end user's messages added.
function chatMergeHistory(c, chatId, history) {
  if (!Array.isArray(history) || !history.length) return 0;
  const h = [];
  for (const x of history) {
    if (!x || typeof x.message !== 'string') continue;
    h.push({ sender: String(x.sender || ''), text: x.message, fromOp: !!x.fromOperator, ts: Number(x.timestamp) || Date.now() });
  }
  const local = c.messages.filter(m => !m.isSystem && m.chatId === chatId);
  let start = 0;
  if (local.length) {
    start = -1;
    for (let m = Math.min(local.length, 5, h.length); m >= 1 && start < 0; m--) {
      for (let j = h.length; j >= m && start < 0; j--) {
        let same = true;
        for (let k = 1; k <= m && same; k++) {
          const a = local[local.length - k], b = h[j - k];
          same = a.text === b.text && !!a.fromOp === b.fromOp;
        }
        if (same) start = j;
      }
    }
    if (start < 0) return 0;
  }
  let fromUser = 0;
  for (const x of h.slice(start)) {
    chatAddMsg(c, x.sender, x.text, false, { fromOp: x.fromOp, chatId: chatId, timestamp: x.ts });
    if (!x.fromOp) fromUser++;
  }
  return fromUser;
}

function onChatRemoteResponse(data, pid) {
  const c = data ? chatConvById(data.chatId, pid) : null;
  if (!c) return;
  c.remoteAsked = false;
  chatAddMsg(c, 'System', { k: data.allowed ? 'chat.sys.remoteGranted' : 'chat.sys.remoteDenied' }, true);
  chatRender();
}

function onChatTyping(data, pid) {
  const c = data ? chatConvById(data.chatId, pid) : null;
  if (!c || chatActiveKey !== c.key) return;
  let shown = false;
  for (const v of chatViews()) {
    if (v.renderedKey !== c.key || !v.typing) continue;
    v.typing.classList.add('visible');
    if (v.box) v.box.scrollTop = v.box.scrollHeight;
    shown = true;
  }
  if (!shown) return;
  clearTimeout(chatTypingTimer);
  chatTypingTimer = setTimeout(hideChatTyping, 3000);
}

function hideChatTyping() {
  clearTimeout(chatTypingTimer);
  for (const v of chatViews()) if (v.typing) v.typing.classList.remove('visible');
}

function emitChatTyping() {
  const now = Date.now();
  if (now - lastChatTypingEmit < 2000) return;
  lastChatTypingEmit = now;
  const c = chatActive();
  const sock = c ? chatSock(c.pid) : null;
  if (sock && c.chatId && !c.ended) {
    sock.emit('chat:typing', { chatId: c.chatId });
  }
}

// Under the messages of a view: "Request Remote Control" (a live chat), the
// wait for the user's answer, or "Start a new chat" on an ended chat (or one
// that could not open), since the Chat page has no device button. New
// elements at every render: nothing bound twice.
function chatRenderRequest(v, c) {
  const wrap = v.req;
  if (!wrap) return;
  wrap.innerHTML = '';
  if (!c) return;
  const button = (label, title, icon, fn) => {
    const b = document.createElement('button');
    b.className = 'btn-sm chat-req-btn'; b.title = title;
    b.innerHTML = icon + esc(label);
    b.addEventListener('click', fn);
    wrap.appendChild(b);
  };
  if (chatCanRestart(c)) {
    button(c.ended ? T('chat.startNew') : T('common.tryAgain'), T('chat.startNewTitle'), REACH_ICON.chat, () => chatRestart(c));
    return;
  }
  if (!c.connected || c.userClosed) return;
  if (c.remoteAsked) {
    const w = document.createElement('div'); w.className = 'chat-req-wait'; w.textContent = T('chat.waitingResponse');
    wrap.appendChild(w);
    return;
  }
  button(T('chat.requestRemote'), T('chat.requestRemoteTitle'),
    '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>',
    () => chatRequestRemote(c));
}
function chatRequestRemote(c) {
  const sock = chatSock(c.pid);
  if (!sock || !c.chatId || c.remoteAsked || c.ended) return;
  sock.emit('chat:request_remote', { chatId: c.chatId, message: '' });
  c.remoteAsked = true;
  chatAddMsg(c, 'System', { k: 'chat.sys.remoteRequested' }, true);
  chatRender();
}

function playNotifSound() {
  try {
    const ctx = new AudioContext();
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.connect(gain); gain.connect(ctx.destination);
    osc.frequency.value = 800; gain.gain.value = 0.12;
    osc.start(); gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.25);
    osc.stop(ctx.currentTime + 0.25);
  } catch {}
}

// ── Top bar ──────────────────────────────────────────────────────────────────
document.getElementById('user-menu-logout').addEventListener('click', async () => {
  // Only this server is signed out: the chats of the other servers go on.
  // Its socket goes too (authenticated as the user who is leaving). Its chat
  // tabs go; on a unified server the chats themselves go on in this user's
  // other windows (Obliance web): signing out here does not end them there.
  const pid = activeServerId || '';
  if (serverMode !== 'multi' && srvIsLocal(pid)) return; // (a local base: no account to sign out of; hidden: userMenuServerRender)
  if (serverMode === 'multi') { await multiSignOut(pid); return; }
  appReady = false;
  reachDisposeAllTabs(); reachCloseAllDetached(); closeChat(pid, true); chatDropSocket(pid); chatRetryClear(pid);
  // The terminals end too, their POST /end made with this session (before it goes).
  const toolsEnded = toolDisposeAll();
  // (signed out: no chat socket for it until its next sign-in; the other
  // servers' sockets stay, their chats go on)
  { const st = srvState(pid); st.seq++; st.signedIn = false; st.me = null; st.mePayload = null; }
  sidePanelClose(); sidePanelForget(); // (script runs and their output: this user's)
  await toolsEnded;
  await fetch('/local/logout', { method: 'POST' });
  // Close menus
  document.getElementById('user-menu').classList.remove('open');
  document.getElementById('tenant-menu').classList.remove('open');
  document.getElementById('app').style.display = 'none';
  document.getElementById('login-overlay').style.display = 'flex';
  document.getElementById('inp-pass').value = '';
  document.getElementById('login-err').textContent = '';
  document.getElementById('sso-err').textContent = '';
  const server = document.getElementById('inp-server').value.trim();
  if (server) checkSso();
  loadServers().then(renderLoginServers); // the way back to the other servers
});
// Multi-server mode: one server signed out (the active one, the top bar's
// account); its sessions and windows end, the other servers' go on, and the
// app stays on them (the sign-in page when none is left).
async function multiSignOut(pid) {
  appReady = false;
  reachLeaveServerOnly(pid);
  closeChat(pid, true); chatDropSocket(pid); chatRetryClear(pid);
  sidePanelClose(); sidePanelForget();
  // Signed out from now on (no chat socket for it is opened meanwhile).
  const st = srvState(pid);
  st.seq++; st.signedIn = false; st.me = null; st.mePayload = null; st.groups = []; st.tenants = []; st.scoped = false; st.error = '';
  await toolEndsWait(); // (its terminals' POST /end, made with this session, before it goes)
  try { await fetch('/local/logout', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ profile: pid }) }); } catch {}
  document.getElementById('user-menu').classList.remove('open');
  document.getElementById('tenant-menu').classList.remove('open');
  if (currentRoute === 'device-detail' && selectedDevice && devPid(selectedDevice) === pid) { currentRoute = 'devices'; currentRouteParams = null; }
  if (serverProfiles.some(p => p.id !== pid && srvState(p.id).signedIn === true)) { await enterApp({ keepPage: true }); return; }
  document.getElementById('sso-err').textContent = '';
  await showSignInPage();
}
document.getElementById('btn-refresh').addEventListener('click', async () => {
  // The account too: a theme chosen meanwhile in the Obliance web app
  // (multi-server mode: every server's).
  if (serverMode === 'multi') { scriptsCache.clear(); await loadAllServers(); }
  else await Promise.all([loadMe(), loadOverview()]);
  if (currentRoute === 'dashboard' || currentRoute === 'devices' || currentRoute === 'scripts' || currentRoute === 'deployment' || currentRoute === 'vault' || currentRoute === 'vms' || currentRoute === 'run') navigateTo(currentRoute, currentRouteParams);
});

// ── Detached session window (?detached=<id>, detached_windows.go) ─────────────
// One remote session in its own window: focus mode (the picture, and the
// auto-hiding toolbar with the device name, Scripts, Info and Chat), connected
// at once. It signs in silently: the server session of the main window (same
// local proxy, same cookies). No sign-in page here: when that session is
// gone, the window says so. Closing the window ends its session.
async function detachedBoot() {
  const p = reachDetachedParams;
  // Its device is this window's until its tab exists (or for good, when it
  // cannot start: the notice stays here): the app never frees it meanwhile.
  const bootKey = reachBootKey();
  reachWinClaims.add(bootKey);
  document.body.classList.add('reach-detached', 'reach-focus');
  document.getElementById('login-overlay').style.display = 'none';
  document.getElementById('app').style.display = 'flex';
  await loadServers();
  // This window's account is its server's, whatever server becomes active
  // later (multi-server mode, entered while it lives on).
  srvShownPid = p.profile || null;
  // A local connection (localconn_tools.go): the vault's, whatever server.
  if (p.profile === LOCAL_PID) { await detachedBootLocal(p, bootKey); return; }
  if (serverMode === 'multi') { await detachedBootMulti(p, bootKey); return; }
  // The device id belongs to the server the main window was on when it asked
  // for this window: the app switched servers since (ids of two servers
  // overlap), so another machine could have that id here. Nothing starts,
  // nothing is asked of any server.
  if ((activeServerId || '') !== (p.profile || '')) { reachDetachedNotice('detached.serverChanged', 'detached.serverChangedText'); return; }
  let cfg = {};
  try { cfg = await (await fetch('/local/config')).json() || {}; } catch {}
  const signedIn = !!(cfg.hasSession && cfg.serverUrl) && await loadMe();
  if (!signedIn) { reachDetachedNotice('detached.signedOut', 'detached.signedOutText'); return; }
  loadServerLocalState();
  await loadOverview();
  let dev = null;
  for (const g of overview.groups || []) for (const d of g.devices || []) if (d && String(d.id) === p.deviceId) dev = d;
  if (!dev) { reachDetachedNotice('detached.notFound', 'detached.notFoundText'); return; }
  await detachedStart(dev, p, bootKey);
}
// Multi-server mode: the window's device is of the server it was opened for
// (p.profile), whichever server is active: that server is read (its own
// session), and the window's operator is its account.
async function detachedBootMulti(p, bootKey) {
  multiFilter = { pid: '', tenantId: null }; // (its "+" lists every server; not saved)
  if (!p.profile || !serverProfiles.some(x => x.id === p.profile)) { reachDetachedNotice('detached.serverGone', 'detached.serverGoneText'); return; }
  const st = await srvLoad(p.profile);
  if (st.signedIn === false) { reachDetachedNotice('detached.signedOut', 'detached.signedOutText'); return; }
  // Its server could not be read (unreachable, an error, its devices): said
  // as it is, with "Try again" (the window boots again).
  if (st.error) {
    reachDetachedNotice('detached.serverGone', st.errorTimeout ? 'devices.server.noAnswer' : 'devices.server.error', { server: srvName(p.profile), error: st.error }, () => { detachedBoot(); });
    return;
  }
  if (st.signedIn !== true) { reachDetachedNotice('detached.serverGone', 'detached.serverGoneText'); return; }
  srvShownPid = p.profile; // (this window's operator: its server's account, even after the others are read)
  srvApplyMe(p.profile, st.mePayload, true);
  loadServerLocalState();
  overview = { groups: st.groups };
  const dev = reachFindDevice(p.deviceId, p.profile);
  if (!dev) { reachDetachedNotice('detached.notFound', 'detached.notFoundText'); return; }
  await detachedStart(dev, p, bootKey);
}
// A window opened for a local connection (or a tab of one moved here): the
// vault's connection (its description: reachVaultConnInfo), never a server.
// The vault locked: its unlock first.
async function detachedBootLocal(p, bootKey) {
  if (serverMode === 'multi') multiFilter = { pid: '', tenantId: null }; // (its "+" lists every server; not saved)
  srvShownPid = null;
  const vs = await vaultSyncCall('reachVaultState');
  if (!vs || vs.state !== 'unlocked') {
    reachDetachedNotice('vault.locked.title', 'devices.local.locked', null, () => { cxnUnlockThen(() => { detachedBoot(); }); });
    return;
  }
  const dev = await cxnFindDevice(p.deviceId);
  if (!dev) { reachDetachedNotice('detached.notFound', 'detached.notFoundText'); return; }
  if (typeof localConnsLoad === 'function') localConnsLoad().catch(() => {}); // (its "+" list: the other connections)
  await detachedStart(dev, p, bootKey);
}
async function detachedStart(dev, p, bootKey) {
  // A Hyper-V VM console's window (vm_console.go): its VM, on this host.
  if (p.vm) dev = vmDevice(dev, p.vm);
  reachDetachedDevice = dev;
  document.title = T('win.docTitle', { name: dev.hostname || T('win.session') });
  reachWinSetTitle(dev);
  // (a local connection: no server's scripts; the chats: those of the servers, if any)
  if (devIsLocal(dev)) { /* nothing to ask a server */ }
  else if (serverMode === 'multi') scriptsFor(dev); // (its server's and tenant's)
  else loadScripts().then(() => { if (sidePanelShown === 'scripts') sidePanelRender(); });
  if (!devIsLocal(dev) || serverMode === 'multi' || !activeServerIsLocal()) initSocketIO();
  // A tool window (?...&m=<kind>: a terminal, the file explorer): its tool
  // tab, whatever ObliReach does (never an ObliReach session here). Its
  // gates and privacy code were passed in the window that asked for it.
  if (p.kind) {
    appReady = true;
    const r = await toolTabOpen(dev, p.kind, 'boot', { park: p.park, path: p.path }); // (a tab moved here: toolTearOff)
    if (r !== 'here' && r !== 'self') openDeviceDetail(dev, 'remote'); // (no such tool in this build: the device's page)
    reachWinClaims.delete(bootKey); // (its tab now, reported)
    reachReportTabs(true);
    return;
  }
  openDeviceDetail(dev, 'remote');
  appReady = true;
  if (!dev.oblireach || !dev.oblireach.online) return; // the pane says the agent is offline
  const tab = addSessionTab(dev);
  reachWinClaims.delete(bootKey);
  if (p.viewOnly) tab.viewOnly = true;
  startRemote(p.session >= 0 ? p.session : undefined, tab);
}

// The servers changed (servers.go notifyServers: a server switch, the mode,
// a server added or removed, from the main window). A session window reads
// them again: the mode decides how its device actions go (multi-server mode:
// each tab's server session put on its device's tenant first, so each of
// those servers is read, its scope known) and which tabs it may take
// (reachWinAdopt). Its own calls always name their server (api()). The main
// window made the change itself: nothing to do there.
window.__reachOnServers = function () {
  if (!reachDetachedMode) return;
  reachWinServersChanged().catch(() => {});
};
async function reachWinServersChanged() {
  const was = serverMode;
  await loadServers();
  if (serverMode === 'multi') {
    const pids = new Set();
    if (reachDetachedDevice) pids.add(devPid(reachDetachedDevice));
    for (const t of sessionTabs) if (t && t.device) pids.add(devPid(t.device));
    for (const t of toolTabs) if (t && t.device) pids.add(devPid(t.device));
    for (const pid of pids) {
      if (!pid || !serverProfiles.some(x => x.id === pid)) continue;
      if (was !== 'multi' || srvState(pid).signedIn !== true) await srvLoad(pid);
    }
  }
  renderSessionTabs(); // (the title bar: its tabs' servers' dots)
}

// ── "+" of a session window: the device list, to connect another one here ──
// The Devices table (same rows, tree, search and filters: devViewFill), over
// the window below its title bar. A row (or its Connect button) goes to
// reachWinAddDevice (reach_viewer.go): its tab here or elsewhere comes to the
// front, else it connects in a new tab (the Windows session picked first when
// there are several). Esc, the backdrop or "+" again close it. Under each
// Hyper-V host, its VMs (vms_page.go dpVmRows: the VMs page's lists, read
// while this list is open): a click opens its console here.
let devPickerFilter = { q: '', online: '', os: '' };
let devPickerSearchFolded = new Set();
let devPickerBuilt = false;
function devViewPicker() {
  // ▶ / the row: ObliReach here (reachWinAddDevice), else the agent's tools
  // here; a tool button: that tool here (devPickerLaunch, agent_tools.go).
  return { tbodyId: 'dp-tbody', countId: 'dp-count', filter: () => Object.assign({}, devPickerFilter, { qMore: dpVmQMore }), searchFolded: () => devPickerSearchFolded,
    actions: [['remote', T('picker.connectHere')], ['tools', ''], ['files', '']], refresh: devPickerRefresh,
    localConns: true, // (the local bases' connections too: their terminal here)
    open: (dev, act) => { devPickerLaunch(dev, act); },
    // A Hyper-V host's VMs under it, found by the search too (vms_page.go).
    prepare: dpVmPrepare, children: dpVmRows,
    // Boxes to tick several devices, "Connect" in the footer (picker_multi.go);
    // the listed VMs' too.
    select: dpSelectHooks, onTree: (tree) => { dpPrune(tree.devs.concat(dpVmListedDevs(tree.devs))); dpFooter(); } };
}
function devPickerRefresh() { devViewFill(devViewPicker()); }
function devPickerIsOpen() { return devPickerBuilt && document.getElementById('dev-picker').classList.contains('open'); }
function devPickerNote(text) {
  if (dpBatch) { if (text) dpBatch.notes.push(text); return; } // ("Connect" on several: said once at the end)
  if (!devPickerBuilt) { if (text) reachToast(text, 'warn'); return; }
  const n = document.getElementById('dp-note');
  n.textContent = text || '';
  n.style.display = text ? '' : 'none';
}
function devPickerClose() {
  if (!devPickerBuilt) return;
  document.getElementById('dev-picker').classList.remove('open');
  document.getElementById('wt-add').classList.remove('on');
  devPickerNote('');
  dlSchedule(); // (a session window reads no more: devices_live.go)
}
function devPickerOpen() {
  if (!devPickerBuilt) devPickerBuild();
  devPickerNote('');
  dpSelClear(); // (a fresh selection each time)
  document.getElementById('dev-picker').classList.add('open');
  document.getElementById('wt-add').classList.add('on');
  if (typeof vmsKick === 'function') vmsKick(); // (the Hyper-V hosts' VMs, read while it is open: vms_page.go vmsDataOn)
  devPickerRefresh();
  const q = document.getElementById('dp-q');
  try { q.focus(); } catch (e) {}
  // The machines as they are now (online, their Windows sessions); the mode
  // may have changed since this window opened: every server, or the one.
  (async () => {
    await loadServers();
    if (serverMode === 'multi') { multiFilter = { pid: '', tenantId: null }; await loadAllServers(); } else await loadOverview();
  })().then(() => { if (devPickerIsOpen()) devPickerRefresh(); }).catch(() => {}).then(() => { dlStart(); }); // (kept up to date while open: devices_live.go)
}
// id: '' for none (the Chat page's picker: no id, none shared with another view).
function devPickerSelect(id, options, value, onChange) {
  const sel = document.createElement('select'); if (id) sel.id = id;
  // label: a catalog key (the option redrawn at a language switch), or a name.
  for (const [v, label] of options) {
    const o = document.createElement('option'); o.value = v;
    if (/^[a-z]+\.[A-Za-z.]+$/.test(label)) { o.textContent = T(label); o.setAttribute('data-i18n', label); } else o.textContent = label;
    sel.appendChild(o);
  }
  sel.value = value;
  sel.addEventListener('change', e => onChange(e.target.value));
  return sel;
}
function devPickerBuild() {
  devPickerBuilt = true;
  const ov = document.createElement('div'); ov.className = 'dev-picker'; ov.id = 'dev-picker';
  const box = document.createElement('div'); box.className = 'dev-picker-box';
  // Built once: its texts carry their keys (data-i18n*), redrawn at a language switch.
  const i18n = (el, attr, key) => { el.setAttribute(attr, key); return el; };
  box.setAttribute('role', 'dialog'); box.setAttribute('aria-label', T('picker.title')); i18n(box, 'data-i18n-aria', 'picker.title');
  const head = document.createElement('div'); head.className = 'dev-picker-head';
  const h = document.createElement('h3'); h.textContent = T('picker.title'); i18n(h, 'data-i18n', 'picker.title');
  const sub = document.createElement('span'); sub.className = 'muted-hint'; sub.textContent = T('picker.sub'); i18n(sub, 'data-i18n', 'picker.sub');
  const sp = document.createElement('span'); sp.className = 'spacer';
  const refresh = document.createElement('button'); refresh.className = 'btn-sm'; refresh.id = 'dp-refresh'; refresh.textContent = T('common.refresh'); i18n(refresh, 'data-i18n', 'common.refresh');
  refresh.addEventListener('click', async () => { try { await loadOverview(); } catch (e) {} devPickerRefresh(); });
  const close = document.createElement('button'); close.className = 'btn-sm'; close.id = 'dp-close'; close.title = T('common.close'); i18n(close, 'data-i18n-title', 'common.close'); close.style.cssText = 'padding:4px 8px';
  close.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
  close.addEventListener('click', devPickerClose);
  head.appendChild(h); head.appendChild(sub); head.appendChild(sp); head.appendChild(refresh); head.appendChild(close);
  const bar = document.createElement('div'); bar.className = 'filter-bar';
  const q = document.createElement('input'); q.id = 'dp-q'; q.style.minWidth = '220px'; q.placeholder = T('devices.searchPlaceholder'); i18n(q, 'data-i18n-ph', 'devices.searchPlaceholder'); q.value = devPickerFilter.q;
  q.addEventListener('input', e => { devPickerFilter.q = e.target.value; devPickerSearchFolded = new Set(); devPickerRefresh(); });
  bar.appendChild(q);
  bar.appendChild(devPickerSelect('dp-online', [['', 'devices.filter.allStates'], ['online', 'devices.filter.online'], ['offline', 'devices.filter.offline']], devPickerFilter.online, v => { devPickerFilter.online = v; devPickerRefresh(); }));
  bar.appendChild(devPickerSelect('dp-os', [['', 'devices.filter.allOs'], ['windows', 'Windows'], ['linux', 'Linux'], ['macos', 'macOS'], ['ssh', 'SSH'], ['rdp', 'RDP']], devPickerFilter.os, v => { devPickerFilter.os = v; devPickerRefresh(); }));
  const spacer = document.createElement('span'); spacer.className = 'spacer'; bar.appendChild(spacer);
  const hint = document.createElement('span'); hint.className = 'muted-hint';
  const cnt = document.createElement('span'); cnt.id = 'dp-count'; cnt.textContent = '0';
  const rs = document.createElement('span'); rs.id = 'dp-count-label'; rs.textContent = T('devices.resultsWord', { count: 0 });
  rs.style.marginInlineStart = '.25em'; // (the count, then the word)
  hint.appendChild(cnt); hint.appendChild(rs);
  bar.appendChild(hint);
  const note = document.createElement('div'); note.className = 'dev-picker-note'; note.id = 'dp-note'; note.style.display = 'none';
  const list = document.createElement('div'); list.className = 'panel dev-picker-list';
  const pb = document.createElement('div'); pb.className = 'panel-body flush';
  const table = document.createElement('table'); table.className = 'dt';
  const thead = document.createElement('thead');
  thead.innerHTML = devTableHead();
  const tbody = document.createElement('tbody'); tbody.id = 'dp-tbody';
  table.appendChild(thead); table.appendChild(tbody); pb.appendChild(table); list.appendChild(pb);
  // Several devices ticked: their count, "Clear", "Connect" (picker_multi.go).
  const foot = document.createElement('div'); foot.className = 'dp-foot'; foot.id = 'dp-foot'; foot.style.display = 'none';
  const fc = document.createElement('span'); fc.className = 'dp-foot-count'; fc.id = 'dp-sel-count';
  const fsp = document.createElement('span'); fsp.className = 'spacer';
  const fclr = document.createElement('button'); fclr.className = 'btn-sm'; fclr.id = 'dp-sel-clear'; fclr.textContent = T('common.clear'); i18n(fclr, 'data-i18n', 'common.clear');
  fclr.addEventListener('click', () => { dpSelClear(); devPickerRefresh(); });
  const fgo = document.createElement('button'); fgo.className = 'btn-primary'; fgo.id = 'dp-sel-go'; fgo.textContent = T('common.connect'); i18n(fgo, 'data-i18n', 'common.connect');
  fgo.addEventListener('click', () => { dpConnect(); });
  foot.appendChild(fc); foot.appendChild(fsp); foot.appendChild(fclr); foot.appendChild(fgo);
  box.appendChild(head); box.appendChild(bar); box.appendChild(note); box.appendChild(list); box.appendChild(foot);
  ov.appendChild(box);
  ov.addEventListener('mousedown', e => { if (e && e.target === ov) devPickerClose(); }); // the backdrop
  ov.addEventListener('keydown', e => { if (e && e.key === 'Escape' && !reachPickerDone) { e.preventDefault(); devPickerClose(); } });
  document.body.appendChild(ov);
}

// Detached window that cannot show its session: why, and a way to close it.
// titleKey / textKey: catalog keys (kept: drawn again at a language switch).
// textParams: its parameters; retry: a "Try again" (the window boots again).
let reachDetachedNoticeKeys = null;
function reachDetachedNotice(titleKey, textKey, textParams, retry) {
  reachDetachedNoticeKeys = [titleKey, textKey, textParams || null, retry || null];
  const pc = document.getElementById('page-content');
  pc.innerHTML = '';
  const box = document.createElement('div'); box.className = 'empty reach-detached-pane';
  const t = document.createElement('div'); t.className = 'rd-title'; t.textContent = T(titleKey);
  const d = document.createElement('div'); d.className = 'rd-text'; d.textContent = textParams ? T(textKey, textParams) : T(textKey);
  const b = document.createElement('button'); b.className = 'btn-sm'; b.textContent = T('detached.closeWindow');
  b.addEventListener('click', reachCloseOwnWindow);
  box.appendChild(t); box.appendChild(d);
  if (retry) {
    const rb = document.createElement('button'); rb.className = 'btn-sm'; rb.textContent = T('common.tryAgain');
    rb.addEventListener('click', () => { reachDetachedNoticeKeys = null; retry(); });
    box.appendChild(rb);
  }
  box.appendChild(b);
  pc.appendChild(box);
}

// The window is closing (detached_windows.go beginClose): the tunnel closes
// cleanly. Chats stay: a unified server keeps them for the main window (and
// the Obliance web); an older one would lose them, so they end here.
window.__reachDetachedClosing = function () {
  if (!reachDetachedMode) return;
  try { reachDisposeAllTabs(); } catch (e) {}
  try { toolDisposeAll(); } catch (e) {} // (its terminals: POST /end, outliving the page)
  try {
    for (const [k, c] of Array.from(chatConvs)) { const s = chatSock(c.pid); if (!(s && s._unified)) chatCloseConv(k); }
    chatSyncOn = false; // (nothing opened again while it closes)
    Object.keys(chatRetry).forEach(chatRetryClear);
    Object.keys(chatSockets).forEach(chatDropSocket);
  } catch (e) {}
};

// The app quits to install an update (mainwindow_windows.go quitApp): as on a
// sign-out of every server, the sessions of this window end cleanly and the
// chats are left (on a unified server they go on in the other windows,
// Obliance web; an older one gets chat:close). The session windows close
// their own way (__reachDetachedClosing).
window.__reachAppClosing = function () {
  if (reachDetachedMode) return;
  try { reachDisposeAllTabs(); } catch (e) {}
  try { toolDisposeAll(); } catch (e) {} // (its terminals: POST /end, outliving the page)
  try {
    closeChat(undefined, true);
    chatSyncOn = false; // (nothing opened again while it quits)
    Object.keys(chatRetry).forEach(chatRetryClear);
    Object.keys(chatSockets).forEach(chatDropSocket);
  } catch (e) {}
};

// ── Init ─────────────────────────────────────────────────────────────────────
(async function init() {
  if (reachDetachedMode) { await detachedBoot(); return; }
  // The main window says at load which sessions it has (none after a
  // reload): the app must not bring it forward for sessions it no longer has.
  // (From here, not from reach_viewer.go's top level: this script's globals,
  // sessionTabs, must exist when it runs.)
  reachReportTabs(true);
  const cfgR = await fetch('/local/config');
  const cfg = await cfgR.json();
  if (cfg.serverUrl) {
    document.getElementById('inp-server').value = cfg.serverUrl;
    const ts = document.getElementById('top-server'); if (ts) ts.textContent = cfg.serverUrl;
  }
  if (cfg.username) document.getElementById('inp-user').value = cfg.username;

  if (cfg.hasSession && cfg.serverUrl) {
    try { const r = await api('GET', '/api/auth/me'); if (r.ok) { await enterApp(); return; } } catch {}
  }
  await loadServers();
  // A local base: its connections at once, never a sign-in page.
  if (serverMode !== 'multi' && activeServerIsLocal()) { await enterApp(); return; }
  document.getElementById('login-overlay').style.display = 'flex';
  // Multi-server mode: the app shows the servers signed in (enterApp puts
  // one of them in front), even when the active one is signed out.
  if (serverMode === 'multi' && serverProfiles.some(p => p.id !== activeServerId && p.hasSession)) { await enterApp(); return; }
  renderLoginServers();
  if (cfg.serverUrl) checkSso();
})();
</script>
</body>
</html>`,
		esc(cfg.ServerURL),
		esc(cfg.Username),
	)

	// The page's language (i18n.go): <html lang dir>, the {{t:key}} texts of the
	// markup (before the scripts go in: they hold no such token).
	lang := cfg.uiLang()
	html = strings.Replace(html, `<html lang="en">`, `<html `+htmlLangAttrs(lang)+`>`, 1)
	html = i18nMarkup(html, lang)

	// The theme (theme.go): its variables, and the theme worn from the first
	// paint (on <body>: <html> keeps its lang / dir only); a change comes to
	// every window afterwards by window.__reachOnTheme.
	html = strings.Replace(html, `<!--THEME_CSS-->`, "<style>"+themeCSS+"</style>\n<style>"+controlsCSS+dropdownCSS+"</style>", 1) // (themeCSS starts on a new line; then the base of the controls and the drop-down lists, before the page's rules: controls.go, dropdown.go)
	html = strings.Replace(html, "\n<body>\n", "\n<body data-theme=\""+cfg.uiTheme()+"\">\n", 1)
	html = strings.Replace(html, "[]/*THEME_CATALOG*/", themeCatalogJS(), 1) // (the settings' list of themes: theme.go themeCatalog)

	// Inject embedded SVG logos (done via Replace, not Sprintf, because SVGs may contain %)
	// Both SVGs use Illustrator-generated class names (.cls-1, .cls-2, etc.) that would
	// conflict when both are inline in the same HTML. Prefix each to scope them.

	// Login logo: prefix cls- → lcls-
	loginSVG := strings.TrimPrefix(logoLoginSVG, `<?xml version="1.0" encoding="UTF-8"?>`)
	loginSVG = strings.TrimSpace(loginSVG)
	loginSVG = strings.ReplaceAll(loginSVG, "cls-", "lcls-")
	loginSVG = strings.Replace(loginSVG, "<svg ", `<svg style="width:260px;height:auto" `, 1)
	html = strings.Replace(html, `<div style="text-align:center;padding:4px 0 0" id="login-logo"></div>`,
		`<div style="text-align:center;padding:4px 0 0" id="login-logo">`+loginSVG+`</div>`, 1)

	// Topbar logo: same full logo as login, sized to fit the 44px topbar.
	// Must also prefix gradient/element IDs to avoid conflicts with the login copy.
	topbarSVG := strings.TrimPrefix(logoLoginSVG, `<?xml version="1.0" encoding="UTF-8"?>`)
	topbarSVG = strings.TrimSpace(topbarSVG)
	topbarSVG = strings.ReplaceAll(topbarSVG, "cls-", "tcls-")
	topbarSVG = strings.ReplaceAll(topbarSVG, `id="`, `id="tb_`)
	topbarSVG = strings.ReplaceAll(topbarSVG, `url(#`, `url(#tb_`)
	topbarSVG = strings.ReplaceAll(topbarSVG, `xlink:href="#`, `xlink:href="#tb_`)
	topbarSVG = strings.Replace(topbarSVG, "<svg ", `<svg style="height:24px;width:auto" `, 1)
	html = strings.Replace(html, `<span class="logo" id="topbar-logo"></span>`,
		`<span class="logo" id="topbar-logo">`+topbarSVG+`</span>`, 1)

	// Slim title bar icon (focus mode, detached windows): the app's icon, its
	// class names and IDs prefixed like the top bar logo's.
	iconSVG := strings.TrimPrefix(logoIconSVG, `<?xml version="1.0" encoding="UTF-8"?>`)
	iconSVG = strings.TrimSpace(iconSVG)
	iconSVG = strings.ReplaceAll(iconSVG, "cls-", "icls-")
	iconSVG = strings.ReplaceAll(iconSVG, `id="`, `id="ti_`)
	iconSVG = strings.ReplaceAll(iconSVG, `url(#`, `url(#ti_`)
	iconSVG = strings.ReplaceAll(iconSVG, `xlink:href="#`, `xlink:href="#ti_`)
	iconSVG = strings.Replace(iconSVG, "<svg ", `<svg style="width:16px;height:16px;display:block" `, 1)
	html = strings.Replace(html, `<span class="wt-logo" id="wt-logo"></span>`,
		`<span class="wt-logo" id="wt-logo">`+iconSVG+`</span>`, 1)

	// Remote viewer additions (reach_viewer.go), injected verbatim (not through
	// Sprintf): protocol constants first, then the library the main script uses.
	// The Obliance agent's tools (agent_tools.go), their tabs and terminals
	// (terminal_viewer.go), the file explorer (explorer_viewer.go) and the
	// main window's Files page (files_page.go) go in the same blocks, after
	// the library.
	html = strings.Replace(html, `<!--REACH_CSS-->`, "<style>"+reachViewerCSS+toolsCSS+termCSS+termThemesCSS+tabGroupsCSS+explorerCSS+usageCSS+filesPageCSS+filesDockCSS+tabMoveCSS+localPaneCSS+xferCSS+editCSS+termDropCSS+vaultCSS+vaultPageCSS+scopePagesCSS+scriptsPageCSS+scriptsPanelCSS+runPageCSS+deploymentPageCSS+connectionsPageCSS+connImportCSS+localConnCSS+rdpViewCSS+pickMultiCSS+vaultDevicesCSS+reachResolutionCSS+reachCodecCSS+reachPresenceCSS+vmConsoleCSS+vmsPageCSS+globalSearchCSS+devLiveCSS+supportCSS+supChatCSS+helpCSS+supDashCSS+platformPageCSS(uiTargetOS)+"</style>", 1)
	html = strings.Replace(html, `<!--REACH_LIB-->`,
		"<script>"+i18nPageJS(lang)+reachProtocolJS+"</script>\n<script>"+reachViewerJS+toolsJS+termJS+termThemesJS+tabGroupsJS+explorerJS+usageJS+filesPageJS+filesDockJS+tabMoveJS+localPaneJS+xferJS+editJS+termDropJS+vaultJS+vaultPageJS+scopePagesJS+scriptsPageJS+scriptsPanelJS+runPageJS+deploymentPageJS+connectionsPageJS+connImportJS+localConnJS+rdpViewJS+pickMultiJS+vaultDevicesJS+reachResolutionJS+reachCodecJS+reachPresenceJS+vmConsoleJS+vmsPageJS+globalSearchJS+devLiveJS+supportJS+supChatJS+helpJS+supDashJS+dropdownJS+platformPageJS(uiTargetOS)+"</script>", 1)
	html = injectLateJS(html, platformLateJS(uiTargetOS)) // (ui_platform.go: nothing on Windows)

	return html
}

// reachProtocolJS is the desktop client's copy of the ObliReach viewer<->agent
// protocol constants (Go copy in agent/protocol.go, TypeScript copy in
// Obliance client/src/components/remote/reachProtocol.ts). Every new behaviour
// is gated on a feature the agent advertises in init.features.
const reachProtocolJS = `
var REACH = Object.freeze({
  // Binary frame type (first byte).
  FRAME: Object.freeze({
    JPEG: 0x01, H264: 0x02, VP9: 0x03, H265: 0x04, AV1: 0x05,
    AUDIO_PCM: 0x06,  // legacy: PCM s16le mono, rate = init.audioRate
    AUDIO_V2: 0x07,   // 20-byte header (version, codec, channels, rate, seq, pos)
    FILE_UP: 0x20,    // viewer -> agent: [0x20][tid u32 LE][seq u32 LE][data]
    FILE_DOWN: 0x21,  // agent -> viewer: same layout
  }),
  // init.features values.
  FEATURE: Object.freeze({
    VIEW_GEN: 'view_gen', MOSAIC: 'mosaic',
    CLIP_TEXT: 'clipboard_text', CLIP_PUSH: 'clipboard_push', CLIP_FILES: 'clipboard_files',
    FILE_UPLOAD: 'file_upload', FILE_DOWNLOAD: 'file_download',
    AUDIO_V2: 'audio_v2', SESSION_FOLLOW: 'session_follow', ERROR_CODES: 'error_codes',
    KEEPALIVE: 'keepalive', // viewer -> agent {type:'keepalive'}: resets the idle timer, injects nothing
    // Key messages may carry macMap (macOS keyboard mapping, agent
    // keymap_mac.go): only macOS agents list it (reachKeyMsg).
    KEY_MAP_MAC: 'key_map_mac',
    // set_input_block blocks ONLY the remote machine's own mouse/keyboard (the
    // operator keeps control) and input_block_status reports the real state.
    // Agents without it block the operator too (Win32 BlockInput) or block
    // nothing (macOS/Linux): the Desktop hides the button for them.
    INPUT_BLOCK_LOCAL: 'input_block_local',
    // display_modes_get / display_mode_set: the remote screen's resolution
    // among the modes Windows offers (reach_resolution.go). Windows agents.
    DISPLAY_MODES: 'display_modes',
    // set_blank_screen: the remote's screens show an "Oblireach session in
    // progress" page, the operator keeps seeing the desktop (reach_presence.go).
    // Windows 10 2004+ agents; init.blankScreen.reason says why otherwise.
    BLANK_SCREEN: 'blank_screen',
    // Lock the session when the connection ends (agent lock_on_end.go):
    // session_lock_state {lockedAtConnect, lockOnEnd} agent -> viewer,
    // set_lock_on_end {enabled} viewer -> agent (reach_viewer.go). Windows agents.
    LOCK_ON_END: 'lock_on_end',
  }),
  // Keepalive period of a watched session (the agent's idle timeout is 10 min).
  KEEPALIVE_MS: 60000,
  // set_view budgets tried with VideoDecoder.isConfigSupported, largest first.
  MOSAIC_BUDGETS: [[4096, 2304], [3840, 2160], [2560, 1440]],
  MOSAIC_MAX_PIXELS: 8294400,
  CLIP_MAX_TEXT: 1048576,
  CONNECT_TIMEOUT_MS: 45000,     // no paired / session_state / init
  QUEUED_TIMEOUT_MS: 300000,     // POST answered delivery:'queued'
  RECONNECT_DELAYS_MS: [2000, 2000, 3000, 5000, 5000, 8000, 8000, 10000],
  // Default retry flag per error code when an error message carries none.
  ERROR_RETRY: Object.freeze({
    helper_spawn_failed: true, helper_timeout: true, capture_init_failed: true,
    encoder_init_failed: true, session_not_found: false, session_no_display: false, session_logged_off: false,
    session_switch_timeout: true, capture_unavailable: false, privacy_mode: false, internal: true,
    agent_timeout: false, agent_offline: false, agent_error: false, session_closed: false,
    // Hyper-V VM consoles (vm_console.go): the agent / server said why, never retried.
    vm_off: false, vm_not_found: false, vm_not_hyperv: false, vmconsole_disabled: false, vmconsole_install_failed: false,
    vm_console_start_failed: false, vm_console_connect_failed: false, vm_console_taken: false, vm_console_auth: false,
    vm_console_closed: false, vm_console_lost: false, vm_console_encoder: false, vm_console_ended: false, relay_refused: false,
  }),
});
`

func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
