package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoCodecDLLs lists the third-party DLL files of the repository: the
// agent folder, agent/dist (what the MSI ships) and the prebuilt packages.
func repoCodecDLLs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"..", filepath.Join("..", "dist")} {
		m, err := filepath.Glob(filepath.Join(dir, "*.dll"))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m...)
	}
	for _, root := range []string{filepath.Join("..", "..", "libvpx-prebuilt"), filepath.Join("..", "..", "svtav1-prebuilt")} {
		_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && strings.EqualFold(filepath.Ext(p), ".dll") {
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

// TestCodecDLLPinsMatchRepo: every DLL of the repository matches a pin of
// codec_pins.go (a DLL updated without its pin fails here) and every pin is
// the hash of a DLL of the repository (no stale pin).
func TestCodecDLLPinsMatchRepo(t *testing.T) {
	files := repoCodecDLLs(t)
	if len(files) < len(codecDLLPins) {
		t.Fatalf("found %d DLL files in the repository, expected at least %d (agent/dist)", len(files), len(codecDLLPins))
	}
	used := map[string]bool{}
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.ToLower(filepath.Base(p))
		if strings.HasPrefix(name, "libx265") {
			// GPL, never shipped (gitignored): a build host may hold a local copy.
			continue
		}
		ok, sum := codecDLLPinned(name, data)
		if !ok {
			t.Errorf("%s: sha256 %s is not pinned for %q in codec_pins.go: update the pin with the DLL", p, sum, name)
			continue
		}
		used[name+" "+sum] = true
	}
	for name, sums := range codecDLLPins {
		if name != strings.ToLower(name) {
			t.Errorf("pin name %q must be lower case", name)
		}
		for _, s := range sums {
			if !used[name+" "+s] {
				t.Errorf("pin %s %s matches no DLL of the repository (stale pin?)", name, s)
			}
		}
	}
}

func TestPEImportDLLs(t *testing.T) {
	cases := map[string][]string{
		"openh264-2.4.1-win64.dll": {"kernel32.dll"},
		"libvpx-1.dll":             {"kernel32.dll", "msvcrt.dll", "libwinpthread-1.dll"},
		"libwinpthread-1.dll":      {"kernel32.dll", "msvcrt.dll"},
	}
	for name, want := range cases {
		data, err := os.ReadFile(filepath.Join("..", "dist", name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := peImportDLLs(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s imports %v, want %v", name, got, want)
		}
	}
	if _, err := peImportDLLs([]byte("MZ not a PE")); err == nil {
		t.Error("garbage parsed as a PE")
	}
}

func TestCodecDLLPinnedRejectsOtherContent(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "dist", "openh264-2.4.1-win64.dll"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := codecDLLPinned("OpenH264-2.4.1-WIN64.dll", data); !ok {
		t.Error("genuine DLL refused (the name is case-insensitive)")
	}
	if ok, _ := codecDLLPinned("openh264.dll", data); ok {
		t.Error("genuine content accepted under an unpinned name")
	}
	bad := append([]byte(nil), data...)
	bad[len(bad)/2] ^= 1
	if ok, _ := codecDLLPinned("openh264-2.4.1-win64.dll", bad); ok {
		t.Error("modified DLL accepted")
	}
}
