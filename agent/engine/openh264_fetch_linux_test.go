//go:build linux

package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOpenH264BZ2 is bzip2 of "\x7fELF fake openh264 library" (Go only
// decompresses bzip2).
const fakeOpenH264BZ2 = "425a683931415926535906761fb000000c9d80400015000304336dd020a000228681a0c8da4281a686464c4a91ead6733d84508cde259014cbfc5dc914e1424019d87ec0"

func TestFetchOpenH264VerifiesPins(t *testing.T) {
	lib := []byte("\x7fELF fake openh264 library")
	bz, _ := hex.DecodeString(fakeOpenH264BZ2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bz) }))
	defer srv.Close()

	saved := openH264LinuxPins
	defer func() { openH264LinuxPins = saved }()
	set := func(bzSHA, soSHA string) {
		openH264LinuxPins = map[string]openH264Pin{"testarch": {URL: srv.URL + "/lib.bz2", File: "libtest.so", BZ2SHA: bzSHA, SOSHA: soSHA}}
	}

	dir := t.TempDir()
	set(strings.Repeat("0", 64), sha256Hex(lib))
	if _, err := fetchOpenH264(context.Background(), dir, srv.Client(), "testarch"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a wrong download hash must be refused: %v", err)
	}
	set(sha256Hex(bz), strings.Repeat("1", 64))
	if _, err := fetchOpenH264(context.Background(), dir, srv.Client(), "testarch"); err == nil {
		t.Fatal("a wrong library hash must be refused")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("nothing may be installed after a refusal: %v", entries)
	}
	set(sha256Hex(bz), sha256Hex(lib))
	p, err := fetchOpenH264(context.Background(), dir, srv.Client(), "testarch")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, lib) || filepath.Base(p) != "libtest.so" {
		t.Errorf("installed %s = %q", p, got)
	}
	if err := verifyFileSHA256(p, sha256Hex(lib)); err != nil {
		t.Error(err)
	}
	// A tampered file is refused before it is loaded.
	if err := os.WriteFile(p, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyFileSHA256(p, sha256Hex(lib)); err == nil {
		t.Error("tampered library accepted")
	}
	if _, err := fetchOpenH264(context.Background(), dir, srv.Client(), "nosucharch"); err == nil {
		t.Error("an unpinned architecture must be refused")
	}
}

func TestOpenH264Candidates(t *testing.T) {
	dir := t.TempDir()
	SetOpenH264Dir(dir)
	defer SetOpenH264Dir("")
	c := openH264Candidates()
	if len(c) == 0 || c[len(c)-1].path != "libopenh264.so.7" || c[len(c)-1].verify != nil {
		t.Fatalf("the system soname must be the last candidate: %+v", c)
	}
	pin, ok := openH264LinuxPins["amd64"]
	if !ok || len(pin.SOSHA) != 64 || len(pin.BZ2SHA) != 64 || !strings.HasPrefix(pin.URL, "https://ciscobinary.openh264.org/") {
		t.Fatalf("amd64 pin = %+v", pin)
	}
	if !strings.Contains(OpenH264Notice, "Cisco Systems") {
		t.Error("the license notice must name Cisco")
	}
}
