package main

// Translations of the Desktop (every visible string translated). One flat
// JSON catalog per language, embedded from i18n/<code>.json
// (the 18 languages of the Obliance web app), used by BOTH sides:
//
//   - the page (ui.go, reach_viewer.go): T(key, params) for text,
//     TH(key, params) for HTML (the translation and every parameter escaped),
//     static markup through {{t:key}} tokens (replaced here when the page is
//     built) plus data-i18n* attributes (applied again at a language switch);
//   - Go (tr / trLang): the updater's messages shown in the banner, the SSO
//     callback pages, the start-up and session-window errors the page shows.
//
// Keys: "area.name" (flat). Parameters: {{name}}. Plurals: the parameter
// "count" selects key_<CLDR category> (key_one, key_few, key_other...),
// then key_other, then key: English has _one / _other; each language gives
// the categories its grammar needs (see pluralCategory), _other always.
// Fallback: the language on screen, then English, then the key itself. A
// catalog value left empty counts as missing (the English text shows).
//
// The language: the "Language" setting (Settings > Application; config.json
// "language", "" = automatic), else the preferred language of the Obliance
// account of the ACTIVE server profile (reported by the page after sign-in,
// kept per profile), else the Windows display language, else English. When
// it changes (the setting, a server switch, the account's language), every
// window of the app is told (__reachOnLang) and redraws itself in it; the
// Go side (tr) follows at once.

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

//go:embed i18n/*.json
var i18nFS embed.FS

// i18nPlatformOverlay: on Linux, a catalog's "linux.<key>" texts replace
// <key> (what differs there: the fingerprint instead of Windows Hello, the
// limits of "Receive help"...). Elsewhere the catalog stays as the file is
// (the linux.* texts are simply never asked for).
func i18nPlatformOverlay(m map[string]string, goos string) {
	if goos != "linux" {
		return
	}
	const pfx = "linux."
	for k, v := range m {
		if strings.HasPrefix(k, pfx) {
			m[k[len(pfx):]] = v
		}
	}
}

type uiLanguage struct {
	Code string `json:"code"`
	Name string `json:"name"` // in its own language
	RTL  bool   `json:"rtl,omitempty"`
}

// uiLanguages: the Obliance web app's languages, in its order.
var uiLanguages = []uiLanguage{
	{Code: "en", Name: "English"},
	{Code: "fr", Name: "Français"},
	{Code: "es", Name: "Español"},
	{Code: "de", Name: "Deutsch"},
	{Code: "pt-BR", Name: "Português (Brasil)"},
	{Code: "zh-CN", Name: "简体中文"},
	{Code: "ja", Name: "日本語"},
	{Code: "ko", Name: "한국어"},
	{Code: "ru", Name: "Русский"},
	{Code: "ar", Name: "العربية", RTL: true},
	{Code: "it", Name: "Italiano"},
	{Code: "nl", Name: "Nederlands"},
	{Code: "pl", Name: "Polski"},
	{Code: "tr", Name: "Türkçe"},
	{Code: "sv", Name: "Svenska"},
	{Code: "da", Name: "Dansk"},
	{Code: "cs", Name: "Čeština"},
	{Code: "uk", Name: "Українська"},
}

// normalizeLang maps a language tag (an account's preferredLanguage, the
// Windows display language, the setting) to one of uiLanguages: the same
// code (any case, "_" or "-"), else the same primary language ("fr-CA" ->
// "fr", "pt-PT" -> "pt-BR", "zh-TW" -> "zh-CN"). "" when none ("auto").
func normalizeLang(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "_", "-"))
	if s == "" || len(s) > 35 {
		return ""
	}
	for _, l := range uiLanguages {
		if strings.EqualFold(l.Code, s) {
			return l.Code
		}
	}
	primary := strings.ToLower(strings.SplitN(s, "-", 2)[0])
	for _, l := range uiLanguages {
		if strings.ToLower(strings.SplitN(l.Code, "-", 2)[0]) == primary {
			return l.Code
		}
	}
	return ""
}

func langRTL(code string) bool {
	for _, l := range uiLanguages {
		if l.Code == code {
			return l.RTL
		}
	}
	return false
}

// resolveLang: the setting (a language, or ""/"auto"), else the account's,
// else the system's, else English.
func resolveLang(setting, account, system string) string {
	for _, s := range []string{setting, account, system} {
		if l := normalizeLang(s); l != "" {
			return l
		}
	}
	return "en"
}

// systemUILanguage: the Windows display language ("fr-FR"; i18n_windows.go),
// "" when unknown. Replaced by the tests.
var systemUILanguage = func() string { return "" }

// uiLang: the language the app shows now, for this config.
func (c *Config) uiLang() string {
	account := ""
	if a := c.active(); a != nil {
		account = a.AccountLanguage
	}
	return resolveLang(c.Language, account, systemUILanguage())
}

// ── Catalogs ────────────────────────────────────────────────────────────────

var (
	i18nOnce sync.Once
	i18nCats map[string]map[string]string // code -> key -> text
	i18nJSON map[string][]byte            // code -> the catalog, compact and HTML-safe (for <script>)
)

func i18nLoad() {
	i18nOnce.Do(func() {
		i18nCats = map[string]map[string]string{}
		i18nJSON = map[string][]byte{}
		for _, l := range uiLanguages {
			data, err := i18nFS.ReadFile("i18n/" + l.Code + ".json")
			if err != nil {
				continue // not translated yet: English
			}
			var m map[string]string
			if err := json.Unmarshal(data, &m); err != nil {
				log.Printf("i18n: %s.json: %v", l.Code, err)
				continue
			}
			i18nPlatformOverlay(m, runtime.GOOS)
			i18nCats[l.Code] = m
			// json.Marshal escapes <, > and & (<...) and U+2028/2029:
			// safe inside the page's <script>.
			j, err := json.Marshal(m)
			if err != nil {
				continue
			}
			i18nJSON[l.Code] = j
		}
	})
}

// i18nAvailable: the languages that have a catalog.
func i18nAvailable() []string {
	i18nLoad()
	out := []string{}
	for _, l := range uiLanguages {
		if _, ok := i18nCats[l.Code]; ok {
			out = append(out, l.Code)
		}
	}
	return out
}

// pluralCategory: the CLDR plural category of the integer n in lang (the
// rules of the 18 languages for whole numbers; the page uses
// Intl.PluralRules, which gives the same).
func pluralCategory(lang string, n int) string {
	if n < 0 {
		n = -n
	}
	mod10, mod100 := n%10, n%100
	million := n != 0 && n%1000000 == 0
	switch lang {
	case "ja", "ko", "zh-CN":
		return "other"
	case "fr", "pt-BR":
		if n == 0 || n == 1 {
			return "one"
		}
		if million {
			return "many"
		}
		return "other"
	case "es", "it":
		if n == 1 {
			return "one"
		}
		if million {
			return "many"
		}
		return "other"
	case "ru", "uk":
		switch {
		case mod10 == 1 && mod100 != 11:
			return "one"
		case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
			return "few"
		default:
			return "many"
		}
	case "pl":
		switch {
		case n == 1:
			return "one"
		case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
			return "few"
		default:
			return "many"
		}
	case "cs":
		switch {
		case n == 1:
			return "one"
		case n >= 2 && n <= 4:
			return "few"
		default:
			return "other"
		}
	case "ar":
		switch {
		case n == 0:
			return "zero"
		case n == 1:
			return "one"
		case n == 2:
			return "two"
		case mod100 >= 3 && mod100 <= 10:
			return "few"
		case mod100 >= 11:
			return "many"
		default:
			return "other"
		}
	}
	// en, de, nl, sv, da, tr
	if n == 1 {
		return "one"
	}
	return "other"
}

func i18nLookup(lang, key string, count int, hasCount bool) (string, bool) {
	cat := i18nCats[lang]
	if cat == nil {
		return "", false
	}
	if hasCount {
		if v := cat[key+"_"+pluralCategory(lang, count)]; v != "" {
			return v, true
		}
		if v := cat[key+"_other"]; v != "" {
			return v, true
		}
	}
	v := cat[key]
	return v, v != ""
}

var i18nParamRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// trLang translates key into lang. kv: parameter names and values, in pairs
// ("error", err, "count", 3); a value is written with fmt.Sprint (an error:
// its message). Unknown parameters stay as they are ({{name}}).
func trLang(lang, key string, kv ...interface{}) string {
	i18nLoad()
	params := map[string]string{}
	count, hasCount := 0, false
	for i := 0; i+1 < len(kv); i += 2 {
		name, _ := kv[i].(string)
		if name == "" {
			continue
		}
		v := kv[i+1]
		if name == "count" {
			switch n := v.(type) {
			case int:
				count, hasCount = n, true
			case int64:
				count, hasCount = int(n), true
			}
		}
		switch x := v.(type) {
		case *i18nMsg: // a fragment: said in the same language
			params[name] = x.text(lang)
		case i18nMsger:
			params[name] = x.i18nMsg().text(lang)
		default:
			params[name] = fmt.Sprint(v)
		}
	}
	tpl, ok := i18nLookup(lang, key, count, hasCount)
	if !ok && lang != "en" {
		tpl, ok = i18nLookup("en", key, count, hasCount)
	}
	if !ok {
		tpl = key
	}
	return i18nParamRe.ReplaceAllStringFunc(tpl, func(m string) string {
		name := i18nParamRe.FindStringSubmatch(m)[1]
		if v, ok := params[name]; ok {
			return v
		}
		return m
	})
}

// The language of the Go side's texts (tr): the app's, set at start-up
// (main.go) and at every change (Proxy.syncLang).
var (
	i18nCurMu sync.RWMutex
	i18nCur   = "en"
)

func i18nSetCurrent(lang string) {
	if l := normalizeLang(lang); l != "" {
		i18nCurMu.Lock()
		i18nCur = l
		i18nCurMu.Unlock()
	}
}

func i18nCurrent() string {
	i18nCurMu.RLock()
	defer i18nCurMu.RUnlock()
	return i18nCur
}

// tr translates key into the app's language (see trLang).
func tr(key string, kv ...interface{}) string { return trLang(i18nCurrent(), key, kv...) }

// i18nMsg: a text by its key and parameters, for a message the page keeps
// on screen (the update banner): the page says it again in a new language
// (i18nMsgText there) instead of keeping the words of the old one. A
// parameter may be another i18nMsg (a fragment: update.trust.*).
type i18nMsg struct {
	Key    string                 `json:"key"`
	Params map[string]interface{} `json:"params,omitempty"`
}

// i18nMsger: an error that can say itself as an i18nMsg (trustError).
type i18nMsger interface{ i18nMsg() *i18nMsg }

// i18nM builds an i18nMsg from tr's pairs: a fragment stays a fragment, an
// error becomes its message, a number stays a number (plurals), anything
// else its text.
func i18nM(key string, kv ...interface{}) *i18nMsg {
	m := &i18nMsg{Key: key}
	for i := 0; i+1 < len(kv); i += 2 {
		name, _ := kv[i].(string)
		if name == "" {
			continue
		}
		if m.Params == nil {
			m.Params = map[string]interface{}{}
		}
		switch x := kv[i+1].(type) {
		case *i18nMsg:
			m.Params[name] = x
		case i18nMsger:
			m.Params[name] = x.i18nMsg()
		case error:
			m.Params[name] = x.Error()
		case int, int64, uint32, float64:
			m.Params[name] = x
		default:
			m.Params[name] = fmt.Sprint(x)
		}
	}
	return m
}

// text: the message in lang.
func (m *i18nMsg) text(lang string) string {
	if m == nil {
		return ""
	}
	kv := make([]interface{}, 0, 2*len(m.Params))
	for k, v := range m.Params {
		kv = append(kv, k, v)
	}
	return trLang(lang, m.Key, kv...)
}

// ── The page ────────────────────────────────────────────────────────────────

// {{t:key}} in the page's markup (ui.go): replaced by the escaped text when
// the page is built, so it appears in its language at once.
var i18nTokenRe = regexp.MustCompile(`\{\{t:([A-Za-z0-9_.\-]+)\}\}`)

func i18nMarkup(html, lang string) string {
	return i18nTokenRe.ReplaceAllStringFunc(html, func(m string) string {
		return esc(trLang(lang, i18nTokenRe.FindStringSubmatch(m)[1]))
	})
}

// i18nPageJS: the runtime of the page (first script): the languages, the
// English catalog and the one on screen, T() / TH().
func i18nPageJS(lang string) string {
	i18nLoad()
	langs, _ := json.Marshal(uiLanguages)
	code, _ := json.Marshal(lang)
	cats := `"en":` + string(orEmptyObject(i18nJSON["en"]))
	if lang != "en" {
		if j, ok := i18nJSON[lang]; ok {
			cats += "," + string(code) + ":" + string(j)
		}
	}
	return "var I18N_BOOT = { lang: " + string(code) + ", langs: " + string(langs) + ", cats: {" + cats + "} };\n" + i18nRuntimeJS
}

func orEmptyObject(j []byte) []byte {
	if len(j) == 0 {
		return []byte("{}")
	}
	return j
}

// i18nRuntimeJS: T / TH, the static markup (data-i18n, data-i18n-title,
// data-i18n-ph for a placeholder, data-i18n-aria for an aria-label), the
// switch (i18nUse: the catalog fetched from /local/i18n/catalog, the document
// lang / dir, then I18N.onChange redraws the app). No backtick (Go raw
// string), injected verbatim (not through Sprintf).
const i18nRuntimeJS = `
var I18N = (function (b) {
  b = b || {};
  return { lang: b.lang || 'en', langs: Array.isArray(b.langs) ? b.langs : [], cats: b.cats || { en: {} }, seq: 0, onChange: null };
})(typeof I18N_BOOT !== 'undefined' ? I18N_BOOT : null);
// Same escaping as the page's esc() (attributes are double-quoted).
function i18nEsc(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
function i18nPlural(lang, n) {
  try { if (typeof Intl !== 'undefined' && Intl.PluralRules) return new Intl.PluralRules(lang).select(n); } catch (e) {}
  return n === 1 ? 'one' : 'other';
}
function i18nLookup(lang, key, params) {
  var cat = I18N.cats[lang];
  if (!cat) return undefined;
  if (params && typeof params.count === 'number' && isFinite(params.count)) {
    var p = cat[key + '_' + i18nPlural(lang, params.count)];
    if (typeof p !== 'string' || !p) p = cat[key + '_other'];
    if (typeof p === 'string' && p) return p;
  }
  var v = cat[key];
  return (typeof v === 'string' && v) ? v : undefined;
}
// The text of key: the language on screen, else English, else the key.
function i18nText(key, params) {
  var v = i18nLookup(I18N.lang, key, params);
  if (v === undefined && I18N.lang !== 'en') v = i18nLookup('en', key, params);
  return v === undefined ? String(key) : v;
}
// i18nHtml(markup): a parameter TH() inserts as it is (markup the caller
// built, its own values escaped). Only what this function made counts: any
// other value (an object from a server's JSON included, {html: ...} or not)
// is text, escaped.
var i18nHtml = (function () {
  var made = typeof WeakSet === 'function' ? new WeakSet() : null;
  function mark(s) {
    var o = { html: String(s === null || s === undefined ? '' : s) };
    if (made) made.add(o);
    return Object.freeze(o);
  }
  mark.is = function (v) { return !!made && !!v && typeof v === 'object' && made.has(v); };
  return mark;
})();
// {{name}} -> params.name. html: the value escaped, except an i18nHtml one
// taken as it is.
function i18nFill(tpl, params, html) {
  return tpl.replace(/\{\{\s*([A-Za-z0-9_]+)\s*\}\}/g, function (m, name) {
    if (!params || !Object.prototype.hasOwnProperty.call(params, name)) return m;
    var v = params[name];
    if (i18nHtml.is(v)) return html ? v.html : v.html.replace(/<[^>]*>/g, '');
    v = (v === null || v === undefined) ? '' : String(v);
    return html ? i18nEsc(v) : v;
  });
}
// Text (textContent, title, confirm...).
function T(key, params) { return i18nFill(i18nText(key, params), params, false); }
// HTML (innerHTML): the translation and the parameters escaped.
function TH(key, params) { return i18nFill(i18nEsc(i18nText(key, params)), params, true); }
function i18nLang() { return I18N.lang; }
// A text the app sent by its key (i18n.go i18nMsg: {key, params}, a
// parameter may be another one), said in the language on screen; '' if m is
// not one.
function i18nMsgText(m, depth) {
  if (!m || typeof m !== 'object' || typeof m.key !== 'string' || !m.key || (depth | 0) > 3) return '';
  var p = {}, src = (m.params && typeof m.params === 'object') ? m.params : {};
  for (var k in src) {
    if (!Object.prototype.hasOwnProperty.call(src, k)) continue;
    var v = src[k];
    p[k] = (v && typeof v === 'object') ? i18nMsgText(v, (depth | 0) + 1) : v;
  }
  return T(m.key, p);
}
// A number as the language on screen writes it (decimal separator, digit
// grouping): at most maxFrac decimals (default 1), at least minFrac (0).
function i18nNum(n, maxFrac, minFrac) {
  n = Number(n);
  if (!isFinite(n)) n = 0;
  var mx = typeof maxFrac === 'number' ? maxFrac : 1, mn = typeof minFrac === 'number' ? Math.min(minFrac, mx) : 0;
  try {
    if (typeof Intl !== 'undefined' && Intl.NumberFormat) return new Intl.NumberFormat(I18N.lang, { maximumFractionDigits: mx, minimumFractionDigits: mn }).format(n);
  } catch (e) {}
  var f = Math.pow(10, mx);
  return String(Math.round(n * f) / f);
}
function i18nIsRtl(code) {
  for (var i = 0; i < I18N.langs.length; i++) if (I18N.langs[i].code === code) return !!I18N.langs[i].rtl;
  return false;
}
// A language tag -> one of I18N.langs (same rules as i18n.go normalizeLang), '' if none.
function i18nNormalize(s) {
  s = String(s || '').trim().replace(/_/g, '-');
  if (!s) return '';
  var i, low = s.toLowerCase();
  for (i = 0; i < I18N.langs.length; i++) if (I18N.langs[i].code.toLowerCase() === low) return I18N.langs[i].code;
  var p = low.split('-')[0];
  for (i = 0; i < I18N.langs.length; i++) if (I18N.langs[i].code.toLowerCase().split('-')[0] === p) return I18N.langs[i].code;
  return '';
}
// The page's static texts (markup built with data-i18n attributes).
function i18nApply(root) {
  root = root || (typeof document !== 'undefined' ? document : null);
  if (!root || typeof root.querySelectorAll !== 'function') return;
  var els;
  try { els = root.querySelectorAll('[data-i18n],[data-i18n-title],[data-i18n-ph],[data-i18n-aria]'); } catch (e) { return; }
  for (var i = 0; i < els.length; i++) {
    var el = els[i], k;
    if (!el || typeof el.getAttribute !== 'function') continue;
    if ((k = el.getAttribute('data-i18n'))) el.textContent = T(k);
    if ((k = el.getAttribute('data-i18n-title'))) el.title = T(k);
    if ((k = el.getAttribute('data-i18n-ph'))) el.placeholder = T(k);
    if ((k = el.getAttribute('data-i18n-aria')) && typeof el.setAttribute === 'function') el.setAttribute('aria-label', T(k));
  }
}
function i18nDocLang() {
  var de = (typeof document !== 'undefined' && document) ? document.documentElement : null;
  if (!de) return;
  try { de.lang = I18N.lang; de.dir = i18nIsRtl(I18N.lang) ? 'rtl' : 'ltr'; } catch (e) {}
}
// Switches to code, whose catalog is loaded (none: English texts).
function i18nSet(code) {
  I18N.lang = code;
  i18nDocLang();
  i18nApply();
  if (typeof I18N.onChange === 'function') { try { I18N.onChange(code); } catch (e) { console.warn('i18n: redraw failed', e); } }
}
// Makes code the page's language: its catalog fetched once (a language
// without one yet shows English), then i18nSet. The latest call wins; a call
// for the language a switch is already on its way to joins that switch (the
// app's broadcast and the Settings page's own call, in either order: one
// fetch, and both resume once the language is on). Resolves true when the
// language changed.
function i18nUse(code) {
  code = i18nNormalize(code);
  if (!code) return Promise.resolve(false);
  var f = I18N.inflight;
  if (f && f.code === code && f.seq === I18N.seq) return f.p;
  var seq = ++I18N.seq;
  // The promise exists before the switch starts: a call made meanwhile (even
  // from within it: the fetch, a redraw) joins it.
  var settle = null;
  var entry = { code: code, seq: seq, p: new Promise(function (res, rej) { settle = { res: res, rej: rej }; }) };
  I18N.inflight = entry;
  var done = function () { if (I18N.inflight === entry) I18N.inflight = null; };
  i18nSwitch(code, seq).then(function (r) { done(); settle.res(r); }, function (e) { done(); settle.rej(e); });
  return entry.p;
}
async function i18nSwitch(code, seq) {
  if (code === I18N.lang) return false;
  if (!I18N.cats[code]) {
    var cat = null, missing = false;
    try {
      var r = await fetch('/local/i18n/catalog?lang=' + encodeURIComponent(code));
      if (r && r.ok) cat = await r.json();
      else if (r && r.status === 404) missing = true;
    } catch (e) {}
    if (seq !== I18N.seq) return false; // another switch meanwhile
    if (cat && typeof cat === 'object') I18N.cats[code] = cat;
    else if (missing) I18N.cats[code] = {};
    else return false; // not reachable now: stays as it is
  }
  if (seq !== I18N.seq || code === I18N.lang) return false;
  i18nSet(code);
  return true;
}
// The app says the language changed (Settings, a server switch, the
// account's language: Proxy.syncLang), in every window.
window.__reachOnLang = function (code) { return i18nUse(code); };
`

// ── /local/i18n ─────────────────────────────────────────────────────────────

type i18nState struct {
	Setting   string       `json:"setting"`   // "auto" or a language
	Lang      string       `json:"lang"`      // the language the app shows
	Account   string       `json:"account"`   // the active server account's ("" unknown)
	System    string       `json:"system"`    // the Windows display language ("" none)
	Langs     []uiLanguage `json:"langs"`     // every language (own names)
	Available []string     `json:"available"` // those with a catalog
}

func (p *Proxy) i18nStateLocked() i18nState {
	st := i18nState{Setting: "auto", Lang: p.cfg.uiLang(), System: normalizeLang(systemUILanguage()),
		Langs: uiLanguages, Available: i18nAvailable()}
	if l := normalizeLang(p.cfg.Language); l != "" {
		st.Setting = l
	}
	if a := p.cfg.active(); a != nil {
		st.Account = normalizeLang(a.AccountLanguage)
	}
	return st
}

// /local/i18n          GET  -> i18nState
//
//	POST {setting?: "auto"|code, profile?: id, account?: code|""}
//	     setting: the Settings choice; account: the preferred language
//	     of the Obliance account signed in on that server profile (the
//	     active one when profile is "").
//
// /local/i18n/catalog  GET ?lang=<code> -> that language's catalog (404: none)
func (p *Proxy) handleI18n(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/local/i18n/catalog" {
		i18nLoad()
		code := normalizeLang(r.URL.Query().Get("lang"))
		j, ok := i18nJSON[code]
		if code == "" || code != r.URL.Query().Get("lang") || !ok {
			http.Error(w, `{"error":"no catalog"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(j)
		return
	}
	if r.URL.Path != "/local/i18n" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodPost {
		var body struct {
			Setting *string `json:"setting"`
			Profile string  `json:"profile"`
			Account *string `json:"account"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		changed := false
		if body.Setting != nil {
			l := normalizeLang(*body.Setting)
			if l == "" && *body.Setting != "auto" && *body.Setting != "" {
				p.mu.Unlock()
				http.Error(w, `{"error":"unknown language"}`, http.StatusBadRequest)
				return
			}
			if p.cfg.Language != l {
				p.cfg.Language, changed = l, true
			}
		}
		if body.Account != nil {
			sp := p.cfg.active()
			if body.Profile != "" {
				sp = p.cfg.profileByID(body.Profile)
			}
			if a := normalizeLang(*body.Account); sp != nil && sp.AccountLanguage != a {
				sp.AccountLanguage, changed = a, true
			}
		}
		if changed {
			saveConfig(p.cfg)
		}
		p.mu.Unlock()
		p.syncLang()
	} else if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET or POST"}`, http.StatusMethodNotAllowed)
		return
	}
	p.mu.RLock()
	st := p.i18nStateLocked()
	p.mu.RUnlock()
	_ = json.NewEncoder(w).Encode(st)
}

// syncLang: after anything that may change the language (the setting, a
// server switch, an account's language): the Go side follows, and every
// window is told (onLang: main.go, the windows' bridges).
func (p *Proxy) syncLang() {
	p.mu.RLock()
	lang := p.cfg.uiLang()
	p.mu.RUnlock()
	p.langMu.Lock()
	// "" : not known yet (a Proxy not made by newProxy): noted, nothing told.
	changed := p.lang != "" && lang != p.lang
	p.lang = lang
	hook := p.onLang
	p.langMu.Unlock()
	if !changed {
		return
	}
	i18nSetCurrent(lang)
	if hook != nil {
		hook(lang)
	}
}

// htmlLangAttrs: the lang / dir attributes of an HTML document in lang.
func htmlLangAttrs(lang string) string {
	dir := "ltr"
	if langRTL(lang) {
		dir = "rtl"
	}
	return `lang="` + esc(lang) + `" dir="` + dir + `"`
}
