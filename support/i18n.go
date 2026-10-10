package main

// Translations. One flat JSON catalog per language, embedded from
// i18n/<code>.json (same format as the Desktop's catalogs): "area.name" keys,
// {{name}} parameters. Lookup: the chosen language, then English, then the
// key itself. A value left empty counts as missing.
//
// The language follows the Windows display language of the account
// (GetUserDefaultUILanguage) through a fallback chain (pt-PT -> pt -> pt-BR,
// zh-TW -> zh -> zh-CN, de-AT -> de...), English when no catalog matches;
// the person can change it on the first screen (kept in memory only).

import (
	"embed"
	"encoding/json"
	"strings"
)

//go:embed i18n/*.json
var i18nFS embed.FS

// languages with a catalog (English first: the reference), in the order of
// the language picker.
var languages = []string{
	"en", "ar", "cs", "da", "de", "es", "fr", "it", "ja",
	"ko", "nl", "pl", "pt-BR", "ru", "sv", "tr", "uk", "zh-CN",
}

// languageNames: each language in its own script (the picker).
var languageNames = map[string]string{
	"en": "English", "ar": "العربية", "cs": "Čeština", "da": "Dansk",
	"de": "Deutsch", "es": "Español", "fr": "Français", "it": "Italiano",
	"ja": "日本語", "ko": "한국어", "nl": "Nederlands", "pl": "Polski",
	"pt-BR": "Português (Brasil)", "ru": "Русский", "sv": "Svenska",
	"tr": "Türkçe", "uk": "Українська", "zh-CN": "简体中文",
}

// baseDefault: the catalog for a base language without its own one.
var baseDefault = map[string]string{"pt": "pt-BR", "zh": "zh-CN"}

// rtlLanguages are written right to left.
var rtlLanguages = map[string]bool{"ar": true}

var catalogs = loadCatalogs()

func loadCatalogs() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, l := range languages {
		b, err := i18nFS.ReadFile("i18n/" + l + ".json")
		if err != nil {
			panic("i18n: missing catalog " + l)
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			panic("i18n: bad catalog " + l + ": " + err.Error())
		}
		out[l] = m
	}
	return out
}

// pickLanguage maps a locale name ("fr-FR", "pt_BR", "zh-Hans-CN", "") to a
// catalog: the exact tag, then the base language, then the base language's
// default catalog, then English.
func pickLanguage(locale string) string {
	tag := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	if i := strings.IndexAny(tag, ".@"); i >= 0 { // POSIX "pt_BR.UTF-8"
		tag = tag[:i]
	}
	byLower := func(s string) string {
		for _, l := range languages {
			if strings.ToLower(l) == s {
				return l
			}
		}
		return ""
	}
	if l := byLower(tag); l != "" {
		return l
	}
	parts := strings.Split(tag, "-")
	// Drop a script subtag: zh-hans-cn -> zh-cn.
	if len(parts) == 3 && len(parts[1]) == 4 {
		if l := byLower(parts[0] + "-" + parts[2]); l != "" {
			return l
		}
	}
	if l := byLower(parts[0]); l != "" {
		return l
	}
	if l, ok := baseDefault[parts[0]]; ok {
		return l
	}
	return "en"
}

// Tr translates into one language.
type Tr struct{ lang string }

func newTr(locale string) Tr { return Tr{lang: pickLanguage(locale)} }

// Lang is the catalog in use.
func (t Tr) Lang() string { return t.lang }

// RTL: the language is written right to left.
func (t Tr) RTL() bool { return rtlLanguages[t.lang] }

// T returns the text of key with its {{name}} parameters replaced; params
// are name, value pairs.
func (t Tr) T(key string, params ...string) string {
	s := catalogs[t.lang][key]
	if s == "" {
		s = catalogs["en"][key]
	}
	if s == "" {
		s = key
	}
	for i := 0; i+1 < len(params); i += 2 {
		s = strings.ReplaceAll(s, "{{"+params[i]+"}}", params[i+1])
	}
	return s
}
