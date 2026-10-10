package main

import (
	"regexp"
	"strings"
	"testing"
)

// Borderless design: the
// Desktop draws no decorative border any more, as the Obli web apps. The
// surfaces differ by their background (and --surface-shadow, a soft one on
// Daylight whose cards are close to its page), the fields by theirs, the
// focus rings and the active tabs' accent underline stay. Kept on purpose:
// the spinner's ring, the file drop zone over the picture, the monitors of
// the Screens map (the drawing of the screens), and the server's colour mark
// on the Devices / picker server separators (an inline border-inline-start).
// The chat tabs name their server with a dot, never a border.
func TestBorderlessPage(t *testing.T) {
	page := buildUI(&Config{Language: "en"})
	end := strings.Index(page, "</head>")
	if end < 0 {
		t.Fatal("no </head>")
	}
	styles := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindAllStringSubmatch(page[:end], -1)
	if len(styles) < 3 {
		t.Fatalf("%d style blocks, want the theme's, the page's and the viewer's", len(styles))
	}
	var b strings.Builder
	for _, st := range styles[1:] { // [0]: the theme's values
		b.WriteString(st[1])
		b.WriteString("\n")
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(b.String(), "")

	decl := regexp.MustCompile(`^\s*border(-(top|right|bottom|left|inline-start|inline-end|block-start|block-end))?(-color|-style|-width)?\s*:\s*(.*?)\s*$`)
	kept := map[string]bool{
		".reach-spinner": true, ".reach-drop": true,
		".toolbar-dropdown-menu .reach-minimap-mon": true, ".toolbar-dropdown-menu .reach-minimap-mon.active": true,
	}
	rules, keptSeen := 0, 0
	for _, m := range regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(css, -1) {
		sel := strings.TrimSpace(m[1])
		rules++
		for _, d := range strings.Split(m[2], ";") {
			dm := decl.FindStringSubmatch(d)
			if dm == nil || dm[4] == "none" || dm[4] == "0" {
				continue
			}
			if kept[sel] {
				keptSeen++
				continue
			}
			t.Errorf("a border line: %s{%s}", sel, strings.TrimSpace(d))
		}
	}
	if rules < 300 || keptSeen < 4 {
		t.Fatalf("%d rules, %d kept borders seen: the scan is broken", rules, keptSeen)
	}

	// No line in the border colours, anywhere (inline styles of the scripts too).
	if strings.Contains(page, "solid var(--border") {
		t.Error("the page still draws a line in var(--border)")
	}
	body := page[end:]
	if m := regexp.MustCompile(`border(-top|-right|-bottom|-left)?:\s*\d+(\.\d+)?px\s+(solid|dashed)`).FindString(body); m != "" {
		t.Errorf("an inline border line in the scripts: %q", m)
	}
	if !strings.Contains(body, `border-inline-start:4px solid ' + row.color`) {
		t.Error("the server separators lost their colour mark")
	}
	// Nor set by a script on an element (el.style.border*, border-inline /
	// border-block in an HTML string): only the server separators' mark.
	if m := regexp.MustCompile(`\.style\.border[A-Za-z]*\s*=`).FindString(body); m != "" {
		t.Errorf("a script sets a border: %q", m)
	}
	logical := regexp.MustCompile(`border-(inline|block)(-(start|end))?(-width)?\s*:\s*(0?\.\d|[1-9])`)
	if ms := logical.FindAllString(strings.ReplaceAll(body, `border-inline-start:4px solid ' + row.color`, ""), -1); len(ms) > 0 {
		t.Errorf("logical border lines in the scripts: %q", ms)
	}
	// Nor a hairline drawn by a background in the border colours (the "OR"
	// of the sign-in page: its label alone now).
	if m := regexp.MustCompile(`(height|width):\s*1px;[^"'{}]*background:\s*var\(--border`).FindString(page); m != "" {
		t.Errorf("a hairline separator: %q", m)
	}
	if !strings.Contains(page, `.chat-tab:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}`) {
		t.Error("the chat tabs (focusable) lost their focus ring")
	}

	for _, want := range []string{
		// The cards, panels and filter bars: their background and a shadow.
		".kpi-card{background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow)",
		".panel{background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow)",
		"background:var(--bg2);border-radius:14px;box-shadow:var(--surface-shadow);margin-bottom:14px}", // .filter-bar
		// The fields: their background, an accent ring on focus.
		".filter-bar input, .filter-bar select{background:var(--input-bg);border:none",
		".filter-bar input:focus, .filter-bar select:focus{box-shadow:0 0 0 2px var(--accent)}",
		".form-group input:focus{box-shadow:0 0 0 2px var(--accent)}",
		".otp-input:focus{box-shadow:0 0 0 2px var(--accent)}",
		".chat-input-row:focus-within{box-shadow:",
		".session-select:focus-visible{box-shadow:0 0 0 2px var(--accent)}",
		".rsp-item:focus-visible{box-shadow:0 0 0 2px var(--accent)}",
		".dt tbody tr.tree-row:focus-visible{outline:1px solid var(--accent)",
		// The active tabs: the accent underline, drawn inside.
		".tab-btn.active{color:var(--accent-l);box-shadow:inset 0 -2px 0 var(--accent)}",
		".session-tab.active{color:var(--accent-l);box-shadow:inset 0 -2px 0 var(--accent)}",
		// The buttons: a background of their own.
		".btn-sm{padding:6px 13px;border-radius:10px;border:none;background:rgb(var(--ink) / .06)",
		".btn-ghost{background:rgb(var(--ink) / .06);border:none",
		// The separators: gaps.
		".toolbar-sep{width:4px;height:20px;flex-shrink:0}",
		".toolbar-dropdown-menu .reach-menu-sep{height:0;margin:4px 2px}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS lacks %q", want)
		}
	}
	// Every theme gives the cards their depth; Daylight a visible one.
	blocks := themeBlocks(t, page)
	for _, th := range knownThemes {
		if blocks[th]["--surface-shadow"] == "" {
			t.Errorf("%s: no --surface-shadow", th)
		}
	}
	if !strings.Contains(blocks["obli-daylight"]["--surface-shadow"], "rgba(46,52,64,.10)") {
		t.Errorf("daylight shadow %q", blocks["obli-daylight"]["--surface-shadow"])
	}
	// The fields stand out from the cards they sit on (bg2): no border to
	// draw them any more.
	for _, th := range knownThemes {
		if blocks[th]["--input-bg"] == blocks[th]["--bg2"] {
			t.Errorf("%s: fields the colour of the cards", th)
		}
	}
}

// Every page's title zone has one height, the Devices one (with its Refresh
// button): one rule, nothing on a page changes it, no band nor line.
func TestPageHeaderOneHeight(t *testing.T) {
	page := buildUI(&Config{Language: "en"})
	if n := strings.Count(page, ".page-header{"); n != 1 {
		t.Errorf(".page-header{ appears %d times, want 1 (a page-specific rule would change its height)", n)
	}
	if !strings.Contains(page, ".page-header{min-height:56px;padding:8px 24px;display:flex;align-items:center;gap:12px;flex-shrink:0}") {
		t.Error(".page-header is not 56px high, content centred, on the page")
	}
	// Every page of the nav rail builds its title zone with it.
	for _, fn := range []string{"renderStubPage", "renderDashboardPage", "renderDevicesPage", "renderLogsPage", "renderSettingsPage", "chatPageBuild"} {
		i := strings.Index(page, "function "+fn+"(")
		if i < 0 {
			t.Errorf("no %s", fn)
			continue
		}
		if j := strings.Index(page[i:], "'page-header'"); j < 0 || j > 800 {
			t.Errorf("%s does not open with a .page-header", fn)
		}
	}
}
