//go:build windows || linux

package main

import (
	"testing"

	"github.com/oblireach/agent/engine"
)

// The engine gets exactly what the user accepted, and the hooks of Desktop.
func TestHelpEngineOptions(t *testing.T) {
	chat, file := 0, 0
	ev := helpEvents{OnChat: func(string) { chat++ }, FileReceived: func(string) { file++ }}
	o := helpEngineOptions(helpPerms{Control: true, Files: true}, ev)
	if !o.AllowInput || o.AllowClipboard || !o.AllowFiles || o.AllowBlankScreen || o.AllowInputBlock || o.AllowDisplayModes {
		t.Fatalf("%+v", o)
	}
	o.OnChat("x")
	o.OnFileReceived("y")
	if chat != 1 || file != 1 {
		t.Fatal("hooks")
	}
	old := helpPortalPermission
	defer func() { helpPortalPermission = old }()
	var store engine.PortalPermissionStore = fakePortalStore{}
	helpPortalPermission = func() engine.PortalPermissionStore { return store }
	if helpEngineOptions(helpPerms{}, ev).PortalPermission != store {
		t.Fatal("portal store not passed")
	}
}

type fakePortalStore struct{}

func (fakePortalStore) Load() (string, error) { return "", nil }
func (fakePortalStore) Save(string) error     { return nil }
