package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ── Catalog files ───────────────────────────────────────────────────────────

// i18nFiles: every catalog embedded (i18n/*.json), code -> key -> text.
func i18nFiles(t *testing.T) map[string]map[string]string {
	t.Helper()
	entries, err := i18nFS.ReadDir("i18n")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	for _, e := range entries {
		name := e.Name()
		code := strings.TrimSuffix(name, ".json")
		if e.IsDir() || code == name {
			t.Errorf("i18n/%s: only <code>.json files belong there", name)
			continue
		}
		if normalizeLang(code) != code {
			t.Errorf("i18n/%s: %q is not one of the app's language codes (%v)", name, code, langCodes())
			continue
		}
		data, _ := i18nFS.ReadFile("i18n/" + name)
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Errorf("i18n/%s: not a JSON object: %v", name, err)
			continue
		}
		m := map[string]string{}
		for k, v := range raw {
			s, ok := v.(string)
			if !ok {
				t.Errorf("i18n/%s: %q is not a string (catalogs are flat: \"area.name\": \"text\")", name, k)
				continue
			}
			m[k] = s
		}
		out[code] = m
	}
	if out["en"] == nil || out["fr"] == nil {
		t.Fatal("i18n/en.json and i18n/fr.json are required")
	}
	return out
}

func langCodes() []string {
	var c []string
	for _, l := range uiLanguages {
		c = append(c, l.Code)
	}
	return c
}

var (
	i18nKeyRe     = regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9_]+)+$`)
	i18nPluralRe  = regexp.MustCompile(`^(.+)_(zero|one|two|few|many|other)$`)
	i18nPlaceRe   = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)
	pluralNeeded  = map[string][]string{} // code -> categories a plural key must have
	pluralAllowed = map[string][]string{} // code -> categories it may have
)

func init() {
	two := []string{"one", "other"}
	for _, c := range []string{"en", "de", "nl", "sv", "da", "tr", "es", "it", "fr", "pt-BR"} {
		pluralNeeded[c] = two
		pluralAllowed[c] = two
	}
	for _, c := range []string{"es", "it", "fr", "pt-BR"} {
		pluralAllowed[c] = []string{"one", "many", "other"} // "many": whole millions
	}
	for _, c := range []string{"ja", "ko", "zh-CN"} {
		pluralNeeded[c] = []string{"other"}
		pluralAllowed[c] = []string{"other"}
	}
	for _, c := range []string{"ru", "uk", "pl"} {
		pluralNeeded[c] = []string{"one", "few", "many", "other"}
		pluralAllowed[c] = pluralNeeded[c]
	}
	pluralNeeded["cs"] = []string{"one", "few", "other"}
	pluralAllowed["cs"] = []string{"one", "few", "many", "other"}
	pluralNeeded["ar"] = []string{"zero", "one", "two", "few", "many", "other"}
	pluralAllowed["ar"] = pluralNeeded["ar"]
}

func placeholders(s string) []string {
	set := map[string]bool{}
	for _, m := range i18nPlaceRe.FindAllStringSubmatch(s, -1) {
		set[m[1]] = true
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// pluralBases: the keys of cat that are plural forms, base -> categories.
func pluralBases(cat map[string]string) map[string][]string {
	out := map[string][]string{}
	for k := range cat {
		if m := i18nPluralRe.FindStringSubmatch(k); m != nil {
			out[m[1]] = append(out[m[1]], m[2])
		}
	}
	return out
}

// Every catalog: keys shaped "area.name", no empty text, no page token, and
// (the other languages, once their files exist) exactly the keys of the
// English one, with the same {{placeholders}} and the plural forms their
// grammar needs. The French one has the same key set as the English one.
func TestI18nCatalogs(t *testing.T) {
	files := i18nFiles(t)
	en := files["en"]
	enPlural := pluralBases(en)
	for base, cats := range enPlural {
		sort.Strings(cats)
		if strings.Join(cats, ",") != "one,other" {
			t.Errorf("en: plural key %s has forms %v, want _one and _other", base, cats)
		}
		if _, ok := en[base]; ok {
			t.Errorf("en: %s is both a plural key and a plain one", base)
		}
	}
	for code, cat := range files {
		for k, v := range cat {
			if !i18nKeyRe.MatchString(k) {
				t.Errorf("%s: key %q is not shaped area.name", code, k)
			}
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: %s is empty: translate it (an empty text would show the English one)", code, k)
			}
			if strings.Contains(v, "{{t:") {
				t.Errorf("%s: %s holds a page token {{t:...}}", code, k)
			}
		}
		if code == "en" {
			continue
		}
		plural := pluralBases(cat)
		// Plain keys: the same set as English, same placeholders.
		for k, ev := range en {
			if i18nPluralRe.MatchString(k) {
				continue
			}
			v, ok := cat[k]
			if !ok {
				t.Errorf("%s: missing %s (English: %q)", code, k, ev)
				continue
			}
			if a, b := placeholders(ev), placeholders(v); strings.Join(a, ",") != strings.Join(b, ",") {
				t.Errorf("%s: %s has placeholders %v, English %v", code, k, b, a)
			}
		}
		for k := range cat {
			if m := i18nPluralRe.FindStringSubmatch(k); m != nil {
				if _, ok := enPlural[m[1]]; !ok {
					t.Errorf("%s: %s is not a plural key in English", code, k)
				}
				continue
			}
			if _, ok := en[k]; !ok {
				t.Errorf("%s: %s is not in English (unused or misspelt)", code, k)
			}
		}
		// Plural keys: the forms the language needs, only those it has;
		// placeholders: _other has English's, the others may leave out
		// {{count}} ("one device", "zero devices") but nothing else.
		for base := range enPlural {
			want := en[base+"_other"]
			for _, c := range pluralNeeded[code] {
				if _, ok := cat[base+"_"+c]; !ok {
					t.Errorf("%s: missing plural form %s_%s", code, base, c)
				}
			}
			for _, c := range plural[base] {
				if !contains(pluralAllowed[code], c) {
					t.Errorf("%s: %s_%s: %s has no %q plural form (it has %v)", code, base, c, code, c, pluralAllowed[code])
				}
				got := placeholders(cat[base+"_"+c])
				full := placeholders(want)
				if c == "other" {
					if strings.Join(got, ",") != strings.Join(full, ",") {
						t.Errorf("%s: %s_other has placeholders %v, English %v", code, base, got, full)
					}
					continue
				}
				for _, p := range got {
					if !contains(full, p) {
						t.Errorf("%s: %s_%s has an unknown placeholder {{%s}}", code, base, c, p)
					}
				}
				for _, p := range full {
					if p != "count" && !contains(got, p) {
						t.Errorf("%s: %s_%s lacks {{%s}}", code, base, c, p)
					}
				}
			}
		}
	}
	// French: the same key set as English (forms included).
	fr := files["fr"]
	for k := range en {
		if _, ok := fr[k]; !ok {
			t.Errorf("fr: missing %s", k)
		}
	}
	for k := range fr {
		if _, ok := en[k]; !ok {
			t.Errorf("fr: %s is not in English", k)
		}
	}
}

// Style rule for French: "vous" or a neutral turn, never "tu".
func TestI18nFrenchNoTutoiement(t *testing.T) {
	fr := i18nFiles(t)["fr"]
	re := regexp.MustCompile(`(?i)(^|[^\p{L}'’])(tu|toi|ton|ta|tes|te)([^\p{L}'’]|$)|(^|[^\p{L}])t['’]`)
	for k, v := range fr {
		if re.MatchString(v) {
			t.Errorf("fr: %s may use tutoiement: %q", k, v)
		}
	}
}

// ── Keys used in the code ───────────────────────────────────────────────────

type keyUse struct {
	key, where string
}

// i18nPageFiles: the Go files holding page JS (their keys are scanned like
// the page's). The tool files (agent_tools.go; the terminals', the
// explorer's and the Files page's) are injected into the page's library script.
var i18nPageFiles = map[string]bool{
	"ui.go": true, "reach_viewer.go": true, "i18n.go": true,
	"agent_tools.go": true, "terminal_viewer.go": true, "explorer_viewer.go": true, "files_page.go": true,
	"local_pane.go": true, "transfers.go": true, "edit_page.go": true, // (the Files page's split view, its transfers, the editing round trip)
	"term_drop.go":         true, // (files dropped on a terminal: to the shell's folder)
	"files_dock.go":        true, // (the explorer of a session in a side panel)
	"explorer_tabs.go":     true, // (the explorer's sub-tabs and their mosaic)
	"tab_move.go":          true, // (a tab's "Move to window" menu)
	"vault_ui.go":          true, // (the password vault: its toolbar popover and Settings > Vault page)
	"scripts_page.go":      true, // (the Scripts nav page: list, create/edit/delete)
	"scripts_panel.go":     true, // (the Scripts side panel: lists kept fresh, preview, parameters, runs)
	"run_page.go":          true, // (the Run page: a script on several devices, its results)
	"deployment_page.go":   true, // (the Deployment nav page: install commands/MSI links per API key)
	"scope_pages.go":       true, // (server / tenant grouping and destination picker of Scripts and Deployment)
	"connections_page.go":  true, // (the local RDP/SSH connection tree, "local" server profile)
	"connimport_page.go":   true, // (the connection tree's import / export)
	"localconn_tools.go":   true, // (the local connections in the tool tabs: their terminal and explorer transports)
	"rdp_view.go":          true, // (the local connection tree's embedded RDP view)
	"rdp_stream_view.go":   true, // (... its stream mode, Linux)
	"help_linux_page.go":   true, // (receive help on Linux: its settings)
	"ui_platform.go":       true, // (Linux: the update banner)
	"localconn_info.go":    true, // (a local SSH connection's Info panel)
	"picker_multi.go":      true, // (the "+" list: several devices ticked, "Connect" on all of them)
	"devices_select.go":    true, // (the Devices page: several devices ticked, its bar, "Connect" / "Open as mosaic")
	"vault_devices_ui.go":  true, // (the vault: the machines a password is offered on, its picker)
	"vault_page.go":        true, // (the Vault page: entries grouped General / server / tenant / Local, the local credentials)
	"reach_resolution.go":  true, // (the session toolbar's screen resolution selector, its setting)
	"reach_codec.go":       true, // (the session toolbar's video codec selector)
	"reach_presence.go":    true, // (the session toolbar: other operators on the machine, hide the remote screen)
	"tab_groups.go":        true, // (mosaics: tabs of any kind grouped, shown together)
	"term_themes.go":       true, // (the terminals' themes: per type in Settings, per tab from its menu)
	"vm_console.go":        true, // (Hyper-V VM consoles: the VM list, the console tab's tools and messages)
	"vms_page.go":          true, // (the VMs page: Hyper-V VMs under their hosts, their consoles and power actions)
	"global_search.go":     true, // (the top bar's search: devices and VMs of the lists, their sessions)
	"devices_live.go":      true, // (the device lists kept up to date: periodic reads, socket events, the "New" chip, its setting)
	"usage_viewer.go":      true, // (the file explorer's disk usage analyzer, the drives' space on the roots listing)
	"support_page.go":      true, // (connect by code: Oblireach Support sessions, the support servers in Settings)
	"support_chat.go":      true, // (the chat of a code session, operator side)
	"help_page.go":         true, // (receive help: this computer helped by code)
	"support_dashboard.go": true, // (the Dashboard's cards: this computer's ID and code, connect to another one)
}

// i18nUsedKeys: every catalog key the code names. Page (i18nPageFiles):
// T('k') / TH('k'), {{t:k}} tokens, data-i18n*
// attributes, and every other quoted 'area.name' whose area is one of the
// catalog's (keys kept in tables: NAV_TREE, REACH_ERROR_TEXT...). Go:
// tr("k"), trLang(x, "k"), uerr(code, "k"), i18nM("k"), r.say("k").
func i18nUsedKeys(t *testing.T, areas map[string]bool) []keyUse {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	jsRes := []*regexp.Regexp{
		regexp.MustCompile(`\bTH?\(\s*'([^']+)'`),
		regexp.MustCompile(`\{\{t:([a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9_]+)+)\}\}`),
		regexp.MustCompile(`data-i18n(?:-title|-ph|-aria)?="([a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9_]+)+)"`),
		regexp.MustCompile(`'data-i18n(?:-title|-ph|-aria)?',\s*'([^']+)'`),
		regexp.MustCompile(`'([a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9_]+)+)'`),
	}
	goRes := []*regexp.Regexp{
		regexp.MustCompile(`\btr\(\s*"([^"]+)"`),
		regexp.MustCompile(`\btrLang\([^,()]+,\s*"([^"]+)"`),
		regexp.MustCompile(`\buerr\(\s*"[^"]*",\s*"([^"]+)"`),
		regexp.MustCompile(`\bi18nM\(\s*"([^"]+)"`),
		regexp.MustCompile(`\.say\(\s*"([^"]+)"`),
	}
	var out []keyUse
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		res := goRes
		if i18nPageFiles[f] {
			res = append(append([]*regexp.Regexp{}, goRes...), jsRes...)
		}
		for i, re := range res {
			generic := i == len(res)-1 && len(res) > len(goRes)
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				k := m[1]
				if generic && !areas[strings.SplitN(k, ".", 2)[0]] {
					continue // another dotted string ('video/webm', 'avc1.64...')
				}
				out = append(out, keyUse{k, f})
			}
		}
	}
	return out
}

func catalogAreas(en map[string]string) map[string]bool {
	areas := map[string]bool{}
	for k := range en {
		areas[strings.SplitN(k, ".", 2)[0]] = true
	}
	return areas
}

// Families of keys named by a prefix in the code: the Obliance agent status
// of a device, the Windows session states, the variants of a banner text
// without the version (renderUpdateBanner tv()).
var i18nKeyFamilies = []string{"devices.agentStatus.", "session.state.", "connections.rdp.err."}

// Every key the code names exists in English (plural keys by their _other
// form), so no English user ever sees a raw key.
func TestI18nKeysUsedExist(t *testing.T) {
	en := i18nFiles(t)["en"]
	uses := i18nUsedKeys(t, catalogAreas(en))
	if len(uses) < 400 {
		t.Fatalf("only %d key uses found: the scan is broken", len(uses))
	}
	for _, u := range uses {
		if _, ok := en[u.key]; ok {
			continue
		}
		if _, ok := en[u.key+"_other"]; ok {
			continue
		}
		isPrefix := false
		for _, f := range i18nKeyFamilies {
			if u.key == strings.TrimSuffix(f, ".") || u.key+"." == f {
				isPrefix = true
			}
		}
		if !isPrefix {
			t.Errorf("%s uses %q: not in i18n/en.json", u.where, u.key)
		}
	}
	// tv('k'): k and kNoVersion.
	ui, _ := os.ReadFile("ui.go")
	for _, m := range regexp.MustCompile(`\btv\('([^']+)'`).FindAllStringSubmatch(string(ui), -1) {
		for _, k := range []string{m[1], m[1] + "NoVersion"} {
			if _, ok := en[k]; !ok {
				t.Errorf("tv(%q): %s not in i18n/en.json", m[1], k)
			}
		}
	}
}

// Every English key is used somewhere (no dead text for translators).
func TestI18nNoUnusedKeys(t *testing.T) {
	en := i18nFiles(t)["en"]
	used := map[string]bool{}
	for _, u := range i18nUsedKeys(t, catalogAreas(en)) {
		used[u.key] = true
	}
	for k := range en {
		// (a platform's variant: used where its key is: i18nPlatformOverlay)
		base := strings.TrimPrefix(k, "linux.")
		if base != k {
			if _, ok := en[base]; !ok {
				t.Errorf("i18n/en.json: %s replaces %s, which does not exist", k, base)
			}
		}
		if m := i18nPluralRe.FindStringSubmatch(base); m != nil {
			base = m[1]
		}
		if used[base] || used[strings.TrimSuffix(base, "NoVersion")] {
			continue
		}
		family := false
		for _, f := range i18nKeyFamilies {
			family = family || strings.HasPrefix(k, f)
		}
		if !family {
			t.Errorf("i18n/en.json: %s is not used by the code", k)
		}
	}
}

// ── Runtime ─────────────────────────────────────────────────────────────────

func TestNormalizeAndResolveLang(t *testing.T) {
	cases := map[string]string{
		"fr": "fr", "FR": "fr", "fr-FR": "fr", "fr_CA": "fr", "pt-br": "pt-BR", "pt": "pt-BR", "pt-PT": "pt-BR",
		"zh": "zh-CN", "zh-TW": "zh-CN", "en-US": "en", "ar-SA": "ar", "auto": "", "": "", "nb-NO": "", "xx": "",
		" de ": "de", "uk-UA": "uk",
	}
	for in, want := range cases {
		if got := normalizeLang(in); got != want {
			t.Errorf("normalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct{ setting, account, system, want string }{
		{"", "", "", "en"},
		{"", "", "fr-FR", "fr"},
		{"", "de", "fr-FR", "de"},
		{"auto", "de", "fr-FR", "de"},
		{"it", "de", "fr-FR", "it"},
		{"", "xx", "nb-NO", "en"},
	} {
		if got := resolveLang(c.setting, c.account, c.system); got != c.want {
			t.Errorf("resolveLang(%q, %q, %q) = %q, want %q", c.setting, c.account, c.system, got, c.want)
		}
	}
	// The config: the setting, else the ACTIVE profile's account, else the system.
	was := systemUILanguage
	defer func() { systemUILanguage = was }()
	systemUILanguage = func() string { return "es-ES" }
	cfg := &Config{Profiles: []ServerProfile{{ID: "a", AccountLanguage: "de"}, {ID: "b"}}, ActiveID: "b"}
	if got := cfg.uiLang(); got != "es" {
		t.Errorf("no setting, account unknown: %q, want the system's es", got)
	}
	cfg.ActiveID = "a"
	if got := cfg.uiLang(); got != "de" {
		t.Errorf("active profile's account: %q, want de", got)
	}
	cfg.Language = "ja"
	if got := cfg.uiLang(); got != "ja" {
		t.Errorf("setting: %q, want ja", got)
	}
}

func TestPluralCategory(t *testing.T) {
	cases := []struct {
		lang string
		n    int
		want string
	}{
		{"en", 0, "other"}, {"en", 1, "one"}, {"en", 2, "other"},
		{"fr", 0, "one"}, {"fr", 1, "one"}, {"fr", 2, "other"}, {"fr", 1000000, "many"},
		{"pt-BR", 0, "one"}, {"es", 0, "other"}, {"es", 1, "one"},
		{"ja", 1, "other"}, {"zh-CN", 5, "other"},
		{"ru", 1, "one"}, {"ru", 21, "one"}, {"ru", 11, "many"}, {"ru", 3, "few"}, {"ru", 13, "many"}, {"ru", 24, "few"}, {"ru", 5, "many"}, {"ru", 0, "many"},
		{"uk", 22, "few"}, {"pl", 1, "one"}, {"pl", 21, "many"}, {"pl", 22, "few"}, {"pl", 12, "many"},
		{"cs", 1, "one"}, {"cs", 3, "few"}, {"cs", 5, "other"}, {"cs", 0, "other"},
		{"ar", 0, "zero"}, {"ar", 1, "one"}, {"ar", 2, "two"}, {"ar", 3, "few"}, {"ar", 10, "few"}, {"ar", 11, "many"}, {"ar", 99, "many"}, {"ar", 100, "other"}, {"ar", 103, "few"},
		{"tr", 1, "one"}, {"tr", 2, "other"},
	}
	for _, c := range cases {
		if got := pluralCategory(c.lang, c.n); got != c.want {
			t.Errorf("pluralCategory(%s, %d) = %s, want %s", c.lang, c.n, got, c.want)
		}
	}
}

func TestTrLang(t *testing.T) {
	i18nLoad()
	if got := trLang("fr", "update.err.folder", "error", "accès refusé"); got != "Le dossier de mise à jour n'a pas pu être créé : accès refusé." {
		t.Errorf("fr interpolation: %q", got)
	}
	if got := trLang("en", "status.readyDevices", "count", 1); got != "Ready — 1 device" {
		t.Errorf("en plural one: %q", got)
	}
	if got := trLang("en", "status.readyDevices", "count", 3); got != "Ready — 3 devices" {
		t.Errorf("en plural other: %q", got)
	}
	if got := trLang("fr", "status.readyDevices", "count", 0); got != "Prêt — 0 appareil" {
		t.Errorf("fr plural: 0 is singular in French: %q", got)
	}
	// A language without a catalog (or a key missing in it): English.
	if got := trLang("de", "update.err.noServer"); got != "No server is selected." && i18nCats["de"] == nil {
		t.Errorf("fallback to English: %q", got)
	}
	saved, had := i18nCats["fr"]["update.err.noServer"]
	delete(i18nCats["fr"], "update.err.noServer")
	if got := trLang("fr", "update.err.noServer"); got != "No server is selected." {
		t.Errorf("key missing in fr: English expected, got %q", got)
	}
	if had {
		i18nCats["fr"]["update.err.noServer"] = saved
	}
	if got := trLang("fr", "no.such.key"); got != "no.such.key" {
		t.Errorf("unknown key: the key itself, got %q", got)
	}
	if got := trLang("en", "update.err.folder"); got != "The update folder could not be created: {{error}}." {
		t.Errorf("missing parameter left as is: %q", got)
	}
	// tr follows the app's language.
	defer i18nSetCurrent(i18nCurrent())
	i18nSetCurrent("fr-FR")
	if got := tr("update.err.noServer"); got != "Aucun serveur n'est sélectionné." {
		t.Errorf("tr in fr: %q", got)
	}
	i18nSetCurrent("klingon") // ignored
	if i18nCurrent() != "fr" {
		t.Errorf("an unknown language must not change the current one")
	}
}

// ── The page ────────────────────────────────────────────────────────────────

var bootRe = regexp.MustCompile(`var I18N_BOOT = \{ lang: "([^"]+)", langs: (\[.*?\]), cats: \{(.*?)\} \};\n`)

func TestBuildUILanguages(t *testing.T) {
	was := systemUILanguage
	defer func() { systemUILanguage = was }()
	systemUILanguage = func() string { return "" }

	en := buildUI(&Config{Language: "en"})
	if !strings.Contains(en, `<html lang="en" dir="ltr">`) || strings.Contains(en, "{{t:") {
		t.Fatal("en page: lang/dir or a token left")
	}
	if !strings.Contains(en, `data-i18n="login.subtitle">Connect to your Obliance server</p>`) {
		t.Error("en page: the markup text in English with its key")
	}
	m := bootRe.FindStringSubmatch(en)
	if m == nil || m[1] != "en" || strings.Contains(m[3], `"fr":`) {
		t.Fatalf("en page: boot data %v", m != nil)
	}

	fr := buildUI(&Config{Language: "fr"})
	if !strings.Contains(fr, `<html lang="fr" dir="ltr">`) || !strings.Contains(fr, `>Connectez-vous à votre serveur Obliance</p>`) ||
		!strings.Contains(fr, `title="Réduire"`) {
		t.Error("fr page: markup in French")
	}
	m = bootRe.FindStringSubmatch(fr)
	if m == nil || m[1] != "fr" || !strings.Contains(m[3], `"en":{`) || !strings.Contains(m[3], `"fr":{`) {
		t.Fatal("fr page: English and French catalogs")
	}
	var langs []uiLanguage
	if err := json.Unmarshal([]byte(m[2]), &langs); err != nil || len(langs) != 18 {
		t.Fatalf("languages: %v %d", err, len(langs))
	}
	// Automatic, nothing known: English; the system's language otherwise.
	if !strings.Contains(buildUI(&Config{}), `<html lang="en" dir="ltr">`) {
		t.Error("automatic without account nor system: English")
	}
	systemUILanguage = func() string { return "fr-BE" }
	if !strings.Contains(buildUI(&Config{}), `<html lang="fr" dir="ltr">`) {
		t.Error("automatic: the Windows language")
	}
	// Arabic: right to left (no catalog yet: English texts meanwhile).
	ar := buildUI(&Config{Language: "ar"})
	if !strings.Contains(ar, `<html lang="ar" dir="rtl">`) || !strings.Contains(ar, `html[dir=rtl] .script-code`) {
		t.Error("ar page: dir=rtl and its CSS")
	}
	// Catalog text cannot end the script (json.Marshal escapes < > &).
	for _, page := range []string{en, fr} {
		boot := bootRe.FindString(page)
		if strings.Contains(boot, "</") || strings.Contains(boot, "<!--") {
			t.Error("boot data holds markup")
		}
	}
}

// ── /local/i18n and the switch ──────────────────────────────────────────────

func TestI18nEndpointAndSwitch(t *testing.T) {
	was := systemUILanguage
	defer func() { systemUILanguage = was }()
	systemUILanguage = func() string { return "en-US" }
	oldDir, oldFile := configDir, configFile
	configDir = t.TempDir()
	configFile = filepath.Join(configDir, "config.json")
	defer func() { configDir, configFile = oldDir, oldFile }()
	defer i18nSetCurrent(i18nCurrent())

	cfg := &Config{ServerURL: "https://a.example", Profiles: []ServerProfile{
		{ID: "pa", ServerURL: "https://a.example", Name: "a", SessionFile: "session.json"},
		{ID: "pb", ServerURL: "https://b.example", Name: "b", SessionFile: "session-pb.json", AccountLanguage: "de"},
	}, ActiveID: "pa"}
	p := newProxy(cfg, configDir, 0)
	var told []string
	p.setLangHook(func(code string) { told = append(told, code) })
	do := func(method, path, body string) (*httptest.ResponseRecorder, i18nState) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		p.handleI18n(rec, req)
		var st i18nState
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		return rec, st
	}

	rec, st := do(http.MethodGet, "/local/i18n", "")
	if rec.Code != 200 || st.Setting != "auto" || st.Lang != "en" || st.System != "en" || len(st.Langs) != 18 || !contains(st.Available, "fr") {
		t.Fatalf("GET: %d %+v", rec.Code, st)
	}
	// The account of the active server reports French: the app follows.
	_, st = do(http.MethodPost, "/local/i18n", `{"profile":"pa","account":"fr-FR"}`)
	if st.Lang != "fr" || st.Account != "fr" || cfg.Profiles[0].AccountLanguage != "fr" || i18nCurrent() != "fr" || strings.Join(told, ",") != "fr" {
		t.Fatalf("account fr: %+v told=%v current=%s", st, told, i18nCurrent())
	}
	// Reported again: nothing new said.
	do(http.MethodPost, "/local/i18n", `{"profile":"pa","account":"fr"}`)
	if len(told) != 1 {
		t.Errorf("same language: told again %v", told)
	}
	// Another server (its account: German).
	rec2 := httptest.NewRecorder()
	p.handleServers(rec2, httptest.NewRequest(http.MethodPost, "/local/servers/switch", strings.NewReader(`{"id":"pb"}`)))
	if rec2.Code != 200 || i18nCurrent() != "de" || told[len(told)-1] != "de" {
		t.Fatalf("server switch: %d current=%s told=%v", rec2.Code, i18nCurrent(), told)
	}
	// The setting wins over the account.
	_, st = do(http.MethodPost, "/local/i18n", `{"setting":"ja"}`)
	if st.Setting != "ja" || st.Lang != "ja" || cfg.Language != "ja" || i18nCurrent() != "ja" {
		t.Fatalf("setting ja: %+v", st)
	}
	saved, _ := os.ReadFile(configFile)
	if !strings.Contains(string(saved), `"language": "ja"`) || !strings.Contains(string(saved), `"accountLanguage": "fr"`) {
		t.Errorf("config.json keeps the setting and the accounts' languages: %s", saved)
	}
	_, st = do(http.MethodPost, "/local/i18n", `{"setting":"auto"}`)
	if st.Setting != "auto" || st.Lang != "de" || cfg.Language != "" {
		t.Fatalf("back to automatic: %+v", st)
	}
	// Refused: an unknown language, a bad body; an unknown profile is ignored.
	if rec, _ := do(http.MethodPost, "/local/i18n", `{"setting":"klingon"}`); rec.Code != 400 || cfg.Language != "" {
		t.Errorf("unknown language: %d", rec.Code)
	}
	if rec, _ := do(http.MethodPost, "/local/i18n", `not json`); rec.Code != 400 {
		t.Errorf("bad body: %d", rec.Code)
	}
	do(http.MethodPost, "/local/i18n", `{"profile":"zz","account":"it"}`)
	if cfg.Profiles[0].AccountLanguage != "fr" || cfg.Profiles[1].AccountLanguage != "de" {
		t.Error("an unknown profile changed an account's language")
	}
	// The catalogs.
	cat := httptest.NewRecorder()
	p.handleI18n(cat, httptest.NewRequest(http.MethodGet, "/local/i18n/catalog?lang=fr", nil))
	var fr map[string]string
	if cat.Code != 200 || json.Unmarshal(cat.Body.Bytes(), &fr) != nil || fr["tfa.verify"] != "Vérifier" {
		t.Fatalf("catalog fr: %d", cat.Code)
	}
	for _, q := range []string{"xx", "FR", "../en", ""} {
		r := httptest.NewRecorder()
		p.handleI18n(r, httptest.NewRequest(http.MethodGet, "/local/i18n/catalog?lang="+q, nil))
		if r.Code != 404 {
			t.Errorf("catalog %q: %d, want 404", q, r.Code)
		}
	}
}

// The SSO return pages speak the app's language.
func TestSsoPagesTranslated(t *testing.T) {
	defer i18nSetCurrent(i18nCurrent())
	i18nSetCurrent("fr")
	rec := httptest.NewRecorder()
	ssoFailedPage(rec, "<b>x</b>", `<i>"y"</i>`)
	body := rec.Body.String()
	if !strings.Contains(body, `<html lang="fr" dir="ltr">`) || !strings.Contains(body, "Échec du SSO") || !strings.Contains(body, "Retour à la connexion") ||
		strings.Contains(body, "<b>x</b>") || strings.Contains(body, "<i>") || !strings.Contains(body, "&lt;i&gt;&#34;y&#34;&lt;/i&gt;") {
		t.Errorf("SSO failed page: %s", body)
	}
	// No detail, or the same text: no second line.
	rec = httptest.NewRecorder()
	ssoFailedPage(rec, "same", "same")
	if strings.Count(rec.Body.String(), "same") != 1 {
		t.Errorf("SSO failed page repeats its text: %s", rec.Body.String())
	}

	// The server's refusals (its JSON {error}, in English): said in the app's
	// language, the server's words kept as the detail.
	cases := []struct {
		status      int
		body        string
		why, detail string
	}{
		{400, `{"success":false,"error":"Request expired"}`, "La demande de connexion a expiré. Veuillez réessayer depuis la page de connexion.", "Request expired"},
		{400, `{"success":false,"error":"Invalid or expired request"}`, "La demande de connexion a expiré. Veuillez réessayer depuis la page de connexion.", "Invalid or expired request"},
		{400, `{"success":false,"error":"State mismatch"}`, "La réponse de connexion ne correspond pas à la demande. Veuillez réessayer depuis la page de connexion.", "State mismatch"},
		{400, `{"success":false,"error":"Code exchange failed"}`, "Obligate n'a pas confirmé la connexion.", "Code exchange failed"},
		{500, `{"success":false,"error":"Internal error"}`, "Le serveur Obliance a refusé la connexion (HTTP 500).", "Internal error"},
		{502, `<html>Bad gateway</html>`, "Le serveur Obliance a refusé la connexion (HTTP 502).", ""},
	}
	for _, c := range cases {
		why, detail := ssoRefusalText(c.status, []byte(c.body))
		if why != c.why || detail != c.detail {
			t.Errorf("ssoRefusalText(%d, %s) = %q, %q; want %q, %q", c.status, c.body, why, detail, c.why, c.detail)
		}
	}
	long := `{"error":"` + strings.Repeat("é", 300) + `"}`
	if _, d := ssoRefusalText(400, []byte(long)); len([]rune(d)) != 200 || !strings.HasSuffix(d, "é") {
		t.Errorf("a long server error is cut at 200 characters (whole ones): %d", len([]rune(d)))
	}
	i18nSetCurrent("en")
	if why, _ := ssoRefusalText(400, []byte(`{"error":"Request expired"}`)); why != "The sign-in request expired. Try again from the sign-in page." {
		t.Errorf("English: %q", why)
	}
}
