//go:build windows

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The bindings are reachable from any document the webview shows: every call
// must carry the per-run token, and files are only readable through handles.
func TestNativeBridgeRefusesCallsWithoutToken(t *testing.T) {
	b := &nativeBridge{token: "0123456789abcdef0123456789abcdef", files: map[string]fileHandle{}}
	if _, err := b.clipboardRead(""); err != errNotAllowed {
		t.Fatalf("clipboardRead without token: %v", err)
	}
	if _, err := b.clipboardWrite("nope", "x"); err != errNotAllowed {
		t.Fatalf("clipboardWrite with bad token: %v", err)
	}
	if _, err := b.clipboardFiles("0123456789abcdef0123456789abcdeF"); err != errNotAllowed {
		t.Fatalf("clipboardFiles with bad token: %v", err)
	}
	if _, err := b.readFileChunk("", "h", 0, 1); err != errNotAllowed {
		t.Fatalf("readFileChunk without token: %v", err)
	}
	if _, err := b.clipboardWatch("", true); err != errNotAllowed || b.watchOn.Load() {
		t.Fatalf("clipboardWatch without token: %v", err)
	}
}

func TestNativeBridgeFileChunks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	data := []byte("hello, remote session")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	tok := "0123456789abcdef0123456789abcdef"
	b := &nativeBridge{token: tok, files: map[string]fileHandle{
		"h1": {path: p, size: int64(len(data)), created: time.Now()},
	}}
	got, err := b.readFileChunk(tok, "h1", 7, 6)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := base64.StdEncoding.DecodeString(got); string(raw) != "remote" {
		t.Fatalf("chunk = %q", raw)
	}
	// Past the end: short read, no error.
	got, err = b.readFileChunk(tok, "h1", 15, 100)
	if raw, _ := base64.StdEncoding.DecodeString(got); err != nil || string(raw) != "ession" {
		t.Fatalf("tail chunk = %q, %v", raw, err)
	}
	for _, c := range []struct {
		h   string
		off int64
		n   int
	}{{"nope", 0, 1}, {"h1", -1, 1}, {"h1", 0, 0}, {"h1", 0, maxFileChunk + 1}, {"h1", 1000, 1}} {
		if _, err := b.readFileChunk(tok, c.h, c.off, c.n); err == nil {
			t.Fatalf("readFileChunk(%q, %d, %d) should fail", c.h, c.off, c.n)
		}
	}
	// Old handles expire.
	b.files["old"] = fileHandle{path: p, size: 1, created: time.Now().Add(-2 * fileHandleTTL)}
	b.pruneHandlesLocked(time.Now())
	if _, ok := b.files["old"]; ok {
		t.Fatal("expired handle kept")
	}
}
