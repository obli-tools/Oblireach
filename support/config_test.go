package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveConfigDefault(t *testing.T) {
	c, err := resolveConfig("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != DefaultServer || c.Source != "default" || c.ServerName != DefaultServerName {
		t.Fatalf("got %+v", c)
	}
	c, err = resolveConfig("", "")
	if err != nil || c.Server != DefaultServer {
		t.Fatalf("no exe dir: %+v %v", c, err)
	}
}

func TestResolveConfigFile(t *testing.T) {
	dir := writeConfig(t, "\xEF\xBB\xBF"+`{"server":"https://help.example.org","serverName":"Example IT‮"}`)
	c, err := resolveConfig("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "https://help.example.org" || c.Source != "file" || c.ServerName != "Example IT" {
		t.Fatalf("got %+v", c)
	}
	// A file without a server keeps the default but may name it.
	dir = writeConfig(t, `{"serverName":"Default"}`)
	if c, _ = resolveConfig("", dir); c.Server != DefaultServer || c.ServerName != "Default" {
		t.Fatalf("got %+v", c)
	}
	// Without a label, the public instance keeps its own name.
	dir = writeConfig(t, `{}`)
	if c, _ = resolveConfig("", dir); c.Server != DefaultServer || c.ServerName != DefaultServerName || c.Source != "default" {
		t.Fatalf("got %+v", c)
	}
}

func TestResolveConfigFlagWins(t *testing.T) {
	dir := writeConfig(t, `{"server":"help.example.org","serverName":"Example IT"}`)
	c, err := resolveConfig("  wss://other.example.net/v1/ws ", dir)
	if err != nil {
		t.Fatal(err)
	}
	// The product's label must not describe another server.
	if c.Server != "wss://other.example.net/v1/ws" || c.Source != "flag" || c.ServerName != "other.example.net" {
		t.Fatalf("got %+v", c)
	}
}

func TestResolveConfigErrors(t *testing.T) {
	for name, content := range map[string]string{
		"malformed": `{"server":`,
		"bad URL":   `{"server":"ftp://example.org"}`,
		"too large": `{"server":"x.example.org","pad":"` + strings.Repeat("a", maxConfigSize) + `"}`,
	} {
		if _, err := resolveConfig("", writeConfig(t, content)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, bad := range []string{"https://", "mailto:x@y", "://x"} {
		if _, err := resolveConfig(bad, ""); err == nil {
			t.Errorf("flag %q accepted", bad)
		}
	}
	for _, good := range []string{"support.example.com", "https://support.example.com", "ws://127.0.0.1:8080", "wss://a.example/v1/ws"} {
		if _, err := resolveConfig(good, ""); err != nil {
			t.Errorf("flag %q refused: %v", good, err)
		}
	}
}
