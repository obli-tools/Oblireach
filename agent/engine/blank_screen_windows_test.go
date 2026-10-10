//go:build windows

package engine

import (
	"image"
	"image/png"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// The decision follows the Windows build: 10 2004 (19041) and later only.
func TestBlankScreenWindowsVersionGate(t *testing.T) {
	saved := WindowsVersion
	defer func() { WindowsVersion = saved }()
	cases := []struct {
		v  WinVersion
		ok bool
	}{
		{WinVersion{Major: 6, Minor: 1, Build: 7601}, false},       // 7 / 2008 R2
		{WinVersion{Major: 6, Minor: 3, Build: 9600}, false},       // 8.1 / 2012 R2
		{WinVersion{Major: 10, Build: 17763, Server: true}, false}, // 2019
		{WinVersion{Major: 10, Build: 18363}, false},               // 1909
		{WinVersion{Major: 10, Build: 19041}, true},                // 2004
		{WinVersion{Major: 10, Build: 20348, Server: true}, true},  // 2022
		{WinVersion{Major: 10, Build: 26100}, true},                // 11 24H2 / 2025
		{WinVersion{}, false},                                      // unknown: never risk it
	}
	for _, c := range cases {
		WindowsVersion = func() WinVersion { return c.v }
		ok, reason := platformBlankScreenAvailable()
		if ok != c.ok || (!ok && reason != "os_version") {
			t.Errorf("%+v: available=%v reason=%q, want %v", c.v, ok, reason, c.ok)
		}
	}
}

func TestBlankLogoDecodes(t *testing.T) {
	px, w, h := bsLogo()
	if px == nil || w < 200 || h < 40 || len(px) != w*h*4 {
		t.Fatalf("logo %dx%d, %d bytes", w, h, len(px))
	}
	// Composed over the page colour: a corner pixel is the page (#0d1117).
	if px[0] != 0x17 || px[1] != 0x11 || px[2] != 0x0D || px[3] != 0xff {
		t.Fatalf("corner pixel %v, want the page colour", px[:4])
	}
}

// Opt-in, on a real interactive Windows 10 2004+ desktop (it shows the page
// on this session's screens for about a second):
//
//	set OBLIREACH_BLANK_LIVE=1
//	go test -run TestBlankScreenLive -count=1 .
//
// The page comes up with its capture exclusion confirmed, a GDI capture of
// the screen does NOT contain it (the middle of the primary screen is the
// same before and while it is shown), and it is removed on request.
func TestBlankScreenLive(t *testing.T) {
	if os.Getenv("OBLIREACH_BLANK_LIVE") != "1" {
		t.Skip("set OBLIREACH_BLANK_LIVE=1 to show the page on this desktop")
	}
	if ok, reason := platformBlankScreenAvailable(); !ok {
		t.Skipf("not available here: %s", reason)
	}
	before := grabCenter(t)
	blankScreenBeat()
	if reason := bsDo(bsRequest{show: true, text: blankScreenTextFor("fr", "Test Oblireach")}); reason != "" {
		t.Fatalf("show failed: %s", reason)
	}
	time.Sleep(700 * time.Millisecond)
	shown := len(bsWindows)
	for _, w := range bsWindows {
		if !bsExcluded(w.hwnd) {
			t.Errorf("window %#x not excluded from capture", w.hwnd)
		}
	}
	during := grabCenter(t)
	bsDo(bsRequest{show: false})
	if shown == 0 {
		t.Fatal("no window shown")
	}
	same := 0
	for i := range before {
		d := int(before[i]) - int(during[i])
		if d < 0 {
			d = -d
		}
		if d <= 8 {
			same++
		}
	}
	// The page is #0d1117 with a logo: a capture that saw it would differ
	// almost everywhere in the middle of the screen.
	if same*100/len(before) < 80 {
		t.Fatalf("the GDI capture changed while the page was up (%d%% identical): the page is not excluded", same*100/len(before))
	}
	// And the page colour itself did not show up in the capture.
	pageBefore, pageDuring := pagePixels(before), pagePixels(during)
	if pageDuring > pageBefore+len(before)/4/10 {
		t.Fatalf("the page colour appears in the capture (%d px before, %d while shown)", pageBefore, pageDuring)
	}
	t.Logf("center capture: %d%% identical; page-colour pixels %d → %d", same*100/len(before), pageBefore, pageDuring)
	if len(bsWindows) != 0 {
		t.Fatal("windows left after hide")
	}
}

// pagePixels counts the pixels of exactly the page colour (#0d1117).
func pagePixels(bgra []byte) int {
	n := 0
	for i := 0; i+3 < len(bgra); i += 4 {
		if bgra[i] == 0x17 && bgra[i+1] == 0x11 && bgra[i+2] == 0x0D {
			n++
		}
	}
	return n
}

// grabCenter: a 200×200 GDI capture (BitBlt from the screen) of the middle of
// the primary screen, as BGRA.
func grabCenter(t *testing.T) []byte {
	t.Helper()
	gdi := syscall.NewLazyDLL("gdi32.dll")
	u32 := syscall.NewLazyDLL("user32.dll")
	getDC := u32.NewProc("GetDC")
	releaseDC := u32.NewProc("ReleaseDC")
	sysMetrics := u32.NewProc("GetSystemMetrics")
	createDC := gdi.NewProc("CreateCompatibleDC")
	createBmp := gdi.NewProc("CreateCompatibleBitmap")
	selectObj := gdi.NewProc("SelectObject")
	bitBlt := gdi.NewProc("BitBlt")
	getDIBits := gdi.NewProc("GetDIBits")
	delObj := gdi.NewProc("DeleteObject")
	delDC := gdi.NewProc("DeleteDC")
	cx, _, _ := sysMetrics.Call(0)
	cy, _, _ := sysMetrics.Call(1)
	const n = 200
	x, y := int(cx)/2-n/2, int(cy)/2-n/2
	screen, _, _ := getDC.Call(0)
	defer releaseDC.Call(0, screen)
	mem, _, _ := createDC.Call(screen)
	defer delDC.Call(mem)
	bmp, _, _ := createBmp.Call(screen, n, n)
	defer delObj.Call(bmp)
	old, _, _ := selectObj.Call(mem, bmp)
	bitBlt.Call(mem, 0, 0, n, n, screen, uintptr(x), uintptr(y), bsSrcCopy)
	selectObj.Call(mem, old)
	bi := bsBitmapInfoHeader{biWidth: n, biHeight: -n, biPlanes: 1, biBitCount: 32}
	bi.biSize = uint32(unsafe.Sizeof(bi))
	buf := make([]byte, n*n*4)
	if r, _, _ := getDIBits.Call(mem, bmp, 0, n, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); r == 0 {
		t.Fatal("GetDIBits failed")
	}
	return buf
}

// Opt-in: renders the page (1920×1080 and 1280×1024, French and Arabic) to
// PNG files in OBLIREACH_BLANK_PREVIEW (a folder) for a visual check.
func TestBlankScreenPreview(t *testing.T) {
	dir := os.Getenv("OBLIREACH_BLANK_PREVIEW")
	if dir == "" {
		t.Skip("set OBLIREACH_BLANK_PREVIEW=<folder> to render the page")
	}
	for _, c := range []struct {
		name, lang string
		w, h       int32
	}{{"fr-1080p", "fr", 1920, 1080}, {"ar-1280x1024", "ar", 1280, 1024}, {"ja-portrait", "ja", 1080, 1920}} {
		px := renderPage(t, c.w, c.h, blankScreenTextFor(c.lang, "Alice Martin"))
		img := image.NewNRGBA(image.Rect(0, 0, int(c.w), int(c.h)))
		for i := 0; i < int(c.w*c.h); i++ {
			img.Pix[i*4+0], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = px[i*4+2], px[i*4+1], px[i*4+0], 0xff
		}
		f, err := os.Create(dir + "/blank-" + c.name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		_ = png.Encode(f, img)
		f.Close()
	}
}

func renderPage(t *testing.T, w, h int32, text blankScreenText) []byte {
	t.Helper()
	gdi := syscall.NewLazyDLL("gdi32.dll")
	u32 := syscall.NewLazyDLL("user32.dll")
	getDC := u32.NewProc("GetDC")
	releaseDC := u32.NewProc("ReleaseDC")
	createDC := gdi.NewProc("CreateCompatibleDC")
	createBmp := gdi.NewProc("CreateCompatibleBitmap")
	selectObj := gdi.NewProc("SelectObject")
	getDIBits := gdi.NewProc("GetDIBits")
	delObj := gdi.NewProc("DeleteObject")
	delDC := gdi.NewProc("DeleteDC")
	screen, _, _ := getDC.Call(0)
	defer releaseDC.Call(0, screen)
	mem, _, _ := createDC.Call(screen)
	defer delDC.Call(mem)
	bmp, _, _ := createBmp.Call(screen, uintptr(w), uintptr(h))
	defer delObj.Call(bmp)
	old, _, _ := selectObj.Call(mem, bmp)
	bsRender(mem, w, h, text)
	selectObj.Call(mem, old)
	bi := bsBitmapInfoHeader{biWidth: w, biHeight: -h, biPlanes: 1, biBitCount: 32}
	bi.biSize = uint32(unsafe.Sizeof(bi))
	buf := make([]byte, int(w*h*4))
	if r, _, _ := getDIBits.Call(mem, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); r == 0 {
		t.Fatal("GetDIBits failed")
	}
	return buf
}
