package main

// support_chat.go — the chat of a code session (support_page.go), operator
// side. The host of an Oblireach Support session (the thin client, or
// Desktop's "Receive help": receive_help.go) advertises "chat" in its init
// features when it has a chat; lines go both ways as {"type":"chat","text"}
// on the session itself (not Obliance's chat: a code session has no server
// device). Text only, 4096 bytes at most per line, drawn with textContent
// only; the transcript lives in the tab (memory) and goes with it.
//
// The look is the main chat panel's (ui.go .chat-msg / .bubble /
// .chat-input-row / .chat-send-btn), in a panel over the right side of the
// picture, opened from a toolbar button with an unread badge.
//
// Both consts are injected verbatim after the other tool blocks (ui.go
// buildUI); no backtick here.

const supChatCSS = `
/* ── Chat of a code session (support_chat.go) ── */
.sup-chat{position:absolute;top:10px;right:10px;bottom:10px;width:330px;max-width:calc(100% - 20px);z-index:35;display:none;flex-direction:column;border-radius:14px;overflow:hidden;background:linear-gradient(180deg,var(--bg) 0%,var(--bg2) 100%);box-shadow:0 10px 30px var(--shadow)}
.sup-chat.open{display:flex}
.sup-chat-head{display:flex;align-items:center;gap:8px;padding:10px 12px;flex-shrink:0;font-size:12.5px;font-weight:600;color:var(--text)}
.sup-chat-head .sup-chat-title{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sup-chat .chat-messages{padding:8px 12px}
.sup-chat-note{font-size:10.5px;color:var(--muted2);text-align:center;padding:2px 8px 6px}
.sup-chat .chat-msg .bubble{white-space:pre-wrap}
.sup-chat-err{display:none;font-size:11px;color:var(--danger);padding:0 14px 4px}
.sup-chat-err.visible{display:block}
.sup-chat-badge{position:absolute;top:-6px;right:-6px;min-width:16px;height:16px;padding:0 4px;border-radius:8px;background:var(--accent-fill);color:var(--on-accent);font-size:9.5px;font-weight:700;line-height:16px;text-align:center;display:none;box-shadow:0 0 0 2px var(--bg2)}
.sup-chat-badge.visible{display:block}
`

const supChatJS = `
// ── Chat of a code session (support_chat.go) ────────────────────────────────
var SUP_CHAT_MAX_BYTES = 4096;
var SUP_CHAT_KEEP = 500;
var SUP_CHAT_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z"/></svg>';

// The UTF-8 length of s (the wire limit is in bytes).
function supUtf8Len(s) {
  var n = 0;
  s = String(s);
  for (var i = 0; i < s.length; i++) {
    var c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xD800 && c < 0xDC00 && i + 1 < s.length) { n += 4; i++; }
    else n += 3;
  }
  return n;
}
// A line as kept and shown: text, no control characters but line breaks,
// 4096 bytes at most (cut on a character), not blank.
function supChatClean(t) {
  var s = String(t === undefined || t === null ? '' : t).replace(/\r\n?/g, '\n').replace(/\t/g, ' ')
    .replace(/[\u0000-\u0009\u000b-\u001f\u007f-\u009f\u2028\u2029]/g, '').trim();
  if (supUtf8Len(s) <= SUP_CHAT_MAX_BYTES) return s;
  var out = '', n = 0;
  for (var i = 0; i < s.length; i++) {
    var c = s.charCodeAt(i), w = c < 0x80 ? 1 : c < 0x800 ? 2 : (c >= 0xD800 && c < 0xDC00 && i + 1 < s.length) ? 4 : 3;
    if (n + w > SUP_CHAT_MAX_BYTES) break;
    out += w === 4 ? s.slice(i, i + 2) : s[i];
    if (w === 4) i++;
    n += w;
  }
  return out;
}
function supChatOf(tab) {
  if (!tab.supChat) tab.supChat = { lines: [], unread: 0, open: false, err: '' };
  return tab.supChat;
}
// The host has a chat, and the session streams.
function supChatAvail(tab) { return !!(tab && devIsSupport(tab.device) && tab.ws && tab.gotInit && reachHas(tab, 'chat')); }
function supChatPush(c, from, text) {
  c.lines.push({ from: from, text: text });
  if (c.lines.length > SUP_CHAT_KEEP) c.lines.splice(0, c.lines.length - SUP_CHAT_KEEP);
}

// A line from the person (reach_viewer.go handleRemoteMessage).
function supChatOnMessage(tab, msg) {
  var text = supChatClean(msg && msg.text);
  if (!text) return;
  var c = supChatOf(tab);
  supChatPush(c, 'peer', text);
  var seen = c.open && !!reachUI(tab);
  if (!seen) {
    c.unread++;
    reachNotify(T('support.chat.newFrom'), 'ok');
  }
  supChatRefresh(tab);
}

function supChatSend(tab, text) {
  var c = supChatOf(tab);
  var raw = String(text === undefined || text === null ? '' : text);
  if (supUtf8Len(raw) > SUP_CHAT_MAX_BYTES) { c.err = T('support.chat.tooLong'); supChatRefresh(tab); return false; }
  var line = supChatClean(raw);
  if (!line) return false;
  if (!supChatAvail(tab) || !reachSend(tab, { type: 'chat', text: line })) { c.err = T('support.chat.failed'); supChatRefresh(tab); return false; }
  c.err = '';
  supChatPush(c, 'me', line);
  supChatRefresh(tab);
  return true;
}

// The toolbar's button (ui.go renderRemoteTab, a code session only):
// shown while the host's chat is there.
function supChatToolButton(toolbar) {
  var b = document.createElement('button');
  b.className = 'btn-sm reach-chat-btn'; b.id = 'sup-chat-btn'; b.style.display = 'none';
  b.title = T('support.chat.title');
  b.innerHTML = SUP_CHAT_ICON + '<span>' + TH('support.chat.button') + '</span>';
  var badge = document.createElement('span'); badge.className = 'sup-chat-badge'; badge.id = 'sup-chat-badge';
  b.appendChild(badge);
  b.addEventListener('click', function () {
    var tab = reachActiveTab();
    if (!tab || !devIsSupport(tab.device)) return;
    var c = supChatOf(tab);
    c.open = !c.open;
    supChatRefresh(tab);
    if (c.open) { var i = document.getElementById('sup-chat-input'); if (i) try { i.focus(); } catch (e) {} }
  });
  toolbar.appendChild(b);
}

// Its panel, over the picture (ui.go renderRemoteTab).
function supChatBuildPanel(vp) {
  var p = document.createElement('div'); p.className = 'sup-chat'; p.id = 'sup-chat';
  // Nothing typed or clicked here goes to the remote computer.
  ['mousedown', 'mouseup', 'click', 'wheel', 'keydown', 'keyup', 'contextmenu', 'dblclick'].forEach(function (t) {
    p.addEventListener(t, function (ev) { if (ev && ev.stopPropagation) ev.stopPropagation(); });
  });
  var head = document.createElement('div'); head.className = 'sup-chat-head';
  var title = document.createElement('span'); title.className = 'sup-chat-title'; title.textContent = T('support.chat.title');
  var close = document.createElement('button'); close.className = 'btn-sm'; close.id = 'sup-chat-close'; close.style.padding = '4px 8px';
  close.title = T('common.close'); close.textContent = '×';
  close.addEventListener('click', function () { var tab = reachActiveTab(); if (tab && tab.supChat) { tab.supChat.open = false; supChatRefresh(tab); } });
  head.appendChild(title); head.appendChild(close);
  var list = document.createElement('div'); list.className = 'chat-messages'; list.id = 'sup-chat-list';
  var note = document.createElement('div'); note.className = 'sup-chat-note'; note.textContent = T('support.chat.note');
  var err = document.createElement('div'); err.className = 'sup-chat-err'; err.id = 'sup-chat-err';
  var area = document.createElement('div'); area.className = 'chat-input-area';
  var row = document.createElement('div'); row.className = 'chat-input-row';
  var input = document.createElement('input'); input.type = 'text'; input.id = 'sup-chat-input';
  input.placeholder = T('chat.placeholder'); input.setAttribute('maxlength', '4096'); input.setAttribute('autocomplete', 'off');
  var send = document.createElement('button'); send.className = 'chat-send-btn'; send.id = 'sup-chat-send'; send.title = T('chat.send');
  send.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>';
  var go = function () {
    var tab = reachActiveTab();
    if (!tab || !devIsSupport(tab.device)) return;
    if (supChatSend(tab, input.value)) input.value = '';
  };
  send.addEventListener('click', go);
  input.addEventListener('keydown', function (e) { if (e && e.key === 'Enter') { if (e.preventDefault) e.preventDefault(); go(); } });
  row.appendChild(input); row.appendChild(send);
  area.appendChild(row);
  p.appendChild(head); p.appendChild(list); p.appendChild(note); p.appendChild(err); p.appendChild(area);
  vp.appendChild(p);
}

// Draws the button, the badge and the panel of tab when it is on screen
// (reach_viewer.go reachRefreshToolbar, and every line).
function supChatRefresh(tab) {
  if (!tab || !devIsSupport(tab.device) || !reachUI(tab)) return;
  var c = supChatOf(tab);
  var avail = supChatAvail(tab);
  if (!avail) c.open = false;
  if (c.open) c.unread = 0;
  var btn = document.getElementById('sup-chat-btn');
  if (btn) { btn.style.display = avail ? '' : 'none'; btn.classList.toggle('on', c.open); }
  var badge = document.getElementById('sup-chat-badge');
  if (badge) { badge.textContent = c.unread > 99 ? '99+' : String(c.unread); badge.classList.toggle('visible', c.unread > 0); }
  var p = document.getElementById('sup-chat');
  if (!p) return;
  p.classList.toggle('open', c.open);
  var err = document.getElementById('sup-chat-err');
  if (err) { err.textContent = c.err || ''; err.classList.toggle('visible', !!c.err); }
  var list = document.getElementById('sup-chat-list');
  if (!list) return;
  if ((list.dataset.n | 0) > c.lines.length || list.dataset.tab !== tab.id) { list.innerHTML = ''; list.dataset.n = '0'; list.dataset.tab = tab.id; }
  for (var i = list.dataset.n | 0; i < c.lines.length; i++) {
    var l = c.lines[i];
    var m = document.createElement('div'); m.className = 'chat-msg ' + (l.from === 'me' ? 'user' : 'op');
    var b = document.createElement('div'); b.className = 'bubble'; b.textContent = l.text;
    m.appendChild(b); list.appendChild(m);
  }
  list.dataset.n = String(c.lines.length);
  try { list.scrollTop = list.scrollHeight; } catch (e) {}
}
`
