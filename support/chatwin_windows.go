//go:build windows

package main

// The chat window: the transcript with the operator (bubbles, scrollable
// with the mouse wheel, pinned to the newest line) and a one-line input
// (a system EDIT control, for the caret, selection, clipboard and IME),
// Enter or the Send button to send. It opens only when the person asks
// (the Chat button of the banner or of the main window); a new operator
// line never brings it up by itself: the banner shows a badge and a
// preview, and an open chat window in the background flashes its taskbar
// button.

import (
	"errors"
	"sync"
	"syscall"
	"unsafe"
)

const (
	idChatSend = 200 + iota
	idChatOpen
	idBannerChat
)

const (
	chatWinW = 380
	chatWinH = 520
	// chatWheelStep: logical pixels per wheel notch.
	chatWheelStep = 48
)

type chatWin struct {
	s    *surface
	edit uintptr
	font uintptr // font last given to the edit control
	// inputR is the input area (before mirroring), set by the layout.
	inputR rect
	// scrollBack: physical pixels between the bottom of the transcript and
	// the bottom of the viewport (0 = pinned to the newest line).
	scrollBack int32
	lastH      int32
	maxBack    int32
}

// ── edit control subclass ────────────────────────────────────────────────────

var (
	editProcOnce sync.Once
	editProcCB   uintptr
	editMu       sync.Mutex
	editOrig     = map[uintptr]uintptr{}
	editEnter    = map[uintptr]func(){}
	editKeys     = map[uintptr]editKeyOpts{}
)

// editKeyOpts: a multi-line edit control keeps Enter (new line); Tab and
// Escape go to the window when handlers are set.
type editKeyOpts struct {
	multiline bool
	tab, esc  func()
}

func editProc(hwnd, m, wp, lp uintptr) uintptr {
	editMu.Lock()
	orig, enter, keys := editOrig[hwnd], editEnter[hwnd], editKeys[hwnd]
	editMu.Unlock()
	switch uint32(m) {
	case wmKeyDown:
		switch {
		case wp == vkTab && keys.tab != nil:
			keys.tab()
			return 0
		case wp == vkEscape && keys.esc != nil:
			keys.esc()
			return 0
		case wp == vkReturn && !keys.multiline:
			if enter != nil {
				enter()
			}
			return 0
		}
	case wmChar:
		if keys.multiline && (wp == '\r' || wp == '\n') {
			break // a new line
		}
		if wp == '\r' || wp == '\n' || wp == '\t' || wp == 0x1b {
			return 0 // no beep
		}
	case wmDestroy:
		editMu.Lock()
		delete(editOrig, hwnd)
		delete(editEnter, hwnd)
		delete(editKeys, hwnd)
		editMu.Unlock()
	}
	r, _, _ := pCallWindowProcW.Call(orig, hwnd, m, wp, lp)
	return r
}

func subclassEdit(hwnd uintptr, enter func()) {
	editProcOnce.Do(func() { editProcCB = syscall.NewCallback(editProc) })
	editMu.Lock()
	defer editMu.Unlock()
	editEnter[hwnd] = enter
	old, _, _ := pSetWindowLongPtrW.Call(hwnd, gwlpWndProc, editProcCB)
	editOrig[hwnd] = old
}

// subclassEditKeys subclasses an edit control with key options (the report
// dialog's multi-line text).
func subclassEditKeys(hwnd uintptr, opts editKeyOpts) {
	editProcOnce.Do(func() { editProcCB = syscall.NewCallback(editProc) })
	editMu.Lock()
	defer editMu.Unlock()
	editKeys[hwnd] = opts
	old, _, _ := pSetWindowLongPtrW.Call(hwnd, gwlpWndProc, editProcCB)
	editOrig[hwnd] = old
}

func windowText(hwnd uintptr) string {
	n, _, _ := pGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

// ── window ───────────────────────────────────────────────────────────────────

// openChat shows the chat window (the person asked for it: it may take the
// focus).
func (a *app) openChat() {
	if a.st.Phase != PhaseSession {
		return
	}
	if a.chat == nil && !a.createChat() {
		return
	}
	h := a.chat.s.hwnd
	if r, _, _ := pIsIconic.Call(h); r != 0 {
		pShowWindow.Call(h, swRestore)
	} else {
		pShowWindow.Call(h, swShow)
	}
	pSetForegroundWindow.Call(h)
	pSetFocus.Call(a.chat.edit)
	a.markChatSeen()
}

func (a *app) createChat() bool {
	w := &chatWin{}
	w.s = &surface{
		style:   wsOverlapped | wsCaption | wsSysMenu | wsMinimizeBox | wsClipChild,
		exStyle: wsExAppWindow,
		width:   chatWinW,
		height:  chatWinH,
		build:   func(b *builder) { a.buildChat(b, w) },
		action: func(id int) {
			if id == idChatSend {
				a.sendChat()
			}
		},
		closed: a.closeChat,
		timer:  func() {},
		wheel: func(n int32) {
			w.scrollBack = min(max(w.scrollBack+n*w.s.px(chatWheelStep), 0), w.maxBack)
			w.s.relayout()
		},
		activated: func(active bool) {
			if active {
				if w.edit != 0 {
					pSetFocus.Call(w.edit)
				}
				a.markChatSeen()
			}
		},
		ctlColor: func() (uint32, uint32) { return colText, colSurface },
		laidOut:  func() { a.placeChatEdit(w) },
	}
	a.look(w.s)
	a.chat = w
	wa := workArea(0)
	if !w.s.create(a.chatTitle(), wa.L+140, wa.T+140) {
		a.chat = nil
		return false
	}
	var wr rect
	pGetWindowRect.Call(w.s.hwnd, uintptrOf(&wr))
	m := w.s.px(24)
	pSetWindowPos.Call(w.s.hwnd, 0, uintptr(wa.R-wr.w()-m), uintptr(wa.B-wr.h()-m), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)

	style := uintptr(wsChild | wsVisible | wsTabStop | esAutoHScroll)
	ex := uintptr(0)
	if w.s.rtl {
		style |= esRight
		ex |= wsExRTLReading
	}
	edit, _, _ := pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(utf16("EDIT"))), uintptr(unsafe.Pointer(utf16(""))),
		style, 0, 0, 10, 10, w.s.hwnd, 0, moduleHandle(), 0)
	if edit == 0 {
		w.s.destroy()
		a.chat = nil
		return false
	}
	w.edit = edit
	pSendMessageW.Call(edit, emLimitText, chatMaxInputUnits, 0)
	pSendMessageW.Call(edit, emSetCueBanner, 1, uintptr(unsafe.Pointer(utf16(a.tr.T("chat.placeholder")))))
	subclassEdit(edit, a.sendChat)
	w.s.relayout()
	return true
}

func (a *app) chatTitle() string {
	return a.tr.T("chat.title", "name", displayName(a.st.Session.Operator))
}

// closeChat closes the window; the transcript stays with the session.
func (a *app) closeChat() {
	if a.chat == nil {
		return
	}
	w := a.chat
	a.chat = nil
	w.s.destroy()
}

func (a *app) sendChat() {
	w := a.chat
	if w == nil || w.edit == 0 {
		return
	}
	err := a.ctl.SendChat(windowText(w.edit))
	if err == nil || errors.Is(err, ErrChatEmpty) {
		pSetWindowTextW.Call(w.edit, uintptr(unsafe.Pointer(utf16(""))))
		w.scrollBack = 0
	}
	pSetFocus.Call(w.edit)
}

// chatActive: the chat window is open, not minimised, and the active window.
func (a *app) chatActive() bool {
	if a.chat == nil || a.chat.s.hwnd == 0 {
		return false
	}
	h := a.chat.s.hwnd
	if r, _, _ := pIsIconic.Call(h); r != 0 {
		return false
	}
	fg, _, _ := pGetForegroundWindow.Call()
	return fg == h
}

// markChatSeen: everything in the transcript has been seen.
func (a *app) markChatSeen() {
	if m := a.st.Session.Chat; len(m) > 0 {
		a.chatSeen = m[len(m)-1].ID
	}
	a.pulseUntil = a.pulseUntil.AddDate(-1, 0, 0)
	a.refreshSession()
}

// chatChanged runs on each new state of a session: new operator lines
// raise the attention signals (never the window itself).
func (a *app) chatChanged() {
	msgs := a.st.Session.Chat
	last, ok := lastOperatorLine(msgs)
	if !ok || last.ID <= a.chatLastOp {
		return
	}
	a.chatLastOp = last.ID
	if a.chatActive() {
		a.chatSeen = msgs[len(msgs)-1].ID
		return
	}
	a.pulseUntil = timeNow().Add(chatPulseFor)
	if a.chat != nil && a.chat.s.hwnd != 0 {
		fi := flashInfo{Hwnd: a.chat.s.hwnd, DwFlags: flashwTray, UCount: 3}
		fi.CbSize = uint32(sizeofFlash)
		pFlashWindowEx.Call(uintptrOf(&fi))
	}
}

// chatReset forgets the chat of the session that ended.
func (a *app) chatReset() {
	a.closeChat()
	a.chatSeen, a.chatLastOp = 0, 0
}

func (a *app) chatUnreadCount() int { return chatUnread(a.st.Session.Chat, a.chatSeen) }

// placeChatEdit puts the edit control in the input area and keeps its font
// in step with the window's DPI.
func (a *app) placeChatEdit(w *chatWin) {
	if w.edit == 0 {
		return
	}
	r := w.inputR
	if w.s.rtl {
		cw := w.s.px(w.s.width)
		r.L, r.R = cw-r.R, cw-r.L
	}
	f := w.s.fonts[fontBody]
	if f != w.font {
		pSendMessageW.Call(w.edit, wmSetFont, f, 0)
		w.font = f
	}
	pad := w.s.px(12)
	eh := w.s.px(22)
	pMoveWindow.Call(w.edit, uintptr(r.L+pad), uintptr(r.T+(r.h()-eh)/2), uintptr(r.w()-2*pad), uintptr(eh), 1)
}

func (a *app) buildChat(b *builder, w *chatWin) {
	t := a.tr.T
	s := w.s
	pad := b.px(16)
	cw, ch := s.px(chatWinW), s.px(chatWinH)
	b.x, b.y, b.w = pad, pad, cw-2*pad
	b.text(a.chatTitle(), fontBold, colText)
	b.space(2)
	b.text(t("chat.private"), fontSmall, colFaint)
	b.space(10)

	// Input row at the bottom: the edit control's area and Send.
	ih := b.px(40)
	iy := ch - pad - ih
	send := t("chat.send")
	sw := max(b.measureW(send, fontBold)+2*b.px(18), b.px(80))
	w.inputR = rect{b.x, iy, b.x + b.w - sw - b.px(8), iy + ih}
	b.out = append(b.out, widget{kind: wFill, r: w.inputR, bg: colSurface, radius: b.px(8)})
	b.button(idChatSend, send, rect{b.x + b.w - sw, iy, b.x + b.w, iy + ih}, btnPrimary, false)

	// Transcript viewport.
	vp := rect{0, b.y, cw, iy - b.px(10)}
	msgs := a.st.Session.Chat
	if len(msgs) == 0 {
		hint := t("chat.empty", "name", displayName(a.st.Session.Operator))
		h := b.measure(hint, fontSmall, b.w, dtWordBreak|dtCenter)
		top := vp.T + (vp.h()-h)/2
		b.out = append(b.out, widget{kind: wText, r: rect{b.x, top, b.x + b.w, top + h}, text: hint, font: fontSmall, fg: colFaint, flags: dtWordBreak | dtCenter})
		w.scrollBack, w.lastH, w.maxBack = 0, 0, 0
		b.y = ch - pad
		b.pad = pad
		return
	}
	start := len(b.out)
	y := int32(0)
	bubbleMax := b.w * 78 / 100
	bp, bv := b.px(12), b.px(8)
	prev := ChatFrom(-1)
	op := displayName(a.st.Session.Operator)
	for _, m := range msgs {
		mine := m.From == ChatFromMe
		if m.From != prev {
			if prev != -1 {
				y += b.px(10)
			}
			who := op
			if mine {
				who = t("chat.you")
			}
			label := who + "  " + m.At.Format("15:04")
			lh := b.measure(label, fontSmall, b.w, dtSingleLine)
			flags := uintptr(dtSingleLine | dtEndEllip)
			if mine {
				flags |= dtRight
			}
			b.out = append(b.out, widget{kind: wText, r: rect{b.x, y, b.x + b.w, y + lh}, text: label, font: fontSmall, fg: colFaint, flags: flags})
			y += lh + b.px(3)
			prev = m.From
		} else {
			y += b.px(4)
		}
		text := chatDisplayText(m.Text)
		flags := uintptr(dtWordBreak | dtEditCtrl)
		tw := min(b.measureW(text, fontBody), bubbleMax-2*bp)
		if tw <= 0 {
			tw = bubbleMax - 2*bp
		}
		th := b.measure(text, fontBody, tw, flags)
		bw := tw + 2*bp
		bx := b.x
		bg, fg := uint32(colSurfaceHi), uint32(colText)
		if mine {
			bx = b.x + b.w - bw
			bg = colButton
			if m.Failed {
				bg = colDisabled
			}
		}
		b.out = append(b.out, widget{kind: wFill, r: rect{bx, y, bx + bw, y + th + 2*bv}, bg: bg, radius: b.px(10)})
		b.out = append(b.out, widget{kind: wText, r: rect{bx + bp, y + bv, bx + bp + tw, y + bv + th}, text: text, font: fontBody, fg: fg, flags: flags})
		y += th + 2*bv
		if m.Failed {
			nl := t("chat.notSent")
			lh := b.measure(nl, fontSmall, b.w, dtSingleLine)
			y += b.px(2)
			b.out = append(b.out, widget{kind: wText, r: rect{b.x, y, b.x + b.w, y + lh}, text: nl, font: fontSmall, fg: colAccent, flags: dtSingleLine | dtRight})
			y += lh
		}
	}
	// Scroll: keep the view where the person left it when lines arrive.
	if w.scrollBack > 0 && y > w.lastH && w.lastH > 0 {
		w.scrollBack += y - w.lastH
	}
	w.lastH = y
	w.maxBack = max(0, y-vp.h())
	w.scrollBack = min(max(w.scrollBack, 0), w.maxBack)
	off := vp.B - y + w.scrollBack // y of the transcript's top
	if y < vp.h() {
		off = vp.T // short transcript: from the top
	}
	for i := start; i < len(b.out); i++ {
		o := &b.out[i]
		o.r.T += off
		o.r.B += off
		o.clip = vp
	}
	b.y = ch - pad
	b.pad = pad
}
