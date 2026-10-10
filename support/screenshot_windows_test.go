//go:build windows

package main

// Screenshots of the windows in several languages, for a visual check of
// the fonts (no boxes for CJK / Arabic), the right-to-left layout and text
// lengths. Not a pass / fail test: it writes PNG files and only fails when
// a window cannot be created or captured.
//
//	SUPPORT_SCREENSHOTS=C:\some\folder go test -run TestScreenshots -v .
//
// SUPPORT_SCREENSHOT_LANGS (comma-separated) narrows the languages.

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/oblireach/agent/engine"
)

var (
	pGetDIBits       = gdi32.NewProc("GetDIBits")
	pPrintWindowTest = user32.NewProc("PrintWindow")
)

type bitmapInfoHeader struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPels, YPels  int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// capture renders a window's client area (WM_PRINT: the painted surface
// and its child controls) to an image.
func capture(t *testing.T, hwnd uintptr) image.Image {
	t.Helper()
	var cr rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
	w, h := cr.w(), cr.h()
	if w <= 0 || h <= 0 {
		t.Fatalf("empty client area")
	}
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	defer pDeleteDC.Call(mem)
	bmp, _, _ := pCreateCompatibleBitmap.Call(screen, uintptr(w), uintptr(h))
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(mem, bmp)
	const pwClientOnly = 1
	if r, _, _ := pPrintWindowTest.Call(hwnd, mem, pwClientOnly); r == 0 {
		t.Fatalf("PrintWindow failed")
	}
	pSelectObject.Call(mem, old)
	bi := bitmapInfoHeader{Width: w, Height: -h, Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	buf := make([]byte, int(w)*int(h)*4)
	pGetDIBits.Call(mem, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0)
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i < len(buf); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = buf[i+2], buf[i+1], buf[i], 0xFF
	}
	return img
}

func savePNG(t *testing.T, dir, name string, img image.Image) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// stack puts images one under the other.
func stack(imgs ...image.Image) image.Image {
	w, h := 0, 0
	for _, im := range imgs {
		w = max(w, im.Bounds().Dx())
		h += im.Bounds().Dy() + 8
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range out.Pix {
		out.Pix[i] = 0x40
	}
	y := 0
	for _, im := range imgs {
		b := im.Bounds()
		for yy := 0; yy < b.Dy(); yy++ {
			for xx := 0; xx < b.Dx(); xx++ {
				out.Set(xx, y+yy, im.At(b.Min.X+xx, b.Min.Y+yy))
			}
		}
		y += b.Dy() + 8
	}
	return out
}

func pumpMessages() {
	var m msg
	pPeek := user32.NewProc("PeekMessageW")
	for {
		r, _, _ := pPeek.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1 /*PM_REMOVE*/)
		if r == 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// sampleChat: lines in the language of the screenshot and in others (an
// operator may write in any script).
func sampleChat(lang string) []ChatMessage {
	at := time.Date(2026, 10, 4, 14, 3, 0, 0, time.Local)
	own := map[string]string{
		"ja":    "画面の右下にあるアイコンをクリックしてください。",
		"zh-CN": "请点击屏幕右下角的图标。",
		"ar":    "يرجى النقر على الأيقونة في أسفل يمين الشاشة.",
		"ru":    "Пожалуйста, нажмите на значок в правом нижнем углу экрана.",
		"ko":    "화면 오른쪽 아래의 아이콘을 클릭해 주십시오.",
	}[lang]
	if own == "" {
		own = "Please click the icon at the bottom right of the screen."
	}
	var l ChatLog
	l.Add(ChatFromOperator, "Hello, I am connected. "+own, at)
	l.Add(ChatFromOperator, "https://example.com/a/very/long/link/without/any/space/that/must/wrap/somewhere", at.Add(10*time.Second))
	l.Add(ChatFromMe, own, at.Add(40*time.Second))
	m, _ := l.Add(ChatFromMe, "OK", at.Add(50*time.Second))
	l.MarkFailed(m.ID)
	l.Add(ChatFromOperator, "日本語 · 中文 · 한국어 · العربية · Русский", at.Add(70*time.Second))
	return l.Messages()
}

func TestScreenshots(t *testing.T) {
	dir := os.Getenv("SUPPORT_SCREENSHOTS")
	if dir == "" {
		t.Skip("set SUPPORT_SCREENSHOTS to a folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	engine.EnablePerMonitorDPIAwareness()
	langs := languages
	if v := os.Getenv("SUPPORT_SCREENSHOT_LANGS"); v != "" {
		langs = strings.Split(v, ",")
	}
	for _, lang := range langs {
		t.Run(lang, func(t *testing.T) { screenshotLang(t, dir, lang) })
	}
}

func screenshotLang(t *testing.T, dir, lang string) {
	// Windows belong to the thread that creates them.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := newApp(newTr(lang), Config{Server: DefaultServer, ServerName: DefaultServerName})
	t.Logf("%s: font %q, rtl %v", lang, a.face, a.tr.RTL())
	if !a.createMain() {
		t.Fatal("main window")
	}
	defer func() {
		a.chatReset()
		a.setBanner(false)
		if a.approval != nil {
			a.finishApproval(a.approval, decision{})
		}
		a.main.destroy()
		pumpMessages()
	}()
	pumpMessages()

	shot := func() image.Image { pumpMessages(); return capture(t, a.main.hwnd) }
	notice := shot()
	a.langOpen = true
	a.main.relayout()
	picker := shot()
	a.langOpen = false
	a.noticeOK = true
	a.setState(State{Phase: PhaseWaiting, ID: "123456789", Code: "987654321", Notice: NoticeFailed})
	waiting := shot()
	savePNG(t, dir, lang+"-1-main", stack(notice, waiting, picker))

	// Acceptance window.
	res := make(chan decision, 1)
	a.setState(State{Phase: PhaseApproval, ID: "123456789", Code: "987654321"})
	a.openApproval(Approval{SID: "s", OperatorName: "Jane Doe", Account: "helpdesk@example.com", SAS: "482 913", Deadline: time.Now().Add(80 * time.Second)}, res)
	pumpMessages()
	appr := capture(t, a.approval.s.hwnd)
	a.finishApproval(a.approval, decision{})

	// Session: main window, banner with an unread line, chat window.
	st := State{Phase: PhaseSession, Session: SessionInfo{Operator: "Jane Doe", Perms: Permissions{Control: true}, Path: "direct", Started: time.Now().Add(-185 * time.Second)}}
	a.setState(st)
	st.Session.Chat = sampleChat(lang)
	a.setState(st)
	if !a.createChat() {
		t.Fatal("chat window")
	}
	pShowWindow.Call(a.chat.s.hwnd, 4 /*SW_SHOWNOACTIVATE*/)
	pSetWindowTextW.Call(a.chat.edit, uintptr(unsafe.Pointer(utf16(map[bool]string{true: "مرحبا", false: "Thanks!"}[a.tr.RTL()]))))
	a.banner.relayout()
	a.main.relayout()
	pumpMessages()
	banner := capture(t, a.banner.hwnd)
	sess := capture(t, a.main.hwnd)
	chat := capture(t, a.chat.s.hwnd)
	savePNG(t, dir, lang+"-2-session", stack(appr, banner, sess))
	savePNG(t, dir, lang+"-3-chat", chat)

	// Report: offered by the banner and the session screen; the dialog
	// (form, failure, sent) and the end screen.
	a.closeChat()
	st.Report = ReportInfo{Available: true}
	a.setState(st)
	a.openReport()
	if a.report == nil {
		t.Fatal("report window")
	}
	defer a.closeReport(a.report)
	w := a.report
	w.dlg.SetReason(reportReasons[0])
	pSetWindowTextW.Call(w.edit, uintptr(unsafe.Pointer(utf16(map[bool]string{true: "طلب رقم بطاقتي المصرفية.", false: "He asked for my card number and my bank password."}[a.tr.RTL()]))))
	w.s.relayout()
	pumpMessages()
	repForm := capture(t, w.s.hwnd)
	w.dlg.Submit()
	w.dlg.Finish(ReportErrUnreachable)
	w.s.relayout()
	pumpMessages()
	repFail := capture(t, w.s.hwnd)
	w.dlg.Submit()
	w.dlg.Finish("")
	w.s.relayout()
	pumpMessages()
	repSent := capture(t, w.s.hwnd)
	a.banner.relayout()
	pumpMessages()
	banner2 := capture(t, a.banner.hwnd)
	sess2 := capture(t, a.main.hwnd)
	a.closeReport(w)
	a.setState(State{Phase: PhaseEnded, Session: st.Session, Ended: EndInfo{Operator: "Jane Doe", Duration: 185 * time.Second, Reason: EndPeer},
		Report: ReportInfo{Available: true, Until: time.Now().Add(5 * time.Minute)}})
	ended := shot()
	savePNG(t, dir, lang+"-4-report", stack(banner2, sess2, ended))
	savePNG(t, dir, lang+"-5-report-dialog", stack(repForm, repFail, repSent))
	if colorsIn(repForm) < 4 || colorsIn(repSent) < 4 {
		t.Errorf("blank report capture")
	}

	// Something was painted (not a blank capture).
	if colorsIn(chat) < 4 || colorsIn(waiting) < 4 {
		t.Errorf("blank capture")
	}
}

func colorsIn(img image.Image) int {
	seen := map[color.RGBA]bool{}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 3 {
		for x := b.Min.X; x < b.Max.X; x += 3 {
			r, g, bb, _ := img.At(x, y).RGBA()
			seen[color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bb >> 8), 0}] = true
		}
	}
	return len(seen)
}
