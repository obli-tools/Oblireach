package supportlink

// The nt6 line (Go 1.20, pion/webrtc v3) has its own copy of the only two
// files that import pion. They must stay the same code as the modern ones:
// only the import paths may differ.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var pionImport = regexp.MustCompile(`"github\.com/pion/([a-z]+)(/v[0-9]+)?(/[a-z]+)?"`)

// codeAfterImports returns the file from its import block on, with the
// pion import paths stripped of their major version and line endings
// normalised.
func codeAfterImports(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	i := strings.Index(s, "\nimport (")
	if i < 0 {
		t.Fatalf("%s: no import block", name)
	}
	return pionImport.ReplaceAllString(s[i:], `"github.com/pion/$1$3"`)
}

func TestNT6FilesInStep(t *testing.T) {
	for _, p := range [][2]string{
		{"peer_webrtc.go", "peer_webrtc_nt6.go"},
		{"pion_v4.go", "pion_v3_nt6.go"},
	} {
		a, b := codeAfterImports(t, p[0]), codeAfterImports(t, p[1])
		if a != b {
			al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
			for i := 0; i < len(al) && i < len(bl); i++ {
				if al[i] != bl[i] {
					t.Fatalf("%s and %s differ at line %d of the code:\n%s\n%s", p[0], p[1], i+1, al[i], bl[i])
				}
			}
			t.Fatalf("%s and %s differ in length", p[0], p[1])
		}
	}
}
