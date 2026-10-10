//go:build windows

package main

// The screens of the thin client and the View the controller drives:
//   - the main window: anti-scam notice, then ID + code with the status
//     line, the running session, the end screen;
//   - the acceptance window (topmost) when an operator proved the code;
//   - the session banner (topmost bar, top centre of the primary screen)
//     with the Chat and Stop buttons; Ctrl+Alt+F12 stops too;
//   - the chat window (chatwin_windows.go), opened from the banner or the
//     main window.
// The language picker sits on the first screens (before any session).
// All of it runs on the UI thread; the controller's calls are posted.

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/oblireach/supportlink"
)

const (
	idContinue = iota + 1
	idQuit
	idCopyID
	idCopyCode
	idStop
	idAgain
	idAccept
	idRefuse
	idTglControl
	idTglClipboard
	idTglFiles
	idBannerStop
	idLang
	idLangBack
	idLangBase = 100 // + index in languages
)

const (
	hotkeyStop = 1
	// acceptArmDelay keeps Accept disabled for a moment after the window
	// appears, so a click or key press meant for something else cannot
	// accept a session.
	acceptArmDelay = 1500 * time.Millisecond
	// chatPulseFor: how long the banner's Chat button pulses after a new
	// operator line.
	chatPulseFor = 6 * time.Second
	// chatPreviewRunes: length of the banner's preview of a new line.
	chatPreviewRunes = 70
)

var timeNow = time.Now

type decision struct {
	perms Permissions
	ok    bool
}

type app struct {
	tr  Tr
	cfg Config
	ctl *Controller

	main     *surface
	banner   *surface
	approval *approvalWin
	chat     *chatWin
	report   *reportWin

	face     string // font family of the language ("" = Segoe UI)
	langOpen bool   // the language picker is shown

	chatSeen   int // last chat line ID the person has seen
	chatLastOp int // last operator line ID signalled
	pulseUntil time.Time

	st          State
	noticeOK    bool
	copied      int
	copiedUntil time.Time
	hotkey      bool
}

type approvalWin struct {
	s      *surface
	req    Approval
	perms  Permissions
	opened time.Time
	res    chan decision
	done   bool
}

func newApp(tr Tr, cfg Config) *app {
	return &app{tr: tr, cfg: cfg, face: uiFontFace(tr.Lang())}
}

// look gives a new surface the font family and direction of the language.
func (a *app) look(s *surface) {
	s.face, s.rtl = a.face, a.tr.RTL()
}

// setLanguage switches every open window to another language.
func (a *app) setLanguage(lang string) {
	a.tr = newTr(lang)
	a.face = uiFontFace(a.tr.Lang())
	for _, s := range a.surfaces() {
		s.setLook(a.face, a.tr.RTL())
	}
	if a.main != nil && a.main.hwnd != 0 {
		pSetWindowTextW.Call(a.main.hwnd, uintptr(unsafe.Pointer(utf16(a.tr.T("app.title")))))
	}
	if a.approval != nil && a.approval.s.hwnd != 0 {
		pSetWindowTextW.Call(a.approval.s.hwnd, uintptr(unsafe.Pointer(utf16(a.tr.T("approve.title")))))
	}
}

func (a *app) surfaces() []*surface {
	var out []*surface
	for _, s := range []*surface{a.main, a.banner} {
		if s != nil {
			out = append(out, s)
		}
	}
	if a.approval != nil {
		out = append(out, a.approval.s)
	}
	if a.chat != nil {
		out = append(out, a.chat.s)
	}
	if a.report != nil {
		out = append(out, a.report.s)
	}
	return out
}

// refreshSession lays out the windows that show the session again.
func (a *app) refreshSession() {
	if a.main != nil && a.main.hwnd != 0 {
		a.main.relayout()
	}
	if a.banner != nil && a.banner.hwnd != 0 {
		a.banner.relayout()
	}
}

// ── View (called from network goroutines) ────────────────────────────────────

func (a *app) Show(st State) { post(func() { a.setState(st) }) }

func (a *app) Ask(ctx context.Context, req Approval) (Permissions, bool) {
	res := make(chan decision, 1)
	post(func() { a.openApproval(req, res) })
	select {
	case d := <-res:
		return d.perms, d.ok
	case <-ctx.Done():
		post(func() { a.closeApproval(res) })
		return Permissions{}, false
	}
}

// ── main window ──────────────────────────────────────────────────────────────

func (a *app) createMain() bool {
	a.main = &surface{
		style:      wsOverlapped | wsCaption | wsSysMenu | wsMinimizeBox | wsClipChild,
		exStyle:    wsExAppWindow,
		width:      440,
		autoHeight: true,
		build:      a.buildMain,
		action:     a.mainAction,
		key: func(vk uintptr) bool {
			if vk == vkEscape && !a.noticeOK {
				a.quit()
				return true
			}
			return false
		},
		closed:    a.quit,
		destroyed: func() { pPostQuitMessage.Call(0) },
		hotkey: func(id uintptr) {
			if id == hotkeyStop {
				a.ctl.StopSession()
			}
		},
		timer: a.tick,
	}
	a.look(a.main)
	wa := workArea(0)
	if !a.main.create(a.tr.T("app.title"), wa.L+100, wa.T+100) {
		return false
	}
	a.main.placeCenter()
	pShowWindow.Call(a.main.hwnd, swShowNormal)
	pSetForegroundWindow.Call(a.main.hwnd)
	setDispatchWindow(a.main.hwnd)
	return true
}

func (a *app) quit() {
	if a.approval != nil {
		a.closeApproval(a.approval.res)
	}
	if a.ctl != nil {
		a.ctl.StopSession()
	}
	a.closeReport(a.report)
	a.chatReset()
	a.setBanner(false)
	a.setHotkey(false)
	if a.main != nil {
		a.main.destroy()
	}
}

func (a *app) tick() {
	switch {
	case a.st.Phase == PhaseUnreachable, a.st.Phase == PhaseSession, a.copied != 0,
		a.st.Phase == PhaseEnded && a.st.Report.Available && !a.st.Report.Sent: // the report offer expires
		if a.copied != 0 && time.Now().After(a.copiedUntil) {
			a.copied = 0
		}
		a.main.relayout()
	}
	if a.banner != nil && a.banner.hwnd != 0 {
		a.banner.relayout()
	}
}

func (a *app) setState(st State) {
	prev := a.st.Phase
	a.st = st
	if st.Phase == PhaseSession && prev != PhaseSession {
		a.setBanner(true)
		a.setHotkey(true)
	}
	if st.Phase != PhaseSession && prev == PhaseSession {
		a.setBanner(false)
		a.setHotkey(false)
		a.chatReset()
	}
	if st.Phase == PhaseSession {
		a.langOpen = false
		a.chatChanged()
		if a.chat != nil && a.chat.s.hwnd != 0 {
			a.chat.s.relayout()
		}
	}
	if st.Phase == PhaseEnded && prev != PhaseEnded && a.main != nil {
		pShowWindow.Call(a.main.hwnd, swRestore)
		pSetForegroundWindow.Call(a.main.hwnd)
	}
	if a.main != nil {
		a.main.relayout()
	}
	if a.banner != nil && a.banner.hwnd != 0 {
		a.banner.relayout()
	}
	a.reportStateChanged()
}

func (a *app) mainAction(id int) {
	switch id {
	case idContinue:
		a.noticeOK = true
		a.main.focus = 0
	case idQuit:
		a.quit()
		return
	case idCopyID, idCopyCode:
		v := a.st.ID
		if id == idCopyCode {
			v = a.st.Code
		}
		if v != "" && copyToClipboard(a.main.hwnd, v) {
			a.copied, a.copiedUntil = id, time.Now().Add(1500*time.Millisecond)
		}
	case idStop:
		a.ctl.StopSession()
	case idAgain:
		a.ctl.Acknowledge()
	case idChatOpen:
		a.openChat()
	case idReportOpen:
		a.openReport()
	case idLang:
		a.langOpen = true
		a.main.focus = 0
	case idLangBack:
		a.langOpen = false
		a.main.focus = 0
	default:
		if i := id - idLangBase; i >= 0 && i < len(languages) {
			a.langOpen = false
			a.main.focus = 0
			a.setLanguage(languages[i])
		}
	}
	a.main.relayout()
}

// langChoosable: the language can change (no request or session running).
func (a *app) langChoosable() bool {
	switch a.st.Phase {
	case PhaseConnecting, PhaseUnreachable, PhaseWaiting:
		return true
	}
	return !a.noticeOK
}

func (a *app) buildMain(b *builder) {
	t := a.tr.T
	// Title, with the language picker's small button on the same line.
	top := b.y
	titleW := b.w
	if a.langChoosable() && !a.langOpen {
		name := languageNames[a.tr.Lang()]
		face := uiFontFace(a.tr.Lang())
		lw := b.measureWFace(name, face, fontSmall) + 2*b.px(12)
		lh := b.px(26)
		th := b.measure("Ag", fontTitle, b.w, dtSingleLine)
		ly := top + (th-lh)/2
		b.out = append(b.out, widget{kind: wButton, id: idLang, r: rect{b.x + b.w - lw, ly, b.x + b.w, ly + lh},
			text: name, face: face, style: btnSmall, radius: b.px(8)})
		titleW = b.w - lw - b.px(8)
	}
	th := b.measure(t("app.title"), fontTitle, titleW, dtWordBreak)
	b.out = append(b.out, widget{kind: wText, r: rect{b.x, top, b.x + titleW, top + th}, text: t("app.title"), font: fontTitle, fg: colText, flags: dtWordBreak})
	b.y = top + th
	b.space(2)
	b.text(t("main.server", "name", a.cfg.ServerName), fontSmall, colMuted)
	b.space(20)
	if a.langOpen && a.langChoosable() {
		a.buildLangPicker(b)
		return
	}
	if !a.noticeOK {
		a.buildNotice(b)
		return
	}
	switch a.st.Phase {
	case PhaseSession:
		a.buildSession(b)
	case PhaseEnded:
		a.buildEnded(b)
	default:
		a.buildWaiting(b)
	}
}

// buildLangPicker: every language in its own script, two columns.
func (a *app) buildLangPicker(b *builder) {
	t := a.tr.T
	b.text(t("lang.title"), fontBold, colText)
	b.space(2)
	b.text(t("lang.hint"), fontSmall, colMuted)
	b.space(12)
	gap := b.px(8)
	cw := (b.w - gap) / 2
	h := b.px(34)
	rows := (len(languages) + 1) / 2
	for i, l := range languages {
		col, row := int32(i/rows), int32(i%rows)
		x := b.x + col*(cw+gap)
		y := b.y + row*(h+gap/2)
		style := btnSecondary
		if l == a.tr.Lang() {
			style = btnPrimary
		}
		b.out = append(b.out, widget{kind: wButton, id: idLangBase + i, r: rect{x, y, x + cw, y + h},
			text: languageNames[l], face: uiFontFace(l), style: style, radius: b.px(8)})
	}
	b.y += int32(rows)*(h+gap/2) - gap/2
	b.space(18)
	b.buttonRow(rowButton{id: idLangBack, label: t("common.back"), style: btnSecondary})
}

func (a *app) buildNotice(b *builder) {
	t := a.tr.T
	b.beginCard()
	b.text(t("notice.title"), fontBold, colAccent)
	b.space(8)
	b.text(t("notice.body"), fontBody, colText)
	b.space(10)
	b.text(t("notice.scam"), fontBody, colMuted)
	b.endCard(colSurface)
	b.space(24)
	b.buttonRow(
		rowButton{id: idQuit, label: t("common.quit"), style: btnSecondary},
		rowButton{id: idContinue, label: t("notice.continue"), style: btnPrimary},
	)
}

// digitsCard shows a label, nine digits in groups and a copy button.
func (a *app) digitsCard(b *builder, label, digits string, copyID int) {
	b.beginCard()
	b.text(label, fontSmall, colMuted)
	b.space(2)
	shown := "––– ––– –––"
	if digits != "" {
		shown = supportlink.FormatDigits(digits)
	}
	h := b.measure(shown, fontBig, b.w, dtSingleLine)
	bw, bh := b.px(86), b.px(30)
	b.textAt(shown, rect{b.x, b.y, b.x + b.w - bw - b.px(8), b.y + h}, fontBig, colText, dtLeft|dtVCenter)
	b.ltr()
	lbl := a.tr.T("main.copy")
	if a.copied == copyID {
		lbl = a.tr.T("main.copied")
	}
	by := b.y + (h-bh)/2
	b.button(copyID, lbl, rect{b.x + b.w - bw, by, b.x + b.w, by + bh}, btnSmall, digits == "")
	b.y += h
	b.endCard(colSurface)
}

func (a *app) buildWaiting(b *builder) {
	t := a.tr.T
	a.digitsCard(b, t("main.yourId"), a.st.ID, idCopyID)
	b.space(12)
	a.digitsCard(b, t("main.code"), a.st.Code, idCopyCode)
	b.space(18)
	text, color := a.statusText()
	b.statusLine(text, color)
	b.space(10)
	b.text(t("main.hint"), fontSmall, colMuted)
	b.space(22)
	b.buttonRow(rowButton{id: idQuit, label: t("common.quit"), style: btnSecondary})
}

func (a *app) statusText() (string, uint32) {
	t := a.tr.T
	st := a.st
	switch st.Phase {
	case PhaseConnecting:
		return t("status.connecting"), colWarn
	case PhaseUnreachable:
		secs := fmt.Sprint(max(0, int(time.Until(st.RetryAt).Round(time.Second).Seconds())))
		switch st.ErrKind {
		case ErrKindUpgrade:
			return t("status.upgrade"), colAccent
		case ErrKindRefused:
			return t("status.refusedByServer", "seconds", secs), colAccent
		}
		return t("status.unreachable", "seconds", secs), colAccent
	case PhaseApproval:
		return t("approve.title"), colWarn
	case PhaseStarting:
		return t("status.starting", "name", displayName(st.Session.Operator)), colWarn
	}
	if st.Notice != NoticeNone {
		return t("status." + string(st.Notice)), colWarn
	}
	return t("status.waiting"), colOK
}

func (a *app) sessionLines() (who, mode, detail string) {
	t := a.tr.T
	s := a.st.Session
	who = t("session.who", "name", displayName(s.Operator))
	mode = t("session.view")
	if s.Perms.Control {
		mode = t("session.control")
	}
	path := t("session.direct")
	if s.Relayed {
		path = t("session.relay")
	}
	return who, mode, path + " · " + formatDuration(time.Since(s.Started))
}

// fileNotice is the line about the last file the operator sent, "" when
// there is none or it is older than FileNoticeFor.
func (a *app) fileNotice() string {
	s := a.st.Session
	if s.LastFile == "" || time.Since(s.LastFileAt) > FileNoticeFor {
		return ""
	}
	return a.tr.T("session.fileReceived", "name", s.LastFile, "folder", filepath.Base(s.LastFileDir))
}

func (a *app) buildSession(b *builder) {
	t := a.tr.T
	who, mode, detail := a.sessionLines()
	b.beginCard()
	b.statusLine(t("session.title"), colAccent)
	b.space(10)
	b.text(who, fontBold, colText)
	b.space(2)
	b.text(mode, fontBody, colMuted)
	b.space(6)
	b.text(detail, fontSmall, colMuted)
	if n := a.fileNotice(); n != "" {
		b.space(6)
		b.text(n, fontSmall, colAccent)
	}
	b.endCard(colSurface)
	b.space(18)
	b.buttonRow(
		rowButton{id: idChatOpen, label: t("chat.open"), style: btnSecondary, badge: a.chatUnreadCount()},
		rowButton{id: idStop, label: t("session.stopLong"), style: btnPrimary},
	)
	b.space(12)
	b.text(t("session.hotkey"), fontSmall, colMuted)
	a.reportRow(b)
}

// reportRow offers the report of the operator (session and end screens),
// or says it was sent.
func (a *app) reportRow(b *builder) {
	r := a.st.Report
	switch {
	case r.Sent:
		b.space(14)
		b.statusLine(a.tr.T("report.done"), colOK)
	case r.Open(timeNow()):
		b.space(14)
		b.buttonRow(rowButton{id: idReportOpen, label: a.tr.T("report.open"), style: btnSecondary})
	}
}

func (a *app) buildEnded(b *builder) {
	t := a.tr.T
	e := a.st.Ended
	var why string
	switch e.Reason {
	case EndLocal:
		why = t("ended.byYou")
	case EndPeer:
		why = t("ended.byHelper", "name", displayName(e.Operator))
	case EndLost:
		why = t("ended.lost")
	default:
		why = t("ended.error")
	}
	b.beginCard()
	b.text(t("ended.title"), fontBold, colText)
	b.space(8)
	b.text(why, fontBody, colMuted)
	b.space(6)
	b.text(t("ended.duration", "duration", formatDuration(e.Duration)), fontSmall, colMuted)
	b.endCard(colSurface)
	a.reportRow(b)
	b.space(24)
	b.buttonRow(
		rowButton{id: idQuit, label: t("common.quit"), style: btnSecondary},
		rowButton{id: idAgain, label: t("ended.again"), style: btnPrimary},
	)
}

// ── acceptance window ────────────────────────────────────────────────────────

func (a *app) openApproval(req Approval, res chan decision) {
	if a.approval != nil {
		a.finishApproval(a.approval, decision{})
	}
	w := &approvalWin{req: req, perms: DefaultPermissions(), opened: time.Now(), res: res}
	w.s = &surface{
		style:      wsOverlapped | wsCaption | wsSysMenu,
		exStyle:    wsExTopmost | wsExAppWindow,
		width:      460,
		autoHeight: true,
		build:      func(b *builder) { a.buildApproval(b, w) },
		action:     func(id int) { a.approvalAction(w, id) },
		key: func(vk uintptr) bool {
			if vk == vkEscape {
				a.finishApproval(w, decision{})
				return true
			}
			return false
		},
		closed: func() { a.finishApproval(w, decision{}) },
		timer: func() {
			if time.Now().After(w.req.Deadline) {
				a.finishApproval(w, decision{})
				return
			}
			w.s.relayout()
		},
	}
	a.approval = w
	a.look(w.s)
	wa := workArea(0)
	if !w.s.create(a.tr.T("approve.title"), wa.L+120, wa.T+120) {
		a.finishApproval(w, decision{})
		return
	}
	w.s.placeCenter()
	pShowWindow.Call(w.s.hwnd, swShow)
	pSetWindowPos.Call(w.s.hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
	pSetForegroundWindow.Call(w.s.hwnd)
	fi := flashInfo{Hwnd: w.s.hwnd, DwFlags: flashwAll | flashwTimerNF, UCount: 3}
	fi.CbSize = uint32(sizeofFlash)
	pFlashWindowEx.Call(uintptrOf(&fi))
	time.AfterFunc(acceptArmDelay+50*time.Millisecond, func() {
		post(func() {
			if !w.done && w.s.hwnd != 0 {
				w.s.relayout()
			}
		})
	})
}

// closeApproval closes the window of an abandoned request (the operator
// left or the deadline passed).
func (a *app) closeApproval(res chan decision) {
	if a.approval != nil && a.approval.res == res {
		a.finishApproval(a.approval, decision{})
	}
}

func (a *app) finishApproval(w *approvalWin, d decision) {
	if w.done {
		return
	}
	w.done = true
	select {
	case w.res <- d:
	default:
	}
	if a.approval == w {
		a.approval = nil
	}
	w.s.destroy()
}

func (a *app) approvalAction(w *approvalWin, id int) {
	switch id {
	case idAccept:
		if time.Since(w.opened) < acceptArmDelay {
			return
		}
		a.finishApproval(w, decision{perms: w.perms, ok: true})
		return
	case idRefuse:
		a.finishApproval(w, decision{})
		return
	case idTglControl:
		w.perms.Control = !w.perms.Control
	case idTglClipboard:
		w.perms.Clipboard = !w.perms.Clipboard
	case idTglFiles:
		w.perms.Files = !w.perms.Files
	}
	w.s.relayout()
}

func (a *app) buildApproval(b *builder, w *approvalWin) {
	t := a.tr.T
	r := w.req
	b.text(t("approve.title"), fontSmall, colMuted)
	b.space(6)
	b.text(displayName(r.OperatorName), fontTitle, colText)
	b.text(t("approve.wants"), fontBody, colMuted)
	b.space(2)
	b.text(t("approve.selfDeclared"), fontSmall, colFaint)
	b.space(12)
	if r.Account != "" {
		b.statusLine(t("approve.account", "account", r.Account), colOK)
	} else {
		b.statusLine(t("approve.noAccount"), colWarn)
	}
	b.space(14)
	b.beginCard()
	b.text(t("approve.sas"), fontSmall, colMuted)
	b.space(2)
	b.text(r.SAS, fontBig, colAccent)
	b.ltr()
	b.space(4)
	b.text(t("approve.sasHint"), fontSmall, colMuted)
	b.endCard(colSurface)
	b.space(12)
	b.toggle(idTglControl, t("approve.allowControl"), w.perms.Control)
	b.toggle(idTglClipboard, t("approve.allowClipboard"), w.perms.Clipboard)
	b.toggle(idTglFiles, t("approve.allowFiles"), w.perms.Files)
	b.space(12)
	secs := max(0, int(time.Until(r.Deadline).Round(time.Second).Seconds()))
	b.text(t("approve.countdown", "seconds", fmt.Sprint(secs)), fontSmall, colMuted)
	b.space(14)
	b.buttonRow(
		rowButton{id: idRefuse, label: t("approve.refuse"), style: btnSecondary},
		rowButton{id: idAccept, label: t("approve.accept"), style: btnPrimary, disabled: time.Since(w.opened) < acceptArmDelay},
	)
}

// ── session banner ───────────────────────────────────────────────────────────

func (a *app) setBanner(on bool) {
	if !on {
		if a.banner != nil {
			a.banner.destroy()
			a.banner = nil
		}
		return
	}
	if a.banner != nil {
		return
	}
	a.banner = &surface{
		style:        wsPopup,
		exStyle:      wsExTopmost | wsExToolWindow | wsExNoActivate,
		width:        bannerMinW,
		autoHeight:   true,
		dragAnywhere: true,
		noActivate:   true,
		build:        a.buildBanner,
		action: func(id int) {
			switch id {
			case idBannerStop:
				a.ctl.StopSession()
			case idBannerChat:
				a.openChat()
			case idBannerReport:
				a.openReport()
			}
		},
		closed: func() {},
		timer:  func() {},
	}
	a.look(a.banner)
	wa := workArea(0)
	if !a.banner.create(a.tr.T("app.title")+" - "+a.tr.T("session.title"), wa.L, wa.T) {
		a.banner = nil
		return
	}
	var wr rect
	pGetWindowRect.Call(a.banner.hwnd, uintptrOf(&wr))
	x := wa.L + (wa.w()-wr.w())/2
	y := wa.T + a.banner.px(8)
	pSetWindowPos.Call(a.banner.hwnd, hwndTopmost, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoActivate|swpShowWindow)
	pShowWindow.Call(a.banner.hwnd, swShowNA)
}

func (a *app) buildBanner(b *builder) {
	_, mode, detail := a.sessionLines()
	text := displayName(a.st.Session.Operator) + " · " + mode + " · " + detail
	pad := b.px(10)
	h := b.px(32)
	stop := a.tr.T("session.stop")
	bw := b.measureW(stop, fontBold) + 2*b.px(18)
	chat := a.tr.T("chat.open")
	cw := b.measureW(chat, fontBold) + 2*b.px(16)
	gap := b.px(8)
	// Report: a quiet button, left of Chat, while it is offered.
	report := a.tr.T("report.short")
	rw := int32(0)
	if a.st.Report.Open(timeNow()) {
		rw = b.measureW(report, fontSmall) + 2*b.px(12)
	}
	// The width follows the text (the elapsed time keeps it stable enough),
	// between bannerMinW and bannerMaxW logical pixels.
	need := pad + b.px(20) + b.measureW(text, fontBody) + b.px(16) + cw + gap + bw + pad
	if rw > 0 {
		need += rw + gap
	}
	w := min(max(need, b.px(bannerMinW)), b.px(bannerMaxW))
	a.banner.width = (w*96 + b.s.dpi/2) / b.s.dpi
	b.x, b.y, b.w = pad, pad, w-2*pad
	b.pad = pad
	b.dot(b.x+b.px(4), b.y, h, colAccent)
	b.button(idBannerStop, stop, rect{b.x + b.w - bw, b.y, b.x + b.w, b.y + h}, btnPrimary, false)
	// Chat: a badge with the unread count; after a new line it pulses
	// gently (one colour change per second) for chatPulseFor.
	unread := a.chatUnreadCount()
	style := btnSecondary
	if unread > 0 && timeNow().Before(a.pulseUntil) && timeNow().Unix()%2 == 0 {
		style = btnPrimary
	}
	cx := b.x + b.w - bw - gap - cw
	b.button(idBannerChat, chat, rect{cx, b.y, cx + cw, b.y + h}, style, false)
	b.out[len(b.out)-1].badge = unread
	left := cx
	if rw > 0 {
		left = cx - gap - rw
		b.button(idBannerReport, report, rect{left, b.y, left + rw, b.y + h}, btnSmall, false)
	}
	tx := b.x + b.px(20)
	b.textAt(text, rect{tx, b.y, left - b.px(12), b.y + h}, fontBody, colText, dtLeft|dtVCenter)
	b.y += h
	// An unread operator line: its preview, under the session line.
	if last, ok := lastOperatorLine(a.st.Session.Chat); ok && unread > 0 {
		lh := b.px(22)
		line := a.tr.T("chat.newMessage", "name", displayName(a.st.Session.Operator)) + ": " + chatPreview(last.Text, chatPreviewRunes)
		b.textAt(line, rect{tx, b.y, b.x + b.w, b.y + lh}, fontSmall, colText, dtLeft|dtVCenter)
		b.y += lh
	}
	// A file the operator just sent: one more line, under the session line.
	if n := a.fileNotice(); n != "" {
		lh := b.px(22)
		b.textAt(n, rect{tx, b.y, b.x + b.w, b.y + lh}, fontSmall, colAccent, dtLeft|dtVCenter)
		b.y += lh
	}
}

const (
	bannerMinW = 360
	bannerMaxW = 760
)

// ── hotkey ───────────────────────────────────────────────────────────────────

func (a *app) setHotkey(on bool) {
	if a.main == nil || a.main.hwnd == 0 || on == a.hotkey {
		return
	}
	if on {
		r, _, _ := pRegisterHotKey.Call(a.main.hwnd, hotkeyStop, modControl|modAlt|modNoRepeat, vkF12)
		a.hotkey = r != 0
		return
	}
	pUnregisterHotKey.Call(a.main.hwnd, hotkeyStop)
	a.hotkey = false
}
