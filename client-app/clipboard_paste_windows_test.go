//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"
)

func pasteRels(res *pasteResult) []string {
	out := make([]string, 0, len(res.Items))
	for _, it := range res.Items {
		s := it.Rel
		if it.IsDir {
			s += "/"
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func pasteRead(t *testing.T, h string) string {
	t.Helper()
	b64, err := clipPastes.readB64(h, 0, 1<<20)
	if err != nil {
		t.Fatalf("read %s: %v", h, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(b64)
	return string(raw)
}

// Folders copied in Explorer: walked (each folder before its contents),
// links never followed, the app's own data never read, the files readable
// through their handles only.
func TestPasteFilesWalk(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "site", "index.html"), "<h1>")
	mustWrite(t, filepath.Join(root, "site", "css", "a.css"), "b{}")
	if err := os.MkdirAll(filepath.Join(root, "site", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "notes.txt"), "n")
	mustWrite(t, filepath.Join(root, "secret", "cookies"), "s")
	mustWrite(t, filepath.Join(root, "site", "private", "k"), "k")
	// A junction inside the folder: never followed.
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "site", "jn"), filepath.Join(root, "secret")).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	paths := []string{filepath.Join(root, "site"), filepath.Join(root, "notes.txt"), filepath.Join(root, "secret"), filepath.Join(root, "missing"), `C:\`}
	res, err := pasteFiles(winSys{}, []string{filepath.Join(root, "secret"), filepath.Join(root, "site", "private")}, paths, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"notes.txt", "site/", "site/css/", "site/css/a.css", "site/empty/", "site/index.html"}
	if got := pasteRels(res); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("items = %v, want %v", got, want)
	}
	if res.Kind != "files" || res.Skipped.Links != 1 || res.Skipped.Denied != 4 || res.Limit != "" || res.TotalBytes != 8 {
		t.Errorf("result = kind %s links %d denied %d limit %q bytes %d", res.Kind, res.Skipped.Links, res.Skipped.Denied, res.Limit, res.TotalBytes)
	}
	// Each folder before what it holds.
	pos := map[string]int{}
	for i, it := range res.Items {
		pos[it.Rel] = i
	}
	if !(pos["site"] < pos["site/css"] && pos["site/css"] < pos["site/css/a.css"]) {
		t.Errorf("order: %v", pos)
	}
	for _, it := range res.Items {
		if it.IsDir != (it.Handle == "") {
			t.Errorf("%s: handle %q (a file has one, a folder none)", it.Rel, it.Handle)
		}
		if it.Rel == "site/css/a.css" && pasteRead(t, it.Handle) != "b{}" {
			t.Error("a.css: wrong content")
		}
		if strings.Contains(it.Handle, root) || strings.Contains(it.Handle, `\`) {
			t.Error("a handle carries a path")
		}
	}
	// Too large (100 MB per file to a device of a server): left out, said.
	w := &pasteWalker{sys: winSys{}, maxFile: 2, maxTotal: 1 << 30}
	r2 := w.walkPaths([]string{filepath.Join(root, "site")})
	if r2.Skipped.TooLarge != 2 || len(r2.Skipped.TooLargeNames) != 2 {
		t.Errorf("too large: %+v", r2.Skipped)
	}
	// The batch limits: nothing at all.
	w = &pasteWalker{sys: winSys{}, maxFile: 1 << 20, maxTotal: 5}
	if r3 := w.walkPaths([]string{filepath.Join(root, "site")}); r3.Limit != "bytes" || len(r3.Items) != 0 {
		t.Errorf("bytes limit: %q %d items", r3.Limit, len(r3.Items))
	}
	old := localWalkCap
	localWalkCap = 3
	defer func() { localWalkCap = old }()
	if r4, _ := pasteFiles(winSys{}, nil, paths, false); r4.Limit != "entries" || len(r4.Items) != 0 {
		t.Errorf("entries limit: %q %d items", r4.Limit, len(r4.Items))
	}
	localWalkCap = old
	w = &pasteWalker{sys: winSys{}, maxFile: 1 << 20, maxTotal: 1 << 30}
	deep := root
	for i := 0; i < localWalkMaxDepth+2; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Skip("deep folder:", err)
	}
	if r5 := w.walkPaths([]string{filepath.Join(root, "d")}); r5.Limit != "depth" {
		t.Errorf("depth limit: %q", r5.Limit)
	}
}

// ── The real Windows clipboard ──
// Set by the test through SetClipboardData on a window of its own, read back
// by the code under test; the operator's clipboard saved first and put back
// afterwards (OBLIREACH_SKIP_CLIPBOARD_TEST=1: skipped).

var procEnumClipboardFormats = modUser32.NewProc("EnumClipboardFormats")

type savedFormat struct {
	format uintptr
	data   []byte
}

// GDI handles and owner formats cannot be copied as memory; Windows makes
// the synthesized ones again.
var clipNotMemory = map[uintptr]bool{2: true, 3: true, 9: true, 14: true, 0x80: true, 0x82: true, 0x83: true, 0x8E: true}

func clipSaveLocked() []savedFormat {
	var out []savedFormat
	total := 0
	for f := uintptr(0); ; {
		r, _, _ := procEnumClipboardFormats.Call(f)
		if r == 0 {
			break
		}
		f = r
		if clipNotMemory[f] {
			continue
		}
		if b, ok := clipReadRawLocked(f, 64<<20); ok && total+len(b) <= 128<<20 {
			total += len(b)
			out = append(out, savedFormat{f, b})
		}
	}
	return out
}

func clipSetLocked(format uintptr, data []byte) error {
	h, _, e := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if h == 0 {
		return e
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("GlobalLock")
	}
	copy(unsafe.Slice((*byte)(globalPtr(p)), len(data)), data)
	procGlobalUnlock.Call(h)
	if r, _, e := procSetClipboardData.Call(format, h); r == 0 {
		procGlobalFree.Call(h)
		return e
	}
	return nil
}

func clipReplace(t *testing.T, hwnd uintptr, formats ...savedFormat) {
	t.Helper()
	if err := clipOpen(hwnd); err != nil {
		t.Fatal(err)
	}
	defer clipClose()
	procEmptyClipboard.Call()
	for _, f := range formats {
		if err := clipSetLocked(f.format, f.data); err != nil {
			t.Fatalf("SetClipboardData(%d): %v", f.format, err)
		}
	}
}

func dropFiles(paths []string) []byte {
	var u []uint16
	for _, p := range paths {
		u = append(u, utf16.Encode([]rune(p))...)
		u = append(u, 0)
	}
	u = append(u, 0)
	b := make([]byte, 20+2*len(u))
	binary.LittleEndian.PutUint32(b[0:], 20) // pFiles
	binary.LittleEndian.PutUint32(b[16:], 1) // fWide
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[20+2*i:], c)
	}
	return b
}

func utf16z(s string) []byte {
	u := append(utf16.Encode([]rune(s)), 0)
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

func TestClipboardPasteReal(t *testing.T) {
	if os.Getenv("OBLIREACH_SKIP_CLIPBOARD_TEST") == "1" {
		t.Skip("OBLIREACH_SKIP_CLIPBOARD_TEST=1")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cls, _ := utf16PtrOrNil("STATIC")
	hwnd, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, ^uintptr(2) /* HWND_MESSAGE */, 0, 0, 0)
	if hwnd == 0 {
		t.Skip("no window for the clipboard:", e)
	}
	defer procDestroyWindow.Call(hwnd)
	clipFormats()
	// The operator's clipboard: saved, put back whatever happens.
	if err := clipOpen(hwnd); err != nil {
		t.Skip("clipboard busy:", err)
	}
	saved := clipSaveLocked()
	clipClose()
	defer func() {
		if err := clipOpen(hwnd); err == nil {
			procEmptyClipboard.Call()
			for _, f := range saved {
				_ = clipSetLocked(f.format, f.data)
			}
			clipClose()
		}
	}()

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "dir", "inner.txt"), "inner")
	mustWrite(t, filepath.Join(root, "top.txt"), "top!")
	move := []byte{dropEffMove, 0, 0, 0}
	clipReplace(t, hwnd, savedFormat{cfHDrop, dropFiles([]string{filepath.Join(root, "dir"), filepath.Join(root, "top.txt")})}, savedFormat{cfPreferredEffect, move})

	if k := clipPasteKindsNow(); !k.Files || k.Image || k.Text {
		t.Errorf("kinds with files = %+v", k)
	}
	if err := clipOpen(hwnd); err != nil {
		t.Fatal(err)
	}
	raw := clipPasteReadLocked()
	clipClose()
	if raw.kind != "files" || !raw.cut || len(raw.paths) != 2 || raw.paths[1] != filepath.Join(root, "top.txt") {
		t.Fatalf("raw = %+v", raw)
	}
	res, err := pasteBuild(raw, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := pasteRels(res); strings.Join(got, ",") != "dir/,dir/inner.txt,top.txt" || !res.Cut {
		t.Errorf("paste = %v cut=%v", got, res.Cut)
	}
	for _, it := range res.Items {
		if it.Rel == "dir/inner.txt" && pasteRead(t, it.Handle) != "inner" {
			t.Error("inner.txt: wrong content")
		}
	}
	// Cut: the files copied, never removed here.
	if _, err := os.Stat(filepath.Join(root, "top.txt")); err != nil {
		t.Error("a cut file was touched:", err)
	}
	// The bridge's binding answers through __reachLocalFs, its handles read
	// through reachReadFileChunk.
	host := &syncHost{}
	b := &nativeBridge{w: host, hwnd: hwnd, token: "0123456789abcdef0123456789abcdef", origin: "http://127.0.0.1:1", files: map[string]fileHandle{}, done: make(chan struct{})}
	if _, err := b.clipboardPaste("bad", "0123abcd", false); err != errNotAllowed {
		t.Errorf("without the token: %v", err)
	}
	if _, err := b.clipboardPaste(b.token, "../x", false); err == nil {
		t.Error("a bad request id accepted")
	}
	if ok, err := b.clipboardPaste(b.token, "0123abcd", false); !ok || err != nil {
		t.Fatalf("clipboardPaste = %v, %v", ok, err)
	}
	evals := host.waitEvals(t, 1)
	if len(evals) != 1 || !strings.Contains(evals[0], `f("0123abcd", true, {"kind":"files","cut":true`) || strings.Contains(evals[0], strings.ReplaceAll(root, `\`, `\\`)) {
		t.Errorf("answer = %.300s", evals[0])
	}

	// An image alone (a screenshot's DIB): a PNG; with text: nothing.
	h := dibHeader(40, 2, 2, 24, biRGB, 0, [4]uint32{})
	dib := append(h, 255, 0, 0, 0, 255, 0, 0, 0, 0, 0, 255, 255, 255, 255, 0, 0)
	clipReplace(t, hwnd, savedFormat{cfDIB, dib})
	if k := clipPasteKindsNow(); !k.Image || k.Files || k.Text {
		t.Errorf("kinds with an image = %+v", k)
	}
	if err := clipOpen(hwnd); err != nil {
		t.Fatal(err)
	}
	raw = clipPasteReadLocked()
	clipClose()
	if raw.kind != "dib" && raw.kind != "png" {
		t.Fatalf("an image read as %q", raw.kind)
	}
	res, err = pasteBuild(raw, false, time.Date(2026, 10, 4, 12, 0, 0, 0, time.Local))
	if err != nil || res.Kind != "image" || res.Items[0].Rel != "Clipboard 2026-10-04 12-00-00.png" {
		t.Fatalf("image paste = %+v, %v", res, err)
	}
	img, err := png.Decode(bytes.NewReader([]byte(pasteRead(t, res.Items[0].Handle))))
	if err != nil || img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("the PNG: %v", err)
	}
	if r, _, _, _ := img.At(0, 0).RGBA(); r != 0xffff {
		t.Error("the PNG's top-left pixel is not red")
	}
	clipReplace(t, hwnd, savedFormat{cfDIB, dib}, savedFormat{cfUnicodeText, utf16z("A1\tB1")})
	if k := clipPasteKindsNow(); k.Image || !k.Text {
		t.Errorf("kinds with text and an image = %+v", k)
	}
	if err := clipOpen(hwnd); err != nil {
		t.Fatal(err)
	}
	raw = clipPasteReadLocked()
	clipClose()
	if raw.kind != "none" {
		t.Errorf("text with an image read as %q (text: nothing pasted)", raw.kind)
	}
}

func utf16PtrOrNil(s string) (*uint16, error) {
	u := utf16.Encode([]rune(s + "\x00"))
	return &u[0], nil
}
