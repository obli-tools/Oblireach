//go:build windows

package main

// Oblireach Desktop needs the Microsoft Edge WebView2 Runtime: every window
// is a WebView2 page. When it is missing (Windows Server, Windows 10 LTSC,
// images that never received it) the app used to stop at start-up without a
// word (log.Fatalf in a GUI program). Now, before anything else starts, a
// simple native message says why and offers Microsoft's installer (the
// Evergreen Bootstrapper; run by the user it installs the runtime for them,
// no administrator needed).
//
// Checked in init() (this file sorts after main.go and i18n_windows.go, whose
// init() set the config paths and the Windows display language), so no other
// start-up code needs to know about it.

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
)

// webview2DownloadURL: Microsoft's permanent link to the Evergreen Bootstrapper.
const webview2DownloadURL = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

var (
	wv2StartupUser32  = syscall.NewLazyDLL("user32.dll")
	wv2StartupShell32 = syscall.NewLazyDLL("shell32.dll")
	wv2ProcMessageBox = wv2StartupUser32.NewProc("MessageBoxW")
	wv2ProcShellExec  = wv2StartupShell32.NewProc("ShellExecuteW")
)

// webview2Probe: (installed, known). known=false when the check itself failed
// (the start goes on and reports its own error as before).
func webview2Probe() (bool, bool) {
	v, err := webviewloader.GetInstalledVersion()
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(v) != "", true
}

// webview2MissingText: the message's title and text in lang.
func webview2MissingText(lang string) (title, text string) {
	return trLang(lang, "win.webview2MissingTitle"), trLang(lang, "win.webview2Missing")
}

// webview2MissingDialog shows the message; true when the user asks for the
// installer.
func webview2MissingDialog(lang string) bool {
	title, text := webview2MissingText(lang)
	t16, err1 := syscall.UTF16PtrFromString(title)
	m16, err2 := syscall.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		return false
	}
	const (
		mbYesNo         = 0x00000004
		mbIconWarning   = 0x00000030
		mbSetForeground = 0x00010000
		mbTopmost       = 0x00040000
		mbRight         = 0x00080000
		mbRTLReading    = 0x00100000
		idYes           = 6
	)
	flags := uintptr(mbYesNo | mbIconWarning | mbSetForeground | mbTopmost)
	if langRTL(lang) {
		flags |= mbRight | mbRTLReading
	}
	r, _, _ := wv2ProcMessageBox.Call(0, uintptr(unsafe.Pointer(m16)), uintptr(unsafe.Pointer(t16)), flags)
	return r == idYes
}

func webview2OpenDownload() {
	verb, _ := syscall.UTF16PtrFromString("open")
	url, _ := syscall.UTF16PtrFromString(webview2DownloadURL)
	const swShowNormal = 1
	wv2ProcShellExec.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(url)), 0, 0, swShowNormal)
}

// webview2StartupCheck: when the runtime is known to be missing, tell the
// user (offering the installer) and end the process. Returns whether it did.
func webview2StartupCheck(probe func() (bool, bool), lang func() string, dialog func(string) bool, open func(), exit func(int)) bool {
	installed, known := probe()
	if !known || installed {
		return false
	}
	if dialog(lang()) {
		open()
	}
	exit(1)
	return true
}

func init() {
	if testing.Testing() {
		return // go test (and the test binary reused as a fake tool)
	}
	webview2StartupCheck(webview2Probe, func() string { return loadConfig().uiLang() },
		webview2MissingDialog, webview2OpenDownload, os.Exit)
}
