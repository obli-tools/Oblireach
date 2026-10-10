//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
)

var (
	chatTestProcPeekMessageW   = chatUser32.NewProc("PeekMessageW")
	chatTestProcGetWindowRect  = chatUser32.NewProc("GetWindowRect")
	chatTestProcIsWindow       = chatUser32.NewProc("IsWindow")
	chatTestProcGetLayeredAttr = chatUser32.NewProc("GetLayeredWindowAttributes")
)

const (
	chatTestGwlStyle     = ^uintptr(15) // GWL_STYLE = -16
	chatTestWsCaption    = 0x00C00000
	chatTestWsThickFrame = 0x00040000
	chatTestWsVisible    = 0x10000000
)

// chatTestPump runs the thread's message queue (timers included) until cond
// holds or the timeout expires.
func chatTestPump(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	var m chatMsg
	for time.Now().Before(deadline) {
		for {
			r, _, _ := chatTestProcPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1) // PM_REMOVE
			if r == 0 {
				break
			}
			chatProcTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			chatProcDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func chatTestVisible(hwnd uintptr) bool {
	v, _, _ := chatProcIsWindowVisible.Call(hwnd)
	return v != 0
}

func chatTestRect(hwnd uintptr) chatRect {
	var r chatRect
	chatTestProcGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

// newTestChatHost creates a real (hidden) host window at a fixed work area,
// or skips when no window can be created (no desktop).
func newTestChatHost(t *testing.T, work chatRect) *chatHost {
	t.Helper()
	h := newChatHost()
	h.workArea = func() chatRect { return work }
	h.quitOnDestroy = false // keep WM_QUIT out of the test thread's queue
	h.embedDeadline = 0     // no WebView2 here
	if err := h.create("Obliance Chat test"); err != nil {
		t.Skipf("no window station / desktop: %v", err)
	}
	t.Cleanup(func() {
		if ok, _, _ := chatTestProcIsWindow.Call(h.hwnd); ok != 0 {
			chatProcDestroyWindow.Call(h.hwnd)
		}
	})
	return h
}

// The window exists hidden, already in its final form and place, and the
// fallback timer shows it IN PLACE: never visible before it is styled and
// placed, never hidden forever (no WebView2 page-loaded signal here).
func TestChatHostHiddenThenRevealedInPlace(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Far outside any real screen: nothing flashes on the developer's desktop.
	work := chatRect{-20000, -20000, -18000, -18900}
	want := chatBottomRight(work, chatWinW, chatWinH, chatWinMargin)

	h := newTestChatHost(t, work)
	h.revealAfter = 150 * time.Millisecond

	if chatTestVisible(h.hwnd) {
		t.Fatal("window visible right after creation")
	}
	if got := chatTestRect(h.hwnd); got != want {
		t.Fatalf("created at %+v, want %+v (final place before being shown)", got, want)
	}
	style, _, _ := chatProcGetWindowLongPtrW.Call(h.hwnd, chatTestGwlStyle)
	if style&chatWsPopup == 0 || style&chatTestWsCaption != 0 || style&chatTestWsThickFrame != 0 || style&chatTestWsVisible != 0 {
		t.Fatalf("style %#x: want WS_POPUP, no caption / frame / WS_VISIBLE", style)
	}
	ex, _, _ := chatProcGetWindowLongPtrW.Call(h.hwnd, chatGwlExStyle)
	if ex&chatWsExToolWindow == 0 || ex&chatWsExTopmost == 0 {
		t.Fatalf("ex style %#x: want WS_EX_TOOLWINDOW|WS_EX_TOPMOST", ex)
	}

	h.armTimers()
	if !chatTestPump(5*time.Second, func() bool { return chatTestVisible(h.hwnd) }) {
		t.Fatal("fallback timer never showed the window (hidden forever)")
	}
	if got := chatTestRect(h.hwnd); got != want {
		t.Fatalf("shown at %+v, want %+v (no jump)", got, want)
	}
	// 94 % opaque: layered, and WITH its alpha (a layered window without
	// attributes is never drawn).
	ex, _, _ = chatProcGetWindowLongPtrW.Call(h.hwnd, chatGwlExStyle)
	var alpha byte
	var flags uint32
	chatTestProcGetLayeredAttr.Call(h.hwnd, 0, uintptr(unsafe.Pointer(&alpha)), uintptr(unsafe.Pointer(&flags)))
	if ex&chatWsExLayered == 0 || flags&chatLwaAlpha == 0 || alpha != chatWinAlpha {
		t.Fatalf("ex style %#x alpha=%d flags=%#x: want layered at %d", ex, alpha, flags, chatWinAlpha)
	}

	// A second reveal (e.g. the page loads after the fallback) changes nothing.
	h.reveal("again")
	if got := chatTestRect(h.hwnd); got != want || !chatTestVisible(h.hwnd) {
		t.Fatalf("second reveal moved or hid the window: %+v", got)
	}
}

func TestChatHostBubbleAndCloseFromOtherGoroutine(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	h := newTestChatHost(t, chatRect{-20000, -20000, -18000, -18900})
	h.reveal("test")

	// Work dispatched from another goroutine runs on the UI thread.
	done := make(chan struct{})
	go func() { h.dispatch(h.toBubble); close(done) }()
	<-done
	if !chatTestPump(2*time.Second, func() bool { return chatTestRect(h.hwnd).width() == chatBubbleSize }) {
		t.Fatalf("bubble not applied: %+v", chatTestRect(h.hwnd))
	}
	if r := chatTestRect(h.hwnd); r.height() != chatBubbleSize || !chatTestVisible(h.hwnd) {
		t.Fatalf("bubble: %+v visible=%v", r, chatTestVisible(h.hwnd))
	}
	h.dispatch(h.toFull)
	if !chatTestPump(2*time.Second, func() bool { return chatTestRect(h.hwnd).width() == chatWinW }) {
		t.Fatalf("full size not restored: %+v", chatTestRect(h.hwnd))
	}

	go h.close()
	if !chatTestPump(2*time.Second, func() bool { ok, _, _ := chatTestProcIsWindow.Call(h.hwnd); return ok == 0 }) {
		t.Fatal("close() did not destroy the window")
	}
	chatHostsMu.Lock()
	_, still := chatHosts[h.hwnd]
	chatHostsMu.Unlock()
	if still {
		t.Fatal("destroyed host still registered")
	}
}

func TestBuildChatHTML(t *testing.T) {
	names := []string{"", "Jane Doe", `100% <b>Bob</b> & "Co" 'x'`}
	avatars := []string{"", "data:image/png;base64,iVBORw0KGgo=", `data:image/png;base64,AA" onerror="x`}
	for _, fr := range []bool{false, true} {
		i18n := chatStrings(fr)
		for _, name := range names {
			for _, av := range avatars {
				page := buildChatHTML(name, av, "", chatInitials(""), i18n)
				if strings.Contains(page, "%!") {
					t.Fatalf("fmt artefact in page (name=%q): %s", name, regexp.MustCompile(`%!.{0,20}`).FindString(page))
				}
				if strings.Contains(page, "<b>Bob") || strings.Contains(page, `onerror="x`) {
					t.Fatalf("unescaped operator data in page (name=%q avatar=%q)", name, av)
				}
				if strings.Contains(page, "-webkit-app-region") {
					t.Fatal("app-region drag is ignored by WebView2 by default; the header uses goStartDrag")
				}
				if fr && !strings.Contains(page, "Discussion avec") {
					t.Fatal("French header missing")
				}
			}
		}
	}
	page := buildChatHTML("100% Bob", "", "", "JD", chatStrings(false))
	if !strings.Contains(page, "Chatting with 100% Bob") {
		t.Fatal("operator name with % not rendered verbatim")
	}
}

// The page script must parse (a syntax error = a dead chat window). Uses
// node when available.
func TestBuildChatHTMLScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page := buildChatHTML(`O'Brien "x" </script>`, "", "", "OB", chatStrings(true))
	m := regexp.MustCompile(`(?s)<script>(.*)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no script in page")
	}
	f := filepath.Join(t.TempDir(), "chat.js")
	if err := os.WriteFile(f, []byte(chatBridgeScript()+"\n"+m[1]), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("chat page script does not parse: %v\n%s", err, out)
	}
}

// The chat window's WebView2 (a real runtime, when this machine has one):
// browser shortcut keys off through ICoreWebView2Settings3 (asked with
// QueryInterface — the IID and the vtable slot are checked here against the
// real runtime), status bar / zoom off, and its profile in the user's LOCAL
// profile (%LOCALAPPDATA%\Oblireach\ChatWebView2), never the roaming one.
func TestChatWebView2Hardening(t *testing.T) {
	if v, err := webviewloader.GetInstalledVersion(); err != nil || strings.TrimSpace(v) == "" {
		t.Skip("no WebView2 runtime on this machine")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// The helper runs on the main thread, where go-webview2's init() set up
	// COM (STA); a test goroutine's thread must do it itself.
	ole32 := syscall.NewLazyDLL("ole32.dll")
	if r, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2 /* COINIT_APARTMENTTHREADED */); int32(r) < 0 {
		t.Skipf("CoInitializeEx: %08x", uint32(r))
	}
	defer ole32.NewProc("CoUninitialize").Call()

	dir, err := os.MkdirTemp("", "oblireach-chat-wv2-")
	if err != nil {
		t.Fatal(err)
	}
	// Best effort: the runtime's processes may hold the profile a moment.
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("LOCALAPPDATA", dir)
	want := filepath.Join(dir, "Oblireach", "ChatWebView2")
	if got := chatWebViewDataPath(); got != want {
		t.Fatalf("profile path %q, want %q", got, want)
	}

	h := newTestChatHost(t, chatRect{-20000, -20000, -18000, -18900})
	if !h.embed() {
		t.Skip("WebView2 could not start in this test session")
	}
	if h.web.DataPath != want {
		t.Fatalf("WebView2 profile %q, want %q", h.web.DataPath, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("profile folder not created: %v", err)
	}
	s, err := h.web.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !chatDisableBrowserKeys(s) {
		t.Fatal("ICoreWebView2Settings3 not found (IID) or the setting did not apply")
	}
	if on, err := s.GetAreBrowserAcceleratorKeysEnabled(); err != nil || on {
		t.Fatalf("browser shortcut keys still on (err=%v)", err)
	}
	if on, err := s.GetIsStatusBarEnabled(); err != nil || on {
		t.Fatalf("status bar still on (err=%v)", err)
	}
	if on, err := s.GetIsZoomControlEnabled(); err != nil || on {
		t.Fatalf("zoom still on (err=%v)", err)
	}
	if on, err := s.GetAreDevToolsEnabled(); err != nil || on {
		t.Fatalf("DevTools still on (err=%v)", err)
	}
	// An interface the object does not implement: nil, no crash.
	if p := chatQueryInterface(unsafe.Pointer(s), edge.NewGUID("{00000000-1111-2222-3333-444444444444}")); p != nil {
		chatComRelease(p)
		t.Fatal("QueryInterface of an unknown IID must fail")
	}
}

// The chat page never lets a dropped file replace it.
func TestChatHTMLRefusesDrops(t *testing.T) {
	page := buildChatHTML("Op", "", "user", "U", chatStrings(false))
	for _, want := range []string{"addEventListener('dragover'", "addEventListener('drop'", "preventDefault()"} {
		if !strings.Contains(page, want) {
			t.Fatalf("chat page lacks %q", want)
		}
	}
}
