// go120.mod - module file of the "nt6" build line (Windows 7 SP1 / 8.1 /
// Server 2008 R2 / 2012 / 2012 R2), compiled with Go 1.20.14
// (C:\Go1.20\bin\go.exe) through -modfile=go120.mod and the nt6 build tag:
//
//	C:\Go1.20\bin\go.exe build -tags nt6 -modfile=go120.mod ./...
//	C:\Go1.20\bin\go.exe test  -tags nt6 -modfile=go120.mod ./...
//
// Same module and sources as go.mod (the modern line, pion/webrtc v4); the
// nt6 tag swaps the two files that import pion (peer_webrtc_nt6.go,
// pion_v3_nt6.go) for pion/webrtc v3.3.6, the last WebRTC line that builds
// with Go 1.20. Every version here must build with Go 1.20: never take one
// whose go.mod needs Go > 1.20 (dtls v2, ice v2, x/crypto 0.21, x/net 0.22,
// x/sys 0.18 are the last ones). pion v3 / dtls v2 are in maintenance:
// follow their security advisories.

module github.com/oblireach/supportlink

go 1.20

require (
	filippo.io/cpace v0.0.0-20210101143347-24d601e2e469
	github.com/gorilla/websocket v1.5.3
	github.com/pion/stun v0.6.1
	github.com/pion/transport/v2 v2.2.10
	github.com/pion/turn/v2 v2.1.6
	github.com/pion/webrtc/v3 v3.3.6
	golang.org/x/crypto v0.21.0
	golang.org/x/exp v0.0.0-20240119083558-1b970713d09a
	golang.org/x/sys v0.18.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/uuid v1.3.1 // indirect
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
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/testify v1.9.0 // indirect
	github.com/wlynxg/anet v0.0.3 // indirect
	golang.org/x/net v0.22.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
