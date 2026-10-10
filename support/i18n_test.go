package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every key exists, non-empty, in every catalog, with the same parameters.
func TestCatalogsComplete(t *testing.T) {
	param := regexp.MustCompile(`\{\{(\w+)\}\}`)
	ref := catalogs["en"]
	for _, l := range languages {
		c := catalogs[l]
		for k, v := range ref {
			got, ok := c[k]
			if !ok || strings.TrimSpace(got) == "" {
				t.Errorf("%s: missing %q", l, k)
				continue
			}
			want := param.FindAllString(v, -1)
			have := param.FindAllString(got, -1)
			sort.Strings(want)
			sort.Strings(have)
			if strings.Join(want, ",") != strings.Join(have, ",") {
				t.Errorf("%s: %q parameters %v, English has %v", l, k, have, want)
			}
		}
		for k := range c {
			if _, ok := ref[k]; !ok {
				t.Errorf("%s: %q is not in the English catalog", l, k)
			}
		}
	}
}

// Every key the code uses exists (literal t("...") / T("...") calls, and
// the status notices built from Notice values).
func TestCatalogCoversCode(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			name := ""
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			if name != "t" && name != "T" {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if k, err := strconv.Unquote(lit.Value); err == nil {
					used[k] = true
				}
			}
			return true
		})
	}
	for _, n := range []Notice{NoticeLocked, NoticeFailed, NoticeRefused, NoticeTimedOut, NoticeWithdrawn, NoticeSetupFailed} {
		used["status."+string(n)] = true
	}
	if len(used) < 20 {
		t.Fatalf("only %d keys found in the code: the scan is broken", len(used))
	}
	for k := range used {
		if _, ok := catalogs["en"][k]; !ok {
			t.Errorf("key %q used in the code but missing from the catalogs", k)
		}
	}
}

func TestPickLanguageAndT(t *testing.T) {
	for in, want := range map[string]string{
		"fr-FR": "fr", "fr_CA": "fr", "FR": "fr", "en-US": "en", "de-DE": "de", "de-AT": "de", "": "en",
		"pt-BR": "pt-BR", "pt_br": "pt-BR", "pt-PT": "pt-BR", "pt": "pt-BR", "pt_BR.UTF-8": "pt-BR",
		"zh-CN": "zh-CN", "zh-Hans-CN": "zh-CN", "zh-TW": "zh-CN", "zh-Hant-TW": "zh-CN", "zh": "zh-CN",
		"ja-JP": "ja", "ko-KR": "ko", "ar-SA": "ar", "ar-MA": "ar", "uk-UA": "uk", "ru-RU": "ru",
		"sv-SE": "sv", "nb-NO": "en", "xx": "en", "es-419": "es", "sr-Latn-RS": "en",
	} {
		if got := pickLanguage(in); got != want {
			t.Errorf("pickLanguage(%q) = %q, want %q", in, got, want)
		}
	}
	fr := newTr("fr-FR")
	if got := fr.T("main.server", "name", "x.example"); got != "Serveur : x.example" {
		t.Errorf("fr T = %q", got)
	}
	if got := newTr("en").T("no.such.key"); got != "no.such.key" {
		t.Errorf("missing key = %q", got)
	}
}

// Every language has its catalog and a native name, and only Arabic is
// right to left.
func TestLanguages(t *testing.T) {
	if len(languages) != 18 || languages[0] != "en" {
		t.Fatalf("languages: %v", languages)
	}
	files, _ := i18nFS.ReadDir("i18n")
	if len(files) != len(languages) {
		t.Errorf("%d catalog files for %d languages", len(files), len(languages))
	}
	for _, l := range languages {
		if languageNames[l] == "" {
			t.Errorf("%s has no native name", l)
		}
		if newTr(l).Lang() != l {
			t.Errorf("newTr(%q) = %q", l, newTr(l).Lang())
		}
		if newTr(l).RTL() != (l == "ar") {
			t.Errorf("%s RTL = %v", l, newTr(l).RTL())
		}
	}
}

// No catalog value carries control characters or a stray placeholder
// syntax, and no translation is left identical to the English text for a
// key that clearly needs translating (sentences).
func TestCatalogValuesClean(t *testing.T) {
	valid := regexp.MustCompile(`\{\{\w+\}\}`)
	for _, l := range languages {
		for k, v := range catalogs[l] {
			for _, r := range v {
				if r < 0x20 || (r >= 0x7f && r < 0xa0) {
					t.Errorf("%s %q: control character %U", l, k, r)
				}
			}
			if strings.ContainsAny(valid.ReplaceAllString(v, ""), "{}") {
				t.Errorf("%s %q: malformed placeholder: %q", l, k, v)
			}
			if l != "en" && v == catalogs["en"][k] && len(strings.Fields(v)) >= 4 {
				t.Errorf("%s %q: left in English: %q", l, k, v)
			}
		}
	}
}

// French texts use "vous", never "tu" / "ton" / "ta" / "tes" / "toi".
func TestFrenchNoTutoiement(t *testing.T) {
	word := regexp.MustCompile(`(?i)\b(tu|ton|ta|tes|toi|te|t')\b`)
	for k, v := range catalogs["fr"] {
		if m := word.FindString(v); m != "" {
			t.Errorf("fr %q uses %q: %q", k, m, v)
		}
	}
}

// Formal register where the language has one: no informal pronoun or
// possessive (whole words, any case).
func TestFormalRegister(t *testing.T) {
	informal := map[string][]string{
		"fr":    {"tu", "toi", "ton", "ta", "tes", "te"},
		"de":    {"du", "dich", "dir", "dein", "deine", "deinen", "deinem", "deiner", "deines"},
		"es":    {"tú", "tu", "tus", "ti", "contigo", "te"},
		"it":    {"tu", "ti", "tuo", "tua", "tuoi", "tue", "te"},
		"nl":    {"je", "jij", "jou", "jouw"},
		"pt-BR": {"tu", "teu", "tua", "teus", "tuas", "contigo"},
		"cs":    {"ty", "tebe", "tobě", "tebou", "tvůj", "tvoje", "tvá", "tvé", "tvého"},
		"pl":    {"ty", "ciebie", "cię", "tobie", "ci", "twój", "twoja", "twoje", "twojego"},
		"ru":    {"ты", "тебя", "тебе", "тобой", "твой", "твоя", "твоё", "твое", "твои", "твоего"},
		"uk":    {"ти", "тебе", "тобі", "тобою", "твій", "твоя", "твоє", "твої", "твого"},
		"tr":    {"sen", "seni", "sana", "senin", "sende", "senden"},
	}
	for l, words := range informal {
		re := regexp.MustCompile(`(?i)(^|[^\p{L}])(` + strings.Join(words, "|") + `)([^\p{L}]|$)`)
		if !re.MatchString("Ok, "+strings.ToUpper(words[0])+".") || re.MatchString("x"+words[0]+"x") {
			t.Fatalf("%s: the informal-word pattern does not work", l)
		}
		for k, v := range catalogs[l] {
			if m := re.FindStringSubmatch(v); m != nil {
				t.Errorf("%s %q uses the informal %q: %q", l, k, m[2], v)
			}
		}
	}
	// Chinese: the polite 您, never 你.
	for k, v := range catalogs["zh-CN"] {
		if strings.Contains(v, "你") {
			t.Errorf("zh-CN %q uses 你: %q", k, v)
		}
	}
}
