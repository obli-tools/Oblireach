//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oblireach/agent/engine"
)

// The screen sharing permission: asked every time by default; remembered
// in the app's data folder (0600) once turned on; forgotten when turned off.
// The engine's options carry it (receive_help_link.go helpEngineOptions).
func TestHelpLinuxPortalPermission(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	c := newHelpLinuxCfg(cfgDir, dataDir)
	old := helpLinux
	helpLinux = c
	defer func() { helpLinux = old }()

	o := helpEngineOptions(helpPerms{Control: true, Clipboard: true}, helpEvents{})
	if !o.AllowInput || !o.AllowClipboard || o.AllowFiles || o.PortalPermission != nil {
		t.Fatalf("default options %+v", o)
	}
	if err := c.setRemember(true); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(cfgDir, helpLinuxSettingsFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("settings file %v %v", st, err)
	}
	o = helpEngineOptions(helpPerms{}, helpEvents{})
	ps, ok := o.PortalPermission.(*engine.FilePortalStore)
	if !ok || ps.Path != filepath.Join(dataDir, helpPortalFile) {
		t.Fatalf("store %#v", o.PortalPermission)
	}
	if err := ps.Save("restore-token"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(ps.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %v", st.Mode())
	}
	// (a new run reads the setting back)
	if c2 := newHelpLinuxCfg(cfgDir, dataDir); !c2.view().RememberPortal || c2.portalStore() == nil {
		t.Fatal("setting not kept")
	}
	if err := c.setRemember(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ps.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the kept permission was not forgotten")
	}
	if helpEngineOptions(helpPerms{}, helpEvents{}).PortalPermission != nil {
		t.Fatal("still remembered")
	}
	// No data folder: nothing can be kept.
	if v := newHelpLinuxCfg(cfgDir, "").view(); v.CanRemember {
		t.Fatal("canRemember without a folder")
	}
}

// OpenH264 on demand: outside the Flatpak only, once at a time; Cisco's
// notice always given; the page's requests.
func TestHelpLinuxOpenH264AndHTTP(t *testing.T) {
	t.Setenv("OBLIREACH_FAKE_FLATPAK", "")
	c := newHelpLinuxCfg(t.TempDir(), t.TempDir())
	installed := false
	release := make(chan struct{})
	c.installed = func() bool { return installed }
	c.fetchFn = func(ctx context.Context, dir string) (string, error) {
		<-release
		return "", errors.New("hash mismatch")
	}
	v := c.view()
	if v.Notice != engine.OpenH264Notice || !strings.Contains(v.Notice, "Cisco") {
		t.Fatalf("notice %q", v.Notice)
	}
	if _, err := os.Stat("/.flatpak-info"); err == nil {
		t.Skip("running inside a Flatpak")
	}
	if !v.CanFetch || v.OpenH264 {
		t.Fatalf("view %+v", v)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.serveHTTP(w, r, strings.TrimPrefix(r.URL.Path, "/")) {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	post := func(path, body string) helpLinuxView {
		r, err := http.Post(srv.URL+"/"+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var out helpLinuxView
		json.NewDecoder(r.Body).Decode(&out)
		return out
	}
	if out := post("openh264", "{}"); !out.Fetching {
		t.Fatalf("not fetching %+v", out)
	}
	if c.fetch() {
		t.Fatal("a second download started")
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for c.view().Fetching && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if v := c.view(); v.Fetching || v.FetchError != "failed" {
		t.Fatalf("after %+v", v)
	}
	if out := post("linux", `{"rememberScreenShare":true}`); !out.RememberPortal {
		t.Fatalf("remember %+v", out)
	}
	if r, _ := http.Get(srv.URL + "/nothing"); r.StatusCode != 404 {
		t.Fatal("unknown path answered")
	}
	// Inside the Flatpak the runtime's extension provides it: no download.
	t.Setenv("OBLIREACH_FAKE_FLATPAK", "1")
	if c.view().CanFetch || c.fetch() {
		t.Fatal("download offered inside the Flatpak")
	}
}
