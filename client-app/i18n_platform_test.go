package main

import "testing"

// The catalog's linux.* texts: on Linux they replace their key, elsewhere
// the catalog is the file as it is.
func TestI18nPlatformOverlay(t *testing.T) {
	mk := func() map[string]string {
		return map[string]string{"vault.hello.setting": "Unlock with Windows Hello", "linux.vault.hello.setting": "Unlock with your fingerprint", "a.b": "c"}
	}
	w := mk()
	i18nPlatformOverlay(w, "windows")
	if len(w) != 3 || w["vault.hello.setting"] != "Unlock with Windows Hello" || w["a.b"] != "c" {
		t.Fatalf("windows: %v", w)
	}
	l := mk()
	i18nPlatformOverlay(l, "linux")
	if l["vault.hello.setting"] != "Unlock with your fingerprint" || l["a.b"] != "c" {
		t.Fatalf("linux: %v", l)
	}
	// Every linux.* text of the real catalogs replaces an existing key.
	en := i18nFiles(t)["en"]
	n := 0
	for k := range en {
		if len(k) > 6 && k[:6] == "linux." {
			n++
			if _, ok := en[k[6:]]; !ok {
				t.Errorf("%s replaces nothing", k)
			}
		}
	}
	if n == 0 {
		t.Fatal("no linux.* text")
	}
}
