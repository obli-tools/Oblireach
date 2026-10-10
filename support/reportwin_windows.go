//go:build windows

package main

// The report dialog (report.go for the state machine): a topmost window
// opened from the session banner, the session screen or the end screen.
// The person picks a reason, may write a few lines (a multi-line system EDIT
// control, for the caret, selection, clipboard and IME) and, while the
// session runs, keeps "End the session now" (on by default). After sending:
// a confirmation and advice (bank, passwords, police). Nothing is written
// on disk; the text lives in the window until it closes.

import (
	"time"
	"unsafe"

	"github.com/oblireach/supportlink/wire"
)

const (
	idReportOpen = 300 + iota
	idBannerReport
	idRepSend
	idRepCancel
	idRepClose
	idRepEnd
	idRepReasonBase = 320 // + index in reportReasons
)

// Styles of the multi-line edit control.
const (
	esMultiline   = 0x0004
	esAutoVScroll = 0x0040
	esWantReturn  = 0x1000
)

const (
	reportWinW  = 460
	reportEditH = 104 // logical pixels
)

type reportWin struct {
	s    *surface
	dlg  *ReportDialog
	edit uintptr
	font uintptr // font last given to the edit control
	// editR is the text area (before mirroring), empty when hidden.
	editR rect
	// who: the helper being reported, as shown in the session.
	who string
}

// openReport shows the report dialog (or brings it back).
func (a *app) openReport() {
	if w := a.report; w != nil && w.s.hwnd != 0 {
		pShowWindow.Call(w.s.hwnd, swShow)
		pSetForegroundWindow.Call(w.s.hwnd)
		return
	}
	if !a.st.Report.Open(time.Now()) {
		return
	}
	who := displayName(a.st.Session.Operator)
	if acc := a.st.Session.Account; acc != "" {
		who += " (" + acc + ")"
	}
	w := &reportWin{dlg: NewReportDialog(a.st.Phase == PhaseSession), who: who}
	w.s = &surface{
		style:      wsOverlapped | wsCaption | wsSysMenu | wsClipChild,
		exStyle:    wsExTopmost | wsExAppWindow,
		width:      reportWinW,
		autoHeight: true,
		build:      func(b *builder) { a.buildReport(b, w) },
		action:     func(id int) { a.reportAction(w, id) },
		key: func(vk uintptr) bool {
			if vk == vkEscape {
				a.closeReport(w)
				return true
			}
			return false
		},
		closed:   func() { a.closeReport(w) },
		ctlColor: func() (uint32, uint32) { return colText, colSurface },
		laidOut:  func() { a.placeReportEdit(w) },
	}
	a.report = w
	a.look(w.s)
	wa := workArea(0)
	if !w.s.create(a.tr.T("report.title"), wa.L+160, wa.T+160) {
		a.report = nil
		return
	}
	// No scroll bar (the system one would break the borderless look): the
	// text scrolls with the caret.
	style := uintptr(wsChild | wsVisible | wsTabStop | esMultiline | esAutoVScroll | esWantReturn)
	ex := uintptr(0)
	if w.s.rtl {
		style |= esRight
		ex |= wsExRTLReading
	}
	edit, _, _ := pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(utf16("EDIT"))), uintptr(unsafe.Pointer(utf16(""))),
		style, 0, 0, 10, 10, w.s.hwnd, 0, moduleHandle(), 0)
	if edit != 0 {
		w.edit = edit
		pSendMessageW.Call(edit, emLimitText, wire.ReportMaxText, 0)
		subclassEditKeys(edit, editKeyOpts{
			multiline: true,
			// Tab leaves the text for the buttons of the window.
			tab: func() {
				pSetFocus.Call(w.s.hwnd)
				if ids := w.s.interactive(); len(ids) > 0 {
					w.s.focus = ids[0]
				}
				pInvalidateRect.Call(w.s.hwnd, 0, 0)
			},
			esc: func() { a.closeReport(w) },
		})
	}
	w.s.relayout()
	w.s.placeCenter()
	pShowWindow.Call(w.s.hwnd, swShow)
	pSetWindowPos.Call(w.s.hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
	pSetForegroundWindow.Call(w.s.hwnd)
}

// closeReport closes the dialog (a report being sent still goes).
func (a *app) closeReport(w *reportWin) {
	if w == nil {
		return
	}
	if a.report == w {
		a.report = nil
	}
	w.s.destroy()
}

// reportStateChanged follows the session: the "End the session now"
// option goes away with the session; a form not sent closes once the
// report is no longer offered (the person left the end screen).
func (a *app) reportStateChanged() {
	w := a.report
	if w == nil || w.s.hwnd == 0 {
		return
	}
	if a.st.Phase != PhaseSession {
		w.dlg.SessionEnded()
	}
	if w.dlg.Editable() && !a.st.Report.Open(time.Now()) && !a.st.Report.Sent {
		a.closeReport(w)
		return
	}
	w.s.relayout()
}

func (a *app) reportAction(w *reportWin, id int) {
	switch id {
	case idRepCancel, idRepClose:
		a.closeReport(w)
		return
	case idRepEnd:
		w.dlg.ToggleEnd()
	case idRepSend:
		if w.edit != 0 {
			w.dlg.SetText(windowText(w.edit))
		}
		form, ok := w.dlg.Submit()
		if !ok {
			break
		}
		err := a.ctl.SubmitReport(form, func(kind string) {
			post(func() {
				w.dlg.Finish(kind)
				if a.report == w && w.s.hwnd != 0 {
					w.s.relayout()
				}
				a.refreshSession()
			})
		})
		if err != nil {
			w.dlg.Finish(reportErrKind(err))
		}
	default:
		if i := id - idRepReasonBase; i >= 0 && i < len(reportReasons) {
			w.dlg.SetReason(reportReasons[i])
		}
	}
	if w.s.hwnd != 0 {
		w.s.relayout()
	}
}

func (a *app) buildReport(b *builder, w *reportWin) {
	t := a.tr.T
	d := w.dlg
	if d.Step == ReportSent {
		w.editR = rect{}
		b.statusLine(t("report.sent.title"), colOK)
		b.space(8)
		b.text(t("report.sent.body"), fontBody, colMuted)
		b.space(16)
		b.beginCard()
		b.text(t("report.advice.title"), fontBold, colAccent)
		b.space(6)
		b.text(t("report.advice"), fontBody, colText)
		b.endCard(colSurface)
		b.space(22)
		b.buttonRow(rowButton{id: idRepClose, label: t("report.close"), style: btnPrimary})
		return
	}
	b.text(t("report.title"), fontTitle, colText)
	b.space(4)
	b.text(t("report.intro", "name", a.cfg.ServerName), fontSmall, colMuted)
	b.space(12)
	b.statusLine(t("report.who", "name", w.who), colAccent)
	b.space(16)

	b.text(t("report.reason"), fontBold, colText)
	b.space(8)
	// One reason per row: the labels stay whole in every language; the
	// chosen one is filled.
	gap := b.px(6)
	h := b.px(34)
	for i, r := range reportReasons {
		style := btnSecondary
		if d.Reason == r {
			style = btnPrimary
		}
		b.button(idRepReasonBase+i, t(reportReasonKeys[r]), rect{b.x, b.y, b.x + b.w, b.y + h}, style, !d.Editable())
		b.y += h + gap
	}
	b.y -= gap
	b.space(16)

	b.text(t("report.details"), fontSmall, colMuted)
	b.space(6)
	eh := b.px(reportEditH)
	w.editR = rect{b.x, b.y, b.x + b.w, b.y + eh}
	b.out = append(b.out, widget{kind: wFill, r: w.editR, bg: colSurface, radius: b.px(8)})
	b.y += eh
	if d.SessionRunning {
		b.space(10)
		b.toggle(idRepEnd, t("report.endNow"), d.EndSession)
	}
	switch d.Step {
	case ReportSending:
		b.space(12)
		b.statusLine(t("report.sending"), colWarn)
	case ReportFailed:
		b.space(12)
		b.statusLine(t("report.err."+d.ErrKind), colAccent)
	}
	b.space(18)
	if d.Step == ReportFailed && !d.Retryable() {
		b.buttonRow(rowButton{id: idRepClose, label: t("report.close"), style: btnPrimary})
		return
	}
	send := t("report.send")
	if d.Step == ReportFailed {
		send = t("report.retry")
	}
	b.buttonRow(
		rowButton{id: idRepCancel, label: t("common.cancel"), style: btnSecondary, disabled: d.Step == ReportSending},
		rowButton{id: idRepSend, label: send, style: btnPrimary, disabled: !d.CanSubmit()},
	)
}

// placeReportEdit puts the edit control in the text area (hidden when the
// layout has none) and keeps its font in step with the window's DPI.
func (a *app) placeReportEdit(w *reportWin) {
	if w.edit == 0 {
		return
	}
	r := w.editR
	if r == (rect{}) {
		pShowWindow.Call(w.edit, swHide)
		return
	}
	if w.s.rtl {
		cw := w.s.px(w.s.width)
		r.L, r.R = cw-r.R, cw-r.L
	}
	f := w.s.fonts[fontBody]
	if f != w.font {
		pSendMessageW.Call(w.edit, wmSetFont, f, 0)
		w.font = f
	}
	pad := w.s.px(10)
	pMoveWindow.Call(w.edit, uintptr(r.L+pad), uintptr(r.T+pad), uintptr(r.w()-2*pad), uintptr(r.h()-2*pad), 1)
	// While sending, the text shown is what was sent (later edits are not
	// read: Submit took a copy).
	pShowWindow.Call(w.edit, swShowNA)
}
