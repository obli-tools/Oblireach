// go120.mod - module file of OblireachSupport-nt6.exe, the "nt6" build line
// (Windows 7 SP1 / 8.1 / Server 2008 R2 / 2012 / 2012 R2), compiled with
// Go 1.20.14 (C:\Go1.20\bin\go.exe), the nt6 build tag and the overlay that
// makes ../supportlink read its own go120.mod (a replaced module's go.mod
// cannot be chosen otherwise, and supportlink's go.mod pins the modern pion
// v4 / x/* versions that need Go 1.24):
//
//	set CGO_ENABLED=1 (TDM-GCC or MinGW-w64 gcc in PATH)
//	C:\Go1.20\bin\go.exe build -tags nt6 -modfile=go120.mod -overlay=nt6overlay.json -trimpath -ldflags "-s -w -H windowsgui -X main.version=0.1.0" -o dist\OblireachSupport-nt6.exe .
//
// Same requirements as ../supportlink/go120.mod, plus the agent (engine).
// Every version must build with Go 1.20.

module github.com/oblireach/support

go 1.20

require (
	github.com/oblireach/agent v0.0.0
	github.com/oblireach/supportlink v0.0.0
	golang.org/x/exp v0.0.0-20240119083558-1b970713d09a
	golang.org/x/sys v0.18.0
)

require (
	filippo.io/cpace v0.0.0-20210101143347-24d601e2e469 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/uuid v1.3.1 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/gtank/ristretto255 v0.1.2 // indirect
	github.com/pion/datachannel v1.5.8 // indirect
	github.com/pion/dtls/v2 v2.2.12 // indirect
	github.com/pion/ice/v2 v2.3.38 // indirect
	github.com/pion/interceptor v0.1.29 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/mdns v0.0.12 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.14 // indirect
	github.com/pion/rtp v1.8.7 // indirect
	github.com/pion/sctp v1.8.19 // indirect
	github.com/pion/sdp/v3 v3.0.9 // indirect
	github.com/pion/srtp/v2 v2.0.20 // indirect
	github.com/pion/stun v0.6.1 // indirect
	github.com/pion/transport/v2 v2.2.10 // indirect
	github.com/pion/turn/v2 v2.1.6 // indirect
	github.com/pion/webrtc/v3 v3.3.6 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/testify v1.9.0 // indirect
	github.com/wlynxg/anet v0.0.3 // indirect
	golang.org/x/crypto v0.21.0 // indirect
	golang.org/x/net v0.22.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/oblireach/agent => ../agent
	github.com/oblireach/supportlink => ../supportlink
)
