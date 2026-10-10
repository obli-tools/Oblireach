//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestWebView2StartupCheck(t *testing.T) {
	type run struct {
		dialogLang string
		opened     bool
		exitCode   int
	}
	check := func(installed, known, yes bool) (bool, run) {
		var r run
		r.exitCode = -1
		did := webview2StartupCheck(
			func() (bool, bool) { return installed, known },
			func() string { return "fr" },
			func(lang string) bool { r.dialogLang = lang; return yes },
			func() { r.opened = true },
			func(code int) { r.exitCode = code },
		)
		return did, r
	}
	if did, r := check(true, true, true); did || r.dialogLang != "" || r.exitCode != -1 {
		t.Fatalf("runtime present: nothing shown, start goes on (%+v)", r)
	}
	if did, r := check(false, false, true); did || r.dialogLang != "" || r.exitCode != -1 {
		t.Fatalf("detection failed: the start goes on (its own error as before) (%+v)", r)
	}
	if did, r := check(false, true, true); !did || r.dialogLang != "fr" || !r.opened || r.exitCode != 1 {
		t.Fatalf("missing, user asks for the installer: %+v", r)
	}
	if did, r := check(false, true, false); !did || r.opened || r.exitCode != 1 {
		t.Fatalf("missing, user declines: message then exit, nothing opened: %+v", r)
	}
	if !strings.HasPrefix(webview2DownloadURL, "https://go.microsoft.com/") {
		t.Fatal("the installer comes from Microsoft's own link")
	}
}

func TestWebView2MissingTextTranslated(t *testing.T) {
	enTitle, enText := webview2MissingText("en")
	if !strings.Contains(enText, "Microsoft Edge WebView2 Runtime") || enTitle == "win.webview2MissingTitle" {
		t.Fatalf("English: %q / %q", enTitle, enText)
	}
	for _, l := range uiLanguages {
		title, text := webview2MissingText(l.Code)
		if title == "" || text == "" || strings.HasPrefix(title, "win.") || strings.HasPrefix(text, "win.") {
			t.Fatalf("%s: untranslated key", l.Code)
		}
		if l.Code != "en" && text == enText {
			t.Fatalf("%s: English text shown", l.Code)
		}
		if !strings.Contains(text, "WebView2") {
			t.Fatalf("%s: the product name must stay in the message: %q", l.Code, text)
		}
	}
	if _, fr := webview2MissingText("fr"); !strings.Contains(fr, "Voulez-vous") {
		t.Fatalf("French: formal question expected: %q", fr)
	}
}
