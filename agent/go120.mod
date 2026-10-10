// go120.mod - module file of the "nt6" Windows build line (Windows 7 SP1 /
// 8.1 / Server 2008 R2 / 2012 / 2012 R2), compiled with Go 1.20.14
// (C:\Go1.20\bin\go.exe) through -modfile=go120.mod. Same module, same
// sources and same dependency versions as go.mod (which stays at go 1.22 for
// the modern build); only the `go` directive differs. When a dependency moves
// in go.mod, copy the require blocks here and go.sum to go120.sum, and never
// take a version whose own go.mod needs Go > 1.20. Check:
//   C:\Go1.20\bin\go.exe build -modfile=go120.mod -tags h265 .
// (CGO_ENABLED=1, TDM-GCC in PATH, -p 1). Build commands: BUILD_NT6.txt.

module github.com/oblireach/agent

go 1.20

require github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808

require (
	github.com/godbus/dbus/v5 v5.1.0
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	golang.org/x/sys v0.0.0-20210218145245-beda7e5e158e // indirect
)
