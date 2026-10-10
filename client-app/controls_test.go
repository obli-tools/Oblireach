package main

import (
	"strings"
	"testing"
)

// No native light borders: the
// native look of the controls is neutralised for the whole page (controls.go,
// before the page's rules), the option groups share the segmented control,
// and the <select>s get the app's own drop-down list (dropdown.go). The
// behaviour is checked in a real Chromium by the JS tests (test-dropdown.mjs).
func TestControlsBaseAndDropdown(t *testing.T) {
	page := buildUI(&Config{Language: "en"})
	head := page[:strings.Index(page, "</head>")]
	for _, want := range []string{
		"button,input,select,textarea,[role=button]{border:none;outline:none;font-family:inherit;-webkit-appearance:none;appearance:none}",
		":focus,:focus-visible{outline:none}",
		"button:focus-visible,[role=button]:focus-visible",
		"input[type=checkbox]:checked,input[type=checkbox]:indeterminate,input[type=radio]:checked{background:var(--accent-fill)}",
		".seg{display:flex;flex-wrap:wrap;gap:2px;padding:3px;border-radius:10px;background:var(--input-bg)}",
		".seg>.on,.seg>.active,.seg>[aria-checked=true],.seg>[aria-pressed=true]{background:var(--accent-fill);color:var(--on-accent)",
		"select.odd-native{display:none!important}",
		".odd-pop{position:fixed;",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("head lacks %q", want)
		}
	}
	// The base comes after the theme's values and before every rule of the page.
	base, theme, pageRules := strings.Index(head, "/* ── Native controls"), strings.Index(head, `[data-theme="neon"]{`), strings.Index(head, ".topbar{height:44px")
	if !(theme >= 0 && base > theme && pageRules > base) {
		t.Errorf("order theme %d / base %d / page %d", theme, base, pageRules)
	}
	// No white ring nor border in the base and the list.
	for _, bad := range []string{"#fff", "#FFF", "255,255,255", "solid"} {
		if strings.Contains(controlsCSS+dropdownCSS, bad) {
			t.Errorf("controls / dropdown CSS holds %q", bad)
		}
	}
	// The option groups are segmented controls.
	for _, want := range []string{"tabs.className = 'cxn-kind-tabs seg'", "modeTabs.className = 'cxn-kind-tabs seg'", "tabs.className = 'dep-os-tabs seg fit'"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The drop-down list runs in the page (every window: one page).
	body := page[len(head):]
	for _, want := range []string{"// ── odd:begin", "function oddEnhance(sel)", "oddStart();", "// ── odd:end"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}
