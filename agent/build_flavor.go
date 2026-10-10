package main

import (
	"fmt"
	"path"
	"strings"

	"github.com/oblireach/agent/engine"
)

// agentFlavor selects the Windows build line of this binary. Injected at
// build time, "" (= modern) unless the build script says otherwise:
//
//	go build -ldflags "-X main.agentVersion=x.y.z -X main.agentFlavor=nt6" ...
//
// "nt6" = the same agent, every feature, compiled with Go 1.20.14
// (go120.mod) for Windows 7 SP1 / 8.1 / Server 2008 R2 / 2012 / 2012 R2: a
// binary built with Go >= 1.21 dies before main() on Windows 7 / 2008 R2
// (runtime -> ProcessPrng in bcryptprimitives.dll) and Go 1.21+ dropped
// Windows < 10. See BUILD_NT6.txt. Shipped as oblireach-agent-nt6.msi.
var agentFlavor = ""

const (
	agentFlavorModern = "modern"
	agentFlavorNT6    = "nt6"

	obliReachMSIModern = "oblireach-agent.msi"
	obliReachMSINT6    = "oblireach-agent-nt6.msi"
)

// isNT6Build: this binary is the Go 1.20 build for Windows < 10.
func isNT6Build() bool { return strings.EqualFold(strings.TrimSpace(agentFlavor), agentFlavorNT6) }

// agentFlavorName is the `flavor` reported in the heartbeat.
func agentFlavorName() string {
	if isNT6Build() {
		return agentFlavorNT6
	}
	return agentFlavorModern
}

// selfUpdateFlavorCheck refuses, before anything is downloaded, an update
// package of the wrong Windows build line: the modern MSI on Windows < 10
// (its launch condition refuses it, and its Go 1.22 binary cannot start on
// Windows 7 / 2008 R2), or the nt6 MSI on Windows 10+ (the nt6 line is for
// Windows NT 6.x only). url is the package path the server sent; the
// server picks the file from the device's OS (oblireachUpdateGate.ts), so
// this only guards against an older or misconfigured server. needsNT6:
// WindowsVersion().NeedsNT6(). Returns "" when fine, else the reason code.
func selfUpdateFlavorCheck(url string, needsNT6 bool) string {
	u := url
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	base := strings.ToLower(path.Base(strings.ReplaceAll(u, `\`, "/")))
	switch {
	case needsNT6 && base == obliReachMSIModern:
		return "wrong_flavor"
	case !needsNT6 && base == obliReachMSINT6:
		return "wrong_flavor"
	}
	return ""
}

// selfUpdateFlavorDetail: the human text of a wrong_flavor refusal.
func selfUpdateFlavorDetail(url string, needsNT6 bool) string {
	if needsNT6 {
		return fmt.Sprintf("wrong_flavor: %s is the Windows 10+ package; this Windows (%s) needs %s", path.Base(url), engine.WindowsVersion(), obliReachMSINT6)
	}
	return fmt.Sprintf("wrong_flavor: %s is the Windows 7 / 8.1 / 2008 R2 / 2012 package; this Windows (%s) needs %s", path.Base(url), engine.WindowsVersion(), obliReachMSIModern)
}
